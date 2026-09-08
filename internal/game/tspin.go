package game

// TSpin classifies how a piece spun into where it rests: not a spin at all,
// a Mini T-Spin, a full T-Spin — the Guideline's 3-corner rule with the
// pointing-side Mini distinction, the rules the scoring table at
// tetris.wiki/Scoring ("Recent guideline compatible games") assumes — or a
// 180 spin: any piece half-turned (Turn180) into a spot it cannot shift out
// of, scored like a full T-spin and named for the turn (DetectSpin). The
// value travels on the wire (GameEvent.TSpin), so new kinds are appended.
type TSpin int

const (
	TSpinNone TSpin = iota
	TSpinMini
	TSpinFull
	TSpin180
)

// String is the name for the spin ("" for none).
func (s TSpin) String() string {
	switch s {
	case TSpinMini:
		return "MINI T-SPIN"
	case TSpinFull:
		return "T-SPIN"
	case TSpin180:
		return "180 SPIN"
	}
	return ""
}

// SpinState is how the falling piece reached where it rests: whether its
// last successful step was a rotation — a shift, a soft drop, a gravity
// step or a hard drop that actually moved the piece all forget it — and, if
// so, which kick of the turn's own table it used (0 = in place … LastKick
// for a quarter turn, further for a half turn) and whether the turn was a
// half turn (Turn180). The half turn's table has no T-spin-triple kick, so
// its Kick is never the LastKick exception, whatever its index.
type SpinState struct {
	Rotated bool
	Kick    int
	Half    bool
}

// LastKick is the index of the final entry of every SRS quarter-turn kick
// table — the kick that drops a T into a T-spin-triple slot as it turns. A T
// that got there by it is a full T-spin even when only one of its front
// corners is filled (the Guideline's exception to the Mini rule).
const LastKick = 4

// DetectSpin judges how the piece resting at p spun in, if it did. After a
// half turn (s.Half) any piece is a 180 spin when it rests immobile — unable
// to shift left, right or up, the walls and the locked cells blocking it
// (the wiki's "immobile" twist rule, harddrop.com/wiki/List_of_twists,
// "Rewards for twists"; another player's falling piece blocks nothing, it
// may move away) — and so is a T the 3-corner rule calls a full T-spin. After
// a quarter turn a T is judged by that rule alone (DetectTSpin) and any other
// piece is no spin at all.
func DetectSpin(p Piece, pf *Playfield, s SpinState) TSpin {
	if !s.Rotated {
		return TSpinNone
	}
	if !s.Half {
		return DetectTSpin(p, pf, s)
	}
	if immobile(p, pf) || DetectTSpin(p, pf, s) == TSpinFull {
		return TSpin180
	}
	return TSpinNone
}

// immobile reports whether p can shift neither left, right nor up: every
// such move puts a cell off the board or onto a locked one.
func immobile(p Piece, pf *Playfield) bool {
	for _, d := range [3][2]int{{0, -1}, {0, 1}, {-1, 0}} {
		q := p
		q.Row += d[0]
		q.Col += d[1]
		if fitsLocked(q, pf) {
			return false
		}
	}
	return true
}

// fitsLocked reports whether every cell of q is on the board and not locked
// (an active cell — a falling piece's — does not count as in the way).
func fitsLocked(q Piece, pf *Playfield) bool {
	for _, c := range q.Cells() {
		r, col := c[0], c[1]
		if r < 0 || r >= pf.Height || col < 0 || col >= pf.Width {
			return false
		}
		if cell := pf.Rows[r].Cells[col]; cell.Occupied && !cell.Active {
			return false
		}
	}
	return true
}

// DetectTSpin judges the T resting at p: a T-spin needs the piece to be a T
// whose last move was a rotation (s), with at least three of the four
// corners of its 3×3 box filled — a locked cell, or the floor or a wall
// (another player's still-falling piece is not a corner: it may move away).
// It is a full T-spin when both corners on the side the T points to are
// filled, or when a quarter turn used the last kick; otherwise a Mini.
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
	if front || (!s.Half && s.Kick == LastKick) {
		return TSpinFull
	}
	return TSpinMini
}
