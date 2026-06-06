// Package archive records a finished game to the archive stream and tears down
// its NATS resources. It is UI-agnostic so both the web and native front ends
// share one implementation (wired as engine.OnGameFinished).
package archive

import (
	"context"
	"encoding/json"
	"log"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/engine"
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
	// Add our own data first
	playerResults[eng.PlayerID()] = config.PlayerResult{
		PlayerID:   eng.PlayerID(),
		Score:      eng.Score(),
		PieceCount: eng.PieceIdx(),
	}
	// Read EventGameOver events from others
	evtCh, evtCancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(eng.GameID()),
		FilterSubject: config.EventsSubject(eng.GameID()),
	})
	if err == nil {
		done := false
		for !done {
			select {
			case msg, ok := <-evtCh:
				if !ok {
					done = true
					break
				}
				var ev engine.GameEvent
				if json.Unmarshal(msg.Data(), &ev) == nil && ev.Kind == engine.EventGameOver {
					eventSenders[ev.PlayerID] = true
					if _, exists := playerResults[ev.PlayerID]; !exists {
						playerResults[ev.PlayerID] = config.PlayerResult{
							PlayerID:   ev.PlayerID,
							Score:      ev.Score,
							PieceCount: ev.PieceCount,
						}
					}
				}
			default:
				done = true
			}
		}
		evtCancel()
	}
	// Also add players from the game listing who might not have topped out
	for _, p := range gamePlayers {
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
	}
	if meta.Mode == config.ModeCooperative {
		record.TotalScore = eng.Score()
	}

	data, _ := json.Marshal(record)
	if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		log.Printf("archive: publish: %v", err)
		return
	}

	// Delete the game stream and KV entry
	_ = natspkg.DeleteGameStream(ctx, js, eng.GameID())
	_ = kv.Delete(ctx, config.LobbyGameKey(eng.GameID()))

	// Leave game in lobby
	if lb != nil {
		_ = lb.LeaveGame(ctx, eng.GameID())
	}
}
