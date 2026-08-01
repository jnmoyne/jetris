package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
)

// Invitation is the KV value of a game invitation, written to the invitee's
// per-game key config.LobbyInviteKey(invitee, gameID). A player may hold
// invitations to several games at once (one key each). The key's lifecycle IS
// the invitation's state machine, visible to inviter and invitee alike through
// the shared KV watcher:
//
//	written (Declined false)  → pending: the invitee's client pops it up
//	deleted by the invitee    → accepted (JoinGame consumes it) or dismissed (stale)
//	deleted by the inviter    → retracted: the invitee's pop-up disappears
//	rewritten Declined true   → declined: kept so the inviter SEES the refusal,
//	                            until the inviter dismisses it (Uninvite) or it
//	                            outlives config.InviteTTL
type Invitation struct {
	GameID    string          `json:"game_id"`
	InviteeID string          `json:"invitee_id"`
	FromID    string          `json:"from_id"`
	FromName  string          `json:"from_name"`
	Mode      config.GameMode `json:"mode"`
	Team      int             `json:"team"` // teams mode: which team the invitee is asked to join
	Declined  bool            `json:"declined,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// fresh reports whether the invitation is still within its TTL.
func (inv Invitation) fresh() bool {
	return time.Since(inv.CreatedAt) <= config.InviteTTL
}

// inviteKey is the invites-map key for one (invitee, game) pair — the KV key
// minus its "invites." prefix.
func inviteKey(inviteeID, gameID string) string {
	return inviteeID + "." + gameID
}

// Invite writes an invitation to toPlayerID's per-game invite key and
// announces it with an EventInviteSent lobby event. team is the team the
// invitee is asked to join (teams mode; ignored otherwise). The invitee's
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
		InviteeID: toPlayerID,
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
	if _, err := l.kv.Put(ctx, config.LobbyInviteKey(toPlayerID, gameID), data); err != nil {
		return err
	}
	l.publishEvent(EventInviteSent, gameID, toPlayerID, team)
	return nil
}

// Uninvite removes an invitation the local player sent: retracting a pending
// one (the invitee's pop-up disappears with the key) or dismissing a declined
// marker. Announced with an EventInviteRetracted lobby event.
func (l *Lobby) Uninvite(ctx context.Context, toPlayerID, gameID string) error {
	if err := l.kv.Delete(ctx, config.LobbyInviteKey(toPlayerID, gameID)); err != nil {
		return err
	}
	l.publishEvent(EventInviteRetracted, gameID, toPlayerID, 0)
	return nil
}

// MyInvites returns this player's pending invitations — fresh, not declined —
// oldest first (the UI pops them up one at a time in arrival order).
func (l *Lobby) MyInvites() []Invitation {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Invitation
	for _, inv := range l.invites {
		if inv.InviteeID == l.playerID && !inv.Declined && inv.fresh() {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// InviteTo returns this player's pending invitation to the given game, or nil.
func (l *Lobby) InviteTo(gameID string) *Invitation {
	for _, inv := range l.MyInvites() {
		if inv.GameID == gameID {
			inv := inv
			return &inv
		}
	}
	return nil
}

// SentInvites returns every live invitation to the given game — pending and
// declined alike, any inviter — sorted by invitee. This is what the creator's
// lobby row renders: who is still being waited on, and who declined.
func (l *Lobby) SentInvites(gameID string) []Invitation {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Invitation
	for _, inv := range l.invites {
		if inv.GameID == gameID && inv.fresh() {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InviteeID < out[j].InviteeID })
	return out
}

// DeclineInvite declines this player's pending invitation to the given game.
// The key is NOT deleted: it is rewritten with Declined set, so the inviter's
// client can show the refusal (they clear it via Uninvite). Also announced
// with an EventInviteDeclined lobby event.
func (l *Lobby) DeclineInvite(ctx context.Context, gameID string) error {
	l.mu.RLock()
	inv, ok := l.invites[inviteKey(l.playerID, gameID)]
	l.mu.RUnlock()
	if !ok || inv.Declined {
		return fmt.Errorf("no pending invitation to game %s", gameID)
	}
	inv.Declined = true
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	if _, err := l.kv.Put(ctx, config.LobbyInviteKey(l.playerID, gameID), data); err != nil {
		return err
	}
	l.publishEvent(EventInviteDeclined, gameID, "", 0)
	return nil
}

// DismissInvite silently drops this player's invitation to the given game —
// used for stale invitations whose game no longer exists (nothing to decline
// TO; deleting reads as "handled" on the inviter's side too).
func (l *Lobby) DismissInvite(ctx context.Context, gameID string) error {
	return l.kv.Delete(ctx, config.LobbyInviteKey(l.playerID, gameID))
}

// consumeInvite deletes this player's invitation to the game just joined
// (accepting an invitation IS joining; the key's deletion is what tells the
// inviter it was accepted, alongside the roster filling).
func (l *Lobby) consumeInvite(ctx context.Context, gameID string) {
	l.mu.RLock()
	_, ok := l.invites[inviteKey(l.playerID, gameID)]
	l.mu.RUnlock()
	if ok {
		_ = l.kv.Delete(ctx, config.LobbyInviteKey(l.playerID, gameID))
	}
}

// handleInviteUpdate folds an invites.* KV entry into the lobby's invites map.
// EVERY invitation is tracked, not just our own: inviters need to see the
// state of the invitations they sent (pending / declined / gone).
func (l *Lobby) handleInviteUpdate(entry jetstream.KeyValueEntry) {
	key := strings.TrimPrefix(entry.Key(), "invites.")
	// key is "<inviteeID>.<gameID>"; player IDs cannot contain dots.
	inviteeID, gameID, ok := strings.Cut(key, ".")
	if !ok {
		return // pre-multi-invite key ("invites.<player>") from an old client
	}
	l.mu.Lock()
	switch entry.Operation() {
	case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
		delete(l.invites, key)
	default:
		var inv Invitation
		if err := json.Unmarshal(entry.Value(), &inv); err != nil {
			l.mu.Unlock()
			return
		}
		// Trust the key over the payload for identity.
		inv.InviteeID, inv.GameID = inviteeID, gameID
		l.invites[key] = inv
	}
	l.mu.Unlock()
	l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateInvite})
}

// inviteFor reads this player's invitation to the given game directly from the
// KV (the watcher-fed cache can lag an invite written moments ago) and returns
// it when it is fresh and not declined.
func (l *Lobby) inviteFor(ctx context.Context, gameID string) *Invitation {
	entry, err := l.kv.Get(ctx, config.LobbyInviteKey(l.playerID, gameID))
	if err != nil {
		return nil
	}
	var inv Invitation
	if json.Unmarshal(entry.Value(), &inv) != nil {
		return nil
	}
	if inv.Declined || !inv.fresh() {
		return nil
	}
	inv.InviteeID, inv.GameID = l.playerID, gameID
	return &inv
}
