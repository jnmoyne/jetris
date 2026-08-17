package nats

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
)

// EnsureGameStream creates the per-game stream if it does not exist.
func EnsureGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               config.GameStream(gameID),
		Subjects:           []string{config.GameSubjectFilter(gameID)},
		AllowAtomicPublish: true,
		// AllowDirect powers GetLastMsgForSubject (used by the CAS merge-retry's
		// refetch and by FetchGameMeta). On a single-replica stream direct get
		// reads the leader's own state, so the refetched "last sequence per
		// subject" is fresh — the merge-retry does get the new sequence on each
		// retry. (On a multi-replica stream direct get can read a follower that is
		// briefly behind; if that ever causes useless retries we'd switch the
		// merge-retry refetch to a consistent read.)
		AllowDirect: true,
		// The stream retains the FULL game history (no per-subject cap): an
		// ordered consumer then delivers every write in order, so a lagging
		// viewer can never miss a cell's vacate because a later write to the
		// same subject trimmed it (which used to leave stale piece cells on
		// replicas), and spectators joining mid-game replay the whole game
		// from the start. Current state is still read as the last message per
		// subject (AllowDirect above). Growth is bounded by the game's length
		// and the stream is deleted once the game is archived.
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
	})
	return err
}

// EnsureChatStream creates the chat stream. It carries BOTH the lobby chat
// and every game's chat, distinguished purely by the game-ID subject token
// (the lobby uses the reserved ID "lobby", games their own ID).
func EnsureChatStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     config.ChatStream,
		Subjects: []string{config.ChatSubjectFilter},
		MaxAge:   config.ChatMaxAge,
		Storage:  jetstream.FileStorage,
	})
	return err
}

// EnsureArchiveStream creates the game archive stream.
func EnsureArchiveStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     config.ArchiveStream,
		Subjects: []string{config.ArchiveSubject},
		Storage:  jetstream.FileStorage,
	})
	return err
}

// SealGameStream sets Sealed: true on a game stream.
func SealGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error {
	name := config.GameStream(gameID)
	s, err := js.Stream(ctx, name)
	if err != nil {
		return err
	}
	info := s.CachedInfo()
	cfg := info.Config
	cfg.Sealed = true
	_, err = js.UpdateStream(ctx, cfg)
	return err
}

// DeleteGameStream deletes a game stream entirely.
func DeleteGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error {
	return js.DeleteStream(ctx, config.GameStream(gameID))
}

// PurgeGameChat removes one game's chat messages from the shared chat stream
// (they live there under a per-game subject, not on the game stream).
func PurgeGameChat(ctx context.Context, js jetstream.JetStream, gameID string) error {
	s, err := js.Stream(ctx, config.ChatStream)
	if err != nil {
		return err
	}
	return s.Purge(ctx, jetstream.WithPurgeSubject(config.GameChatSubject(gameID)))
}

// PurgeRosterEntry removes one player's roster announcement from a game
// stream. Used when a player un-joins a game that never started: without the
// purge, engines replaying the roster subjects would keep discovering the
// departed player and render a ghost opponent board.
func PurgeRosterEntry(ctx context.Context, js jetstream.JetStream, gameID, playerID string) error {
	s, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		return err
	}
	return s.Purge(ctx, jetstream.WithPurgeSubject(config.RosterSubject(gameID, playerID)))
}

// ListGameStreams returns names of all streams matching the JETRIS_GAME_ prefix.
func ListGameStreams(ctx context.Context, js jetstream.JetStream) ([]string, error) {
	sl := js.StreamNames(ctx, jetstream.WithStreamListSubject("jetris.game.>"))
	var names []string
	for name := range sl.Name() {
		names = append(names, name)
	}
	if err := sl.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

// The replay archive: a finished game that ranks in its bucket's top
// config.ReplayTopN gets the ENTIRE content of its (memory, about-to-be-
// deleted) game stream copied into the ONE shared file-backed replay stream,
// each message republished under the game-scoped subject
// config.ReplayCopySubject — so a single "jetris.replay.<id>.>" filter
// replays a game and a Purge with the same filter deletes a displaced one.
// Republished copies get copy-time stream timestamps, so every copy carries
// its ORIGINAL timestamp in the config.ReplayTsHeader header instead; an
// original-speed replay paces itself from those. The copy ends with a marker
// message (config.ReplayMarkerSubject) — presence of the marker is what makes
// a replay complete and listable, so an in-flight copy is never surfaced.

// EnsureReplayStream creates the shared replay stream. AllowDirect powers the
// per-game marker lookups (GetReplayMarker).
func EnsureReplayStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        config.ReplayStream,
		Subjects:    []string{config.ReplaySubjectFilter},
		Storage:     jetstream.FileStorage,
		AllowDirect: true,
	})
	return err
}

// replayCopyChunk bounds the async publishes in flight during a replay copy —
// pipelined for throughput (a game stream holds thousands of messages, and one
// ack round trip each would take minutes to a remote server), chunked so the
// copy never exceeds the JetStream context's async-pending limit.
const replayCopyChunk = 512

// CopyGameToReplayStream copies every message of a finished game's stream into
// the shared replay stream: the subject remapped by config.ReplayCopySubject,
// the payload verbatim, and the original stream timestamp in the
// config.ReplayTsHeader header (the original headers are deliberately NOT
// copied — CAS-expectation and atomic-batch headers reference the dying game
// stream and must not be re-validated against the replay stream). Every ack is
// checked, and the marker is published only after the last copy is confirmed —
// any failure purges the partial copy and returns the error, so a game either
// has a complete replay or none. Must run before the game stream is deleted.
func CopyGameToReplayStream(ctx context.Context, js jetstream.JetStream, gameID string) error {
	if err := EnsureReplayStream(ctx, js); err != nil {
		return err
	}
	origin, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		return err
	}
	lastSeq := origin.CachedInfo().State.LastSeq

	fail := func(err error) error {
		_ = PurgeReplay(ctx, js, gameID)
		return err
	}

	ch, cancel, err := NewOrderedConsumer(ctx, js, OrderedConsumerConfig{
		Stream:        config.GameStream(gameID),
		FilterSubject: config.GameSubjectFilter(gameID),
	})
	if err != nil {
		return err
	}
	defer cancel()

	copyCtx, copyCancel := context.WithTimeout(ctx, time.Minute)
	defer copyCancel()

	var pending []jetstream.PubAckFuture
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-js.PublishAsyncComplete():
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
		select {
		case <-copyCtx.Done():
			return fail(copyCtx.Err())
		case msg, ok := <-ch:
			if !ok {
				return fail(context.Canceled)
			}
			md, err := msg.Metadata()
			if err != nil {
				continue
			}
			done = md.Sequence.Stream >= lastSeq
			subject := config.ReplayCopySubject(gameID, msg.Subject())
			if subject == "" {
				continue
			}
			f, err := js.PublishMsgAsync(&nats.Msg{
				Subject: subject,
				Data:    msg.Data(),
				Header: nats.Header{
					config.ReplayTsHeader: []string{strconv.FormatInt(md.Timestamp.UnixNano(), 10)},
				},
			})
			if err != nil {
				return fail(err)
			}
			copied++
			if pending = append(pending, f); len(pending) >= replayCopyChunk {
				if err := flush(); err != nil {
					return fail(err)
				}
			}
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}

	// The marker: published synchronously LAST, so its presence proves the
	// whole copy landed.
	marker, _ := json.Marshal(struct {
		GameID string `json:"game_id"`
		Msgs   uint64 `json:"msgs"`
	}{gameID, copied})
	if _, err := js.Publish(ctx, config.ReplayMarkerSubject(gameID), marker); err != nil {
		return fail(err)
	}
	return nil
}

// PurgeReplay removes one game's replay — its copied messages and marker —
// from the shared replay stream (a failed copy, or a game displaced from its
// bucket's top N).
func PurgeReplay(ctx context.Context, js jetstream.JetStream, gameID string) error {
	s, err := js.Stream(ctx, config.ReplayStream)
	if err != nil {
		return err
	}
	return s.Purge(ctx, jetstream.WithPurgeSubject(config.ReplayFilter(gameID)))
}

// GetReplayMarker returns the stream sequence of one game's copy-complete
// marker, or jetstream.ErrMsgNotFound if the game has no (finished) replay.
func GetReplayMarker(ctx context.Context, js jetstream.JetStream, gameID string) (uint64, error) {
	s, err := js.Stream(ctx, config.ReplayStream)
	if err != nil {
		return 0, err
	}
	msg, err := s.GetLastMsgForSubject(ctx, config.ReplayMarkerSubject(gameID))
	if err != nil {
		return 0, err
	}
	return msg.Sequence, nil
}

// ListReplayGameIDs returns the game IDs with a finished replay — the games
// whose copy-complete marker is present, read via a subjects-filtered stream
// info (one marker subject per archived game).
func ListReplayGameIDs(ctx context.Context, js jetstream.JetStream) ([]string, error) {
	s, err := js.Stream(ctx, config.ReplayStream)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, nil // no replay stream yet = no replays
		}
		return nil, err
	}
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(config.ReplayMarkerFilter))
	if err != nil {
		return nil, err
	}
	var ids []string
	for subject := range info.State.Subjects {
		if id := config.GameIDFromReplayMarker(subject); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
