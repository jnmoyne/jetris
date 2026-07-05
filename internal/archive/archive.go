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

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/game"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
)

// ArchiveAndCleanup transitions a finished game to archived (CAS on meta so only
// one caller wins), publishes the ArchiveRecord, deletes the game stream and KV
// listing, and leaves the game in the lobby. gamePlayers is the roster snapshot
// used to fill in players who did not top out. The caller is responsible for
// clearing its own engine reference afterwards.
func ArchiveAndCleanup(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, eng *engine.Engine, lb *lobby.Lobby, gamePlayers []lobby.PlayerSummary) {
	// Use CAS on game meta to transition finished → archived.
	// Only the first caller succeeds; others see a CAS failure and skip.
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, js, eng.GameID())
	if err != nil {
		return // stream might already be deleted
	}
	if meta.Status != config.GameStatusFinished {
		return // already archived by another instance
	}
	meta.Status = config.GameStatusArchived
	archiveData, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, eng.GameID(), archiveData, metaSeq); err != nil {
		return // CAS failed — another instance won the race
	}

	// Collect all players' results from EventGameOver events on the game stream.
	// eventSenders tracks who published their own EventGameOver — i.e., who
	// topped out. In competitive mode, the winner is the player who did NOT
	// top out. On a draw (all topped out), there is no winner.
	playerResults := make(map[string]config.PlayerResult)
	eventSenders := make(map[string]bool)
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
		FilterSubject: config.EventsSubject(eng.GameID()),
	})
	if err == nil {
		// Drain all EventGameOver events on the stream (the consumer uses
		// DeliverAll). The ordered consumer fetches asynchronously, so wait for
		// messages with a short idle timeout rather than a non-blocking poll: a
		// non-blocking poll races delivery and usually reads nothing, leaving
		// every player but the archiver with a zero score in the archive record.
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
					eventSenders[ev.PlayerID] = true
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
	// (and take their team assignment as authoritative in teams mode)
	for _, p := range gamePlayers {
		playerTeams[p.PlayerID] = p.Team
		if _, exists := playerResults[p.PlayerID]; !exists {
			playerResults[p.PlayerID] = config.PlayerResult{PlayerID: p.PlayerID}
		}
	}
	// Determine winners in competitive: any player who did NOT send an
	// EventGameOver survived to the end and wins. On a simultaneous top-out
	// draw, every player sent an event, so there is no winner.
	if meta.Mode == config.ModeCompetitive {
		for id, pr := range playerResults {
			if !eventSenders[id] {
				pr.Winner = true
				playerResults[id] = pr
			}
		}
	}
	// Determine the winning team in teams mode: a team loses when EVERY member
	// sent an EventGameOver. Every member of the other team wins — including
	// its already-eliminated players (a team win is shared). If both teams are
	// fully out (defensive; shouldn't happen with an ordered event stream),
	// there is no winner.
	winningTeam := -1
	if meta.Mode == config.ModeTeams {
		teamDead := [config.TeamCount]bool{true, true}
		for id, t := range playerTeams {
			if t >= 0 && t < config.TeamCount && !eventSenders[id] {
				teamDead[t] = false
			}
		}
		switch {
		case teamDead[0] && !teamDead[1]:
			winningTeam = 1
		case teamDead[1] && !teamDead[0]:
			winningTeam = 0
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
		GameID:      eng.GameID(),
		Mode:        meta.Mode,
		PlayerCount: meta.PlayerCount,
		StartedAt:   meta.StartedAt,
		FinishedAt:  meta.FinishedAt,
		Players:     results,
		TeamSize:    meta.TeamSize,
		WinningTeam: winningTeam,
	}
	if meta.Mode == config.ModeCooperative {
		record.TotalScore = eng.Score()
		record.FinalLevel = eng.AchievedLevel()
	}
	if meta.Mode == config.ModeTeams {
		// The archiving engine folded every team's line-clear events, so its
		// per-team scoreboard is the authoritative end-of-game team totals.
		ts, tl := eng.TeamScores(), eng.TeamLevels()
		record.TeamScores = append([]int(nil), ts[:]...)
		record.TeamLevels = append([]int(nil), tl[:]...)
	}

	// Capture each board's final state from the game stream (latest message per
	// cell) before the stream is deleted below, so the lobby can redraw the
	// end-of-game playfield from the archive record alone.
	record.Boards = buildBoardPictures(ctx, js, meta, results)

	data, _ := json.Marshal(record)
	if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		log.Printf("archive: publish: %v", err)
		return
	}

	// Delete the game stream and KV entry
	_ = natspkg.DeleteGameStream(ctx, js, eng.GameID())
	_ = kv.Delete(ctx, config.LobbyGameKey(eng.GameID()))

	// Purge the game's chat messages from the shared chat stream (they live
	// there under a per-game subject, not on the deleted game stream).
	if s, err := js.Stream(ctx, config.LobbyChatStream); err == nil {
		_ = s.Purge(ctx, jetstream.WithPurgeSubject(config.GameChatSubject(eng.GameID())))
	}

	// Leave game in lobby
	if lb != nil {
		_ = lb.LeaveGame(ctx, eng.GameID())
	}
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
			meta.PlayerCount*config.StandardWidth, config.HeadroomRows+config.VisibleRows, config.VisibleRowStart,
			"", -1, func(r, c int) string { return config.CoopCellSubject(gameID, r, c) })
		if !ok {
			return nil
		}
		return []config.BoardPicture{pic}

	case config.ModeTeams:
		w := config.TeamBoardWidth(meta.TeamSize)
		h := config.TeamTotalRows(meta.TeamSize)
		vs := config.TeamVisibleRowStart(meta.TeamSize)
		var out []config.BoardPicture
		for t := 0; t < config.TeamCount; t++ {
			t := t
			if pic, ok := capturePicture(ctx, js, gameID, w, h, vs, teamLabel(t), t,
				func(r, c int) string { return config.TeamCellSubject(gameID, t, r, c) }); ok {
				out = append(out, pic)
			}
		}
		return out

	default: // competitive: one board per player, ordered by player ID for stable coloring
		w := config.StandardWidth
		h := config.CompetitiveTotalRows(meta.PlayerCount)
		vs := config.CompetitiveVisibleRowStart(meta.PlayerCount)
		ids := make([]string, 0, len(players))
		for _, p := range players {
			ids = append(ids, p.PlayerID)
		}
		sort.Strings(ids)
		var out []config.BoardPicture
		for i, id := range ids {
			id := id
			if pic, ok := capturePicture(ctx, js, gameID, w, h, vs, id, i,
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

// teamLabel is the human label stored for a team board ("Team A" / "Team B").
func teamLabel(team int) string {
	if team == 0 {
		return "Team A"
	}
	return "Team B"
}
