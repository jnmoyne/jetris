package nativeui

import (
	"testing"

	"gioui.org/widget"

	"jetris/internal/engine"
	"jetris/internal/game"
)

// stripBoards builds n empty boards of the given shape for the strip.
func stripBoards(n, rows, cols int) []labeledBoard {
	out := make([]labeledBoard, n)
	for i := range out {
		r := make([]game.Row, rows)
		for j := range r {
			r[j] = game.NewRow(cols)
		}
		out[i] = labeledBoard{label: "p", idx: i, snap: engine.BoardSnapshot{Width: cols, Height: rows, Rows: r}}
	}
	return out
}

// The replay screen and the archive viewer draw their boards through
// boardsStrip, and the boards they are handed are whatever height the game
// was PLAYED at — 20 rows today, more for a game archived before every board
// became config.VisibleRows tall. Whichever it is, the strip has to fit the
// room it is given rather than stand at a fixed cell size and run off the
// bottom of the screen.
func TestBoardsStripFitsItsBoards(t *testing.T) {
	a := newTestApp()
	for _, rows := range []int{20, 26, 30} {
		for _, n := range []int{1, 2, 6} {
			for _, sz := range [][2]int{{1200, 820}, {1200, 500}, {1000, 300}, {390, 560}} {
				gtx := looseCtx(sz[0], sz[1])
				d := a.boardsStrip(gtx, new(widget.List), stripBoards(n, rows, 10))
				if d.Size.Y > sz[1] || d.Size.X > sz[0] {
					t.Errorf("%d board(s) of %d rows in a %dx%d window: strip is %v", n, rows, sz[0], sz[1], d.Size)
				}
			}
		}
	}
}

// A shorter board is drawn bigger than a taller one in the same window: the
// cell answers to the rows in hand, which is what "fits" means here. The
// window is short enough that the height, not the cell's own maximum, is what
// settles it.
func TestBoardsStripCellFollowsTheRows(t *testing.T) {
	a := newTestApp()
	gtx := looseCtx(1000, 400)
	short := a.boardsStrip(gtx, new(widget.List), stripBoards(2, 20, 10))
	tall := a.boardsStrip(gtx, new(widget.List), stripBoards(2, 30, 10))
	if short.Size.X <= tall.Size.X {
		t.Errorf("20-row boards drawn %v, 30-row boards %v: the shorter pair should get the bigger cell", short.Size, tall.Size)
	}
}

// The strip's boards take the room a bigger window opens up, the way the
// game screen's playfield does (boardCellMaxDp is the one cap): a replay or
// an archived board watched on a 2560×1440 or a 4K window is drawn larger
// than on the default one, past the 16 dp cell the strip used to stop at.
func TestBoardsStripGrowsWithTheWindow(t *testing.T) {
	a := newTestApp()
	last := 0
	for _, sz := range [][2]int{{1200, 820}, {2560, 1440}, {3840, 2160}} {
		gtx := looseCtx(sz[0], sz[1])
		d := a.boardsStrip(gtx, new(widget.List), stripBoards(2, 20, 10))
		if d.Size.Y <= last {
			t.Fatalf("%dx%d window: strip is %v, want taller than the %d px of the smaller window", sz[0], sz[1], d.Size, last)
		}
		if d.Size.Y > sz[1] || d.Size.X > sz[0] {
			t.Fatalf("%dx%d window: strip is %v, past the window", sz[0], sz[1], d.Size)
		}
		last = d.Size.Y
	}
	// Two 10-wide boards at the old 16 dp cap are 2×(16×82/8 + 16) px
	// across; a 2560×1440 window draws them well past that.
	gtx := looseCtx(2560, 1440)
	d := a.boardsStrip(gtx, new(widget.List), stripBoards(2, 20, 10))
	if old := 2 * (16*82/8 + 16); d.Size.X <= old {
		t.Fatalf("2560x1440 window: strip is %d px wide, want past the %d px of the old 16 dp cap", d.Size.X, old)
	}
}
