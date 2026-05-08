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

// ClearRows removes the specified rows and shifts everything above them down.
// Returns the new playfield state with empty rows at the top.
func ClearRows(pf *Playfield, rows []int) {
	if len(rows) == 0 {
		return
	}
	// Mark rows to clear
	cleared := make(map[int]bool, len(rows))
	for _, r := range rows {
		cleared[r] = true
	}

	// Build new rows: copy non-cleared rows, add empty rows at top
	var newRows []Row
	for i := 0; i < pf.Height; i++ {
		if !cleared[i] {
			newRows = append(newRows, pf.Rows[i])
		}
	}
	// Prepend empty rows
	for len(newRows) < pf.Height {
		newRows = append([]Row{NewRow(pf.Width)}, newRows...)
	}
	copy(pf.Rows[:], newRows)
}

// ScoreDelta returns the score awarded for clearing n lines at the given level.
func ScoreDelta(linesCleared, level int) int {
	var base int
	switch linesCleared {
	case 1:
		base = 100
	case 2:
		base = 300
	case 3:
		base = 500
	case 4:
		base = 800
	default:
		return 0
	}
	return base * (level + 1)
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
