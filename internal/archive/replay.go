package archive

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// maybeArchiveReplay copies the finished game's stream into the shared replay
// stream if the game ranks in its bucket's (mode × with/without agents) top
// config.ReplayTopN, and purges the replay of whichever game it displaces.
// rec is the game's not-yet-published archive record; prior records are read
// straight off the archive stream so the decision does not depend on any
// client's lobby state. Runs on the archiver only (the caller already won the
// archive CAS race) and MUST run before the game stream is deleted.
// Best-effort: a failed copy is purged again (CopyGameToReplayStream) and the
// game simply archives without a replay.
func maybeArchiveReplay(ctx context.Context, js jetstream.JetStream, rec config.ArchiveRecord) {
	prior, err := fetchArchiveRecords(ctx, js)
	if err != nil {
		log.Printf("replay %s: skipping, can't read archive records: %v", rec.GameID, err)
		return
	}

	bucket := make([]config.ArchiveRecord, 0, len(prior)+1)
	for _, r := range prior {
		if r.SameReplayBucket(rec) && r.GameID != rec.GameID {
			bucket = append(bucket, r)
		}
	}
	bucket = append(bucket, rec)
	sort.SliceStable(bucket, func(i, j int) bool { return bucket[i].RankBefore(bucket[j]) })

	top := make(map[string]bool, config.ReplayTopN)
	qualified := false
	for i, r := range bucket {
		if i >= config.ReplayTopN {
			break
		}
		top[r.GameID] = true
		if r.GameID == rec.GameID {
			qualified = true
		}
	}
	if !qualified {
		return
	}

	if err := natspkg.CopyGameToReplayStream(ctx, js, rec.GameID); err != nil {
		log.Printf("replay %s: %v", rec.GameID, err)
		return
	}
	log.Printf("replay %s: archived (%s bucket top %d)", rec.GameID, rec.Mode, config.ReplayTopN)

	// Displacement: any same-bucket game that still has a replay but no longer
	// ranks in the top N loses it. Replays of other buckets — and of games
	// whose record we can't see (another archiver may be mid-publish) — are
	// left alone.
	byID := make(map[string]bool, len(bucket))
	for _, r := range bucket {
		byID[r.GameID] = true
	}
	ids, err := natspkg.ListReplayGameIDs(ctx, js)
	if err != nil {
		log.Printf("replay %s: displacement check: %v", rec.GameID, err)
		return
	}
	for _, id := range ids {
		if byID[id] && !top[id] {
			log.Printf("replay %s: displaced from the top %d, purging its replay", id, config.ReplayTopN)
			_ = natspkg.PurgeReplay(ctx, js, id)
		}
	}
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
