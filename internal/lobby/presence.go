package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
)

// PresenceStatus represents the player's current state.
type PresenceStatus int

const (
	StatusInLobby PresenceStatus = iota
	StatusInGame
	StatusSpectating
)

// PlayerPresence is the KV value for each player's presence entry.
type PlayerPresence struct {
	PlayerID string         `json:"player_id"`
	Name     string         `json:"name"`
	Status   PresenceStatus `json:"status"`
	GameID   string         `json:"game_id,omitempty"`
	Agent    bool           `json:"agent,omitempty"` // this peer is an agent player (jetricks-agent)
	LastSeen time.Time      `json:"last_seen"`
}

func (l *Lobby) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(config.PresenceHeartbeat)
	defer ticker.Stop()

	// Publish immediately on start
	l.publishPresence(ctx)

	for {
		select {
		case <-ctx.Done():
			// Best-effort removal on any context cancellation (Leave already
			// deletes it explicitly on a clean quit; this covers the rest).
			// Otherwise the presence TTL removes it a few minutes later.
			delCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = l.kv.Delete(delCtx, config.LobbyPlayerKey(l.playerID))
			cancel()
			return
		case <-ticker.C:
			l.publishPresence(ctx)
		}
	}
}

// Leave removes this player's presence entry from the lobby KV immediately, so
// other clients' KV watchers get a delete event and drop the player from their
// display right away instead of waiting for the presence TTL to expire. Call it
// (synchronously, while the connection is still up) before tearing down.
func (l *Lobby) Leave(ctx context.Context) {
	if l.kv == nil {
		return
	}
	if err := l.kv.Delete(ctx, config.LobbyPlayerKey(l.playerID)); err != nil {
		log.Printf("delete presence on leave: %v", err)
	}
}

// IsNameInUse scans the lobby KV for a player presence entry with a matching
// display name. A present key means an active player: stale entries self-expire
// via the presence TTL, so mere existence is the liveness test — no last-seen
// check. Used by the login flow to warn before accepting a duplicate name. The
// lookup is case-insensitive and trims whitespace to match
// config.ValidatePlayerName.
func IsNameInUse(ctx context.Context, kv jetstream.KeyValue, name string) (bool, error) {
	target := strings.ToLower(strings.TrimSpace(name))
	if target == "" {
		return false, nil
	}
	keys, err := kv.Keys(ctx)
	if err != nil {
		// An empty bucket returns ErrNoKeysFound from the JetStream client
		// — treat that as "no one in the lobby" rather than a hard error.
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return false, nil
		}
		return false, err
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "players.") {
			continue
		}
		entry, err := kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var p PlayerPresence
		if err := json.Unmarshal(entry.Value(), &p); err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Name)) == target {
			return true, nil
		}
	}
	return false, nil
}

func (l *Lobby) publishPresence(ctx context.Context) {
	l.mu.RLock()
	presence := PlayerPresence{
		PlayerID: l.playerID,
		Name:     l.name,
		Status:   l.status,
		GameID:   l.currentGameID,
		Agent:    l.isAgent,
		LastSeen: time.Now(),
	}
	l.mu.RUnlock()

	data, err := json.Marshal(presence)
	if err != nil {
		log.Printf("marshal presence: %v", err)
		return
	}
	// Write with a per-key TTL (not a plain Put) so the entry self-expires if
	// this client stops heart-beating — the server then removes it and every
	// watcher is notified, no last-seen bookkeeping needed.
	if err := natspkg.PutLobbyPresence(ctx, l.js, config.LobbyPlayerKey(l.playerID), data); err != nil {
		log.Printf("publish presence: %v", err)
	}
}
