package game

import (
	"math"
	"time"
)

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

// minGravityInterval floors the speed curve at one 60 Hz frame. The engine
// moves a piece one row per gravity tick and each tick is a JetStream batch,
// so the Guideline's sub-frame intervals (its levels 14+: 11 ms, 7 ms, …)
// cannot be honoured row by row; those levels all run at the floor.
const minGravityInterval = time.Second / 60

// gravityTable is the Guideline speed curve by Jetris level (0-based):
//
//	seconds per row = (0.8 − (L − 1) × 0.007)^(L − 1)
//
// with the Guideline's level L = level + 1, rounded to the millisecond and
// floored at minGravityInterval. Level 0 is 1000 ms, then 793, 618, 473, 355,
// 262, 190, 135, 94, 64, 43, 28, 18 ms, and one frame from level 13 on.
var gravityTable = func() (t [20]time.Duration) {
	for level := range t {
		l := float64(level)
		secs := math.Pow(0.8-l*0.007, l)
		d := time.Duration(math.Round(secs*1000)) * time.Millisecond
		if d < minGravityInterval {
			d = minGravityInterval
		}
		t[level] = d
	}
	return t
}()

// GravityInterval returns the time a piece spends on each row at the given
// level (see gravityTable).
func GravityInterval(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	if level >= len(gravityTable) {
		return gravityTable[len(gravityTable)-1]
	}
	return gravityTable[level]
}
