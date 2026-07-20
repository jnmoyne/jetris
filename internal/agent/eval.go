package agent

import (
	"jetricks/internal/game"
)

// Board evaluation: Pierre Dellacherie's six per-move features with the
// "El-Tetris" weights, the standard strong one-ply heuristic. The features are
// computed on the board AFTER the simulated lock and line clear, except
// landing height (position of the just-locked piece) and eroded cells (piece
// cells removed by the clear), which are move features by definition.
//
// Competitive garbage needs no special term: shrink rows are solid, permanent,
// bottom-anchored adversarial cells. They count as filled here, Row.IsFull
// already refuses to complete them in the clear simulation, and the height/
// transition features naturally price in the raised floor.
const (
	weightLandingHeight  = -4.500158825
	weightErodedCells    = 3.4181268
	weightRowTransitions = -3.2178882
	weightColTransitions = -9.348695
	weightHoles          = -7.899265
	weightWells          = -3.3855972
)

// filledAt reports whether the cell holds locked material (stack or garbage).
// Active (falling) cells never count.
func filledAt(pf *game.Playfield, row, col int) bool {
	c := pf.Rows[row].Cells[col]
	return c.Occupied && !c.Active
}

// evaluateMove scores one simulated placement. pf is the board after the lock
// and line clear, dest the piece's final resting position (pre-clear), lines
// the number of rows the lock completed and eroded = lines × (piece cells in
// the completed rows).
func evaluateMove(pf *game.Playfield, dest game.Piece, lines, eroded int) float64 {
	return weightLandingHeight*landingHeight(pf, dest) +
		weightErodedCells*float64(eroded) +
		weightRowTransitions*float64(rowTransitions(pf)) +
		weightColTransitions*float64(colTransitions(pf)) +
		weightHoles*float64(holes(pf)) +
		weightWells*float64(wells(pf))
}

// landingHeight is the height (rows from the bottom of the board) of the
// locked piece's center of mass, before the clear.
func landingHeight(pf *game.Playfield, dest game.Piece) float64 {
	cells := dest.Cells()
	sum := 0.0
	for _, c := range cells {
		sum += float64(pf.Height - 1 - c[0])
	}
	return sum / float64(len(cells))
}

// rowTransitions counts filled↔empty flips scanning each row left to right,
// with the side walls counted as filled.
func rowTransitions(pf *game.Playfield) int {
	n := 0
	for r := 0; r < pf.Height; r++ {
		prev := true // left wall
		for c := 0; c < pf.Width; c++ {
			cur := filledAt(pf, r, c)
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev { // right wall
			n++
		}
	}
	return n
}

// colTransitions counts filled↔empty flips scanning each column top to bottom,
// with the space above the board counted as empty and the floor as filled.
func colTransitions(pf *game.Playfield) int {
	n := 0
	for c := 0; c < pf.Width; c++ {
		prev := false // above the board
		for r := 0; r < pf.Height; r++ {
			cur := filledAt(pf, r, c)
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev { // floor
			n++
		}
	}
	return n
}

// holes counts empty cells with at least one filled cell above them in the
// same column.
func holes(pf *game.Playfield) int {
	n := 0
	for c := 0; c < pf.Width; c++ {
		covered := false
		for r := 0; r < pf.Height; r++ {
			if filledAt(pf, r, c) {
				covered = true
			} else if covered {
				n++
			}
		}
	}
	return n
}

// wells returns Dellacherie's cumulative well depth: every empty cell whose
// side neighbors are both filled (walls count as filled) opens a well and
// contributes the run of consecutive empty cells from itself downward, so a
// well of depth d totals 1+2+…+d.
func wells(pf *game.Playfield) int {
	n := 0
	for c := 0; c < pf.Width; c++ {
		for r := 0; r < pf.Height; r++ {
			if filledAt(pf, r, c) {
				continue
			}
			leftFilled := c == 0 || filledAt(pf, r, c-1)
			rightFilled := c == pf.Width-1 || filledAt(pf, r, c+1)
			if !leftFilled || !rightFilled {
				continue
			}
			for r2 := r; r2 < pf.Height && !filledAt(pf, r2, c); r2++ {
				n++
			}
		}
	}
	return n
}
