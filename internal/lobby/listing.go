package lobby

import (
	"fmt"
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
	TeamNames          []string          `json:"team_names,omitempty"`           // teams mode: what each team is called; mirrors GameMeta.TeamNames for the lobby row, the join buttons and the invitations (absent = the letters)
	ExtraColumns       int               `json:"extra_columns,omitempty"`        // shared boards: columns per seat beyond the first; mirrors GameMeta.ExtraColumns for the lobby row's board-width tag
	MaxAgents          int               `json:"max_agents,omitempty"`           // creator's agent policy: how many roster seats agents may take (0 = agents not allowed)
	NextCount          int               `json:"next_count,omitempty"`           // how many upcoming pieces are shown (0..config.MaxNextCount); mirrors GameMeta.NextCount for the lobby row
	NoGhost            bool              `json:"no_ghost,omitempty"`             // the hard-drop ghost is off; mirrors GameMeta.NoGhost (inverted like it) so the row's "guideline" tag matches the preset exactly
	Hold               bool              `json:"hold,omitempty"`                 // the Guideline hold queue is on; mirrors GameMeta.Hold for the lobby row's "hold" tag
	GarbageHoles       int               `json:"garbage_holes,omitempty"`        // holes per garbage row (0..config.MaxGarbageHoles); mirrors GameMeta.GarbageHoles for the lobby row's "holes N" tag
	RandomGarbageHoles bool              `json:"random_garbage_holes,omitempty"` // each garbage row draws its own holes; mirrors GameMeta.RandomGarbageHoles for the lobby row's "random holes N" tag
	GuidelineGarbage   bool              `json:"guideline_garbage,omitempty"`    // Guideline attack table (0/1/2/4 rows for 1/2/3/4 lines); mirrors GameMeta.GuidelineGarbage for the lobby row's "guideline garbage" tag
	SplitPieces        bool              `json:"split_pieces,omitempty"`         // the seven piece types are dealt out between the seats sharing a playfield; mirrors GameMeta.SplitPieces for the lobby row's "split pieces" tag (see SplitsPieces)
	Bag                config.Bag        `json:"bag,omitempty"`                  // the piece randomizer ("double" / "none"; absent = the 7-bag); mirrors GameMeta.Bag for the lobby row's "double bag" / "no bag" tag
	ShowHeadroom       bool              `json:"show_headroom,omitempty"`        // the hidden headroom rows are drawn above the playfield, behind smoked glass; mirrors GameMeta.ShowHeadroom for the lobby row's "hidden rows" tag
	ExtraRows          int               `json:"extra_rows,omitempty"`           // shared boards: rows per seat beyond the first; mirrors GameMeta.ExtraRows for the lobby row's board-size tag
	LineGoal           int               `json:"line_goal,omitempty"`            // the game's length in lines (0 = until top out); mirrors GameMeta.LineGoal for the lobby row's "N lines" tag
	Scoring            config.Scoring    `json:"scoring,omitempty"`              // single playfield: "individual" when every seat is scored on its own; mirrors GameMeta.Scoring for the lobby row's game-type word
	InviteOnly         bool              `json:"invite_only,omitempty"`          // players join by invitation only (creator excepted); auto-joining agents skip it. An OPEN game (unset) lets players join and leave at any time, mid-game included
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
	Seat     int    `json:"seat"`            // the player's seat: the index every cell the player writes carries (game.Cell.PlayerIdx), and what their piece colour and spawn column follow. Unique within the game and STABLE — a seat is never renumbered when another player leaves, so an open game's roster can change mid-game. Listings written before the field carry zeros: NormalizedSeats reads those by roster position, as the game did then
	Team     int    `json:"team"`            // teams mode: 0 = A, 1 = B, …
	TeamSlot int    `json:"team_slot"`       // teams mode: section index within the team board (the spawn column), stable like Seat
	Agent    bool   `json:"agent,omitempty"` // roster seat taken by an agent player (e.g. golang-mk1)
}

// NormalizedSeats returns the roster with every seat filled in: the recorded
// seats when they are unique (every listing written since the field), and
// the roster positions otherwise — the seat assignment every game used before
// the field, so an old listing reads exactly as it played.
func (g GameListing) NormalizedSeats() []PlayerSummary {
	out := make([]PlayerSummary, len(g.Players))
	copy(out, g.Players)
	seen := make(map[int]bool, len(out))
	unique := true
	for _, p := range out {
		if seen[p.Seat] {
			unique = false
			break
		}
		seen[p.Seat] = true
	}
	if !unique {
		for i := range out {
			out[i].Seat = i
		}
	}
	return out
}

// Rules is the listing's mirror of the game's play rules.
func (g GameListing) Rules() config.GameRules {
	return config.GameRules{
		NextCount:          g.NextCount,
		Ghost:              !g.NoGhost,
		Hold:               g.Hold,
		Bag:                g.Bag,
		ShowHeadroom:       g.ShowHeadroom,
		GarbageHoles:       g.GarbageHoles,
		RandomGarbageHoles: g.RandomGarbageHoles,
		GuidelineGarbage:   g.GuidelineGarbage,
	}
}

// TeamName is what team t of this game is called — the listing's mirror of
// config.GameMeta.TeamName.
func (g GameListing) TeamName(t int) string { return config.TeamName(g.TeamNames, t) }

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
// the seats sharing a playfield — the listing's mirror of
// config.GameMeta.SplitsPieces, and the same rule: at least two seats on the
// playfield.
func (g GameListing) SplitsPieces() bool {
	return g.SplitPieces && g.SeatsPerPlayfield() > 1
}

// Playfields is how many boards this game is played on — the listing's
// mirror of config.GameMeta.Playfields.
func (g GameListing) Playfields() int {
	return g.spec().Playfields()
}

// SeatsPerPlayfield is how many seats share one board — the listing's mirror
// of config.GameMeta.SeatsPerPlayfield.
func (g GameListing) SeatsPerPlayfield() int {
	return g.spec().SeatsPerPlayfield()
}

// IndividualScoring reports whether the seats of this game's single shared
// playfield are scored on their own — the listing's mirror of
// config.GameMeta.IndividualScoring.
func (g GameListing) IndividualScoring() bool {
	return g.spec().IndividualScoring()
}

// Dynamic reports whether the game's roster can change at any time: an OPEN
// game — one not restricted to invited players — starts as soon as its
// present players are ready and lets anyone take a free seat or leave, before
// the start and mid-game alike. An invite-only game's roster is the invited
// set, frozen once the game starts.
func (g GameListing) Dynamic() bool {
	return !g.InviteOnly
}

// spec is the listing's structural settings as a config.GameSpec, for the
// derived-shape helpers shared with the meta.
func (g GameListing) spec() config.GameSpec {
	return config.GameSpec{
		Mode:         g.Mode,
		PlayerCount:  g.PlayerCount,
		TeamCount:    g.TeamCount,
		TeamSize:     g.TeamSize,
		TeamNames:    append([]string(nil), g.TeamNames...),
		ExtraColumns: g.ExtraColumns,
		ExtraRows:    g.ExtraRows,
		LineGoal:     g.LineGoal,
		Scoring:      g.Scoring,
		SplitPieces:  g.SplitPieces,
		MaxAgents:    g.MaxAgents,
		InviteOnly:   g.InviteOnly,
		Rules:        g.Rules(),
	}
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

// BoardHeight returns the height (headroom + visible rows) of the board this
// game plays on: the shared board of a cooperative or teams game (which the
// ExtraRows setting grows per seat), or the standard board each competitive
// player gets.
func (g GameListing) BoardHeight() int {
	switch g.Mode {
	case config.ModeCooperative:
		return config.SharedBoardHeight(g.PlayerCount, g.ExtraRows)
	case config.ModeTeams:
		return config.TeamBoardHeight(g.TeamSize, g.ExtraRows)
	default:
		return config.StandardHeight
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

// FreeSeat finds the lowest free seat of the game — and, in teams mode, the
// lowest free slot of the given team, the seat being the global index of
// that slot (team × TeamSize + slot). Seats are stable: a seat freed by a
// departure is the next one taken, and nobody else's seat moves. ok is
// false when the game (or the team) is full.
func (g GameListing) FreeSeat(team int) (seat, slot int, ok bool) {
	taken := make(map[int]bool, len(g.Players))
	for _, p := range g.NormalizedSeats() {
		taken[p.Seat] = true
	}
	if g.Mode == config.ModeTeams {
		if team < 0 || team >= g.Teams() {
			return 0, 0, false
		}
		size := max(g.TeamSize, 1)
		for slot := 0; slot < size; slot++ {
			if seat := team*size + slot; !taken[seat] {
				return seat, slot, true
			}
		}
		return 0, 0, false
	}
	for seat := 0; seat < g.PlayerCount; seat++ {
		if !taken[seat] {
			return seat, 0, true
		}
	}
	return 0, 0, false
}

// SeatOf returns the roster entry of a player, seat filled in.
func (g GameListing) SeatOf(playerID string) (PlayerSummary, bool) {
	for _, p := range g.NormalizedSeats() {
		if p.PlayerID == playerID {
			return p, true
		}
	}
	return PlayerSummary{}, false
}

// SeatedOn is how many players hold a seat on the given playfield: on the
// one shared board (playfield 0) everyone, on a team's board its members,
// on a competitive board (playfield = seat) its one player or nobody.
func (g GameListing) SeatedOn(playfield int) int {
	switch g.Mode {
	case config.ModeTeams:
		return g.TeamMemberCount(playfield)
	case config.ModeCompetitive:
		for _, p := range g.NormalizedSeats() {
			if p.Seat == playfield {
				return 1
			}
		}
		return 0
	default:
		return len(g.Players)
	}
}

// Started reports whether the listing says the game has left the waiting
// room: in progress, or over.
func (g GameListing) Started() bool {
	switch g.Status {
	case config.GameStatusCreated, config.GameStatusStarting:
		return false
	}
	return true
}

// ReadyToStart reports whether the table is ready for the countdown: for an
// invite game every seat filled and everyone ready — the roster is the
// invited set; for an open game everyone seated ready and every playfield
// with at least one player — the crew's one board trivially, every team's,
// every competitive board (one player each: the full roster) — the seats
// still free being anyone's to take, before the start and after it.
func (g GameListing) ReadyToStart() bool {
	if len(g.Players) == 0 {
		return false
	}
	for _, p := range g.Players {
		if !p.Ready {
			return false
		}
	}
	if !g.Dynamic() {
		return len(g.Players) >= g.PlayerCount
	}
	for pf := 0; pf < g.Playfields(); pf++ {
		if g.SeatedOn(pf) == 0 {
			return false
		}
	}
	return true
}

// ReadyBlocker names what keeps an open game from starting once everyone
// present is ready: a playfield with nobody on it. Empty when nothing does.
func (g GameListing) ReadyBlocker() string {
	if !g.Dynamic() {
		if n := g.PlayerCount - len(g.Players); n > 0 {
			return fmt.Sprintf("waiting for %d more", n)
		}
		return ""
	}
	for pf := 0; pf < g.Playfields(); pf++ {
		if g.SeatedOn(pf) == 0 {
			if g.Mode == config.ModeTeams {
				return "waiting for a player on Team " + g.TeamName(pf)
			}
			return "waiting for a player on every board"
		}
	}
	return ""
}
