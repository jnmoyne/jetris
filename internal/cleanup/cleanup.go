package cleanup

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
)

// creationGracePeriod exempts newly created games from cleanup. Game creation
// is several separate writes (stream, meta, KV listing) and the creator's own
// join is another, so a cleanup pass running on a peer that logs in
// mid-creation would otherwise see a torn or empty-rostered game as
// "orphaned"/"creator absent" and delete it. This matters even more with
// resident agents around: every agent login runs a cleanup pass, and agent-allowed
// games legitimately sit at zero players until the agents' scan picks them up.
const creationGracePeriod = time.Minute

// Run performs the full startup cleanup pass.
func Run(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, lb *lobby.Lobby) error {
	streamNames, err := natspkg.ListGameStreams(ctx, js)
	if err != nil {
		return err
	}

	games := lb.Games()
	players := lb.Players()

	for _, streamName := range streamNames {
		gameID := strings.TrimPrefix(streamName, "JETRICKS_GAME_")

		listing, hasListing := games[gameID]
		if !hasListing {
			// Stream has no KV entry. Check the stream's meta before deleting —
			// never delete a game that is still in progress or starting.
			meta, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
			if err != nil {
				// No meta: either a torn leftover, or a game whose creation is
				// literally in flight (stream exists, meta publish next).
				if s, serr := js.Stream(ctx, streamName); serr == nil &&
					time.Since(s.CachedInfo().Created) < creationGracePeriod {
					continue
				}
				log.Printf("cleanup: deleting orphaned stream %s (no meta)", streamName)
				_ = natspkg.DeleteGameStream(ctx, js, gameID)
				continue
			}
			if time.Since(meta.CreatedAt) < creationGracePeriod {
				continue // just created; the KV listing may simply not be seen yet
			}
			if meta.Status == config.GameStatusInProgress || meta.Status == config.GameStatusStarting {
				log.Printf("cleanup: skipping stream %s (status %s, no KV entry — re-creating KV entry)", streamName, meta.Status)
				// Re-create the KV entry so the game shows up in the lobby
				gl := lobby.GameListing{
					GameID:      gameID,
					Mode:        meta.Mode,
					Status:      meta.Status,
					PlayerCount: meta.PlayerCount,
					CreatedAt:   meta.CreatedAt,
				}
				data, _ := json.Marshal(gl)
				_, _ = kv.Put(ctx, config.LobbyGameKey(gameID), data)
				continue
			}
			log.Printf("cleanup: deleting orphaned stream %s (status %s)", streamName, meta.Status)
			_ = natspkg.DeleteGameStream(ctx, js, gameID)
			continue
		}

		if time.Since(listing.CreatedAt) < creationGracePeriod {
			continue // just created; the roster may legitimately still be empty
		}

		switch listing.Status {
		case config.GameStatusFinished:
			// Games are archived immediately on game over by the UI server.
			// If a finished game still exists here, clean it up.
			archiveGame(ctx, js, kv, gameID)

		case config.GameStatusCreated:
			// Check if creator is absent
			creatorAbsent := true
			for _, p := range listing.Players {
				if _, present := players[p.PlayerID]; present {
					creatorAbsent = false
					break
				}
			}
			if creatorAbsent {
				cancelGame(ctx, js, kv, gameID)
			}

		case config.GameStatusStarting:
			// Check if all players absent
			allAbsent := true
			for _, p := range listing.Players {
				if _, present := players[p.PlayerID]; present {
					allAbsent = false
					break
				}
			}
			if allAbsent {
				cancelGame(ctx, js, kv, gameID)
			}

		case config.GameStatusInProgress:
			// Check if all players disconnected
			allAbsent := true
			for _, p := range listing.Players {
				if _, present := players[p.PlayerID]; present {
					allAbsent = false
					break
				}
			}
			if allAbsent {
				finishAbandonedGame(ctx, js, kv, gameID)
			}
		}
	}

	return nil
}

func archiveGame(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, gameID string) {
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		return
	}
	meta.Status = config.GameStatusArchived
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, metaSeq); err != nil {
		return
	}
	_ = natspkg.SealGameStream(ctx, js, gameID)
	// Update KV
	listing := lobby.GameListing{
		GameID: gameID,
		Status: config.GameStatusArchived,
	}
	listingData, _ := json.Marshal(listing)
	_, _ = kv.Put(ctx, config.LobbyGameKey(gameID), listingData)
}

func cancelGame(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, gameID string) {
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		return
	}
	meta.Status = config.GameStatusCancelled
	data, _ := json.Marshal(meta)
	_ = natspkg.PublishMeta(ctx, js, gameID, data, metaSeq)
	_ = natspkg.DeleteGameStream(ctx, js, gameID)
	_ = kv.Delete(ctx, config.LobbyGameKey(gameID))
}

func finishAbandonedGame(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, gameID string) {
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		return
	}
	meta.Status = config.GameStatusFinished
	meta.Abandoned = true
	meta.FinishedAt = time.Now()
	data, _ := json.Marshal(meta)
	_ = natspkg.PublishMeta(ctx, js, gameID, data, metaSeq)
}
