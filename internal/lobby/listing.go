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
	TeamSize    int               `json:"team_size,omitempty"` // teams mode: players per team
	Players     []PlayerSummary   `json:"players"`
	CreatedAt   time.Time         `json:"created_at"`
	FinishedAt  time.Time         `json:"finished_at,omitempty"`
}

// PlayerSummary is the player info shown in a game listing.
type PlayerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Team     int    `json:"team"`      // teams mode: 0 = A, 1 = B
	TeamSlot int    `json:"team_slot"` // teams mode: section index within the team board (join order)
}

// TeamMemberCount returns how many roster members belong to the given team.
func (g GameListing) TeamMemberCount(team int) int {
	n := 0
	for _, p := range g.Players {
		if p.Team == team {
			n++
		}
	}
	return n
}
