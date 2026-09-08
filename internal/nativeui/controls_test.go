package nativeui

import (
	"image"
	"strconv"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
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
		"cw": glyphCW, "ccw": glyphCCW, "half": glyph180, "hold": glyphHold,
		"menu": glyphMenu, "chat": glyphChat, "pad": glyphPad,
		"boards": glyphBoards, "trophy": glyphTrophy,
		"mic": glyphMic, "micoff": glyphMicOff, "speaker": glyphSpeaker,
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
	empty := a.bufferedMovesStrip(testCtx(1200, 820), nil, 0)
	if empty.Size.X == 0 || empty.Size.Y == 0 {
		t.Fatal("empty strip rendered zero-size; the slot row must always be visible")
	}
	few := a.bufferedMovesStrip(testCtx(1200, 820), [][]engine.MoveType{
		{engine.MoveLeft}, {engine.RotateCW}, {engine.MoveHardDrop},
	}, 0)
	over := make([][]engine.MoveType, 20)
	for i := range over {
		over[i] = []engine.MoveType{engine.MoveDown}
	}
	full := a.bufferedMovesStrip(testCtx(1200, 820), over, 0)
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
		engine.RotateCW, engine.RotateCCW, engine.Rotate180, engine.MoveHardDrop, engine.MoveHold,
		engine.MoveType(99),
	} {
		if d := a.moveGlyph(m, 24, colFg)(looseCtx(64, 64)); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("move %v rendered a zero-size glyph", m)
		}
	}
}

// looseCtx is testCtx with no minimum, so a widget's size is its own.
func looseCtx(w, h int) C {
	gtx := testCtx(w, h)
	gtx.Constraints.Min = image.Point{}
	return gtx
}

// TestControlPad renders the pad at both metrics, enabled and disabled, with
// and without the HOLD bar — which is only there in a game with the hold
// rule, and adds a row to the face cluster when it is — and checks its
// geometry: the D-pad is a square of three arms, the touch pad's arms are
// thumb-sized (bigger than a move-buffer chip and past the 44 dp tap floor),
// and HOLD makes the pad taller, never wider.
func TestControlPad(t *testing.T) {
	a := newTestApp()
	for _, m := range []padMetrics{padMouse, padTouch} {
		p := padSizer{m: m, scale: 1}
		for _, enabled := range []bool{true, false} {
			gtx := looseCtx(1200, 820)
			if d := a.dpad(gtx, p, enabled); d.Size.X != d.Size.Y || d.Size.X != 3*gtx.Dp(unit.Dp(m.btn)) {
				t.Fatalf("D-pad (%+v, enabled=%v) = %v, want a %d square", m, enabled, d.Size, 3*m.btn)
			}
			plain := a.controlPad(looseCtx(1200, 820), p, enabled, false)
			hold := a.controlPad(looseCtx(1200, 820), p, enabled, true)
			if plain.Size.X == 0 || plain.Size.Y == 0 {
				t.Fatalf("pad (%+v, enabled=%v) rendered zero-size", m, enabled)
			}
			if hold.Size.X != plain.Size.X || hold.Size.Y <= plain.Size.Y {
				t.Fatalf("pad with HOLD (%+v, enabled=%v) = %v, want taller than %v at the same width", m, enabled, hold.Size, plain.Size)
			}
			if got, want := plain.Size, p.padSize(looseCtx(1200, 820), false); got != want {
				t.Fatalf("pad (%+v) = %v, padSize says %v", m, got, want)
			}
		}
	}
	if padTouch.btn < 44 || padTouch.btn <= 42 {
		t.Fatalf("touch arm = %d dp, want past the 44 dp tap floor and the 42 dp buffer chip", padTouch.btn)
	}
	if padTouch.btn <= padMouse.btn {
		t.Fatalf("touch arm = %d dp, mouse arm = %d dp: touch should be the bigger", padTouch.btn, padMouse.btn)
	}
}

// TestFitBoardAndPad exercises the pad's placement: a wide, short (landscape)
// column flanks the playfield with the pad and keeps the board's cell bigger
// than stacking would; a tall, narrow (portrait) column stacks it under the
// board; a narrow column shrinks the pad, floored, never clipping; no pad —
// a spectator, the game over — leaves the cell to the board alone; and the
// touch pad's arms stay thumb-sized wherever an iPad's columns put them.
func TestFitBoardAndPad(t *testing.T) {
	const cols, rows = 10, 20 // every board is 20 visible rows tall
	a := newTestApp()
	// The HOLD box and a three-piece NEXT well flank the playfield.
	wells := sideWells{hold: true, next: true,
		holdH: func(cell int) int { return a.holdWellBox(looseCtx(400, 400), game.PieceI, false, false, cell).Size.Y },
		nextH: func(cell int) int {
			return a.nextWell(looseCtx(400, 400), []game.PieceType{game.PieceT, game.PieceT, game.PieceT}, cell).Size.Y
		},
	}
	landscape := a.fitBoardAndPad(looseCtx(1000, 560), cols, rows, wells, true, true, true)
	if !landscape.beside {
		t.Fatalf("landscape column: pad under the board, want beside it (%+v)", landscape)
	}
	if landscape.scale != 1 {
		t.Fatalf("landscape column: pad scaled to %v, want its natural size", landscape.scale)
	}
	noPad := a.fitBoardAndPad(looseCtx(1000, 560), cols, rows, wells, true, false, true)
	if noPad.cell < landscape.cell || noPad.beside {
		t.Fatalf("no pad: cell %d beside=%v, want at least the flanked cell %d and no placement", noPad.cell, noPad.beside, landscape.cell)
	}
	portrait := a.fitBoardAndPad(looseCtx(560, 1000), cols, rows, wells, true, true, true)
	if portrait.beside {
		t.Fatalf("portrait column: pad beside the board, want under it (%+v)", portrait)
	}
	if portrait.scale != 1 {
		t.Fatalf("portrait column: pad scaled to %v, want its natural size", portrait.scale)
	}
	// Columns narrower than the pad's natural row: the pad scales down,
	// floored, and its row still fits the column (a playfield at its cell
	// floor is fitCellPx's own business).
	for _, sz := range []image.Point{{X: 200, Y: 900}, {X: 240, Y: 900}} {
		gtx := looseCtx(sz.X, sz.Y)
		plan := a.fitBoardAndPad(gtx, cols, rows, wells, true, true, true)
		if plan.scale >= 1 || plan.scale < padMinScale {
			t.Fatalf("%v column: pad scale %v, want shrunk within [%v, 1)", sz, plan.scale, padMinScale)
		}
		if plan.width > sz.X {
			t.Fatalf("%v column: %+v is %d px wide", sz, plan, plan.width)
		}
	}
	// The minimum window's competitive column (the opponent column beside
	// it): the pad shrinks to leave a minimum-cell playfield its rows, and
	// nothing sticks out.
	minGtx := looseCtx(387, 506)
	minPlan := a.fitBoardAndPad(minGtx, cols, rows, wells, true, true, true)
	if minPlan.width > 387 {
		t.Fatalf("minimum window: %+v is %d px wide", minPlan, minPlan.width)
	}
	if h := minPlan.cell*(8*rows+2)/8 + minGtx.Dp(90) + minPlan.padSize(minGtx, true).Y + minGtx.Dp(13); !minPlan.beside && h > 506+minGtx.Dp(24) {
		t.Fatalf("minimum window: the board and the pad under it stand %d px tall in a 506 px column", h)
	}
	if minPlan.scale < padMinScaleY {
		t.Fatalf("minimum window: pad scale %v, under its floor %v", minPlan.scale, padMinScaleY)
	}
	// Touch: an iPad's board column — landscape competitive games (20 rows,
	// the opponent column beside them, the browser's toolbar over them: an
	// 11" and a 10.2") and portrait — the arms stay at (about) their
	// natural size and the row fits: under a three-piece NEXT well, the
	// face cluster with its HOLD bar is a few percent short of the 10.2"
	// column's 20-row playfield height at full size — the shortest of them
	// asks the arms for a few percent, the board's cell having grown into
	// the height the 20-row playfield leaves it.
	a.touchUI = true
	for _, c := range []struct {
		sz   image.Point
		rows int
	}{{image.Pt(759, 516), rows}, {image.Pt(702, 460), rows}, {image.Pt(447, 906), rows}} {
		gtx := looseCtx(c.sz.X, c.sz.Y)
		plan := a.fitBoardAndPad(gtx, cols, c.rows, wells, true, true, true)
		if plan.m != padTouch || plan.scale < 0.85 {
			t.Fatalf("touch pad in a %v column: %+v, want the touch metrics at (about) their natural size", c.sz, plan)
		}
		if plan.width > c.sz.X {
			t.Fatalf("touch pad in a %v column: %+v is %d px wide", c.sz, plan, plan.width)
		}
	}
}

// TestHoldWell renders the HOLD box in each of its states — empty, holding a
// piece, and holding a spent (dimmed) piece — at the well's footprint: every
// state keeps the same size, so the box never shifts the D-pad under it.
func TestHoldWell(t *testing.T) {
	a := newTestApp()
	empty := a.holdWell(looseCtx(400, 400), game.PieceI, false, false, 24)
	if empty.Size.X == 0 || empty.Size.Y == 0 {
		t.Fatal("empty HOLD box rendered zero-size")
	}
	for _, pt := range []game.PieceType{game.PieceI, game.PieceO, game.PieceT} {
		for _, used := range []bool{false, true} {
			if d := a.holdWell(looseCtx(400, 400), pt, true, used, 24); d.Size != empty.Size {
				t.Fatalf("HOLD box holding %v (used=%v) = %v, want the empty box's %v", pt, used, d.Size, empty.Size)
			}
		}
	}
	// The box is as wide as the NEXT well across the playfield, at the same
	// cell size.
	next := a.nextWell(looseCtx(400, 400), []game.PieceType{game.PieceT}, 24)
	if next.Size.X != empty.Size.X {
		t.Fatalf("HOLD box width %d, want the NEXT well's %d", empty.Size.X, next.Size.X)
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

// TestBufferedMovesStripCaptionKeepsHeight: the queue filling and draining
// must not change the strip's height — the board is centered over it, and a
// two-pixel change re-centers the whole playfield on every key press. (An
// empty pixel label is taller than a real one, which is exactly what used to
// happen when the caption dropped a run.) Checked at 1× and 2× (a HiDPI
// window).
func TestBufferedMovesStripCaptionKeepsHeight(t *testing.T) {
	a := newTestApp()
	for _, scale := range []float32{1, 2} {
		ctx := func() C {
			var ops op.Ops
			return layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
				Constraints: layout.Constraints{Max: image.Pt(1200, 820)},
			}
		}
		few := [][]engine.MoveType{{engine.MoveLeft}, {engine.MoveDown}}
		grouped := [][]engine.MoveType{{engine.MoveLeft, engine.MoveDown, engine.RotateCW}, {engine.MoveHardDrop}, {engine.MoveDown, engine.MoveDown}}
		many := make([][]engine.MoveType, 12)
		for i := range many {
			many[i] = []engine.MoveType{engine.MoveDown}
		}
		idle := a.bufferedMovesStrip(ctx(), nil, 0).Size.Y
		for _, c := range []struct {
			name  string
			moves [][]engine.MoveType
		}{{"one batch", few[:1]}, {"queued", few}, {"grouped", grouped}, {"two digits queued", many}} {
			if h := a.bufferedMovesStrip(ctx(), c.moves, 3).Size.Y; h != idle {
				t.Fatalf("at %vx, %s: strip height %d px, idle %d px — the board over it would jump", scale, c.name, h, idle)
			}
		}
	}
}

// TestBufferedCaption: the caption is the same label plus the plain count for
// every queue depth — "0" when empty, never a word that comes and goes. It
// used to read EMPTY against "3 QUEUED" with an IN FLIGHT tail behind them,
// re-centering the line on every key press. The label is what the strip
// centres, and the count hangs off its right, so a three-digit queue must
// still land inside the narrowest strip (the compact screen's) — the strip is
// only ever as wide as its slot row.
func TestBufferedCaption(t *testing.T) {
	head, empty := bufferedCaption(0)
	if empty != "0" {
		t.Fatalf("empty buffer's count is %q, want %q", empty, "0")
	}
	for _, n := range []int{1, 3, 8, 12, 137} {
		h, c := bufferedCaption(n)
		if h != head {
			t.Fatalf("caption label changed at %d queued: %q, want %q", n, h, head)
		}
		if c != strconv.Itoa(n) {
			t.Fatalf("count at %d queued is %q, want %q", n, c, strconv.Itoa(n))
		}
	}
	a := newTestApp()
	a.form.compact = true
	gtx := testCtx(390, 780)
	strip := a.stripWidth(gtx)
	headW := a.pixelWidth(gtx, unit.Sp(8), head)
	lead := (strip - headW) / 2 // where the centred label starts
	if lead < 0 {
		t.Fatalf("compact label %d px wide, past the %d px strip it sits on", headW, strip)
	}
	if end := lead + headW + gtx.Dp(capCountGap) + a.pixelWidth(gtx, unit.Sp(8), "137"); end > strip {
		t.Fatalf("compact caption ends at %d px with a three-digit count, past the %d px strip", end, strip)
	}
}

// TestBufferedMovesStripWidthIsConstant: the strip is exactly the slot row
// wide whatever it shows — empty, a few moves, grouped batches with an
// in-flight count, a queue running past the slots — because the board is
// centered on it and a wider caption or overflow marker used to shift the
// whole playfield with every queued move.
func TestBufferedMovesStripWidthIsConstant(t *testing.T) {
	a := newTestApp()
	ctx := func() C {
		var ops op.Ops
		return layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Constraints{Max: image.Pt(1200, 820)},
		}
	}
	many := make([][]engine.MoveType, 12)
	for i := range many {
		many[i] = []engine.MoveType{engine.MoveDown}
	}
	grouped := [][]engine.MoveType{{engine.MoveDown, engine.MoveDown, engine.MoveDown}, {engine.MoveLeft, engine.MoveLeft}, {engine.MoveDown}}
	want := a.stripWidth(ctx())
	for _, c := range []struct {
		name    string
		batches [][]engine.MoveType
	}{{"empty", nil}, {"few", grouped[:1]}, {"grouped", grouped}, {"overflowing", many}} {
		if w := a.bufferedMovesStrip(ctx(), c.batches, 3).Size.X; w != want {
			t.Fatalf("%s: strip width %d px, want the slot row's %d — the board over it would shift", c.name, w, want)
		}
	}
}
