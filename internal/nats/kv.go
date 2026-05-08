package nats

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
)

// EnsureLobbyKV creates or retrieves the lobby KV bucket.
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	// No bucket-level TTL — game listings must persist indefinitely.
	// Player presence entries expire via heartbeat refresh; stale entries
	// are detected by checking timestamps in the presence watcher.
	return js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  config.LobbyKVBucket,
		Storage: jetstream.FileStorage,
	})
}
