package lobby

import (
	"time"

	"jetricks/internal/config"
)

// GameListing represents a game visible in the lobby.
type GameListing struct {
	GameID      string            `json:"game_id"`
	Mode        config.GameMode   `json:"mode"`
	Status      config.GameStatus `json:"status"`
	PlayerCount int               `json:"player_count"`
	Players     []PlayerSummary   `json:"players"`
	CreatedAt   time.Time         `json:"created_at"`
	FinishedAt  time.Time         `json:"finished_at,omitempty"`
}

// PlayerSummary is the player info shown in a game listing.
type PlayerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
}
