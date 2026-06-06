package game

import "time"

// CompletedRows returns indices of fully occupied rows (all locked, no active cells).
func CompletedRows(pf *Playfield) []int {
	var rows []int
	for i := range pf.Rows {
		if pf.Rows[i].IsFull() {
			rows = append(rows, i)
		}
	}
	return rows
}

// Level returns the current level derived from total lines cleared.
func Level(totalLinesCleared int) int {
	l := totalLinesCleared / 10
	if l > 19 {
		return 19
	}
	return l
}

// gravityTable is the standard Tetris Guideline gravity intervals by level.
var gravityTable = [20]time.Duration{
	800 * time.Millisecond, // 0
	717 * time.Millisecond, // 1
	633 * time.Millisecond, // 2
	550 * time.Millisecond, // 3
	467 * time.Millisecond, // 4
	383 * time.Millisecond, // 5
	300 * time.Millisecond, // 6
	217 * time.Millisecond, // 7
	133 * time.Millisecond, // 8
	100 * time.Millisecond, // 9
	83 * time.Millisecond,  // 10
	83 * time.Millisecond,  // 11
	83 * time.Millisecond,  // 12
	67 * time.Millisecond,  // 13
	67 * time.Millisecond,  // 14
	67 * time.Millisecond,  // 15
	50 * time.Millisecond,  // 16
	50 * time.Millisecond,  // 17
	50 * time.Millisecond,  // 18
	33 * time.Millisecond,  // 19
}

// GravityInterval returns the gravity tick duration for the given level.
func GravityInterval(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	if level >= len(gravityTable) {
		return gravityTable[len(gravityTable)-1]
	}
	return gravityTable[level]
}
