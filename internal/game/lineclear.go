package game

import (
	"math"
	"time"
)

// CompletedRows returns indices of fully occupied rows (all locked, no active
// cells) that can clear — see Row.IsFull for the garbage-row rule.
func CompletedRows(pf *Playfield) []int {
	var rows []int
	for i := range pf.Rows {
		if pf.Rows[i].IsFull() {
			rows = append(rows, i)
		}
	}
	return rows
}

// The level and the speed curve are Tetris Worlds', the game the Guideline
// was drawn from (https://harddrop.com/wiki/Tetris_Worlds): the level starts
// at 1 and rises every LinesPerLevel lines, in every game mode, and the time
// a piece spends on each row at level L is
//
//	seconds per row = (0.8 − (L − 1) × 0.007)^(L − 1)
//
// up to MaxLevel — 1000 ms at level 1, then 793, 618, 473, 355, 262, 190,
// 135, 94, 64, 43, 28, 18, 11 and 7 ms. The last two are faster than a 60 Hz
// frame (1.46G and 2.36G in the page's units): the engine honours them by
// moving the piece several rows in one batch when its clock owes more than
// one row (engine/move.go).
const (
	MinLevel      = 1
	MaxLevel      = 15
	LinesPerLevel = 10
)

// Level returns the level a line total has reached: MinLevel until the
// LinesPerLevel-th line, one more per LinesPerLevel after it, MaxLevel at
// most.
func Level(totalLinesCleared int) int {
	return min(MinLevel+max(totalLinesCleared, 0)/LinesPerLevel, MaxLevel)
}

// GravityInterval returns the time a piece spends on each row at the given
// level — the curve above, exactly. A level outside MinLevel..MaxLevel reads
// as the nearer bound.
func GravityInterval(level int) time.Duration {
	level = min(max(level, MinLevel), MaxLevel)
	l := float64(level - 1)
	return time.Duration(math.Pow(0.8-l*0.007, l) * float64(time.Second))
}
