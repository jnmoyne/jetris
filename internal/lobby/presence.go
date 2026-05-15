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
			// On shutdown, delete our presence key
			delCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = l.kv.Delete(delCtx, config.LobbyPlayerKey(l.playerID))
			cancel()
			return
		case <-ticker.C:
			l.publishPresence(ctx)
			l.pruneStalePresence()
		}
	}
}

// pruneStalePresence removes players whose LastSeen is older than 3× the heartbeat interval.
func (l *Lobby) pruneStalePresence() {
	threshold := time.Now().Add(-3 * config.PresenceHeartbeat)
	l.mu.Lock()
	changed := false
	for id, p := range l.players {
		if id == l.playerID {
			continue // never prune ourselves
		}
		if !p.LastSeen.IsZero() && p.LastSeen.Before(threshold) {
			delete(l.players, id)
			changed = true
		}
	}
	l.mu.Unlock()
	if changed {
		l.emitUpdate(LobbyUpdate{Kind: LobbyUpdatePlayers})
	}
}

// IsNameInUse scans the lobby KV for active player presence entries with a
// matching display name. "Active" means LastSeen is within 3× the heartbeat
// interval — stale entries from unclean shutdowns are ignored. Used by the
// login flow to warn before accepting a duplicate name. The lookup is
// case-insensitive and trims whitespace to match config.ValidatePlayerName.
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
	threshold := time.Now().Add(-3 * config.PresenceHeartbeat)
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
		if !p.LastSeen.IsZero() && p.LastSeen.Before(threshold) {
			continue // stale entry, treat the slot as free
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
		LastSeen: time.Now(),
	}
	l.mu.RUnlock()

	data, err := json.Marshal(presence)
	if err != nil {
		log.Printf("marshal presence: %v", err)
		return
	}
	_, err = l.kv.Put(ctx, config.LobbyPlayerKey(l.playerID), data)
	if err != nil {
		log.Printf("publish presence: %v", err)
	}
}
