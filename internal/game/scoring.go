package game

// The Guideline scoring and garbage tables (tetris.wiki/Scoring,
// "Recent guideline compatible games"; tetris.wiki/Garbage, "General
// Garbage System in Guideline Games"). Every mode scores by them; the
// garbage table applies to games created with the guideline-garbage rule.

// Clear is one lock's line clear — or the lack of one — with everything the
// Guideline values it by: how many lines, the spin that made it (a T-spin,
// or a 180 spin, which scores as a full T-spin), whether it extends a
// Back-to-Back chain, its place in a combo, and whether it left the board
// empty. A lock that cleared nothing is a Clear with Lines 0: a spin that
// clears nothing still scores (and keeps a Back-to-Back chain).
type Clear struct {
	Lines      int
	Spin       TSpin
	BackToBack bool // a difficult clear right after another difficult clear (no plain single, double or triple in between)
	Combo      int  // consecutive line-clearing locks before this one: 0 for the first clear of a run, 1 for the next…
	Perfect    bool // the clear left no locked cell on the board (garbage included)
}

// Difficult reports whether the clear is one the Guideline calls difficult
// — a Jetris, or any T-spin that cleared lines — the clears that start and
// extend a Back-to-Back chain. Only a plain single, double or triple breaks
// the chain; a lock that clears nothing leaves it as it was.
func (c Clear) Difficult() bool {
	return c.Lines == 4 || (c.Spin != TSpinNone && c.Lines > 0)
}

// lines is the clear's line count clamped to the tables' range.
func (c Clear) lines() int {
	return min(max(c.Lines, 0), 4)
}

// spin is the spin the tables know: a spin's table stops at three lines (a
// four-line clear is a Jetris, whatever turned the piece), and a three-line
// T-spin is always a full T-spin triple (there is no Mini one).
func (c Clear) spin() TSpin {
	if c.Spin == TSpinMini && c.Lines >= 3 {
		return TSpinFull
	}
	if c.Spin != TSpinNone && c.Lines >= 4 {
		return TSpinNone
	}
	return c.Spin
}

// basePoints is the action's row of the scoring table, before the
// Back-to-Back multiplier, the combo and the perfect-clear bonus.
func (c Clear) basePoints() int {
	n := c.lines()
	switch c.spin() {
	case TSpinFull, TSpin180:
		return [4]int{400, 800, 1200, 1600}[n] // T-Spin no lines, Single, Double, Triple; a 180 spin the same
	case TSpinMini:
		return [3]int{100, 200, 400}[n] // Mini T-Spin no lines, Single, Double
	}
	return [5]int{0, 100, 300, 500, 800}[n] // nothing, Single, Double, Triple, Jetris
}

// perfectPoints is the perfect-clear bonus, added on top of the clear.
func (c Clear) perfectPoints() int {
	n := c.lines()
	if !c.Perfect || n == 0 {
		return 0
	}
	if n == 4 && c.BackToBack {
		return 3200
	}
	return [5]int{0, 800, 1200, 1800, 2000}[n]
}

// Points is what the clear scores at level (the Guideline's 1-based level,
// the level BEFORE the clear): the action's points — one and a half times
// for a Back-to-Back difficult clear — plus 50 per combo count, plus the
// perfect-clear bonus, all multiplied by the level. Drop points are not part
// of it (DropPoints): they are neither multiplied nor doubled.
func (c Clear) Points(level int) int {
	level = max(level, 1)
	pts := c.basePoints()
	if c.BackToBack && c.Difficult() {
		pts = pts * 3 / 2
	}
	if c.Lines > 0 && c.Combo > 0 {
		pts += 50 * c.Combo
	}
	pts += c.perfectPoints()
	return pts * level
}

// AttackRows is the garbage the clear sends the opponents. Without the
// guideline rule every cleared line sends one row (T-spins and perfect
// clears count for nothing extra). With it, the Guideline table: a single
// sends nothing, a double 1, a triple 2, a Jetris 4; a Mini T-Spin single
// nothing and a Mini double 1; a T-Spin single 2, double 4, triple 6; a
// Back-to-Back difficult clear adds 1 (Mini single/double, T-Spin single),
// 2 (T-Spin double, Jetris) or 3 (T-Spin triple); a perfect clear adds 10.
func (c Clear) AttackRows(guideline bool) int {
	if c.Lines <= 0 {
		return 0
	}
	if !guideline {
		return c.Lines
	}
	n := c.lines()
	var rows, b2b int
	switch c.spin() {
	case TSpinFull, TSpin180:
		rows, b2b = [4]int{0, 2, 4, 6}[n], [4]int{0, 1, 2, 3}[n]
	case TSpinMini:
		rows, b2b = [3]int{0, 0, 1}[n], 1
	default:
		rows = [5]int{0, 0, 1, 2, 4}[n]
		if n == 4 {
			b2b = 2
		}
	}
	if c.BackToBack && c.Difficult() {
		rows += b2b
	}
	if c.Perfect {
		rows += 10
	}
	return rows
}

// Name is the Guideline's name for the clear — "JETRIS", "T-SPIN DOUBLE",
// "MINI T-SPIN SINGLE", "180 SPIN TRIPLE", "T-SPIN" for a T-spin that
// cleared nothing — and "" for a lock that neither cleared nor spun.
func (c Clear) Name() string {
	n := c.lines()
	spin := c.spin().String()
	if n == 0 {
		return spin
	}
	name := [5]string{"", "SINGLE", "DOUBLE", "TRIPLE", "JETRIS"}[n]
	if spin == "" {
		return name
	}
	return spin + " " + name
}

// DropPoints is what the piece earned on its way down: one point per cell
// soft-dropped, two per cell hard-dropped. Unlike a clear's points they are
// not multiplied by the level.
func DropPoints(softCells, hardCells int) int {
	return max(softCells, 0) + 2*max(hardCells, 0)
}

// IsPerfectClear reports whether the board holds no locked cell — a
// player's or a garbage row's — once a clear has collapsed it (rows is the
// projected board). Falling pieces do not count: on a shared board the
// other players' pieces are still in the air.
func IsPerfectClear(rows []Row) bool {
	for _, r := range rows {
		for _, c := range r.Cells {
			if c.Occupied && !c.Active {
				return false
			}
		}
	}
	return true
}
