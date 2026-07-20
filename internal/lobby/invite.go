package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
)

// Invitation is the KV value of a pending game invitation, written to the
// invitee's config.LobbyInviteKey. A player holds at most one pending
// invitation (a newer one overwrites an older one); it is consumed — the key
// deleted — when the invitee joins the game, declines, or lets it go stale
// (older than config.InviteTTL).
type Invitation struct {
	GameID    string          `json:"game_id"`
	FromID    string          `json:"from_id"`
	FromName  string          `json:"from_name"`
	Mode      config.GameMode `json:"mode"`
	Team      int             `json:"team"` // teams mode: which team the invitee is asked to join
	CreatedAt time.Time       `json:"created_at"`
}

// Invite writes an invitation to toPlayerID's invite key. team is the team
// the invitee is asked to join (teams mode; ignored otherwise). The invitee's
// lobby surfaces it via the KV watcher; agents accept automatically.
func (l *Lobby) Invite(ctx context.Context, toPlayerID, gameID string, team int) error {
	g, ok := l.Games()[gameID]
	if !ok {
		// The watcher-fed cache lags a listing we ourselves wrote moments ago
		// (create → invite is the normal flow) — read the KV directly.
		entry, err := l.kv.Get(ctx, config.LobbyGameKey(gameID))
		if err != nil {
			return fmt.Errorf("game %s not found", gameID)
		}
		if err := json.Unmarshal(entry.Value(), &g); err != nil {
			return fmt.Errorf("game %s listing: %w", gameID, err)
		}
	}
	inv := Invitation{
		GameID:    gameID,
		FromID:    l.playerID,
		FromName:  l.name,
		Mode:      g.Mode,
		Team:      team,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	_, err = l.kv.Put(ctx, config.LobbyInviteKey(toPlayerID), data)
	return err
}

// MyInvite returns this player's pending invitation, or nil when there is
// none (or the pending one has gone stale).
func (l *Lobby) MyInvite() *Invitation {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.myInvite == nil || time.Since(l.myInvite.CreatedAt) > config.InviteTTL {
		return nil
	}
	inv := *l.myInvite
	return &inv
}

// RespondInvite consumes this player's pending invitation — deleting its KV
// key — and returns it. accept only affects the caller's next step (join the
// game or not); the wire-side effect is the same either way, and the inviter
// observes acceptance as the roster filling.
func (l *Lobby) RespondInvite(ctx context.Context, accept bool) (Invitation, error) {
	l.mu.Lock()
	inv := l.myInvite
	l.myInvite = nil
	l.mu.Unlock()
	if inv == nil {
		return Invitation{}, fmt.Errorf("no pending invitation")
	}
	_ = l.kv.Delete(ctx, config.LobbyInviteKey(l.playerID))
	return *inv, nil
}

// handleInviteUpdate folds an invites.* KV entry into the lobby: only our own
// key matters (everyone watches the whole bucket).
func (l *Lobby) handleInviteUpdate(entry jetstream.KeyValueEntry) {
	playerID := entry.Key()[len("invites."):]
	if playerID != l.playerID {
		return
	}
	l.mu.Lock()
	switch entry.Operation() {
	case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
		l.myInvite = nil
	default:
		var inv Invitation
		if err := json.Unmarshal(entry.Value(), &inv); err != nil {
			l.mu.Unlock()
			return
		}
		l.myInvite = &inv
	}
	l.mu.Unlock()
	l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateInvite})
}

// inviteFor reads this player's pending invitation directly from the KV (the
// watcher-fed cache can lag an invite written moments ago) and returns it when
// it is fresh and for the given game.
func (l *Lobby) inviteFor(ctx context.Context, gameID string) *Invitation {
	entry, err := l.kv.Get(ctx, config.LobbyInviteKey(l.playerID))
	if err != nil {
		return nil
	}
	var inv Invitation
	if json.Unmarshal(entry.Value(), &inv) != nil {
		return nil
	}
	if inv.GameID != gameID || time.Since(inv.CreatedAt) > config.InviteTTL {
		return nil
	}
	return &inv
}
