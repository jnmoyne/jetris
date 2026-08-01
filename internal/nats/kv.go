package nats

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
)

// EnsureLobbyKV creates or retrieves the lobby KV bucket.
//
// The bucket has NO bucket-wide TTL — game listings and invitations must
// persist until they are explicitly deleted. Instead it enables PER-KEY TTL
// (LimitMarkerTTL): presence entries are written with a per-message TTL
// (see PutLobbyPresence) so they self-expire when a client stops heart-beating,
// while game/invite keys, written without a TTL, live on. LimitMarkerTTL also
// makes the server emit a watchable delete marker when a key expires, so every
// client's KV watcher learns a player is gone without polling last-seen.
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	return js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         config.LobbyKVBucket,
		Storage:        jetstream.FileStorage,
		LimitMarkerTTL: config.PresenceTTL,
	})
}

// lobbyKVSubject is the JetStream subject a lobby KV key is stored under
// (mirrors the nats.go KV layout: "$KV.<bucket>.<key>").
func lobbyKVSubject(key string) string {
	return "$KV." + config.LobbyKVBucket + "." + key
}

// PutLobbyPresence writes a presence value that carries a per-message TTL, so
// the entry self-deletes config.PresenceTTL after this (its latest) write if
// the client stops refreshing it. The KV client's Put/Update drop the TTL
// header (only Create carries one, and only once), so this publishes straight
// to the key's KV subject with the TTL. Requires per-key TTL to be enabled on
// the bucket (LimitMarkerTTL, set in EnsureLobbyKV). Semantically it is a plain
// KV put — last write per key wins, and the KV watcher sees a normal PUT.
func PutLobbyPresence(ctx context.Context, js jetstream.JetStream, key string, data []byte) error {
	_, err := js.Publish(ctx, lobbyKVSubject(key), data, jetstream.WithMsgTTL(config.PresenceTTL))
	return err
}
