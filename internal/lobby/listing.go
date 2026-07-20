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
	TeamSize    int               `json:"team_size,omitempty"`   // teams mode: players per team
	MaxAgents   int               `json:"max_agents,omitempty"`  // creator's agent policy: how many roster seats agents may take (0 = agents not allowed)
	InviteOnly  bool              `json:"invite_only,omitempty"` // players join by invitation only (creator excepted); auto-joining agents skip it
	CreatorID   string            `json:"creator_id,omitempty"`  // who created (and may always join) the game
	Players     []PlayerSummary   `json:"players"`
	CreatedAt   time.Time         `json:"created_at"`
	FinishedAt  time.Time         `json:"finished_at,omitempty"`
}

// PlayerSummary is the player info shown in a game listing.
type PlayerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Team     int    `json:"team"`            // teams mode: 0 = A, 1 = B
	TeamSlot int    `json:"team_slot"`       // teams mode: section index within the team board (join order)
	Agent    bool   `json:"agent,omitempty"` // roster seat taken by an agent player (jetricks-agent)
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

// AgentCount returns how many roster seats are taken by agents.
func (g GameListing) AgentCount() int {
	n := 0
	for _, p := range g.Players {
		if p.Agent {
			n++
		}
	}
	return n
}
