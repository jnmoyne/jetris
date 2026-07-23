package lobby

import "time"

// LobbyUpdateKind identifies the type of lobby update.
type LobbyUpdateKind int

const (
	LobbyUpdatePlayers LobbyUpdateKind = iota
	LobbyUpdateGames
	LobbyUpdateChat
	LobbyUpdateArchive
	LobbyUpdateInvite // an invitation (to this player, or sent by anyone) changed state
)

// LobbyUpdate is sent from the lobby to the UI.
type LobbyUpdate struct {
	Kind    LobbyUpdateKind
	ChatMsg *ChatMessage
}

// Lobby event kinds, published as transient core NATS messages on
// config.LobbyEventSubject(kind) whenever lobby state changes (see the
// commentary in config for the state-vs-signal split).
const (
	EventGameCreated     = "game.created"
	EventGameJoined      = "game.joined"
	EventGameLeft        = "game.left" // a roster seat was freed (unjoin), not a mere screen change
	EventInviteSent      = "invite.sent"
	EventInviteRetracted = "invite.retracted"
	EventInviteDeclined  = "invite.declined"
)

// LobbyEvent is the payload of every lobby event message.
type LobbyEvent struct {
	Kind     string    `json:"kind"`
	GameID   string    `json:"game_id,omitempty"`
	PlayerID string    `json:"player_id"`           // the acting player (creator, joiner, inviter, decliner)
	TargetID string    `json:"target_id,omitempty"` // invite events: the invitee
	Team     int       `json:"team,omitempty"`      // teams-mode invites: the team offered
	Time     time.Time `json:"time"`
}

// ChatMessage represents a chat message in the lobby or a game.
type ChatMessage struct {
	PlayerID  string    `json:"player_id"`
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
	Spectator bool      `json:"spectator,omitempty"`
	// GameID scopes the message: "" = lobby chat, otherwise the game whose
	// players/spectators it is for. NOT part of the payload — it is derived
	// from the delivery subject (lobby and game chat share one stream and are
	// distinguished purely by subject naming).
	GameID string `json:"-"`
}
