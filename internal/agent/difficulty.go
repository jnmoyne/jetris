// Package agent implements a headless computer player for competitive jetris.
// It is just another peer: it drives the exact same engine the GUI uses (the
// six move methods plus the playfield accessors) over the exported engine and
// lobby APIs — no engine internals, no direct cell publishes.
package agent

import (
	"fmt"
	"strings"
	"time"
)

// Difficulty selects how strongly the agent plays.
type Difficulty int

const (
	DifficultyEasy Difficulty = iota
	DifficultyMedium
	DifficultyHard
)

func (d Difficulty) String() string {
	switch d {
	case DifficultyEasy:
		return "easy"
	case DifficultyMedium:
		return "medium"
	case DifficultyHard:
		return "hard"
	default:
		return "unknown"
	}
}

// ParseDifficulty maps "easy"/"medium"/"hard" (case-insensitive) to a Difficulty.
func ParseDifficulty(s string) (Difficulty, error) {
	switch strings.ToLower(s) {
	case "easy":
		return DifficultyEasy, nil
	case "medium":
		return DifficultyMedium, nil
	case "hard":
		return DifficultyHard, nil
	default:
		return 0, fmt.Errorf("unknown difficulty %q (want easy, medium or hard)", s)
	}
}

// Tuning holds the per-difficulty knobs. It is exported so tests (and power
// users embedding the agent) can override individual knobs via Config.Tuning.
// There is deliberately NO lookahead knob: the piece sequence is deterministic
// from the game seed, but a human sees no next-piece preview in the UI, and
// agents only decide on what a human player can see.
type Tuning struct {
	PieceDelay   time.Duration // "think" pause after a new piece is observed
	MoveDelay    time.Duration // pause between dispatched moves (also keeps the engine's 8-deep input buffer from overflowing)
	BlunderRate  float64       // probability of not taking the top-ranked placement
	BlunderDepth int           // blunders pick uniformly among ranks 2..1+BlunderDepth

	// Executor timeouts; zero values fall back to the package defaults. Exposed
	// so tests can shrink them.
	MoveTimeout time.Duration // max wait for a dispatched move to become observable
	DropTimeout time.Duration // max wait for a hard drop to lock in (PieceIdx advance)
}

// Tuning returns the standard knob settings for the difficulty level.
func (d Difficulty) Tuning() Tuning {
	switch d {
	case DifficultyEasy:
		return Tuning{
			PieceDelay:   1500 * time.Millisecond,
			MoveDelay:    300 * time.Millisecond,
			BlunderRate:  0.30,
			BlunderDepth: 4,
		}
	case DifficultyMedium:
		return Tuning{
			PieceDelay:   600 * time.Millisecond,
			MoveDelay:    150 * time.Millisecond,
			BlunderRate:  0.10,
			BlunderDepth: 2,
		}
	default: // DifficultyHard
		return Tuning{
			PieceDelay: 100 * time.Millisecond,
			MoveDelay:  30 * time.Millisecond,
		}
	}
}

const (
	defaultMoveTimeout = 500 * time.Millisecond
	defaultDropTimeout = 5 * time.Second
)

// moveTimeout returns the effective per-move effect timeout.
func (tn Tuning) moveTimeout() time.Duration {
	if tn.MoveTimeout <= 0 {
		return defaultMoveTimeout
	}
	return tn.MoveTimeout
}

// dropTimeout returns the effective hard-drop lock-in timeout.
func (tn Tuning) dropTimeout() time.Duration {
	if tn.DropTimeout <= 0 {
		return defaultDropTimeout
	}
	return tn.DropTimeout
}
