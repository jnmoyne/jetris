package nats

import (
	"context"

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
		// The game stream only needs the latest message per subject (the current
		// state for each key), so cap it at one message per subject and keep it in
		// memory rather than on disk.
		MaxMsgsPerSubject: 1,
		Storage:           jetstream.MemoryStorage,
		Retention:         jetstream.LimitsPolicy,
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
