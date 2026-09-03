package engine

import (
	"time"

	"jetris/internal/game"
)

// UpdateKind identifies the type of engine update sent to the UI.
type UpdateKind int

const (
	UpdatePlayfield UpdateKind = iota
	UpdatePieceLocked
	UpdateLineClear
	UpdateGameOver
	UpdateOpponentField
	UpdateOpponentShrink
	UpdateScore
	UpdateLevel
	UpdateGameStatus
	UpdateCountdown
	UpdatePlayerEliminated // competitive/teams: a player was eliminated
	UpdateCASFlash         // a CAS failure flash should be rendered
	UpdateRTT              // a new publish→echo round-trip measurement
	UpdateBufferedMoves    // the buffered-input queue changed (read via Engine.BufferedMoves)
	UpdateTeamStats        // teams: a team's score or level changed (both teams' totals in TeamScores/TeamLevels)
	UpdateRowsCleared      // a clear completed ChangedRows (pre-collapse indices) on this player's board — own lock, or a teammate's on a shared board — arcade feedback hook
	UpdateHold             // the hold slot changed (read via Engine.HeldPiece / HoldUsed)
	UpdateStepAcked        // a step's batch was acknowledged (or a lost pipeline repaired): the acked board (Engine.Snapshot) moved on before the echo
	UpdateAward            // a lock scored a clear or a T-spin — ours, or a teammate's on our shared board: the Guideline's name for it (Clear), its points (Score) and who made it (PlayerID) — the HUD's banner
)

// EngineUpdate is the event sent from engine to UI.
type EngineUpdate struct {
	Kind               UpdateKind
	ChangedRows        []int
	Score              int
	Level              int
	GameStatus         string
	Countdown          int           // seconds remaining (0 = GO!)
	Won                bool          // competitive/teams: true if this player('s team) won
	EliminatedPlayerID string        // competitive/teams: which player was eliminated
	Team               int           // teams: team of the eliminated player (UpdatePlayerEliminated)
	OpponentID         string        // which opponent's board changed (UpdateOpponentField)
	FlashCells         [][2]int      // cells to flash (UpdateCASFlash): the piece as it stood when the step was lost
	FlashTargetCells   [][2]int      // UpdateCASFlash, a lost step: where the piece wanted to be — the outline a UI that pre-renders the move flashes instead (nil for a lost spawn/lock, and on the spectator broadcast)
	FlashPlayerIdx     int           // player index for flash color
	RTT                time.Duration // latest publish→echo round trip (UpdateRTT)
	TeamScores         []int         // teams: every team's score, in team-index order (UpdateTeamStats)
	TeamLevels         []int         // teams: every team's level, in team-index order (UpdateTeamStats)
	PlayerID           string        // UpdateAward: who made the clear (our own id for our own)
	Clear              game.Clear    // UpdateAward: the clear — lines, T-spin, Back-to-Back, combo, perfect (Score carries the lock's points, drop points included)
}

// EventKind identifies the type of game event published to the events subject.
type EventKind string

const (
	EventLineClear EventKind = "line_clear"
	EventGameOver  EventKind = "game_over"
)

// GameEvent is the JSON payload published to the events subject.
//
// Note: CAS-failure feedback is intentionally NOT modelled as a published
// event. A CAS failure that drops a step (player move, gravity tick, or spawn)
// is local information for that player; it surfaces as an UpdateCASFlash on the
// local engine's Updates channel only and never round-trips through NATS.
// Garbage attacks are NOT events either: they are recorded durably in the
// victim board's garbage register (see GarbageRegister), which simultaneous
// attackers CAS-add and victims reconcile against — fire-and-forget events
// from simultaneous attackers could race each other, a cumulative register
// serializes and sums them, and the amount owed is recoverable from any
// snapshot (late join, reconnect).
type GameEvent struct {
	Kind         EventKind `json:"kind"`
	PlayerID     string    `json:"player_id"`
	LinesCleared int       `json:"lines_cleared,omitempty"`
	ClearedRows  []int     `json:"cleared_rows,omitempty"` // EventLineClear: the cleared rows' pre-collapse indices (teammates strobe them)
	Score        int       `json:"score,omitempty"`
	Level        int       `json:"level,omitempty"` // EventGameOver: level achieved (from the sender's line total)
	PieceCount   uint64    `json:"piece_count,omitempty"`
	PlayerIdx    int       `json:"player_idx,omitempty"`
	Team         int       `json:"team"` // teams: sender's team (0 = A, 1 = B)

	// line_clear: what the lock was, by the Guideline's names — the T-spin
	// (0 none, 1 Mini, 2 full: game.TSpin), whether it was Back-to-Back, its
	// combo count and whether it was a perfect clear — for the crew's HUD
	// banner, and for agents that care. A lock that cleared nothing but
	// scored (drop points, a T-spin with no lines) is announced too, with
	// lines_cleared 0; Score is the lock's points, drop points included.
	TSpin      int  `json:"t_spin,omitempty"`
	BackToBack bool `json:"back_to_back,omitempty"`
	Combo      int  `json:"combo,omitempty"`
	Perfect    bool `json:"perfect,omitempty"`

	// line_clear and game_over: the sender's CUMULATIVE totals from its OWN
	// locks — every point its pieces scored, every line they cleared.
	// Receivers fold the DELTA against the last total they saw from that
	// sender, so any missed intermediate event is subsumed by the next, and
	// an engine replaying the full event history (a mid-game spectator, the
	// archiver) converges to the same scoreboard. A game_over carries them so
	// the sender's last points (never announced by a later event) count.
	TotalScore int `json:"total_score,omitempty"`
	TotalLines int `json:"total_lines,omitempty"`
}

// MoveType represents a player move.
type MoveType int

const (
	MoveLeft MoveType = iota
	MoveRight
	MoveDown
	RotateCW
	RotateCCW
	MoveHardDrop
	MoveHold // the Guideline hold (Engine.Hold); a no-op unless the game has the hold rule
)
