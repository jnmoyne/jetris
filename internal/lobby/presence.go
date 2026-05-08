package lobby

import (
	"context"
	"encoding/json"
	"log"
	"time"

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
