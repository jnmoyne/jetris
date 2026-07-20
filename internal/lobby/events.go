package lobby

import "time"

// LobbyUpdateKind identifies the type of lobby update.
type LobbyUpdateKind int

const (
	LobbyUpdatePlayers LobbyUpdateKind = iota
	LobbyUpdateGames
	LobbyUpdateChat
	LobbyUpdateArchive
	LobbyUpdateInvite // this player's pending invitation appeared or was consumed
)

// LobbyUpdate is sent from the lobby to the UI.
type LobbyUpdate struct {
	Kind    LobbyUpdateKind
	ChatMsg *ChatMessage
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
