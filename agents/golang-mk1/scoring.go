package main

import "strconv"

// The Guideline scoring and garbage tables (tetris.wiki/Scoring,
// "Recent guideline compatible games"; tetris.wiki/Garbage, "General Garbage
// System in Guideline Games") — the same rules the GUI plays by
// (internal/game/scoring.go), ported rather than imported: this agent is the
// protocol's reference implementation for other languages and shares no
// code with the GUI. Every mode scores by them; the garbage table applies
// to games created with the guideline_garbage rule.

// tspin classifies how a T came to rest: not a T-spin, a Mini, or a full
// T-spin. This agent never makes one — it rotates at the spawn, shifts, and
// drops, so a rotation is never its last move — but a clear carries the
// field so the table reads like the GUI's (the line_clear event's t_spin).
type tspin int

const (
	spinNone tspin = iota
	spinMini
	spinFull
)

// clearInfo is one lock's line clear — or the lack of one — with
// everything the Guideline values it by.
type clearInfo struct {
	lines      int
	spin       tspin
	backToBack bool // a difficult clear right after another difficult clear (no plain single, double or triple in between)
	combo      int  // consecutive line-clearing locks before this one: 0 for the first clear of a run, 1 for the next…
	perfect    bool // the clear left no settled cell on the board (garbage included)
}

// difficult: a quad, or any T-spin that cleared lines — the clears that
// start and extend a Back-to-Back chain. Only a plain single, double or
// triple breaks the chain; a lock that clears nothing leaves it alone.
func (c clearInfo) difficult() bool {
	return c.lines == 4 || (c.spin != spinNone && c.lines > 0)
}

func (c clearInfo) n() int { return min(max(c.lines, 0), 4) }

// spinKind is the T-spin the tables know: a three-line T-spin is always a
// full T-spin triple, and a T cannot clear four lines.
func (c clearInfo) spinKind() tspin {
	if c.spin == spinMini && c.lines >= 3 {
		return spinFull
	}
	if c.spin != spinNone && c.lines >= 4 {
		return spinNone
	}
	return c.spin
}

// points is what the clear scores at level (the Guideline's 1-based level,
// the level BEFORE the clear): the action's points — Single 100, Double 300,
// Triple 500, quad 800; Mini T-Spin 100/200/400 for none/1/2 lines; T-Spin
// 400/800/1200/1600 for none/1/2/3 — one and a half times for a Back-to-Back
// difficult clear, plus 50 per combo count, plus the perfect-clear bonus
// (800/1200/1800/2000 for 1-4 lines, 3200 for a Back-to-Back quad), all
// multiplied by the level. Drop points are not part of it (dropPoints).
func (c clearInfo) points(level int) int {
	level = max(level, 1)
	n := c.n()
	var pts int
	switch c.spinKind() {
	case spinFull:
		pts = [4]int{400, 800, 1200, 1600}[n]
	case spinMini:
		pts = [3]int{100, 200, 400}[n]
	default:
		pts = [5]int{0, 100, 300, 500, 800}[n]
	}
	if c.backToBack && c.difficult() {
		pts = pts * 3 / 2
	}
	if n > 0 && c.combo > 0 {
		pts += 50 * c.combo
	}
	if c.perfect && n > 0 {
		if n == 4 && c.backToBack {
			pts += 3200
		} else {
			pts += [5]int{0, 800, 1200, 1800, 2000}[n]
		}
	}
	return pts * level
}

// attackRows is the garbage the clear owes the opponents: one row per
// cleared line, or under the guideline_garbage rule the Guideline table — a
// single sends nothing, a double 1, a triple 2, a quad 4; a Mini T-Spin
// single nothing and a Mini double 1; a T-Spin single 2, double 4, triple 6;
// a Back-to-Back difficult clear adds 1 (Mini single/double, T-Spin single),
// 2 (T-Spin double, quad) or 3 (T-Spin triple); a perfect clear adds 10.
func (c clearInfo) attackRows(guideline bool) int {
	if c.lines <= 0 {
		return 0
	}
	if !guideline {
		return c.lines
	}
	n := c.n()
	var rows, b2b int
	switch c.spinKind() {
	case spinFull:
		rows, b2b = [4]int{0, 2, 4, 6}[n], [4]int{0, 1, 2, 3}[n]
	case spinMini:
		rows, b2b = [3]int{0, 0, 1}[n], 1
	default:
		rows = [5]int{0, 0, 1, 2, 4}[n]
		if n == 4 {
			b2b = 2
		}
	}
	if c.backToBack && c.difficult() {
		rows += b2b
	}
	if c.perfect {
		rows += 10
	}
	return rows
}

// name is the Guideline's name for the clear, for the log.
func (c clearInfo) name() string {
	n := c.n()
	name := [5]string{"nothing", "single", "double", "triple", "quad"}[n]
	switch c.spinKind() {
	case spinFull:
		name = "t-spin " + name
	case spinMini:
		name = "mini t-spin " + name
	}
	if c.backToBack {
		name += ", back-to-back"
	}
	if c.combo > 0 {
		name += ", combo " + strconv.Itoa(c.combo)
	}
	if c.perfect {
		name += ", perfect clear"
	}
	return name
}

// dropPoints is what the piece earned on its way down: one point per cell
// soft-dropped, two per cell hard-dropped — never multiplied by the level.
// This agent only ever hard-drops (or lets gravity carry the piece, which
// earns nothing).
func dropPoints(softCells, hardCells int) int {
	return max(softCells, 0) + 2*max(hardCells, 0)
}
