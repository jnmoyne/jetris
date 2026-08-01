package nativeui

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
)

// testCtx builds a manual frame context of the given size (zero input.Source —
// disabled, safe headless).
func testCtx(w, h int) C {
	var ops op.Ops
	return layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(w, h)),
	}
}

// TestGlyphBitmaps pins the pad/chip bitmaps: rectangular (every row the same
// width) and drawn only with '.' and 'X'.
func TestGlyphBitmaps(t *testing.T) {
	for name, bm := range map[string][]string{
		"left": glyphLeft, "right": glyphRight, "down": glyphDown, "drop": glyphDrop,
		"cw": glyphCW, "ccw": glyphCCW,
	} {
		if len(bm) == 0 {
			t.Fatalf("%s: empty bitmap", name)
		}
		w := len(bm[0])
		for r, row := range bm {
			if len(row) != w {
				t.Fatalf("%s row %d: width %d, want %d", name, r, len(row), w)
			}
			for c, ch := range row {
				if ch != '.' && ch != 'X' {
					t.Fatalf("%s row %d col %d: bad rune %q", name, r, c, ch)
				}
			}
		}
	}
}

// TestFitCellPx pins the reactive cell sizing: it grows with the window, is
// clamped at both ends, and splits width across multiple boards.
func TestFitCellPx(t *testing.T) {
	const cols, rows = 10, 16
	small := fitCellPx(testCtx(400, 300), cols, rows, 1, 0, 0, 8, 56)
	big := fitCellPx(testCtx(2400, 1600), cols, rows, 1, 0, 0, 8, 56)
	if small >= big {
		t.Fatalf("cell should grow with the window: small=%d big=%d", small, big)
	}
	if got := fitCellPx(testCtx(50, 40), cols, rows, 1, 0, 0, 8, 56); got != 8 {
		t.Fatalf("tiny window should clamp to the 8dp minimum, got %d", got)
	}
	if got := fitCellPx(testCtx(9000, 9000), cols, rows, 1, 0, 0, 8, 56); got != 56 {
		t.Fatalf("huge window should clamp to the 56dp maximum, got %d", got)
	}
	one := fitCellPx(testCtx(1200, 4000), cols, rows, 1, 0, 0, 1, 1000)
	four := fitCellPx(testCtx(1200, 4000), cols, rows, 4, 0, 0, 1, 1000)
	if four >= one {
		t.Fatalf("four boards must share the width: one=%d four=%d", one, four)
	}
	// Degenerate inputs fall back to the minimum instead of dividing by zero.
	if got := fitCellPx(testCtx(1200, 800), 0, 0, 0, 0, 0, 8, 56); got != 8 {
		t.Fatalf("degenerate dims should return the minimum, got %d", got)
	}
}

// TestBufferedMovesStrip renders the strip empty, part-filled, and overflowing
// (the high-TTL-server case) — the overflowing strip must not be narrower than
// the filled one (slots plus the +N marker).
func TestBufferedMovesStrip(t *testing.T) {
	a := newTestApp()
	empty := a.bufferedMovesStrip(testCtx(1200, 820), nil)
	if empty.Size.X == 0 || empty.Size.Y == 0 {
		t.Fatal("empty strip rendered zero-size; the slot row must always be visible")
	}
	few := a.bufferedMovesStrip(testCtx(1200, 820), []engine.MoveType{
		engine.MoveLeft, engine.RotateCW, engine.MoveHardDrop,
	})
	over := make([]engine.MoveType, 20)
	for i := range over {
		over[i] = engine.MoveDown
	}
	full := a.bufferedMovesStrip(testCtx(1200, 820), over)
	if full.Size.X < few.Size.X {
		t.Fatalf("overflowing strip (%d px) narrower than part-filled (%d px)", full.Size.X, few.Size.X)
	}
	// The slot row keeps a constant footprint as it fills, so the board above
	// it never jumps.
	if empty.Size.Y != few.Size.Y {
		t.Fatalf("strip height changed when moves queued: empty=%d filled=%d", empty.Size.Y, few.Size.Y)
	}
}

// TestMoveGlyphAllMoves renders every move's chip glyph (and an unknown move)
// without panicking.
func TestMoveGlyphAllMoves(t *testing.T) {
	a := newTestApp()
	for _, m := range []engine.MoveType{
		engine.MoveLeft, engine.MoveRight, engine.MoveDown,
		engine.RotateCW, engine.RotateCCW, engine.MoveHardDrop,
		engine.MoveType(99),
	} {
		if d := a.moveChip(testCtx(64, 64), m, 42, 1, colGold); d.Size.X == 0 {
			t.Fatalf("move %v rendered a zero-size chip", m)
		}
		// Mid-pop chips draw shrunk but keep their slot footprint.
		if d := a.moveChip(testCtx(64, 64), m, 42, 0.5, colGold); d.Size.X != 42 {
			t.Fatalf("move %v mid-pop chip footprint = %d, want the 42px slot", m, d.Size.X)
		}
	}
}

// TestControlPad renders the pad enabled and disabled at several window sizes.
func TestControlPad(t *testing.T) {
	a := newTestApp()
	for _, sz := range []image.Point{{X: 700, Y: 500}, {X: 1200, Y: 820}, {X: 2400, Y: 1500}} {
		for _, enabled := range []bool{true, false} {
			if d := a.controlPad(testCtx(sz.X, sz.Y), enabled); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("pad (%v, enabled=%v) rendered zero-size", sz, enabled)
			}
		}
	}
}

// TestGameScreenReactive lays out the full in-progress player game screen —
// control pad and move-buffer strip included — at a small and a large window,
// exercising the window-reactive sizing paths end to end.
func TestGameScreenReactive(t *testing.T) {
	players := []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}
	for _, sz := range []image.Point{{X: 640, Y: 480}, {X: 1200, Y: 820}, {X: 2560, Y: 1440}} {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		gtx := testCtx(sz.X, sz.Y)
		if d := a.layout(gtx); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("game screen at %v rendered zero-size", sz)
		}
	}
}
