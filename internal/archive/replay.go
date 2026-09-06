package archive

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// maybeArchiveReplay applies the replay retention policy for a game whose
// archive record rec has JUST been published: the keep set is the union of
// every bucket's top config.ReplayTopN (mode × with/without agents, ranked by
// RankBefore), the config.ReplayRecentN most recent finishes overall, and
// the PINNED games (config.ReplayKeepSet) — so a finishing game, always among
// the most recent, always gets a replay, and only keeps it past the next
// ReplayRecentN games by ranking or by a pin. Records are read straight off
// the archive stream, and the pins straight off the lobby KV (kv; nil reads
// as no pins), so the decision does not depend on any client's lobby state;
// a record that hasn't landed yet (another archiver mid-publish) is simply
// left alone.
//
// Order matters for the lobby, which tracks replays by their copy-complete
// markers: the replays this game displaces are purged FIRST, then the game's
// own stream is copied and its marker published LAST — so once a marker
// arrives, a single listing reflects every change this archive made.
// Runs on the archiver only (the caller already won the archive CAS race)
// and MUST run before the game stream is deleted. Best-effort: a failed copy
// is purged again (CopyGameToReplayStream) and the game simply has no replay.
func maybeArchiveReplay(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, rec config.ArchiveRecord) {
	prior, err := fetchArchiveRecords(ctx, js)
	if err != nil {
		log.Printf("replay %s: skipping, can't read archive records: %v", rec.GameID, err)
		return
	}
	// The pins: a game someone pinned stays whatever its rank or age. A
	// bucket that cannot be read keeps every listed replay rather than
	// risk purging a pinned one — the purge is the irreversible step.
	var pinned map[string]bool
	if kv != nil {
		if pinned, err = natspkg.ListPinnedReplays(ctx, kv); err != nil {
			log.Printf("replay %s: can't read the pinned replays, purging nothing: %v", rec.GameID, err)
			if ids, err := natspkg.ListReplayGameIDs(ctx, js); err == nil {
				pinned = make(map[string]bool, len(ids))
				for _, id := range ids {
					pinned[id] = true
				}
			}
		}
	}
	all := make([]config.ArchiveRecord, 0, len(prior)+1)
	known := make(map[string]bool, len(prior)+1)
	for _, r := range prior {
		if r.GameID != rec.GameID {
			all = append(all, r)
			known[r.GameID] = true
		}
	}
	all = append(all, rec) // ours, as we know it — whether or not the stream drain caught it
	known[rec.GameID] = true

	keep := config.ReplayKeepSet(all, pinned)
	if !keep[rec.GameID] {
		// Can't happen while ReplayRecentN > 0 (a finishing game is the most
		// recent of all), but the policy is the keep set, not this comment.
		return
	}

	// Displacement: every listed replay whose game we can see and which the
	// keep set no longer covers — aged out of the recent set without ranking
	// in its bucket's top N — loses its replay. Done before our own copy so
	// the marker published last closes the whole change.
	if ids, err := natspkg.ListReplayGameIDs(ctx, js); err != nil {
		log.Printf("replay %s: displacement check: %v", rec.GameID, err)
	} else {
		for _, id := range ids {
			if known[id] && !keep[id] {
				log.Printf("replay %s: no longer in the keep set (top %d per bucket, last %d games, or pinned), purging its replay", id, config.ReplayTopN, config.ReplayRecentN)
				_ = natspkg.PurgeReplay(ctx, js, id)
			}
		}
	}

	if err := natspkg.CopyGameToReplayStream(ctx, js, rec.GameID); err != nil {
		log.Printf("replay %s: %v", rec.GameID, err)
		return
	}
	why := "recent"
	switch {
	case pinned[rec.GameID]:
		why = "pinned"
	case config.ReplayTopRanked(all)[rec.GameID]:
		why = "bucket top"
	}
	log.Printf("replay %s: archived (%s, %s)", rec.GameID, rec.Mode, why)
}

// fetchArchiveRecords reads every record off the archive stream (latest per
// game ID, though a game only ever publishes one). Completion is detected by
// the stream's last sequence at the time of the call — the stream is
// append-only, so once that sequence is delivered the drain is complete.
func fetchArchiveRecords(ctx context.Context, js jetstream.JetStream) ([]config.ArchiveRecord, error) {
	s, err := js.Stream(ctx, config.ArchiveStream)
	if err != nil {
		return nil, err
	}
	lastSeq := s.CachedInfo().State.LastSeq
	if lastSeq == 0 {
		return nil, nil
	}

	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.ArchiveStream,
		FilterSubject: config.ArchiveSubject,
	})
	if err != nil {
		return nil, err
	}
	defer cancel()

	drainCtx, drainCancel := context.WithTimeout(ctx, 10*time.Second)
	defer drainCancel()

	seen := make(map[string]int)
	var out []config.ArchiveRecord
	for {
		select {
		case <-drainCtx.Done():
			return out, nil // partial read beats no decision at all
		case msg, ok := <-ch:
			if !ok {
				return out, nil
			}
			var rec config.ArchiveRecord
			if json.Unmarshal(msg.Data(), &rec) == nil && rec.GameID != "" {
				if i, dup := seen[rec.GameID]; dup {
					out[i] = rec
				} else {
					seen[rec.GameID] = len(out)
					out = append(out, rec)
				}
			}
			if md, err := msg.Metadata(); err == nil && md.Sequence.Stream >= lastSeq {
				return out, nil
			}
		}
	}
}
