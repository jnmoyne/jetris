package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	NATSContext  string
	NATSURL      string
	NATSUser     string
	NATSPassword string
	// PlayerName is a name given before the login screen is ever drawn
	// (--name, or the browser page's ?player=, which the join page sets):
	// the screen plays its own Play button with it and goes straight to the
	// lobby. Empty means the usual "type your name" login.
	PlayerName string
	// ServerLabel names NATSURL for the player (the browser page's ?name=):
	// the server browser's row shows it instead of "--server", and the lobby
	// header reads "<label> (<url>)" the way a favorite's does.
	ServerLabel string
	// ReplayGameID is a game whose replay the app opens as soon as it lands
	// in the lobby (--replay, or a share link's ?replay= — see
	// webdist.ReplayLink): the link to one particular recording. Empty
	// means the lobby as usual.
	ReplayGameID string
	// JoinGameID is an OPEN game the app takes a seat in as soon as it lands
	// in the lobby (--join, or a game's share link's ?game= — see
	// webdist.GameLink): the link into one particular game. Empty means the
	// lobby as usual. Without a PlayerName the login screen is still shown,
	// saying which game the link is for, and a blank name plays anonymously.
	JoinGameID       string
	RunEmbedded      bool   // run an in-process JetStream-enabled nats-server and connect to it
	EmbeddedHost     string // address the embedded server is advertised and dialed on ("" = auto-detected LAN IP); it always LISTENS on every interface, so this only overrides a wrong auto-detection
	EmbeddedPort     int    // port for the embedded server (0 = DefaultEmbeddedPort)
	EmbeddedWSPort   int    // port of its WebSocket listener, what the browser build dials (0 = DefaultEmbeddedWSPort)
	EmbeddedHTTPPort int    // port the browser build is served on, for the phones to open (0 = DefaultEmbeddedHTTPPort)
	EmbeddedName     string // what the LAN party's server is called: the lobby header's "<you> @ <name>", and the join link's label ("" = DefaultEmbeddedName)
}

// Embedded-server settings for the login screen's "LAN party mode (embedded NATS
// server)" option: the default ports the in-process server listens on — plain
// NATS for the desktop builds and agents, WebSocket for the browser build —
// and the one the browser build itself is served on, all on every interface
// (the player can override each in the picker); and the local directory
// holding the server's JetStream storage.
const (
	DefaultEmbeddedPort     = 4222
	DefaultEmbeddedWSPort   = 4223
	DefaultEmbeddedHTTPPort = 8080
	DefaultEmbeddedName     = "Jetris LAN Party nats server"
	EmbeddedStoreDir        = "jetstream-data"
)

// ValidatePlayerName checks that a player name is valid for use as a
// NATS subject token (and thus as a player ID). It rejects empty names,
// names longer than 32 characters, and names containing characters that
// are not allowed in NATS subject tokens.
func ValidatePlayerName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("player name cannot be empty")
	}
	if len(name) > 32 {
		return fmt.Errorf("player name cannot be longer than 32 characters")
	}
	for _, c := range name {
		switch {
		case c == '.' || c == ' ' || c == '*' || c == '>' || c == '\t' || c == '\n' || c == '\r' || c == 0:
			return fmt.Errorf("player name cannot contain %q", c)
		}
	}
	return nil
}

type GameMode int

const (
	ModeCooperative GameMode = iota
	ModeCompetitive
	ModeTeams
)

func (m GameMode) String() string {
	switch m {
	case ModeCooperative:
		return "cooperative"
	case ModeCompetitive:
		return "competitive"
	case ModeTeams:
		return "teams"
	default:
		return "unknown"
	}
}

// The number of teams a teams-mode game is played between. A game records
// its own count in GameMeta.TeamCount; two — Team A vs Team B — is the
// default and what every meta written before the field reads as. Team
// indices run 0..count-1 and are named by their letter (TeamLetter).
const (
	DefaultTeamCount = 2
	MinTeamCount     = 2
	MaxTeamCount     = 6
)

// NormalizeTeamCount reads a recorded team count: absent (the zero value, and
// every meta or listing written before the field) is the historical two, and
// anything out of range is clamped into MinTeamCount..MaxTeamCount.
func NormalizeTeamCount(n int) int {
	if n <= 0 {
		return DefaultTeamCount
	}
	return min(max(n, MinTeamCount), MaxTeamCount)
}

// MinPlayerCount is the fewest players a game of mode can be created for:
// the floor of the create wizard's seat count (players per team in teams
// mode) and of an agent's --players. A cooperative game can be played
// alone — one player on the standard 10-column board, playing for the high
// score (the record it competes for is the best solo co-op score, since
// co-op records are ranked per seat count) — and a team can be a team of
// one; a competitive game needs an opponent, since the last player standing
// wins.
func MinPlayerCount(mode GameMode) int {
	if mode == ModeCompetitive {
		return 2
	}
	return 1
}

// TeamLetter names a team index the way every screen shows it: A, B, C, …
// (the index itself once past the letters, which MaxTeamCount never allows).
func TeamLetter(team int) string {
	if team < 0 || team >= 26 {
		return strconv.Itoa(team)
	}
	return string(rune('A' + team))
}

// MaxTeamNameLen caps a team's name (GameMeta.TeamNames), in runes.
const MaxTeamNameLen = 16

// pieceColorNames are the seven piece types' colours in piece order — I, O,
// T, S, Z, J, L — the names a game's teams are called by unless the creator
// renames them (DefaultTeamNames).
var pieceColorNames = [...]string{"Cyan", "Yellow", "Purple", "Green", "Red", "Blue", "Orange"}

// DefaultTeamNames names n teams after the piece colours, in piece order:
// team 0 is Cyan (the I), team 1 Yellow (the O), and so on — the same on
// every peer, so a game created without names reads the same everywhere.
func DefaultTeamNames(n int) []string {
	names := make([]string, 0, n)
	for t := 0; t < n; t++ {
		names = append(names, pieceColorNames[t%len(pieceColorNames)])
	}
	return names
}

// TeamName is how a team is called: its recorded name when the game has one
// (names[t], set), else its letter (TeamLetter) — what every game before the
// names, and every screen that aggregates across games, calls it.
func TeamName(names []string, t int) string {
	if t >= 0 && t < len(names) {
		if name := strings.TrimSpace(names[t]); name != "" {
			return name
		}
	}
	return TeamLetter(t)
}

// NormalizeTeamNames reads the creator's team names for a game of n teams:
// exactly n of them, each trimmed and cut to MaxTeamNameLen runes, a blank
// one replaced by the team's default piece colour.
func NormalizeTeamNames(names []string, n int) []string {
	if n <= 0 {
		return nil
	}
	out := DefaultTeamNames(n)
	for t := 0; t < n && t < len(names); t++ {
		name := strings.TrimSpace(names[t])
		if r := []rune(name); len(r) > MaxTeamNameLen {
			name = strings.TrimSpace(string(r[:MaxTeamNameLen]))
		}
		if name != "" {
			out[t] = name
		}
	}
	return out
}

// Bag is a game's piece randomizer: how its sequence groups the seven piece
// types. The standard 7-bag — the Guideline's — deals the seven types
// once each, shuffled, seven pieces at a time; the double bag deals them
// twice each, fourteen at a time, so a stretch stays fair over a longer run
// but two of a kind can come back to back and a drought can last twice as
// long; and no bag at all draws every piece on its own, any type as likely as
// any other whatever came before — the old-school randomizer where three S's
// in a row and a forty-piece I drought are both fair game. One rule for every
// seat, chosen with the play rules (GameRules.Bag), stored on the meta
// (GameMeta.Bag) and read by every engine at Start — humans, spectators and
// agents draw the same sequence only if they draw it the same way.
type Bag string

const (
	BagSingle Bag = ""       // the standard 7-bag: the default, and every meta written before the field
	BagDouble Bag = "double" // the double bag: every fourteen pieces are two of each type, shuffled together
	BagNone   Bag = "none"   // no bag: every piece is an independent uniform draw
)

// Normalized reads a recorded bag: the two named kinds as themselves, and
// anything else — absent, the zero value, every meta written before the
// field — as the standard 7-bag.
func (b Bag) Normalized() Bag {
	switch b {
	case BagDouble, BagNone:
		return b
	default:
		return BagSingle
	}
}

// Label names the bag the way the lobby row tags it and the wizard lists it:
// "7-bag", "double bag" or "no bag".
func (b Bag) Label() string {
	switch b.Normalized() {
	case BagDouble:
		return "double bag"
	case BagNone:
		return "no bag"
	default:
		return "7-bag"
	}
}

// Scoring is how the players sharing ONE playfield are scored: together — the
// crew's single score, the cooperative game as it always was — or each on
// their own, the shared-board competitive game where everyone plays the same
// board but only their own locks count, and the top score at the end wins.
// Only a cooperative-mode board (a single playfield) has the choice; a
// multi-playfield game scores per playfield (teams) or per board
// (competitive) by construction. Stored on the meta (GameMeta.Scoring) and
// mirrored on the listing; absent — the zero value, and every meta written
// before the field — is the shared score.
type Scoring string

const (
	ScoringShared     Scoring = ""           // one score for the whole playfield: the default, and every meta written before the field
	ScoringIndividual Scoring = "individual" // every seat scored on its own locks; the top score wins
)

// Normalized reads a recorded scoring: individual as itself, anything else —
// absent, the zero value, every meta written before the field — as shared.
func (s Scoring) Normalized() Scoring {
	if s == ScoringIndividual {
		return ScoringIndividual
	}
	return ScoringShared
}

type GameStatus string

const (
	GameStatusCreated    GameStatus = "created"
	GameStatusStarting   GameStatus = "starting"
	GameStatusInProgress GameStatus = "in_progress"
	GameStatusFinished   GameStatus = "finished"
	GameStatusArchived   GameStatus = "archived"
	GameStatusCancelled  GameStatus = "cancelled"
)

type GameMeta struct {
	GameID             string     `json:"game_id"`
	Mode               GameMode   `json:"mode"`
	PlayerCount        int        `json:"player_count"`
	TeamCount          int        `json:"team_count,omitempty"`           // teams mode: how many teams play each other (MinTeamCount..MaxTeamCount). Absent — the zero value, and every meta written before the field — reads as DefaultTeamCount, the historical Team A vs Team B (see Teams)
	TeamSize           int        `json:"team_size,omitempty"`            // teams mode: players per team (PlayerCount = TeamCount*TeamSize)
	TeamNames          []string   `json:"team_names,omitempty"`           // teams mode: what each team is called, by index (MaxTeamNameLen runes at most) — the piece colours (DefaultTeamNames) unless the creator renamed them; absent — every meta written before the field — reads as the letters A, B, C… (TeamName)
	ExtraColumns       int        `json:"extra_columns,omitempty"`        // shared boards (cooperative, teams): columns every seat beyond the first adds to the board's standard 10 (MinExtraColumns..MaxExtraColumns), and the spacing between neighbouring spawn points. Absent — every meta written before the field — reads as MaxExtraColumns: the historical full section per player (see ExtraColumnsPerPlayer). Meaningless in competitive, where each player has a board of their own
	NextCount          int        `json:"next_count"`                     // how many upcoming pieces are shown (0..MaxNextCount); bounds lookahead for humans and agents alike
	NoGhost            bool       `json:"no_ghost,omitempty"`             // hard-drop ghost preview disabled for this game; inverted so the zero value — and metas written before the field — keep the ghost SHOWN (the default). Meta, not listing: like NextCount it is one rule for every player
	Hold               bool       `json:"hold,omitempty"`                 // the Guideline hold queue is on: a player may swap the falling piece for a held one, once per piece, the piece coming out re-entering at the spawn point (see GameRules.Hold). Unset — the default, and every meta written before the field — no hold. One rule for every seat, like NextCount; agents may use it or ignore it
	GarbageHoles       int        `json:"garbage_holes,omitempty"`        // holes punched in every garbage row a raise lands (0..MaxGarbageHoles; competitive/teams). 0 — the zero value, and every meta written before the field — raises solid rows that never clear; with holes, a garbage row clears like any other line once its holes are filled
	RandomGarbageHoles bool       `json:"random_garbage_holes,omitempty"` // every garbage row draws its own hole columns ("messy" garbage); unset — the default, and every meta written before the field — every row of one raise shares a single draw, so its holes line up into a well ("clean" garbage). Moot at GarbageHoles 0
	GuidelineGarbage   bool       `json:"guideline_garbage,omitempty"`    // attack strength follows the Guideline table — a single sends no garbage, a double 1 row, a triple 2, a Ketris 4 (game.AttackRows); unset — the default, and every meta written before the field — every cleared line sends one row
	SplitPieces        bool       `json:"split_pieces,omitempty"`         // teams mode: the seven piece types are dealt out between the teammates (rng.PieceSets), every seat drawing only from its own ration and the whole bag present across the team. Unset — the default, and every meta written before the field — every seat runs the full 7-bag. Structural like TeamSize, not a play rule: the deal follows Seed, so both teams' slot N hold the same ration (see SplitsPieces)
	Bag                Bag        `json:"bag,omitempty"`                  // the piece randomizer every seat's sequence is drawn with (Bag): "double" for the double bag, "none" for no bag at all; unset — the default, and every meta written before the field — the standard 7-bag. One rule for every seat, like NextCount; in a split-pieces game it shapes each seat's ration the same way (a double bag of the ration, or independent draws from it)
	ShowHeadroom       bool       `json:"show_headroom,omitempty"`        // the hidden headroom rows above the playfield (HeadroomRows, where a piece spawns) are drawn — behind smoked glass, so they read as the out-of-bounds they are — on every board of the game; unset — the default, and every meta written before the field — the boards start at the visible playfield, the Guideline way. Presentation only: nothing about play changes, and agents may ignore it. One setting for every seat and spectator, like NoGhost
	ExtraRows          int        `json:"extra_rows,omitempty"`           // shared boards (cooperative, teams): rows every seat beyond the first adds to the board's standard VisibleRows (MinExtraRows..MaxExtraRows) — the board grows downwards, the headroom and the spawn rows stay where they are (see SharedBoardHeight). Absent — the zero value, and every meta written before the field — adds nothing: the standard 20-row playfield whatever the seat count. Meaningless in competitive, like ExtraColumns
	LineGoal           int        `json:"line_goal,omitempty"`            // the game's length in lines: the game ends the moment a playfield has cleared this many lines in total — on a single playfield the game is over (the crew is done, or in individual scoring the top score wins); across several the first playfield there wins. Absent — the zero value, and every meta written before the field — the game runs until someone tops out (NormalizeLineGoal)
	Scoring            Scoring    `json:"scoring,omitempty"`              // how the seats of a single shared playfield are scored (Scoring): "individual" for every seat on its own; absent — the default, and every meta written before the field — the crew's one shared score. Only meaningful on a cooperative-mode board with more than one seat (IndividualScoring)
	Seed               uint64     `json:"seed"`
	Status             GameStatus `json:"status"`
	CreatorID          string     `json:"creator_id"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          time.Time  `json:"started_at,omitempty"`
	FinishedAt         time.Time  `json:"finished_at,omitempty"`
	Abandoned          bool       `json:"abandoned,omitempty"`
	PieceIdx           uint64     `json:"piece_idx"`
}

// GameRules are the per-game PLAY rules a creator picks in the create wizard,
// bundled for the create paths (lobby.CreateGame and the UI's createGame /
// openInvitePicker): every field lands in GameMeta — the rule book every
// engine reads at Start — and is mirrored on the lobby listing for the row's
// tags. The zero value is the pre-attribute Jetris game (no preview, no hold,
// the 7-bag, solid garbage, one row per line, the headroom out of sight)
// except for Ghost, which the wizard defaults on — the meta stores it
// inverted (NoGhost) for the same reason.
type GameRules struct {
	NextCount          int  // upcoming pieces the game reveals (0..MaxNextCount)
	Ghost              bool // the hard-drop ghost preview (GameMeta.NoGhost, inverted)
	Hold               bool // the Guideline hold queue (GameMeta.Hold): swap the falling piece for a held one, once per piece
	Bag                Bag  // the piece randomizer (GameMeta.Bag): the standard 7-bag, the double bag, or no bag at all
	ShowHeadroom       bool // the hidden headroom rows drawn above the playfield, behind smoked glass (GameMeta.ShowHeadroom; off: the boards start at the visible playfield)
	GarbageHoles       int  // holes per garbage row in the modes that raise garbage (0..MaxGarbageHoles; 0 = solid rows that never clear)
	RandomGarbageHoles bool // every garbage row draws its own hole columns (off: the rows of one attack share a draw)
	GuidelineGarbage   bool // attack strength by the Guideline table — 0/1/2/4 rows for 1/2/3/4 lines (off: one row per line)
}

// GuidelineRules is the create wizard's "Guideline" preset: every rule at the
// setting closest to the Guideline this game can offer — the longest
// next queue the game reveals (MaxNextCount), the ghost piece, the hold queue,
// the standard 7-bag (BagSingle, the zero value), the headroom rows hidden
// (ShowHeadroom off: the Guideline playfield shows twenty rows and no more),
// and Guideline-style garbage: one hole per row, the rows of one attack
// sharing it (clean garbage that digs out as a well), attacks by the
// Guideline table.
func GuidelineRules() GameRules {
	return GameRules{
		NextCount:        MaxNextCount,
		Ghost:            true,
		Hold:             true,
		GarbageHoles:     1,
		GuidelineGarbage: true,
	}
}

// Normalized returns the rules clamped to their legal ranges as a game of
// mode stores them: NextCount and GarbageHoles within their caps, the bag one
// of the kinds there are, random holes only meaningful with holes — and,
// since a cooperative game raises no garbage, its garbage rules zeroed so no
// listing tag misleads.
func (r GameRules) Normalized(mode GameMode) GameRules {
	r.NextCount = min(max(r.NextCount, 0), MaxNextCount)
	r.Bag = r.Bag.Normalized()
	if mode == ModeCooperative {
		r.GarbageHoles, r.RandomGarbageHoles, r.GuidelineGarbage = 0, false, false
	}
	r.GarbageHoles = min(max(r.GarbageHoles, 0), MaxGarbageHoles)
	r.RandomGarbageHoles = r.RandomGarbageHoles && r.GarbageHoles > 0
	return r
}

// IsGuideline reports whether the rules are exactly the Guideline preset as a
// game of mode stores it (a cooperative game has no garbage rules to match).
func (r GameRules) IsGuideline(mode GameMode) bool {
	return r.Normalized(mode) == GuidelineRules().Normalized(mode)
}

// Rules is the play-rule bundle of a game's meta record.
func (m GameMeta) Rules() GameRules {
	return GameRules{
		NextCount:          m.NextCount,
		Ghost:              !m.NoGhost,
		Hold:               m.Hold,
		Bag:                m.Bag,
		ShowHeadroom:       m.ShowHeadroom,
		GarbageHoles:       m.GarbageHoles,
		RandomGarbageHoles: m.RandomGarbageHoles,
		GuidelineGarbage:   m.GuidelineGarbage,
	}
}

// Teams is the number of teams this game is played between, normalized:
// a teams game's recorded TeamCount (two when the meta predates the field),
// and 0 in every other mode, which has no teams at all.
func (m GameMeta) Teams() int {
	if m.Mode != ModeTeams {
		return 0
	}
	return NormalizeTeamCount(m.TeamCount)
}

// SplitsPieces reports whether this game deals its piece types out between
// the seats sharing a playfield: the SplitPieces setting, which only a
// playfield of at least two seats can honour (a seat alone would be dealt the
// whole bag anyway). The one place the rule is decided — engines, the lobby
// row and the HUD all ask here.
func (m GameMeta) SplitsPieces() bool {
	return m.SplitPieces && m.SeatsPerPlayfield() > 1
}

// Playfields is how many boards the game is played on: the one shared board
// of a cooperative game, one per team in teams mode, one per player in
// competitive.
func (m GameMeta) Playfields() int {
	return playfields(m.Mode, m.PlayerCount, m.TeamCount)
}

// SeatsPerPlayfield is how many seats share one board: every seat in
// cooperative, a team's in teams mode, a single one in competitive.
func (m GameMeta) SeatsPerPlayfield() int {
	return seatsPerPlayfield(m.Mode, m.PlayerCount, m.TeamSize)
}

// BoardWidth is the width of the board(s) this game plays on: the shared
// board's (widened per seat by ExtraColumns) or the standard 10 columns of a
// competitive player's own board.
func (m GameMeta) BoardWidth() int {
	if m.Mode == ModeCompetitive {
		return StandardWidth
	}
	return SharedBoardWidth(m.SeatsPerPlayfield(), m.ExtraColumns)
}

// BoardHeight is the height (headroom + visible rows) of the board(s) this
// game plays on: the shared board's, grown per seat by ExtraRows, or the
// standard height of a competitive player's own board.
func (m GameMeta) BoardHeight() int {
	if m.Mode == ModeCompetitive {
		return StandardHeight
	}
	return SharedBoardHeight(m.SeatsPerPlayfield(), m.ExtraRows)
}

// IndividualScoring reports whether the seats of this game's single shared
// playfield are scored on their own (Scoring): the setting, which only a
// cooperative-mode board with more than one seat can honour — a seat alone
// has nobody to rank against, and every other mode scores per playfield or
// per board already.
func (m GameMeta) IndividualScoring() bool {
	return m.Scoring.Normalized() == ScoringIndividual && m.Mode == ModeCooperative && m.PlayerCount > 1
}

// TeamName is what team t of this game is called (see TeamName).
func (m GameMeta) TeamName(t int) string { return TeamName(m.TeamNames, t) }

// Spec is the creation spec this meta was (or could have been) created from:
// the structural settings and the play rules, without the lobby-only agent
// policy and invitation setting (which the listing carries).
func (m GameMeta) Spec() GameSpec {
	return GameSpec{
		Mode:         m.Mode,
		PlayerCount:  m.PlayerCount,
		TeamCount:    m.TeamCount,
		TeamSize:     m.TeamSize,
		TeamNames:    append([]string(nil), m.TeamNames...),
		ExtraColumns: m.ExtraColumns,
		ExtraRows:    m.ExtraRows,
		LineGoal:     m.LineGoal,
		Scoring:      m.Scoring,
		SplitPieces:  m.SplitPieces,
		Rules:        m.Rules(),
	}
}

func playfields(mode GameMode, playerCount, teamCount int) int {
	switch mode {
	case ModeCompetitive:
		return max(playerCount, 1)
	case ModeTeams:
		return NormalizeTeamCount(teamCount)
	default:
		return 1
	}
}

func seatsPerPlayfield(mode GameMode, playerCount, teamSize int) int {
	switch mode {
	case ModeCompetitive:
		return 1
	case ModeTeams:
		return max(teamSize, 1)
	default:
		return max(playerCount, 1)
	}
}

// GameSpec is everything a creator decides about a game, bundled for the one
// create path (lobby.CreateGame) and the screens that feed it (the create
// wizard, the invitee picker, the headless player): the game's shape — its
// mode, how many seats, how many playfields and how many seats share one,
// how the shared boards grow per seat, how the seats are scored, whether the
// pieces are dealt out between seatmates — its length in lines, the lobby's
// agent policy and invitation setting, and the play rules (GameRules). The
// meta records everything but the lobby-only MaxAgents, AgentsPauseAlone and
// InviteOnly, which the listing carries. Normalized clamps every field to
// what the mode can honour; Meta writes the meta record.
//
// The wizard's game types map onto the modes like this: a single playfield is
// a cooperative-mode board — shared score, or every seat scored on its own
// (Scoring) — and several playfields are teams mode when seats share them
// (TeamCount boards of TeamSize seats) or competitive when every player has
// a board of their own.
type GameSpec struct {
	Mode             GameMode
	PlayerCount      int      // seats in the game: the roster's size for an invite game, its MAXIMUM for an open one (every seat beyond the present players stays free to join)
	TeamCount        int      // teams: how many boards (MinTeamCount..MaxTeamCount); 0 elsewhere
	TeamSize         int      // teams: seats per board (PlayerCount = TeamCount*TeamSize); 0 elsewhere
	TeamNames        []string // teams: what each board's team is called (NormalizeTeamNames: the piece colours unless renamed); nil elsewhere
	ExtraColumns     int      // shared boards: columns per seat beyond the first (MinExtraColumns..MaxExtraColumns)
	ExtraRows        int      // shared boards: rows per seat beyond the first (MinExtraRows..MaxExtraRows)
	LineGoal         int      // the game's length in lines (0 = until top out; see GameMeta.LineGoal)
	Scoring          Scoring  // single playfield: shared or individual scores
	SplitPieces      bool     // the seven piece types dealt out between the seats of a playfield
	MaxAgents        int      // lobby: how many seats idle agent players may take — on EACH team of a teams game, in the whole game elsewhere (0 = none; see AgentPolicySeats)
	AgentsPauseAlone bool     // lobby, open games: an agent left as the only player in the game stops playing, keeping its seat, until someone joins; unset, agents play on — alone, or among themselves
	InviteOnly       bool     // lobby: only invited players (and the creator) may join; otherwise the game is open, and its roster changes at any time
	Rules            GameRules
}

// Normalized returns the spec clamped to what its mode can honour — the one
// place a game's settings are made legal: team count and size only in teams
// mode; the agent policy within the seats it counts over (a team's, or the
// game's) and the pause-when-alone rule only in an open game; the
// shared-board growth, the deal and the scoring only where there is a
// shared board with seats to share it; the line goal and the play rules
// within their ranges.
func (s GameSpec) Normalized() GameSpec {
	if s.Mode == ModeTeams {
		s.TeamCount = NormalizeTeamCount(s.TeamCount)
		s.TeamSize = max(s.TeamSize, 1)
		s.PlayerCount = s.TeamCount * s.TeamSize
		s.TeamNames = NormalizeTeamNames(s.TeamNames, s.TeamCount)
	} else {
		s.TeamCount, s.TeamSize, s.TeamNames = 0, 0, nil
		s.PlayerCount = max(s.PlayerCount, 1)
	}
	s.MaxAgents = min(max(s.MaxAgents, 0), s.AgentPolicySeats())
	// Only an open game's roster can dwindle to one player — an invite
	// game's is frozen once it starts — so only an open game has agents
	// pause when left alone.
	s.AgentsPauseAlone = s.AgentsPauseAlone && !s.InviteOnly
	s.Rules = s.Rules.Normalized(s.Mode)
	// Only a shared board has a width and a height to set; competitive
	// boards are always the standard board, so its meta records no setting.
	if s.Mode == ModeCompetitive {
		s.ExtraColumns, s.ExtraRows = 0, 0
	} else {
		if s.ExtraColumns > 0 {
			s.ExtraColumns = ExtraColumnsPerPlayer(s.ExtraColumns)
		}
		s.ExtraRows = ExtraRowsPerPlayer(s.ExtraRows)
	}
	// Only a playfield with seatmates has pieces to split between them;
	// anywhere else the setting would deal the whole bag to everybody, so it
	// is not recorded.
	s.SplitPieces = s.SplitPieces && s.SeatsPerPlayfield() > 1
	// Only a single shared playfield with company chooses how it is scored.
	s.Scoring = s.Scoring.Normalized()
	if s.Mode != ModeCooperative || s.PlayerCount < 2 {
		s.Scoring = ScoringShared
	}
	s.LineGoal = NormalizeLineGoal(s.LineGoal)
	return s
}

// AgentPolicySeats is how many seats the agent policy (MaxAgents) counts
// over: a team's in teams mode — the cap is PER TEAM there, so a creator can
// seat an agent on every side — and the whole game's elsewhere, where there
// are no teams to tell apart.
func (s GameSpec) AgentPolicySeats() int {
	if s.Mode == ModeTeams {
		return max(s.TeamSize, 1)
	}
	return max(s.PlayerCount, 1)
}

// Playfields is how many boards the game is played on (see GameMeta.Playfields).
func (s GameSpec) Playfields() int {
	return playfields(s.Mode, s.PlayerCount, s.TeamCount)
}

// SeatsPerPlayfield is how many seats share one board (see GameMeta.SeatsPerPlayfield).
func (s GameSpec) SeatsPerPlayfield() int {
	return seatsPerPlayfield(s.Mode, s.PlayerCount, s.TeamSize)
}

// SplitsPieces reports whether the game deals its piece types out between
// seatmates (see GameMeta.SplitsPieces).
func (s GameSpec) SplitsPieces() bool {
	return s.SplitPieces && s.SeatsPerPlayfield() > 1
}

// IndividualScoring reports whether the seats of the single shared playfield
// are scored on their own (see GameMeta.IndividualScoring).
func (s GameSpec) IndividualScoring() bool {
	return s.Scoring.Normalized() == ScoringIndividual && s.Mode == ModeCooperative && s.PlayerCount > 1
}

// Meta is the meta record a game created from this (normalized) spec starts
// with: every setting the engines read at Start, the seed the sequences are
// drawn from, and the created status.
func (s GameSpec) Meta(gameID, creatorID string, seed uint64, now time.Time) GameMeta {
	return GameMeta{
		GameID:             gameID,
		Mode:               s.Mode,
		PlayerCount:        s.PlayerCount,
		TeamCount:          s.TeamCount,
		TeamSize:           s.TeamSize,
		TeamNames:          append([]string(nil), s.TeamNames...),
		ExtraColumns:       s.ExtraColumns,
		ExtraRows:          s.ExtraRows,
		LineGoal:           s.LineGoal,
		Scoring:            s.Scoring,
		NextCount:          s.Rules.NextCount,
		NoGhost:            !s.Rules.Ghost,
		Hold:               s.Rules.Hold,
		GarbageHoles:       s.Rules.GarbageHoles,
		RandomGarbageHoles: s.Rules.RandomGarbageHoles,
		GuidelineGarbage:   s.Rules.GuidelineGarbage,
		SplitPieces:        s.SplitPieces,
		Bag:                s.Rules.Bag,
		ShowHeadroom:       s.Rules.ShowHeadroom,
		Seed:               seed,
		Status:             GameStatusCreated,
		CreatorID:          creatorID,
		CreatedAt:          now,
	}
}

// PlayerResult captures per-player stats at game end.
type PlayerResult struct {
	PlayerID   string `json:"player_id"`
	Score      int    `json:"score"`
	Level      int    `json:"level,omitempty"` // level achieved at game end (from the player's line total)
	Lines      int    `json:"lines,omitempty"` // lines the player's own pieces cleared (absent in records written before the field)
	PieceCount uint64 `json:"piece_count"`

	Winner bool `json:"winner,omitempty"`
	Team   int  `json:"team,omitempty"`  // teams mode: 0 = A, 1 = B
	Agent  bool `json:"agent,omitempty"` // seat was played by an agent (from the roster at archive time)
}

// ArchiveRecord is published to the archive stream when a game finishes.
type ArchiveRecord struct {
	GameID       string         `json:"game_id"`
	Mode         GameMode       `json:"mode"`
	PlayerCount  int            `json:"player_count"`
	Players      []PlayerResult `json:"players"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	TotalScore   int            `json:"total_score,omitempty"`   // cooperative
	FinalLevel   int            `json:"final_level,omitempty"`   // cooperative: shared level at game end
	TeamCount    int            `json:"team_count,omitempty"`    // teams mode: how many teams played (GameMeta.TeamCount); absent — every record written before the field — reads as DefaultTeamCount (see Teams)
	TeamSize     int            `json:"team_size,omitempty"`     // teams mode
	TeamNames    []string       `json:"team_names,omitempty"`    // teams mode: what each team was called (GameMeta.TeamNames); absent = the letters
	ExtraColumns int            `json:"extra_columns,omitempty"` // shared boards: columns per seat beyond the first (GameMeta.ExtraColumns) — what the replay rebuilds the board's width from
	ExtraRows    int            `json:"extra_rows,omitempty"`    // shared boards: rows per seat beyond the first (GameMeta.ExtraRows)
	LineGoal     int            `json:"line_goal,omitempty"`     // the game's length in lines (GameMeta.LineGoal); absent = played until a top-out
	Scoring      Scoring        `json:"scoring,omitempty"`       // single playfield: how its seats were scored (GameMeta.Scoring); "individual" ranks by each player's own score, and never against shared-score co-op runs (SameReplayBucket)
	BoardRows    int            `json:"board_rows,omitempty"`    // the boards' height (headroom + visible) the game was played on, as the archiver knew it; the replay reads the height off the replay stream itself and keeps this as its fallback (see BoardHeight)
	WinningTeam  int            `json:"winning_team"`            // teams mode: the winning team's index; -1 = draw or not a team game
	TeamScores   []int          `json:"team_scores,omitempty"`   // teams mode: final score per team (indexed by team)
	TeamLevels   []int          `json:"team_levels,omitempty"`   // teams mode: final level per team (indexed by team)
	Boards       []BoardPicture `json:"boards,omitempty"`        // end-of-game playfield snapshot(s) for the lobby's history view
	Chat         []ChatLine     `json:"chat,omitempty"`          // the game's chat history (last ArchiveChatCap lines), captured before the chat purge
}

// Teams is the number of teams the archived game was played between,
// normalized the way GameMeta.Teams is: the recorded TeamCount, falling back
// to the length of the per-team totals for a record written before the field
// (and to DefaultTeamCount for one that carries neither), and 0 outside teams
// mode.
func (r ArchiveRecord) Teams() int {
	if r.Mode != ModeTeams {
		return 0
	}
	if r.TeamCount <= 0 && len(r.TeamScores) > 0 {
		return NormalizeTeamCount(len(r.TeamScores))
	}
	return NormalizeTeamCount(r.TeamCount)
}

// TeamName is what team t of the archived game was called (see TeamName).
func (r ArchiveRecord) TeamName(t int) string { return TeamName(r.TeamNames, t) }

// BoardHeight is the total rows (headroom + visible) the record SAYS the
// game's boards were played on: what the archiver wrote, or today's board for
// a record from before the field. It is a fallback, not the truth: a game's
// replay stream still holds every cell subject the game ever wrote, and the
// replay measures its boards off those (nats.ReplayBoardHeight) — the height
// a game was played on is not a function of how many played it, and nothing
// here pretends it is. This stands in only while the stream has not answered
// yet, or for a recording with no cells at all.
func (r ArchiveRecord) BoardHeight() int {
	if r.BoardRows > 0 {
		return r.BoardRows
	}
	return TotalRows
}

// ChatLine is one chat message preserved in an ArchiveRecord. The game's chat
// is purged from the shared chat stream when the game is archived, so the
// archiver copies the conversation into the record first — it is the only
// place the history survives for the lobby's archived-game viewer. (Records
// from before this field simply have no chat.)
type ChatLine struct {
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"ts,omitempty"`
	Spectator bool      `json:"spectator,omitempty"`
}

// ArchiveChatCap bounds the chat lines embedded in one ArchiveRecord (the
// most recent lines win; matches the in-memory lobby chat cap so nothing a
// client still holds is ever dropped).
const ArchiveChatCap = 200

// Replay retention. A finished game keeps a full replay archive (its game
// stream copied to the file-backed replay stream before deletion) while it is
// in the KEEP SET — see ReplayKeepSet — which is the union of:
//
//   - the top ReplayTopN games of its replay bucket — one bucket per (mode,
//     with/without agents) pair, ranked by RankBefore — the all-time
//     showcase; and
//   - the ReplayRecentN most recently finished games overall (RecentBefore),
//     so everyone can watch their own game again right after playing it even
//     when it never ranked.
//
// Every finishing game is by definition among the most recent, so every game
// gets a replay at first; it survives the next ReplayRecentN finishes only by
// ranking in its bucket's top N. Games that fall out of both sets lose their
// replay (the archiver purges them — see archive.maybeArchiveReplay) —
// unless someone PINNED it: a pin (a LobbyPinKey entry in the lobby KV, see
// ReplayPin) keeps a replay for good, whatever its rank or age, until the
// pin is removed.
const (
	ReplayTopN    = 10
	ReplayRecentN = 25
)

// IndividualScoring reports whether the archived game's single playfield
// scored every seat on its own (see GameMeta.IndividualScoring).
func (r ArchiveRecord) IndividualScoring() bool {
	return r.Scoring.Normalized() == ScoringIndividual && r.Mode == ModeCooperative
}

// HeadlineScore is the score a finished game is ranked (and listed) by: the
// shared total for cooperative, the best team's total for teams, and the best
// player's score for competitive — and for a shared board whose seats were
// scored on their own.
func (r ArchiveRecord) HeadlineScore() int {
	switch r.Mode {
	case ModeCooperative:
		if !r.IndividualScoring() {
			return r.TotalScore
		}
	case ModeTeams:
		if len(r.TeamScores) > 0 {
			best := r.TeamScores[0]
			for _, s := range r.TeamScores[1:] {
				if s > best {
					best = s
				}
			}
			return best
		}
	}
	best := 0
	for _, p := range r.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

// Duration is how long the game lasted; zero for records missing either
// timestamp (or with a clock skew that made finish precede start).
func (r ArchiveRecord) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	if d := r.FinishedAt.Sub(r.StartedAt); d > 0 {
		return d
	}
	return 0
}

// RankBefore reports whether r outranks o in the "By score" game ordering
// shared by the lobby's history list and the replay archiver's top-N cut:
// higher headline score first, a shorter game breaking a score tie, then the
// more recent finish — with the game ID as a final total-order tie-break so
// every client computes the identical top N.
func (r ArchiveRecord) RankBefore(o ArchiveRecord) bool {
	if si, sj := r.HeadlineScore(), o.HeadlineScore(); si != sj {
		return si > sj
	}
	if di, dj := r.Duration(), o.Duration(); di != dj {
		return di < dj
	}
	if !r.FinishedAt.Equal(o.FinishedAt) {
		return r.FinishedAt.After(o.FinishedAt)
	}
	return r.GameID < o.GameID
}

// RecentBefore reports whether r finished more recently than o — the "most
// recent games" total order (finish time, newest first; game ID as the
// tie-break so every archiver cuts the identical ReplayRecentN).
func (r ArchiveRecord) RecentBefore(o ArchiveRecord) bool {
	if !r.FinishedAt.Equal(o.FinishedAt) {
		return r.FinishedAt.After(o.FinishedAt)
	}
	return r.GameID < o.GameID
}

// SameReplayBucket reports whether two records compete for the same replay
// top-N: same mode, the same scoring (a shared board's individual scores
// never rank against a crew's shared total), and both with (or both without)
// agent seats.
func (r ArchiveRecord) SameReplayBucket(o ArchiveRecord) bool {
	return r.replayBucket() == o.replayBucket()
}

// replayBucketKey identifies a record's replay bucket (see SameReplayBucket).
type replayBucketKey struct {
	mode       GameMode
	individual bool
	agents     bool
}

func (r ArchiveRecord) replayBucket() replayBucketKey {
	return replayBucketKey{r.Mode, r.IndividualScoring(), r.HasAgents()}
}

// uniqueRecords collapses recs to one record per game ID (the last occurrence
// wins, matching an archive-stream drain where a re-published record
// supersedes the earlier copy), in first-seen order.
func uniqueRecords(recs []ArchiveRecord) []ArchiveRecord {
	idx := make(map[string]int, len(recs))
	out := make([]ArchiveRecord, 0, len(recs))
	for _, r := range recs {
		if r.GameID == "" {
			continue
		}
		if i, dup := idx[r.GameID]; dup {
			out[i] = r
			continue
		}
		idx[r.GameID] = len(out)
		out = append(out, r)
	}
	return out
}

// rankedReplayBuckets groups recs (one record per game ID) into their replay
// buckets, each ordered by RankBefore.
func rankedReplayBuckets(recs []ArchiveRecord) map[replayBucketKey][]ArchiveRecord {
	buckets := make(map[replayBucketKey][]ArchiveRecord)
	for _, r := range uniqueRecords(recs) {
		k := r.replayBucket()
		buckets[k] = append(buckets[k], r)
	}
	for _, b := range buckets {
		sort.SliceStable(b, func(i, j int) bool { return b[i].RankBefore(b[j]) })
	}
	return buckets
}

// ReplayTopRanked returns the game IDs that rank in the top ReplayTopN of
// their replay bucket among recs — the first half of the replay keep set.
func ReplayTopRanked(recs []ArchiveRecord) map[string]bool {
	top := make(map[string]bool)
	for _, b := range rankedReplayBuckets(recs) {
		for i, r := range b {
			if i >= ReplayTopN {
				break
			}
			top[r.GameID] = true
		}
	}
	return top
}

// ReplayTopRankedCut returns the game IDs the history marks as TOP 10: the
// top ReplayTopN of every bucket that holds MORE than ReplayTopN games. In a
// bucket of ten or fewer every game is trivially in its top ten — the mark
// would light up the whole list and say nothing — so it appears only once
// the ranking is an actual cut, for the games that beat others to stay in.
// (Retention uses ReplayTopRanked, where the trivial case is exactly right.)
func ReplayTopRankedCut(recs []ArchiveRecord) map[string]bool {
	top := make(map[string]bool)
	for _, b := range rankedReplayBuckets(recs) {
		if len(b) <= ReplayTopN {
			continue
		}
		for _, r := range b[:ReplayTopN] {
			top[r.GameID] = true
		}
	}
	return top
}

// ReplayRank is where rec stands in the "By score" ranking of its replay
// bucket (same mode, with/without agents) among recs — the order behind the
// history's TOP 10 mark and the replay top-N cut: rank 1 is the bucket's best
// game, of is the bucket's size. rec itself always counts, whether or not
// recs already holds it (its archive round-trip may still be in flight), so
// a game is ranked against everything before it plus itself.
func ReplayRank(recs []ArchiveRecord, rec ArchiveRecord) (rank, of int) {
	rank, of = 1, 1
	for _, r := range uniqueRecords(recs) {
		if r.GameID == rec.GameID || !r.SameReplayBucket(rec) {
			continue
		}
		of++
		if r.RankBefore(rec) {
			rank++
		}
	}
	return rank, of
}

// ReplayRecent returns the game IDs of the ReplayRecentN most recently
// finished games among recs (all buckets together) — the second half of the
// replay keep set.
func ReplayRecent(recs []ArchiveRecord) map[string]bool {
	all := uniqueRecords(recs)
	sort.SliceStable(all, func(i, j int) bool { return all[i].RecentBefore(all[j]) })
	recent := make(map[string]bool, ReplayRecentN)
	for i, r := range all {
		if i >= ReplayRecentN {
			break
		}
		recent[r.GameID] = true
	}
	return recent
}

// ReplayKeepSet returns the game IDs whose replay is retained given the full
// set of archive records and the set of pinned games: ReplayTopRanked ∪
// ReplayRecent ∪ pinned. A pure function of the records and the pins, so
// every archiver — GUI or agent — computes the same set and purges the same
// displaced replays. pinned may be nil (no pins).
func ReplayKeepSet(recs []ArchiveRecord, pinned map[string]bool) map[string]bool {
	keep := ReplayTopRanked(recs)
	for id := range ReplayRecent(recs) {
		keep[id] = true
	}
	for id, on := range pinned {
		if on {
			keep[id] = true
		}
	}
	return keep
}

// ReplayPin is the value of a pinned replay's lobby KV entry (LobbyPinKey):
// the pin itself is the key's existence — the value only says who pinned
// the game and when, for the record. Unpinning deletes the key.
type ReplayPin struct {
	GameID   string    `json:"game_id"`
	PinnedBy string    `json:"pinned_by,omitempty"` // the pinning player's name
	PinnedAt time.Time `json:"pinned_at"`
}

// HasAgents reports whether any seat in the archived game was played by an
// agent (records from before the agent flag simply read as all-human).
func (r ArchiveRecord) HasAgents() bool {
	for _, p := range r.Players {
		if p.Agent {
			return true
		}
	}
	return false
}

// Agent-composition classes for the history's primary sort order, most
// "interesting" first: a pure agent-vs-agent match, then a mixed human/agent
// game, then an all-human game.
const (
	AgentClassAgentsOnly = iota // every seat an agent
	AgentClassMixed             // at least one agent and at least one human
	AgentClassHumansOnly        // no agent seats (also empty/legacy rosters)
)

// AgentClass buckets the game by who played it (AgentClassAgentsOnly <
// AgentClassMixed < AgentClassHumansOnly) so the history can group agent
// showcases ahead of mixed games ahead of all-human games.
func (r ArchiveRecord) AgentClass() int {
	agents, humans := 0, 0
	for _, p := range r.Players {
		if p.Agent {
			agents++
		} else {
			humans++
		}
	}
	switch {
	case agents > 0 && humans == 0:
		return AgentClassAgentsOnly
	case agents > 0:
		return AgentClassMixed
	default:
		return AgentClassHumansOnly
	}
}

// BoardPicture is a saved snapshot of one board as it stood when the game
// ended: the latest cell messages from the (now-deleted) game stream for the
// board's visible region. It is embedded in an ArchiveRecord so the lobby can
// redraw the final playfield. There is one picture for cooperative, one per
// player for competitive, and one per team for teams mode.
type BoardPicture struct {
	Label  string      `json:"label,omitempty"` // player ID, "Team A"/"Team B"/…, or "" (cooperative)
	Idx    int         `json:"idx"`             // player/team index for coloring; -1 if not applicable
	Width  int         `json:"w"`               // board width in cells
	Height int         `json:"h"`               // visible row count stored (row 0 = first visible row)
	Cells  []BoardCell `json:"cells,omitempty"` // sparse: only the non-empty cells
}

// BoardCell is one non-empty cell of a BoardPicture. Data is the raw cell
// message exactly as it was published to the game stream (see game.Cell), so a
// renderer reconstructs the cell with the same unmarshal path used live.
type BoardCell struct {
	Row  int             `json:"r"` // 0-based within the stored visible region
	Col  int             `json:"c"`
	Data json.RawMessage `json:"d"`
}

const (
	// A board for one seat is 20 visible rows — the Guideline playfield —
	// above which sit the hidden headroom rows a piece spawns in. The
	// headroom and the spawn rows are the same on every board in every mode;
	// only a SHARED board can be taller, growing by GameMeta.ExtraRows for
	// every seat beyond the first (SharedBoardHeight) — the number of
	// opponents that can send garbage never changes a board's height.
	HeadroomRows    = 4
	VisibleRows     = 20
	VisibleRowStart = HeadroomRows
	TotalRows       = HeadroomRows + VisibleRows
	StandardHeight  = TotalRows // one seat's board: a competitive board, or a shared board before any extra rows
	StandardWidth   = 10

	// A shared board is VisibleRows tall for its first seat and
	// GameMeta.ExtraRows more for every seat after it — the create wizard's
	// extra-rows slider, between MinExtraRows and MaxExtraRows. Zero, the
	// default (and every meta written before the field), keeps the standard
	// playfield whatever the seat count: the Guideline board.
	MinExtraRows     = 0
	MaxExtraRows     = 10
	DefaultExtraRows = MinExtraRows

	// A game's length in lines (GameMeta.LineGoal): the wizard's "X lines"
	// game length defaults to the sprint's classic forty; 0 is "until top
	// out", the game as it always was. MaxLineGoal only keeps the editor's
	// read-out sane.
	DefaultLineGoal = 40
	MaxLineGoal     = 999

	// MaxNextCount caps GameMeta.NextCount, the per-game number of upcoming
	// pieces shown to players (0 = none). The same bound applies to agents:
	// an agent may look ahead at most NextCount pieces in the sequence.
	MaxNextCount = 6

	// MaxGarbageHoles caps GameMeta.GarbageHoles, the per-game number of empty
	// cells punched in every garbage row a raise lands (0 = solid, permanent
	// rows). The holes of one raise share their columns on every row it lands.
	MaxGarbageHoles = 4

	// A shared board (cooperative, or one team's board) is StandardWidth
	// columns for its first player and GameMeta.ExtraColumns more for every
	// player after them — the create wizard's board-width slider, between
	// MinExtraColumns and MaxExtraColumns. The extra columns are also the
	// spacing between neighbouring spawn points (SharedSpawnOffset), so at
	// the minimum of 4 the seats sit shoulder to shoulder (a 4-wide I still
	// clears its neighbour's spawn box) and at the maximum of 10 every player
	// gets a full standard section of their own — the board Jetris had before
	// the slider, which is why a meta without the field reads as 10.
	MinExtraColumns     = 4
	MaxExtraColumns     = StandardWidth
	DefaultExtraColumns = MinExtraColumns

	// LockDelay is the Guideline lock delay: a piece that lands on the stack
	// (or the floor) locks this long after landing, unless a successful shift
	// or rotation restarts the timer — at most LockDelayMoveResets times per
	// piece, the allowance renewed whenever the piece falls to a new lowest
	// row. Only a hard drop locks at once. Each engine times its own piece;
	// nothing about the delay is on the wire.
	LockDelay           = 500 * time.Millisecond
	LockDelayMoveResets = 15

	// IdlePieceVacateAfter is how long a falling piece may stand still on a
	// shared board before the other players vacate it: a piece left behind
	// by a player who crashed, or walked away without vacating it. A live
	// piece never stands still that long — gravity moves it every tick, and
	// the lock delay's resets end well short (LockDelayMoveResets ×
	// LockDelay), so a piece on the stack locks first.
	IdlePieceVacateAfter = 10 * time.Second

	LobbyKVBucket     = "JETRIS_LOBBY"
	ChatStream        = "JETRIS_CHAT"
	LobbyChatGameID   = "lobby"         // reserved chat "game ID" for the lobby chat (real game IDs are UUIDs, so no collision)
	LobbyVoiceRoom    = LobbyChatGameID // the lobby's voice room, by the same token: jetris.voice.lobby.all.<player>
	LobbyChatSubject  = chatSubjectPrefix + LobbyChatGameID
	ArchiveStream     = "JETRIS_ARCHIVE"
	ArchiveSubject    = "jetris.archive"
	ChatMaxAge        = 7 * 24 * time.Hour
	PresenceHeartbeat = 30 * time.Second
	// PresenceTTL is the per-key expiry on lobby presence entries: a player's
	// presence key self-deletes this long after its last heartbeat, so a client
	// that crashes or drops off is removed from the lobby by the server (a KV
	// TTL-expiry delete event) without any client watching last-seen timestamps.
	// Comfortably longer than PresenceHeartbeat so a live client is never
	// dropped between beats.
	PresenceTTL = 5 * time.Minute
)

// Abandoned-game detection: every client re-checks the lobby's games on a
// timer. A started (in-progress) game is abandoned once its stream has seen no
// messages for AbandonedIdleTimeout; a game that was created but never started
// is abandoned AbandonedUnstartedTimeout after creation. An OPEN game — anyone
// may join or leave at any time, mid-game included — is a standing invitation
// rather than a party that failed to gather, so neither rule applies to it:
// it is abandoned only after AbandonedOpenTimeout with no activity at all —
// no one joining, leaving, readying or playing (lobby.isAbandoned). The
// login-time cleanup pass (internal/cleanup) leaves an open game alone on the
// same terms. Abandoned games grow a Delete button in the lobby.
const (
	AbandonedCheckInterval    = 1 * time.Minute
	AbandonedIdleTimeout      = 1 * time.Minute
	AbandonedUnstartedTimeout = 15 * time.Minute
	AbandonedOpenTimeout      = 14 * 24 * time.Hour
)

// ExtraColumnsPerPlayer clamps a game's extra-columns setting to its legal
// range. Zero — the value every meta, listing and archive record written
// before the field carries — means the historical board: a full StandardWidth
// section per player, which is exactly MaxExtraColumns, so an old game still
// reconstructs at the width it was played on.
func ExtraColumnsPerPlayer(extraCols int) int {
	if extraCols <= 0 {
		return MaxExtraColumns
	}
	return min(max(extraCols, MinExtraColumns), MaxExtraColumns)
}

// SharedBoardWidth returns the width of a board shared by players seats — the
// cooperative board (players = PlayerCount) or one team's board (players =
// TeamSize): the standard 10 columns the first seat needs, plus extraCols for
// every seat after it. So a game created with the slider's default of 4 seats
// two players on 14 columns, three on 18, four on 22.
func SharedBoardWidth(players, extraCols int) int {
	return StandardWidth + max(players-1, 0)*ExtraColumnsPerPlayer(extraCols)
}

// SharedSpawnOffset returns the column a shared board's section'th seat
// spawns its pieces at, relative to the standard spawn column: seats are
// spaced one extraCols step apart, so the first spawns at the board's left
// edge and the last exactly StandardWidth columns short of its right edge —
// every spawn box lands wholly on the board (SharedBoardWidth).
func SharedSpawnOffset(section, extraCols int) int {
	return max(section, 0) * ExtraColumnsPerPlayer(extraCols)
}

// TeamBoardWidth returns the width of one team's shared board: the standard
// 10 columns plus extraCols per teammate beyond the first, like the
// cooperative board.
func TeamBoardWidth(teamSize, extraCols int) int {
	return SharedBoardWidth(teamSize, extraCols)
}

// ExtraRowsPerPlayer clamps a game's extra-rows setting to its legal range.
// Unlike the columns, zero — the value every meta written before the field
// carries — means exactly that: no extra rows, the standard playfield.
func ExtraRowsPerPlayer(extraRows int) int {
	return min(max(extraRows, MinExtraRows), MaxExtraRows)
}

// SharedBoardHeight returns the height (headroom + visible rows) of a board
// shared by players seats — the cooperative board (players = PlayerCount) or
// one team's board (players = TeamSize): the standard board the first seat
// needs, plus extraRows for every seat after it. The headroom stays the top
// HeadroomRows rows: the board grows downwards, so the spawn rows and the
// top-out rule are the same on every board.
func SharedBoardHeight(players, extraRows int) int {
	return StandardHeight + max(players-1, 0)*ExtraRowsPerPlayer(extraRows)
}

// TeamBoardHeight returns the height of one team's shared board: the standard
// board plus extraRows per teammate beyond the first, like the cooperative
// board.
func TeamBoardHeight(teamSize, extraRows int) int {
	return SharedBoardHeight(teamSize, extraRows)
}

// NormalizeLineGoal reads a recorded line goal: nothing (the zero value, and
// every meta written before the field) or anything negative is "until top
// out", and a goal is clamped to MaxLineGoal.
func NormalizeLineGoal(n int) int {
	if n <= 0 {
		return 0
	}
	return min(n, MaxLineGoal)
}

func GameStream(gameID string) string {
	return "JETRIS_GAME_" + gameID
}

func GameSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".>"
}

// The replay archive is ONE shared file-backed stream (ReplayStream) holding
// the full copy of every top-ranked finished game's stream. Each copied
// message is republished under the game-scoped prefix
// "jetris.replay.<gameID>.<original tail>" — the game ID right after the
// prefix, so one game is exactly one subject subspace: a consumer filter
// ReplayFilter(gameID) replays it and a Purge with the same filter deletes a
// displaced game. Republishing means the stream stamps COPY-time timestamps,
// so each copied message carries its ORIGINAL timestamp in the ReplayTsHeader
// header instead — the replay viewer paces an original-speed playback from it.
// The copy ends with a marker message on ReplayMarkerSubject; the marker is
// what makes a replay complete and listable (ReplayMarkerFilter), so an
// in-flight copy is never surfaced.
const (
	ReplayStream = "JETRIS_REPLAY"
	// ReplaySubjectFilter matches everything in the replay stream (stream config).
	ReplaySubjectFilter = replaySubjectPrefix + ">"
	// ReplayMarkerFilter matches every game's copy-complete marker (listing).
	ReplayMarkerFilter = replaySubjectPrefix + "*." + replayMarkerToken
	// ReplayTsHeader carries a copied message's ORIGINAL stream timestamp
	// (integer nanoseconds since the Unix epoch).
	ReplayTsHeader = "Jetris-Ts"

	replaySubjectPrefix = "jetris.replay."
	replayMarkerToken   = "done"
)

// ReplayFilter matches one archived game's whole replay subspace — the copied
// messages plus its marker. Used as the replay consumer's filter and as the
// purge filter when the game is displaced.
func ReplayFilter(gameID string) string {
	return replaySubjectPrefix + gameID + ".>"
}

// ReplayMarkerSubject is the copy-complete marker, published last. "done" can
// never collide with a copied subject: game-stream tails all continue past
// their first token (meta and countdown are single tokens, but neither is
// "done").
func ReplayMarkerSubject(gameID string) string {
	return replaySubjectPrefix + gameID + "." + replayMarkerToken
}

// ReplayCopySubject maps one game-stream subject to its home in the replay
// stream: "jetris.game.<id>.<tail>" → "jetris.replay.<id>.<tail>". Returns ""
// for a subject not under the game's prefix (never the case for messages read
// off the game's own stream).
func ReplayCopySubject(gameID, gameSubject string) string {
	prefix := "jetris.game." + gameID + "."
	tail, ok := strings.CutPrefix(gameSubject, prefix)
	if !ok || tail == "" {
		return ""
	}
	return replaySubjectPrefix + gameID + "." + tail
}

// GameIDFromReplayMarker extracts the game ID from a marker subject listed by
// ReplayMarkerFilter ("" if the subject is not a marker).
func GameIDFromReplayMarker(subject string) string {
	tail, ok := strings.CutPrefix(subject, replaySubjectPrefix)
	if !ok {
		return ""
	}
	id, ok := strings.CutSuffix(tail, "."+replayMarkerToken)
	if !ok || strings.Contains(id, ".") {
		return ""
	}
	return id
}

// Cooperative and competitive modes use entirely separate playfield subject
// schemes — they are not parameterisations of a single layout and are free to
// diverge. A given game is one mode or the other, so an engine only ever uses
// one scheme.
//
// CoopCellSubject is the subject one cell (row, col) of the shared cooperative
// board is published to. The board is shared by the whole game, so the subject
// carries NO player token — every player publishes to and consumes from the
// same cell subjects, and per-cell ownership lives in the payload via
// Cell.PlayerIdx (coop never filters cells by player).
func CoopCellSubject(gameID string, row, col int) string {
	return "jetris.game." + gameID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CoopCellSubjectFilter is the wildcard filter matching every cell of the
// shared cooperative board.
func CoopCellSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".playfield.cell.>"
}

// CompetitiveCellSubject is the subject one cell (row, col) of one competitive
// player's private board is published to. Each player owns a separate board
// scoped by player ID.
func CompetitiveCellSubject(gameID, playerID string, row, col int) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CompetitiveCellSubjectFilter is the wildcard filter matching every cell of
// one competitive player's board.
func CompetitiveCellSubjectFilter(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.cell.>"
}

// TeamCellSubject is the subject one cell (row, col) of one team's shared
// board is published to in teams mode. Like the cooperative scheme the subject
// carries no player token — all teammates publish to and consume from the same
// cell subjects and per-cell ownership lives in the payload via Cell.PlayerIdx —
// but the board is scoped by team index so the two teams' boards are disjoint.
func TeamCellSubject(gameID string, team, row, col int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// TeamCellSubjectFilter is the wildcard filter matching every cell of one
// team's shared board.
func TeamCellSubjectFilter(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell.>"
}

// Per-board registers. Every competitive/team board carries two single-subject
// "registers" under its playfield prefix, so the board's consumers and snapshot
// fetches cover them with one widened filter (…playfield.>):
//
//   - The GARBAGE register holds the cumulative number of garbage rows OWED to
//     the board since game start. It is advanced by ATTACKERS (read-add-publish
//     with per-subject CAS and a bounded retry), so simultaneous attacks
//     serialize and converge to the exact sum. Only the latest total matters —
//     the register is a monotonic counter, so a newer value subsumes every
//     older one, and a late joiner or reconnecting client recovers the full
//     amount owed from the last-per-subject snapshot fetch.
//   - The TXN register is the exactly-once gate for every bulk board transform
//     (garbage application, line-clear collapse, teams elimination vacate): it
//     rides as the FIRST message of the transform's atomic batch with a
//     per-subject CAS expectation, so of several racing appliers exactly one
//     commits — a stale gate atomically rejects the loser's entire batch. Its
//     payload records the cumulative rows APPLIED; the board's deficit is
//     garbage.Total − txn.Applied.
//
// Cooperative boards have no registers: coop has no garbage, and its clears
// keep the merge-retry path.
const (
	garbageSubjectSuffix = ".playfield.garbage"
	txnSubjectSuffix     = ".playfield.txn"
)

// CompetitiveGarbageSubject is one competitive player's garbage (rows-owed)
// register subject.
func CompetitiveGarbageSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + garbageSubjectSuffix
}

// CompetitiveTxnSubject is one competitive player's txn (transform gate)
// register subject.
func CompetitiveTxnSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + txnSubjectSuffix
}

// CompetitivePlayfieldFilter matches one competitive player's whole playfield
// namespace: every cell plus the garbage/txn registers.
func CompetitivePlayfieldFilter(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.>"
}

// TeamGarbageSubject is one team board's garbage (rows-owed) register subject.
func TeamGarbageSubject(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + garbageSubjectSuffix
}

// TeamTxnSubject is one team board's txn (transform gate) register subject.
func TeamTxnSubject(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + txnSubjectSuffix
}

// TeamPlayfieldFilter matches one team board's whole playfield namespace:
// every cell plus the garbage/txn registers.
func TeamPlayfieldFilter(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.>"
}

// IsGarbageSubject reports whether a delivered subject is a board's garbage
// register. Consumers branch on the register helpers BEFORE the cell parse.
func IsGarbageSubject(subject string) bool {
	return strings.HasSuffix(subject, garbageSubjectSuffix)
}

// IsTxnSubject reports whether a delivered subject is a board's txn register.
func IsTxnSubject(subject string) bool {
	return strings.HasSuffix(subject, txnSubjectSuffix)
}

func MetaSubject(gameID string) string {
	return "jetris.game." + gameID + ".meta"
}

func RosterSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".roster." + playerID
}

// Game events are published to PER-KIND, PER-PLAYER subjects, so no event can
// ever overwrite an unrelated one, and the payloads stay correct under any
// retention or replay: line_clear carries the sender's CUMULATIVE totals (a
// newer total subsumes any older one, and a full-history replay — e.g. a
// spectator joining mid-game — folds to the same numbers) and each player
// publishes at most one game_over. Stream order across subjects is total, so
// every engine sees the same verdict order.
func EventKindSubject(gameID, kind, playerID string) string {
	return "jetris.game." + gameID + ".events." + kind + "." + playerID
}

// EventsSubjectFilter matches every event of a game, all kinds and senders.
func EventsSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".events.>"
}

func CountdownSubject(gameID string) string {
	return "jetris.game." + gameID + ".countdown"
}

// FlashSubject and FlashSubjectFilter are CORE NATS subjects (deliberately
// OUTSIDE the "jetris.game.<id>.>" filter the game stream captures) used to
// broadcast a player's transient CAS-failure flash to spectators. A flash is
// ephemeral UI feedback — it must NOT be persisted in the stream or replayed
// on join — so it travels as fire-and-forget core pub/sub, not JetStream.
func FlashSubject(gameID, playerID string) string {
	return "jetris.flash." + gameID + "." + playerID
}

// FlashSubjectFilter matches every player's flash subject for a game.
func FlashSubjectFilter(gameID string) string {
	return "jetris.flash." + gameID + ".*"
}

// VoiceSubject, VoiceTeamSubject and their filters are CORE NATS subjects
// (deliberately OUTSIDE the "jetris.game.<id>.>" filter the game stream
// captures) carrying a player's voice frames to everyone on that game's
// screen. Voice is the most ephemeral thing in the game — fifty packets a
// second that mean nothing a moment later — so it must NOT be persisted or
// replayed on join; it travels as fire-and-forget core pub/sub, as the CAS
// flash does. Two rooms: "all" is everyone on the game's screen, spectators
// included; "team.<t>" is one team's own room in a teams game. The sender's
// ID is the last token, so a receiver drops its own frames by subject rather
// than with NoEcho, which would be connection-wide.
func VoiceSubject(gameID, playerID string) string {
	return "jetris.voice." + gameID + ".all." + playerID
}

// VoiceTeamSubject is a player's voice subject in their team's room.
func VoiceTeamSubject(gameID string, team int, playerID string) string {
	return "jetris.voice." + gameID + ".team." + strconv.Itoa(team) + "." + playerID
}

// VoiceSubjectFilter matches every player's frames in a game's "all" room.
func VoiceSubjectFilter(gameID string) string {
	return "jetris.voice." + gameID + ".all.*"
}

// VoiceTeamSubjectFilter matches every player's frames in one team's room.
func VoiceTeamSubjectFilter(gameID string, team int) string {
	return "jetris.voice." + gameID + ".team." + strconv.Itoa(team) + ".*"
}

// VoiceAnySubjectFilter matches every voice frame of a game, every room —
// a spectator's subscription, who hears every team.
func VoiceAnySubjectFilter(gameID string) string {
	return "jetris.voice." + gameID + ".>"
}

// ParseVoiceSubject reads the sender and the room out of a voice subject:
// team is -1 for the "all" room and the team index for a team room. ok is
// false for anything that is not a well-formed voice subject. Player IDs are
// single subject tokens (ValidatePlayerName) and game IDs are UUIDs, so the
// tokens parse unambiguously.
func ParseVoiceSubject(subject string) (playerID string, team int, ok bool) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < 5 || tokens[0] != "jetris" || tokens[1] != "voice" {
		return "", 0, false
	}
	switch {
	case len(tokens) == 5 && tokens[3] == "all" && tokens[4] != "":
		return tokens[4], -1, true
	case len(tokens) == 6 && tokens[3] == "team" && tokens[5] != "":
		t, err := strconv.Atoi(tokens[4])
		if err != nil || t < 0 {
			return "", 0, false
		}
		return tokens[5], t, true
	}
	return "", 0, false
}

// Lobby chat and per-game chat share the SAME stream (ChatStream) and are
// distinguished purely by the game-ID token of the subject
// ("jetris.chat.<gameID>"): the lobby chat uses the reserved game ID
// LobbyChatGameID ("lobby"), a game's messages use its own ID. Game chat
// cannot live on the game stream because game streams keep only the latest
// message per subject.

const chatSubjectPrefix = "jetris.chat."

// GameChatSubject is the subject one game's chat messages are published to.
func GameChatSubject(gameID string) string {
	return chatSubjectPrefix + gameID
}

// ChatSubjectFilter matches every chat subject, lobby and per-game alike
// (stream config).
const ChatSubjectFilter = chatSubjectPrefix + "*"

// GameIDFromChatSubject extracts the game ID from a chat-stream subject; it
// returns "" for the lobby chat subject.
func GameIDFromChatSubject(subject string) string {
	if len(subject) > len(chatSubjectPrefix) && subject[:len(chatSubjectPrefix)] == chatSubjectPrefix {
		if id := subject[len(chatSubjectPrefix):]; id != LobbyChatGameID {
			return id
		}
	}
	return ""
}

func LobbyPlayerKey(playerID string) string {
	return "players." + playerID
}

func LobbyGameKey(gameID string) string {
	return "games." + gameID
}

// LobbyPinPrefix is the KV key prefix under which pinned replays live
// ("pins.<gameID>", see ReplayPin): a key's existence is the pin. Pins are
// lobby KV entries — written without a TTL, so they live until deleted —
// rather than replay-stream messages, so every lobby's KV watcher sees a
// pin and an UNPIN alike the moment it happens (a purge on a stream is
// silent to a consumer), and an archiver reads the pin set straight off the
// bucket when it cuts the keep set (ReplayKeepSet).
const LobbyPinPrefix = "pins."

// LobbyPinKey is the KV key that pins one game's replay.
func LobbyPinKey(gameID string) string {
	return LobbyPinPrefix + gameID
}

// GameIDFromPinKey extracts the game ID from a pin key ("" if the key is not
// one).
func GameIDFromPinKey(key string) string {
	id, ok := strings.CutPrefix(key, LobbyPinPrefix)
	if !ok {
		return ""
	}
	return id
}

// LobbyInviteKey is the KV key holding one player's invitation to one game.
// A player may hold invitations to several games at once — one key per game.
// Every lobby's KV watcher sees the whole invites.* space, so the invitee's
// client surfaces an invitation the moment it is written AND the inviter can
// watch its state: the key is deleted when the invitee joins (accepted) or the
// inviter retracts it, and rewritten with Declined set when the invitee
// declines (kept so the inviter sees the refusal until they dismiss it).
// Player IDs cannot contain '.' (ValidatePlayerName) and game IDs are UUIDs,
// so the two tokens parse back out unambiguously.
func LobbyInviteKey(inviteeID, gameID string) string {
	return LobbyInvitePrefix(inviteeID) + gameID
}

// LobbyInvitePrefix is the KV key prefix under which all of one player's
// pending invitations live ("invites.<playerID>.").
func LobbyInvitePrefix(inviteeID string) string {
	return "invites." + inviteeID + "."
}

// InviteTTL is how long a pending invitation stays valid; older invites are
// ignored (the invitee may have been away from the lobby screen).
const InviteTTL = 2 * time.Minute

// Lobby events are transient core NATS notifications (deliberately NOT
// captured by any stream — real-time signals, not state) published so every
// lobby, human or agent, hears about lobby activity the instant it happens:
// a game created, a player joining or leaving a game's roster, an invitation
// sent, retracted, or declined. State still lives in the KV (listings,
// presence, invites); the events are the low-latency "look now" pings that
// keep player lists and invite pop-ups current between KV watcher deliveries.

// LobbyEventSubject is the subject one kind of lobby event is published to,
// e.g. "jetris.lobby.event.game.joined".
func LobbyEventSubject(kind string) string {
	return "jetris.lobby.event." + kind
}

// LobbyEventsFilter matches every lobby event subject (core NATS subscription).
const LobbyEventsFilter = "jetris.lobby.event.>"

// The server log: a JetStream stream (LogStream, one replica, file storage)
// every client appends to as things happen in the lobby — a player
// connecting, disconnecting, or coming back after a dropped connection, a
// game created, a game started once its countdown has run — and every lobby
// reads back into its SERVER LOG tab. It is a journal, not state: nothing is
// decided from it. Entries are kept LogMaxAge, then age out. Agents are not
// journaled: they come and go by the dozen and create no games; the one
// entry an agent writes is a game's start, when it happened to run the
// countdown.
const (
	LogStream        = "JETRIS_LOG"
	LogSubjectPrefix = "jetris.log."
	LogSubjectFilter = "jetris.log.>"
	LogMaxAge        = 100 * 24 * time.Hour
	// LogDuplicateWindow is how long the stream remembers a message ID: a
	// departure that only the presence TTL reported (the client crashed) is
	// journaled by whichever lobbies saw the key expire, all under the same
	// ID, and the stream keeps one.
	LogDuplicateWindow = 2 * time.Minute

	LogKindConnected    = "connected"
	LogKindDisconnected = "disconnected"
	LogKindReconnected  = "reconnected" // the NATS connection dropped and came back; the player never left the lobby
	LogKindGameCreated  = "game.created"
	LogKindGameStarted  = "game.started"
)

// LogSubject is the subject one kind of server log entry is published to,
// e.g. "jetris.log.game.created".
func LogSubject(kind string) string {
	return LogSubjectPrefix + kind
}

// LogEntry is the payload of every server log message.
type LogEntry struct {
	Kind        string    `json:"kind"`
	PlayerID    string    `json:"player_id"`
	Name        string    `json:"name"`
	Agent       bool      `json:"agent,omitempty"`        // the player is an agent (e.g. golang-mk1)
	GameID      string    `json:"game_id,omitempty"`      // game entries
	Mode        GameMode  `json:"mode,omitempty"`         // game entries
	PlayerCount int       `json:"player_count,omitempty"` // game entries: the seats
	Time        time.Time `json:"time"`
	Seq         uint64    `json:"-"` // the entry's stream sequence, stamped by the reader
}

// Text is the entry as a line of the server log, without its time.
func (e LogEntry) Text() string {
	who := e.Name
	if e.Agent {
		who += " (agent)"
	}
	switch e.Kind {
	case LogKindConnected:
		return who + " connected to the lobby"
	case LogKindDisconnected:
		return who + " disconnected from the lobby"
	case LogKindReconnected:
		return who + " reconnected to the lobby"
	case LogKindGameCreated:
		return who + " created a " + e.gameDesc()
	case LogKindGameStarted:
		return who + " started a " + e.gameDesc()
	default:
		return who + " " + e.Kind
	}
}

// gameDesc is "2-player competitive game", "teams game for 4", etc.
func (e LogEntry) gameDesc() string {
	if e.PlayerCount == 1 {
		return "solo game"
	}
	if e.PlayerCount > 1 {
		return fmt.Sprintf("%d-player %s game", e.PlayerCount, e.Mode)
	}
	return e.Mode.String() + " game"
}
