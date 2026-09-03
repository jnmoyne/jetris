package game

// TSpin classifies how a T came to rest: not a T-spin at all, a Mini T-Spin,
// or a full T-Spin. Jetris plays the Guideline's 3-corner rule with the
// pointing-side Mini distinction — the rules the scoring table at
// tetris.wiki/Scoring ("Recent guideline compatible games") assumes.
type TSpin int

const (
	TSpinNone TSpin = iota
	TSpinMini
	TSpinFull
)

// String is the Guideline's name for the spin ("" for none).
func (s TSpin) String() string {
	switch s {
	case TSpinMini:
		return "MINI T-SPIN"
	case TSpinFull:
		return "T-SPIN"
	}
	return ""
}

// SpinState is how the falling piece reached where it rests: whether its
// last successful step was a rotation — a shift, a soft drop, a gravity
// step or a hard drop that actually moved the piece all forget it — and, if
// so, the SRS kick that rotation used (0 = in place … LastKick).
type SpinState struct {
	Rotated bool
	Kick    int
}

// LastKick is the index of the final entry of every SRS kick table — the
// kick that drops a T into a T-spin-triple slot as it turns. A T that got
// there by it is a full T-spin even when only one of its front corners is
// filled (the Guideline's exception to the Mini rule).
const LastKick = 4

// DetectTSpin judges the T resting at p: a T-spin needs the piece to be a T
// whose last move was a rotation (s), with at least three of the four
// corners of its 3×3 box filled — a locked cell, or the floor or a wall
// (another player's still-falling piece is not a corner: it may move away).
// It is a full T-spin when both corners on the side the T points to are
// filled, or when the rotation used the last kick; otherwise a Mini.
func DetectTSpin(p Piece, pf *Playfield, s SpinState) TSpin {
	if p.Type != PieceT || !s.Rotated {
		return TSpinNone
	}
	filled := func(r, c int) bool {
		if r < 0 || r >= pf.Height || c < 0 || c >= pf.Width {
			return true
		}
		cell := pf.Rows[r].Cells[c]
		return cell.Occupied && !cell.Active
	}
	tl, tr := filled(p.Row, p.Col), filled(p.Row, p.Col+2)
	bl, br := filled(p.Row+2, p.Col), filled(p.Row+2, p.Col+2)
	count := 0
	for _, f := range [4]bool{tl, tr, bl, br} {
		if f {
			count++
		}
	}
	if count < 3 {
		return TSpinNone
	}
	// The front corners flank the T's pointing cell (cellOffsets: the single
	// cell off the bar): up (0) the two top corners, right (1) the two right
	// ones, down (2) the bottom pair, left (3) the left pair.
	var front bool
	switch p.Orientation % 4 {
	case 0:
		front = tl && tr
	case 1:
		front = tr && br
	case 2:
		front = bl && br
	case 3:
		front = tl && bl
	}
	if front || s.Kick == LastKick {
		return TSpinFull
	}
	return TSpinMini
}
