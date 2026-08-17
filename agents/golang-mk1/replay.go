package main

// Replay archive (guide §5 step 6): when the game this agent is archiving
// ranks in the top replayTopN of its bucket — one bucket per (mode,
// with/without agents) pair — the ENTIRE game stream is copied into the ONE
// shared file-backed JETRIS_REPLAY stream before the game stream is deleted,
// so the lobby can replay the game later. Each message is republished under
// "jetris.replay.<gameID>.<original tail>" (the game ID right after the
// prefix), so one filter replays a game and a Purge with the same filter
// deletes a displaced one. Republished copies get copy-time stream
// timestamps, so every copy carries its ORIGINAL timestamp in the Jetris-Ts
// header (integer nanoseconds) — an original-speed replay paces itself from
// it. The copy ends with a "jetris.replay.<gameID>.done" marker message;
// the marker's presence is what makes a replay complete and listable.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	replayTopN          = 10
	replayStream        = "JETRIS_REPLAY"
	replaySubjectPrefix = "jetris.replay."
	replayTsHeader      = "Jetris-Ts"
	replayCopyChunk     = 512 // async publishes in flight during a copy
)

func replayFilter(gameID string) string        { return replaySubjectPrefix + gameID + ".>" }
func replayMarkerSubject(gameID string) string { return replaySubjectPrefix + gameID + ".done" }

// ensureReplayStream creates the shared replay stream (AllowDirect powers the
// marker lookups). Part of the agent's connect-time bootstrap.
func (a *Agent) ensureReplayStream(ctx context.Context) error {
	_, err := a.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        replayStream,
		Subjects:    []string{replaySubjectPrefix + ">"},
		Storage:     jetstream.FileStorage,
		AllowDirect: true,
	})
	return err
}

// replayRecord is the slice of an archive record the replay ranking needs.
type replayRecord struct {
	GameID     string    `json:"game_id"`
	Mode       int       `json:"mode"`
	TotalScore int       `json:"total_score"`
	TeamScores []int     `json:"team_scores"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Players    []struct {
		Score int  `json:"score"`
		Agent bool `json:"agent"`
	} `json:"players"`
}

// headlineScore mirrors the GUI's ranking score: the shared total for
// cooperative, the best team's total for teams, the best player's score
// otherwise.
func (r replayRecord) headlineScore() int {
	switch r.Mode {
	case modeCooperative:
		return r.TotalScore
	case modeTeams:
		best := 0
		for _, s := range r.TeamScores {
			if s > best {
				best = s
			}
		}
		if len(r.TeamScores) > 0 {
			return best
		}
	}
	best := 0
	for _, p := range r.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

func (r replayRecord) hasAgents() bool {
	for _, p := range r.Players {
		if p.Agent {
			return true
		}
	}
	return false
}

func (r replayRecord) duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	if d := r.FinishedAt.Sub(r.StartedAt); d > 0 {
		return d
	}
	return 0
}

// rankBefore is the shared "By score" total order: score desc, shorter game,
// newer finish, game ID — identical to the GUI's cut so every archiver keeps
// the same top N.
func (r replayRecord) rankBefore(o replayRecord) bool {
	if si, sj := r.headlineScore(), o.headlineScore(); si != sj {
		return si > sj
	}
	if di, dj := r.duration(), o.duration(); di != dj {
		return di < dj
	}
	if !r.FinishedAt.Equal(o.FinishedAt) {
		return r.FinishedAt.After(o.FinishedAt)
	}
	return r.GameID < o.GameID
}

func (r replayRecord) sameBucket(o replayRecord) bool {
	return r.Mode == o.Mode && r.hasAgents() == o.hasAgents()
}

// maybeArchiveReplay runs the replay decision for the record this agent is
// about to publish (recJSON, not yet on the archive stream). Must run before
// the game stream is deleted. Best-effort: a failed copy is purged again and
// the game archives without a replay.
func (a *Agent) maybeArchiveReplay(ctx context.Context, recJSON []byte) {
	var rec replayRecord
	if err := json.Unmarshal(recJSON, &rec); err != nil || rec.GameID == "" {
		return
	}
	prior, err := a.fetchArchiveRecords(ctx)
	if err != nil {
		log.Printf("replay %s: can't read archive records: %v", rec.GameID, err)
		return
	}
	bucket := []replayRecord{rec}
	for _, r := range prior {
		if r.sameBucket(rec) && r.GameID != rec.GameID {
			bucket = append(bucket, r)
		}
	}
	sort.SliceStable(bucket, func(i, j int) bool { return bucket[i].rankBefore(bucket[j]) })
	top := make(map[string]bool, replayTopN)
	qualified := false
	for i, r := range bucket {
		if i >= replayTopN {
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

	if err := a.copyGameToReplayStream(ctx, rec.GameID); err != nil {
		log.Printf("replay %s: %v", rec.GameID, err)
		_ = a.purgeReplay(ctx, rec.GameID)
		return
	}
	log.Printf("replay %s: archived (bucket top %d)", rec.GameID, replayTopN)

	// Displace: same-bucket games with a replay but out of the top N.
	inBucket := make(map[string]bool, len(bucket))
	for _, r := range bucket {
		inBucket[r.GameID] = true
	}
	ids, err := a.listReplayGameIDs(ctx)
	if err != nil {
		log.Printf("replay %s: displacement check: %v", rec.GameID, err)
		return
	}
	for _, id := range ids {
		if inBucket[id] && !top[id] {
			log.Printf("replay %s: displaced from the top %d, purging its replay", id, replayTopN)
			_ = a.purgeReplay(ctx, id)
		}
	}
}

// copyGameToReplayStream republishes every game-stream message under the
// game's replay prefix: payload verbatim, the original stream timestamp in
// the Jetris-Ts header, the original headers dropped (their CAS expectations
// reference the dying game stream). Async-published in chunks with every ack
// checked; the marker goes last, synchronously, so its presence proves the
// whole copy landed.
func (a *Agent) copyGameToReplayStream(ctx context.Context, gameID string) error {
	if err := a.ensureReplayStream(ctx); err != nil {
		return err
	}
	s, err := a.js.Stream(ctx, gameStreamName(gameID))
	if err != nil {
		return err
	}
	lastSeq := s.CachedInfo().State.LastSeq
	if lastSeq == 0 {
		return fmt.Errorf("empty game stream")
	}
	cons, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		return err
	}
	iter, err := cons.Messages()
	if err != nil {
		return err
	}
	defer iter.Stop()

	copyCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	go func() { <-copyCtx.Done(); iter.Stop() }()

	prefix := "jetris.game." + gameID + "."
	var pending []jetstream.PubAckFuture
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-a.js.PublishAsyncComplete():
		case <-copyCtx.Done():
			return copyCtx.Err()
		}
		for _, f := range pending {
			select {
			case err := <-f.Err():
				return err
			default:
			}
		}
		pending = pending[:0]
		return nil
	}

	var copied uint64
	for done := false; !done; {
		msg, err := iter.Next()
		if err != nil {
			return err
		}
		md, err := msg.Metadata()
		if err != nil {
			continue
		}
		done = md.Sequence.Stream >= lastSeq
		tail, ok := strings.CutPrefix(msg.Subject(), prefix)
		if !ok || tail == "" {
			continue
		}
		f, err := a.js.PublishMsgAsync(&nats.Msg{
			Subject: replaySubjectPrefix + gameID + "." + tail,
			Data:    msg.Data(),
			Header:  nats.Header{replayTsHeader: []string{strconv.FormatInt(md.Timestamp.UnixNano(), 10)}},
		})
		if err != nil {
			return err
		}
		copied++
		if pending = append(pending, f); len(pending) >= replayCopyChunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	marker, _ := json.Marshal(map[string]any{"game_id": gameID, "msgs": copied})
	_, err = a.js.Publish(ctx, replayMarkerSubject(gameID), marker)
	return err
}

// purgeReplay removes one game's replay — copied messages and marker — from
// the shared replay stream.
func (a *Agent) purgeReplay(ctx context.Context, gameID string) error {
	s, err := a.js.Stream(ctx, replayStream)
	if err != nil {
		return err
	}
	return s.Purge(ctx, jetstream.WithPurgeSubject(replayFilter(gameID)))
}

// listReplayGameIDs returns the game IDs whose copy-complete marker is
// present (a subjects-filtered stream info — one marker subject per game).
func (a *Agent) listReplayGameIDs(ctx context.Context) ([]string, error) {
	s, err := a.js.Stream(ctx, replayStream)
	if err != nil {
		return nil, err
	}
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(replaySubjectPrefix+"*.done"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for subject := range info.State.Subjects {
		tail := strings.TrimPrefix(subject, replaySubjectPrefix)
		if id, ok := strings.CutSuffix(tail, ".done"); ok && id != "" && !strings.Contains(id, ".") {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// fetchArchiveRecords drains the archive stream (append-only: complete once
// the last sequence seen at call time has been delivered).
func (a *Agent) fetchArchiveRecords(ctx context.Context) ([]replayRecord, error) {
	s, err := a.js.Stream(ctx, archiveStream)
	if err != nil {
		return nil, err
	}
	lastSeq := s.CachedInfo().State.LastSeq
	if lastSeq == 0 {
		return nil, nil
	}
	cons, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		return nil, err
	}
	var (
		mu   sync.Mutex
		out  []replayRecord
		done = make(chan struct{})
	)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var rec replayRecord
		if json.Unmarshal(msg.Data(), &rec) == nil && rec.GameID != "" {
			mu.Lock()
			out = append(out, rec)
			mu.Unlock()
		}
		if md, err := msg.Metadata(); err == nil && md.Sequence.Stream >= lastSeq {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second): // partial read beats no decision
	case <-ctx.Done():
	}
	cc.Stop()
	mu.Lock()
	defer mu.Unlock()
	return out, ctx.Err()
}
