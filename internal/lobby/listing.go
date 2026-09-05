package lobby

import (
	"time"

	"jetris/internal/config"
)

// GameListing represents a game visible in the lobby.
type GameListing struct {
	GameID             string            `json:"game_id"`
	Mode               config.GameMode   `json:"mode"`
	Status             config.GameStatus `json:"status"`
	PlayerCount        int               `json:"player_count"`
	TeamCount          int               `json:"team_count,omitempty"`           // teams mode: how many teams play each other; mirrors GameMeta.TeamCount for the lobby row's join buttons and roster grouping (absent = the historical two — see Teams)
	TeamSize           int               `json:"team_size,omitempty"`            // teams mode: players per team
	ExtraColumns       int               `json:"extra_columns,omitempty"`        // shared boards: columns per seat beyond the first; mirrors GameMeta.ExtraColumns for the lobby row's board-width tag
	MaxAgents          int               `json:"max_agents,omitempty"`           // creator's agent policy: how many roster seats agents may take (0 = agents not allowed)
	NextCount          int               `json:"next_count,omitempty"`           // how many upcoming pieces are shown (0..config.MaxNextCount); mirrors GameMeta.NextCount for the lobby row
	NoGhost            bool              `json:"no_ghost,omitempty"`             // the hard-drop ghost is off; mirrors GameMeta.NoGhost (inverted like it) so the row's "guideline" tag matches the preset exactly
	Hold               bool              `json:"hold,omitempty"`                 // the Guideline hold queue is on; mirrors GameMeta.Hold for the lobby row's "hold" tag
	GarbageHoles       int               `json:"garbage_holes,omitempty"`        // holes per garbage row (0..config.MaxGarbageHoles); mirrors GameMeta.GarbageHoles for the lobby row's "holes N" tag
	RandomGarbageHoles bool              `json:"random_garbage_holes,omitempty"` // each garbage row draws its own holes; mirrors GameMeta.RandomGarbageHoles for the lobby row's "random holes N" tag
	GuidelineGarbage   bool              `json:"guideline_garbage,omitempty"`    // Guideline attack table (0/1/2/4 rows for 1/2/3/4 lines); mirrors GameMeta.GuidelineGarbage for the lobby row's "guideline garbage" tag
	SplitPieces        bool              `json:"split_pieces,omitempty"`         // teams: the seven piece types are dealt out between teammates; mirrors GameMeta.SplitPieces for the lobby row's "split pieces" tag (see SplitsPieces)
	Bag                config.Bag        `json:"bag,omitempty"`                  // the piece randomizer ("double" / "none"; absent = the 7-bag); mirrors GameMeta.Bag for the lobby row's "double bag" / "no bag" tag
	InviteOnly         bool              `json:"invite_only,omitempty"`          // players join by invitation only (creator excepted); auto-joining agents skip it
	CreatorID          string            `json:"creator_id,omitempty"`           // who created (and may always join) the game
	Players            []PlayerSummary   `json:"players"`
	CreatedAt          time.Time         `json:"created_at"`
	FinishedAt         time.Time         `json:"finished_at,omitempty"`
}

// PlayerSummary is the player info shown in a game listing.
type PlayerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Team     int    `json:"team"`            // teams mode: 0 = A, 1 = B, …
	TeamSlot int    `json:"team_slot"`       // teams mode: section index within the team board (join order)
	Agent    bool   `json:"agent,omitempty"` // roster seat taken by an agent player (e.g. golang-mk1)
}

// Rules is the listing's mirror of the game's play rules.
func (g GameListing) Rules() config.GameRules {
	return config.GameRules{
		NextCount:          g.NextCount,
		Ghost:              !g.NoGhost,
		Hold:               g.Hold,
		Bag:                g.Bag,
		GarbageHoles:       g.GarbageHoles,
		RandomGarbageHoles: g.RandomGarbageHoles,
		GuidelineGarbage:   g.GuidelineGarbage,
	}
}

// Teams is how many teams this game is played between — the listing's mirror
// of config.GameMeta.Teams, normalized the same way (absent reads as two) and
// 0 outside teams mode.
func (g GameListing) Teams() int {
	if g.Mode != config.ModeTeams {
		return 0
	}
	return config.NormalizeTeamCount(g.TeamCount)
}

// SplitsPieces reports whether this game deals its piece types out between
// teammates — the listing's mirror of config.GameMeta.SplitsPieces, and the
// same rule: teams mode, at least two per team.
func (g GameListing) SplitsPieces() bool {
	return g.SplitPieces && g.Mode == config.ModeTeams && g.TeamSize > 1
}

// BoardWidth returns the width of the board this game plays on: the shared
// board of a cooperative or teams game (which the ExtraColumns setting
// widens per seat), or the standard 10 columns each competitive player gets.
func (g GameListing) BoardWidth() int {
	switch g.Mode {
	case config.ModeCooperative:
		return config.SharedBoardWidth(g.PlayerCount, g.ExtraColumns)
	case config.ModeTeams:
		return config.TeamBoardWidth(g.TeamSize, g.ExtraColumns)
	default:
		return config.StandardWidth
	}
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
