package main

import (
	"fmt"
	"strings"
	"time"
)

// tuning holds the per-difficulty knobs, inherited from the retired in-repo
// mk1 agent. pieceDelay is the "think" pause after a new
// piece appears; moveDelay paces the walks (a whole path to the planned column goes out as one batch); the blunder knobs model
// weaker play; lookahead caps how many revealed preview pieces the planner uses
// (further bounded by what the game actually reveals — a no-preview game yields
// none at any level).
type tuning struct {
	pieceDelay   time.Duration
	moveDelay    time.Duration
	blunderRate  float64
	blunderDepth int
	lookahead    int
}

// difficultyTuning returns the standard knobs for "easy"/"medium"/"hard".
func difficultyTuning(d string) tuning {
	switch d {
	case "easy":
		return tuning{pieceDelay: 1500 * time.Millisecond, moveDelay: 300 * time.Millisecond, blunderRate: 0.30, blunderDepth: 4, lookahead: 0}
	case "medium":
		return tuning{pieceDelay: 600 * time.Millisecond, moveDelay: 150 * time.Millisecond, blunderRate: 0.10, blunderDepth: 2, lookahead: 1}
	default: // hard
		return tuning{pieceDelay: 100 * time.Millisecond, moveDelay: 30 * time.Millisecond, lookahead: maxNextCount}
	}
}

// maxNextCount is the game's maximum piece-preview depth (jetris-gameplays.md);
// "hard" plans with the full revealed preview, whatever a given game exposes.
const maxNextCount = 4

// validDifficulty normalizes and validates a difficulty label.
func validDifficulty(s string) (string, error) {
	switch strings.ToLower(s) {
	case "easy", "medium", "hard":
		return strings.ToLower(s), nil
	default:
		return "", fmt.Errorf("unknown difficulty %q (want easy, medium or hard)", s)
	}
}
