// Package archive records a finished game to the archive stream and tears down
// its NATS resources. It is UI-agnostic so both the web and native front ends
// share one implementation (wired as engine.OnGameFinished).
package archive

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
)

// streamDeleteGrace is how long after the finish the game's stream and lobby
// listing survive: every other peer's ordered consumer (and any spectator
// still catching up) gets this long to receive the final events before the
// stream is deleted under it. Only the destructive steps wait — the archive
// record is published as soon as it is built, so the lobby's history shows
// the game right away.
const streamDeleteGrace = 5 * time.Second

// ArchiveAndCleanup transitions a finished game to archived (CAS on meta so only
// one caller wins), publishes the ArchiveRecord — first, so every lobby sees
// the finished game immediately — then archives the replay (the stream copy
// can take seconds for a long game), and finally, once streamDeleteGrace has
// passed since the finish, deletes the game stream and KV listing and leaves
// the game in the lobby. gamePlayers is the roster snapshot used to fill in
// players who did not top out. The caller is responsible for clearing its own
// engine reference afterwards.
func ArchiveAndCleanup(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, eng *engine.Engine, lb *lobby.Lobby, gamePlayers []lobby.PlayerSummary) {
	finished := time.Now() // the engine fires this callback right at the finish
	// Use CAS on game meta to transition finished → archived.
	// Only the first caller succeeds; others see a CAS failure and skip.
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, js, eng.GameID())
	if err != nil {
		log.Printf("archive %s: skipping, meta unavailable (stream already deleted?): %v", eng.GameID(), err)
		return
	}
	if meta.Status != config.GameStatusFinished {
		log.Printf("archive %s: skipping, meta status is %q (already archived by another instance?)", eng.GameID(), meta.Status)
		return
	}
	meta.Status = config.GameStatusArchived
	archiveData, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, eng.GameID(), archiveData, metaSeq); err != nil {
		log.Printf("archive %s: another instance won the archive race: %v", eng.GameID(), err)
		return
	}

	// Collect players' results. Events live on per-kind, per-player subjects,
	// so each player's single game_over can never be overwritten by other
	// traffic and the replay below recovers EVERY player's final score/level.
	// Verdicts still never come from the replay:
	// the archiving ENGINE lived through the game and its elimination set /
	// GameOutcome remain the authoritative record.
	playerResults := make(map[string]config.PlayerResult)
	// playerTeams maps playerID → team (teams mode). The roster listing is the
	// authoritative source; EventGameOver's Team field is the fallback for
	// players missing from the snapshot.
	playerTeams := make(map[string]int)
	// Add our own data first
	playerResults[eng.PlayerID()] = config.PlayerResult{
		PlayerID:   eng.PlayerID(),
		Score:      eng.Score(),
		Level:      eng.AchievedLevel(),
		PieceCount: eng.PieceIdx(),
	}
	playerTeams[eng.PlayerID()] = eng.TeamIdx()
	// Read EventGameOver events from others
	evtCh, evtCancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(eng.GameID()),
		FilterSubject: config.EventsSubjectFilter(eng.GameID()),
	})
	if err == nil {
		// Drain all EventGameOver events on the stream (the consumer uses
		// DeliverAll). The finish was decided from these very events, so they
		// are all on the stream already: the drain is complete as soon as a
		// delivery reports nothing pending behind it. The idle timer is the
		// fallback for a filter with nothing to deliver (the ordered consumer
		// fetches asynchronously, so a non-blocking poll would race delivery
		// and read nothing, leaving every player but the archiver with a zero
		// score in the archive record).
		const idle = time.Second
		timer := time.NewTimer(idle)
	drain:
		for {
			select {
			case msg, ok := <-evtCh:
				if !ok {
					break drain
				}
				var ev engine.GameEvent
				if json.Unmarshal(msg.Data(), &ev) == nil && ev.Kind == engine.EventGameOver {
					if _, exists := playerTeams[ev.PlayerID]; !exists {
						playerTeams[ev.PlayerID] = ev.Team
					}
					if _, exists := playerResults[ev.PlayerID]; !exists {
						playerResults[ev.PlayerID] = config.PlayerResult{
							PlayerID:   ev.PlayerID,
							Score:      ev.Score,
							Level:      ev.Level,
							PieceCount: ev.PieceCount,
						}
					}
				}
				if md, err := msg.Metadata(); err == nil && md.NumPending == 0 {
					break drain
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				break drain
			}
		}
		timer.Stop()
		evtCancel()
	}
	// Also add players from the game listing who might not have topped out
	// (and take their team assignment — and agent flag — as authoritative)
	agentSeats := make(map[string]bool, len(gamePlayers))
	for _, p := range gamePlayers {
		playerTeams[p.PlayerID] = p.Team
		agentSeats[p.PlayerID] = p.Agent
		if _, exists := playerResults[p.PlayerID]; !exists {
			playerResults[p.PlayerID] = config.PlayerResult{PlayerID: p.PlayerID}
		}
	}
	for id, pr := range playerResults {
		pr.Agent = agentSeats[id]
		playerResults[id] = pr
	}
	// Determine winners in competitive from the archiving engine's live
	// elimination record (it processed every EventGameOver as it happened):
	// any player it never saw eliminated survived to the end and wins. On a
	// simultaneous top-out draw everyone is eliminated — no winner.
	if meta.Mode == config.ModeCompetitive {
		for id, pr := range playerResults {
			if !eng.IsEliminated(id) {
				pr.Winner = true
				playerResults[id] = pr
			}
		}
	}
	// Determine the winning team in teams mode from the archiving engine's
	// own verdict. Near-simultaneous final top-outs put BOTH teams' last
	// events on the stream, so no set-of-eliminated computation can pick the
	// winner — but the engines all decided it from the ordered event stream
	// (first fully-dead team loses), and only engines on the winning side
	// (or draw participants) run this archive. A won verdict names the
	// archiver's team; a lost verdict here means a draw — no winning team.
	winningTeam := -1
	if meta.Mode == config.ModeTeams {
		if won, over := eng.GameOutcome(); over && won {
			winningTeam = eng.TeamIdx()
		}
		for id, pr := range playerResults {
			pr.Team = playerTeams[id]
			pr.Winner = winningTeam >= 0 && pr.Team == winningTeam
			playerResults[id] = pr
		}
	}

	var results []config.PlayerResult
	for _, pr := range playerResults {
		results = append(results, pr)
	}

	record := config.ArchiveRecord{
		GameID:       eng.GameID(),
		Mode:         meta.Mode,
		PlayerCount:  meta.PlayerCount,
		StartedAt:    meta.StartedAt,
		FinishedAt:   meta.FinishedAt,
		Players:      results,
		TeamCount:    meta.Teams(),
		TeamSize:     meta.TeamSize,
		ExtraColumns: meta.ExtraColumns,
		BoardRows:    config.TotalRows,
		WinningTeam:  winningTeam,
		Chat:         gameChatHistory(lb, eng.GameID()),
	}
	if meta.Mode == config.ModeCooperative {
		record.TotalScore = eng.Score()
		record.FinalLevel = eng.AchievedLevel()
	}
	if meta.Mode == config.ModeTeams {
		// The archiving engine folded every team's line-clear events, so its
		// per-team scoreboard is the authoritative end-of-game team totals.
		record.TeamScores = eng.TeamScores()
		record.TeamLevels = eng.TeamLevels()
	}

	// Capture each board's final state from the game stream (latest message per
	// cell) before the stream is deleted below, so the lobby can redraw the
	// end-of-game playfield from the archive record alone.
	record.Boards = buildBoardPictures(ctx, js, meta, results)

	// Publish the record FIRST: it is what every lobby's history shows, and
	// nothing below (the replay copy in particular) should delay it.
	data, _ := json.Marshal(record)
	if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		log.Printf("archive: publish: %v", err)
		// Without a record the game could never be listed, ranked, or
		// displaced — leave its stream for the startup cleanup pass.
		return
	}

	// Replay archive: copy the game's entire stream to the file-backed replay
	// stream (every finished game gets one — it is the most recent — and
	// keeps it while it stays in the keep set), purging the replays it
	// displaces. The lobby learns of the copy from its completion marker, so
	// the record above needn't wait for it. Must run before the stream
	// deletion below.
	maybeArchiveReplay(ctx, js, record)

	// Destructive cleanup waits out the grace period (less the time already
	// spent above) so every peer has received the final events.
	if wait := streamDeleteGrace - time.Since(finished); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}

	// Delete the game stream and KV entry
	_ = natspkg.DeleteGameStream(ctx, js, eng.GameID())
	_ = kv.Delete(ctx, config.LobbyGameKey(eng.GameID()))

	// Purge the game's chat messages from the shared chat stream (they live
	// there under a per-game subject, not on the deleted game stream).
	_ = natspkg.PurgeGameChat(ctx, js, eng.GameID())

	// Leave game in lobby
	if lb != nil {
		_ = lb.LeaveGame(ctx, eng.GameID())
	}
}

// gameChatHistory copies the game's chat out of the archiver's lobby chat log
// (the lobby's chat consumer replayed the whole shared stream, so the log
// holds the full conversation up to its cap) into ArchiveRecord form. It must
// run before PurgeGameChat below — after the purge the record is the only
// place the conversation survives. Best-effort: nil lobby (or no chat) simply
// archives without it.
func gameChatHistory(lb *lobby.Lobby, gameID string) []config.ChatLine {
	if lb == nil {
		return nil
	}
	var out []config.ChatLine
	for _, m := range lb.ChatLog() {
		if m.GameID != gameID {
			continue
		}
		out = append(out, config.ChatLine{
			Name:      m.Name,
			Text:      m.Text,
			Timestamp: m.Timestamp,
			Spectator: m.Spectator,
		})
	}
	if len(out) > config.ArchiveChatCap {
		out = out[len(out)-config.ArchiveChatCap:]
	}
	return out
}

// buildBoardPictures captures the final visible state of every board in the
// game from the game stream (one message per cell, latest wins). The set of
// boards depends on the mode: cooperative has one shared board, competitive has
// one private board per player, and teams has one shared board per team. It
// must run before the game stream is deleted.
func buildBoardPictures(ctx context.Context, js jetstream.JetStream, meta config.GameMeta, players []config.PlayerResult) []config.BoardPicture {
	gameID := meta.GameID
	switch meta.Mode {
	case config.ModeCooperative:
		pic, ok := capturePicture(ctx, js, gameID,
			config.SharedBoardWidth(meta.PlayerCount, meta.ExtraColumns), config.TotalRows, config.VisibleRowStart,
			"", -1, func(r, c int) string { return config.CoopCellSubject(gameID, r, c) })
		if !ok {
			return nil
		}
		return []config.BoardPicture{pic}

	case config.ModeTeams:
		w := config.TeamBoardWidth(meta.TeamSize, meta.ExtraColumns)
		var out []config.BoardPicture
		for t := 0; t < meta.Teams(); t++ {
			t := t
			if pic, ok := capturePicture(ctx, js, gameID, w, config.TotalRows, config.VisibleRowStart, teamLabel(t), t,
				func(r, c int) string { return config.TeamCellSubject(gameID, t, r, c) }); ok {
				out = append(out, pic)
			}
		}
		return out

	default: // competitive: one board per player, ordered by player ID for stable coloring
		ids := make([]string, 0, len(players))
		for _, p := range players {
			ids = append(ids, p.PlayerID)
		}
		sort.Strings(ids)
		var out []config.BoardPicture
		for i, id := range ids {
			id := id
			if pic, ok := capturePicture(ctx, js, gameID, config.StandardWidth, config.TotalRows, config.VisibleRowStart, id, i,
				func(r, c int) string { return config.CompetitiveCellSubject(gameID, id, r, c) }); ok {
				out = append(out, pic)
			}
		}
		return out
	}
}

// capturePicture fetches the latest message for every cell of one board's
// visible region (rows [visibleStart, height)) and stores the non-empty cells
// sparsely, with rows renumbered so the first visible row is 0.
func capturePicture(ctx context.Context, js jetstream.JetStream, gameID string, width, height, visibleStart int, label string, idx int, subjectFor func(row, col int) string) (config.BoardPicture, bool) {
	subjects := make([]string, 0, (height-visibleStart)*width)
	for r := visibleStart; r < height; r++ {
		for c := 0; c < width; c++ {
			subjects = append(subjects, subjectFor(r, c))
		}
	}
	cells, err := natspkg.FetchPlayfieldState(ctx, js, gameID, subjects)
	if err != nil {
		log.Printf("archive: capture board %q: %v", label, err)
		return config.BoardPicture{}, false
	}
	pic := config.BoardPicture{Label: label, Idx: idx, Width: width, Height: height - visibleStart}
	for _, c := range cells {
		cell, err := game.UnmarshalCell(c.Payload)
		if err != nil || isBlankCell(cell) {
			continue
		}
		pic.Cells = append(pic.Cells, config.BoardCell{
			Row:  c.Row - visibleStart,
			Col:  c.Col,
			Data: append(json.RawMessage(nil), c.Payload...),
		})
	}
	return pic, true
}

// isBlankCell reports whether a cell carries nothing worth drawing (a vacated
// cell is published as an empty "{}" message).
func isBlankCell(c game.Cell) bool {
	return !c.Occupied && !c.Active && !c.Adversarial
}

// teamLabel is the human label stored for a team board ("Team A", "Team B", …).
func teamLabel(team int) string {
	return "Team " + config.TeamLetter(team)
}
