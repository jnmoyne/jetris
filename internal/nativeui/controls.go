package nativeui

// On-screen arcade controls (the D-pad and face buttons) and the buffered-moves
// ("MOVE BUFFER") strip for the game screen. Everything here is drawn in the 8-bit chrome: blocky
// bitmap glyphs built from fillRect squares, the pixel face for labels, chunky
// borders and hard shadows — no smooth icon fonts, keeping the 80's low-res
// look at any window size.

import (
	"fmt"
	"image"
	"math"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/engine"
)

// Blocky glyph bitmaps ('X' = filled square) for the control pad and the
// move-buffer chips, scaled square-by-square so they stay crisp and low-res.
var (
	glyphLeft = []string{
		"...X....",
		"..XX....",
		".XXXXXXX",
		"XXXXXXXX",
		"XXXXXXXX",
		".XXXXXXX",
		"..XX....",
		"...X....",
	}
	glyphRight = []string{
		"....X...",
		"....XX..",
		"XXXXXXX.",
		"XXXXXXXX",
		"XXXXXXXX",
		"XXXXXXX.",
		"....XX..",
		"....X...",
	}
	glyphDown = []string{
		"..XXXX..",
		"..XXXX..",
		"..XXXX..",
		"..XXXX..",
		"XXXXXXXX",
		".XXXXXX.",
		"..XXXX..",
		"...XX...",
	}
	// glyphDrop is the hard-drop icon: a down arrow slamming onto a floor bar.
	glyphDrop = []string{
		"..XXXX..",
		"..XXXX..",
		"XXXXXXXX",
		".XXXXXX.",
		"..XXXX..",
		"...XX...",
		"........",
		"XXXXXXXX",
	}
	// glyphCW is the clockwise-rotate icon: a blocky ring open on the right,
	// its top arc ending in a down-pointing arrowhead (curling over the top
	// and diving down the right side = clockwise). glyphCCW is its mirror.
	glyphCW = []string{
		"..XXXXX..",
		".XXXXXXX.",
		"XXX.XXXXX",
		"XX...XXX.",
		"XX....X..",
		"XX.......",
		"XXX...XXX",
		".XXXXXXX.",
		"..XXXXX..",
	}
	glyphCCW = mirrored(glyphCW)
	// glyphHold is the hold icon: two arrows trading places (⇄) — the swap
	// symbol the phone versions of the game put on their hold button — as a
	// blocky 9-row bitmap so it scales like the 8-row arrows.
	glyphHold = []string{
		".....XX.",
		"XXXXXXXX",
		"XXXXXXXX",
		".....XX.",
		"........",
		".XX.....",
		"XXXXXXXX",
		"XXXXXXXX",
		".XX.....",
	}
)

// mirrored flips a glyph bitmap horizontally (derives the CCW rotate arrow
// from the CW one).
func mirrored(bm []string) []string {
	out := make([]string, len(bm))
	for i, row := range bm {
		b := []byte(row)
		for l, r := 0, len(b)-1; l < r; l, r = l+1, r-1 {
			b[l], b[r] = b[r], b[l]
		}
		out[i] = string(b)
	}
	return out
}

// pixelGlyph draws bitmap bm as filled squares scaled so the glyph is px tall,
// at the current transform origin. Returns the drawn size.
func pixelGlyph(gtx C, bm []string, px int, col colorN) D {
	rows := len(bm)
	if rows == 0 {
		return D{}
	}
	cols := len(bm[0])
	u := px / rows
	if u < 1 {
		u = 1
	}
	for r, row := range bm {
		for c := 0; c < len(row); c++ {
			if row[c] == 'X' {
				fillRect(gtx.Ops, image.Rect(c*u, r*u, (c+1)*u, (r+1)*u), col)
			}
		}
	}
	return D{Size: image.Pt(cols*u, rows*u)}
}

// glyphWidget wraps pixelGlyph as a layout.Widget at a dp-specified height.
func glyphWidget(bm []string, size unit.Dp, col colorN) layout.Widget {
	return func(gtx C) D { return pixelGlyph(gtx, bm, gtx.Dp(size), col) }
}

// moveGlyph is the chip/pad symbol for one move: blocky bitmap arrows for the
// shifts and the hard drop, blocky circular arrows for the rotations — one
// glyph language shared by the buffer chips and the control pad.
func (a *App) moveGlyph(m engine.MoveType, size unit.Dp, col colorN) layout.Widget {
	switch m {
	case engine.MoveLeft:
		return glyphWidget(glyphLeft, size, col)
	case engine.MoveRight:
		return glyphWidget(glyphRight, size, col)
	case engine.MoveDown:
		return glyphWidget(glyphDown, size, col)
	case engine.MoveHardDrop:
		return glyphWidget(glyphDrop, size, col)
	case engine.RotateCW:
		return glyphWidget(glyphCW, size, col)
	case engine.RotateCCW:
		return glyphWidget(glyphCCW, size, col)
	case engine.MoveHold:
		return glyphWidget(glyphHold, size, col)
	}
	return a.pixel(unit.Sp(11), "?", col).Layout
}

// bufferedSlots is how many chip slots the move-buffer strip always shows;
// moves beyond it collapse into a "+N" overflow marker. bufPopDur is the
// pop-in animation length of a freshly queued chip.
const (
	bufferedSlots = 8
	bufPopDur     = 200 * time.Millisecond
)

// stripWidth is the move-buffer strip's width in px: its row of chip slots
// (the label over them is narrower). fitBoardAndPad plans around it, as the
// strip is wider than the playfield at most cell sizes.
func stripWidth(gtx C) int { return bufferedSlots * (gtx.Dp(42) + gtx.Dp(7)) }

// bufferedMovesStrip is the player's move-buffer readout under the board: a
// row of big chunky chip slots that fill with bright gold glyphs as inputs
// queue behind the in-flight batch publish (very visible on a high-RTT
// server) and drain as each buffered move's own publish starts. It behaves
// like an arcade combo meter: a freshly queued chip pops in with an
// overshoot, and while anything is queued a glow chases across the chips.
// The dim empty slots are always there while playing, so a filling buffer is
// impossible to miss and the board never jumps as it fills.
func (a *App) bufferedMovesStrip(gtx C, moves []engine.MoveType) D {
	chip := gtx.Dp(42)
	now := gtx.Now
	if n := len(moves); n != a.bufN {
		if n > a.bufN {
			a.bufGrewAt = now // a new chip landed: run its pop-in
		}
		a.bufN = n
	}
	popping := now.Sub(a.bufGrewAt) < bufPopDur
	if len(moves) > 0 || popping {
		animate(gtx) // keep the chase glow / pop-in animating
	}

	count, countCol := "EMPTY", colMuted
	if n := len(moves); n > 0 {
		count, countCol = fmt.Sprintf("%d QUEUED", n), colGold
	}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(a.pixel(unit.Sp(9), "MOVE BUFFER  ", colMuted).Layout),
				layout.Rigid(a.pixel(unit.Sp(9), count, countCol).Layout),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			var kids []layout.FlexChild
			for i := 0; i < bufferedSlots; i++ {
				i := i
				kids = append(kids, layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: unit.Dp(7)}.Layout(gtx, func(gtx C) D {
						if i >= len(moves) {
							return emptySlot(gtx, chip)
						}
						// Chase glow: a brightness wave runs left-to-right
						// across the queued chips (phase offset per slot).
						wave := 0.5 + 0.5*math.Sin(float64(now.UnixNano())/1e9*7-float64(i)*0.9)
						col := lighten(colGold, 0.35*wave)
						scale := 1.0
						if popping && i == len(moves)-1 {
							t := clampF(float64(now.Sub(a.bufGrewAt))/float64(bufPopDur), 0, 1)
							scale = 0.4 + 0.6*easeOutBack(t)
						}
						return a.moveChip(gtx, moves[i], chip, scale, col)
					})
				}))
			}
			if n := len(moves) - bufferedSlots; n > 0 {
				kids = append(kids, layout.Rigid(a.pixel(unit.Sp(14), fmt.Sprintf("+%d", n), colGold).Layout))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		}),
	)
}

// moveChip draws one queued move as a bright gold square with its glyph
// punched out in the background color — unmissable against the dark chrome.
// scale (0..1+, may overshoot past 1 during the pop-in) shrinks the chip
// inside its fixed slot so an animating chip never shifts its neighbours; the
// glyph appears once the chip has mostly landed.
func (a *App) moveChip(gtx C, m engine.MoveType, slot int, scale float64, col colorN) D {
	s := int(float64(slot)*clampF(scale, 0, 1.15) + 0.5)
	off := (slot - s) / 2
	fillRect(gtx.Ops, image.Rect(off, off, off+s, off+s), col)
	if scale > 0.75 {
		gtx.Constraints = layout.Exact(image.Pt(slot, slot))
		layout.Center.Layout(gtx, a.moveGlyph(m, 24, colBg))
	}
	return D{Size: image.Pt(slot, slot)}
}

// emptySlot draws a dim outlined square — a vacant position in the buffer strip.
func emptySlot(gtx C, sz int) D {
	b := gtx.Dp(2)
	fillRect(gtx.Ops, image.Rect(0, 0, sz, sz), colBorder)
	fillRect(gtx.Ops, image.Rect(b, b, sz-b, sz-b), colBg)
	return D{Size: image.Pt(sz, sz)}
}

// The control pad — the whole keyboard scheme as chunky arcade buttons, so
// the game is fully playable without a keyboard, and by tapping on a touch
// screen. It is laid out like a handheld's controls: on the left the D-PAD,
// a blocky cross whose four arms are the arrow keys (▲ rotates clockwise
// like ↑, ◀ ▶ shift, ▼ soft-drops); on the right the FACE BUTTONS — the two
// rotations ↺ ↻ (Z and X) over the wide accent-filled DROP bar (Space), the
// one button that commits a piece, and, only in games with the hold rule,
// the HOLD bar (C; the ⇄ swap icon of the phone versions' hold button). It
// renders dimmed until the game is running; handlePadClicks does the actual
// gating of clicks. Where the pad goes — beside the playfield or under it —
// and how big it is are decided per frame by fitBoardAndPad.

// padMetrics are the pad's natural sizes in dp. There is one set for a
// pointer and one for a touch screen (App.touchUI): a tap lands where the
// finger falls, not where a cursor points, so a touch button is a whole thumb
// wide — 72 dp, well past the 44 dp tap-target floor and bigger than a
// move-buffer chip — and the cluster gap keeps the two thumbs apart.
type padMetrics struct {
	btn        int     // side of the D-pad arms and the rotation buttons (squares)
	gap        int     // gap between the face buttons' rows and columns
	clusterGap int     // gap between the D-pad and the face buttons, pad in one row
	glyph      int     // glyph height in the squares
	barGlyph   int     // glyph height in the bars
	label      float32 // the bars' pixel-face label size (sp)
}

var (
	padMouse = padMetrics{btn: 48, gap: 8, clusterGap: 20, glyph: 18, barGlyph: 16, label: 9}
	padTouch = padMetrics{btn: 72, gap: 10, clusterGap: 28, glyph: 28, barGlyph: 24, label: 11}
)

// padMinScale is how far the pad may shrink to fit a narrow column — as a
// phone's controls shrink with the screen, never a clipped edge.
// padMinScaleY is the lower floor for a pad under the playfield in a column
// too short for both: the minimum window's competitive column, mouse play,
// where the pad gives the minimum-cell playfield its rows first (small
// buttons still click; a playfield under the chat panel does not play).
// padSideGap (dp) is the gap between a flanking pad cluster and the
// playfield.
const (
	padMinScale  = 0.55
	padMinScaleY = 0.4
	padSideGap   = 24
)

// padSizer is the pad's metrics under the frame's scale factor: 1 at the
// natural sizes, less when the column cannot fit them.
type padSizer struct {
	m     padMetrics
	scale float32
}

func (p padSizer) dp(v int) unit.Dp    { return unit.Dp(float32(v) * p.scale) }
func (p padSizer) px(gtx C, v int) int { return gtx.Dp(p.dp(v)) }

// dpadSize is the D-pad's side in px: three arms' worth, the hub in the
// middle.
func (p padSizer) dpadSize(gtx C) int { return 3 * p.px(gtx, p.m.btn) }

// faceSize is the face cluster's size in px: the two rotation squares over
// the DROP bar and, with the hold rule, the HOLD bar; the bars span the pair.
func (p padSizer) faceSize(gtx C, hold bool) image.Point {
	btn, gap := p.px(gtx, p.m.btn), p.px(gtx, p.m.gap)
	rows := 2
	if hold {
		rows = 3
	}
	return image.Pt(2*btn+gap, rows*btn+(rows-1)*gap)
}

// padSize is the whole pad's footprint in px laid out in one row — D-pad,
// cluster gap, face buttons — the hard shadow excluded, as for every
// hardShadow widget.
func (p padSizer) padSize(gtx C, hold bool) image.Point {
	d := p.dpadSize(gtx)
	f := p.faceSize(gtx, hold)
	return image.Pt(d+p.px(gtx, p.m.clusterGap)+f.X, max(d, f.Y))
}

// padPlan is one frame's fit of the control pad and the playfield to the
// board column: the pad's metrics and scale, whether it flanks the playfield
// or sits under it, and the board cell size that leaves it its room.
type padPlan struct {
	padSizer
	beside bool // the pad's clusters flank the playfield (D-pad left, face buttons right) rather than sit under it
	cell   int  // board cell size (px)
	width  int  // the pad's row per the plan's model, px: flanked, the whole row with the strip; under, the pad
}

// flankGeom is the flanked row's horizontal geometry — the D-pad, the wells
// column (0 wide without one), the playfield and the face buttons, each
// padSideGap from the next — and the move-buffer strip hung under the
// playfield, centered on it: wider than the playfield, it runs on under the
// pads. Everything in px; the pads' hard shadow excluded.
type flankGeom struct {
	dpadW, faceW, gap, wellsW, boardW, stripW int
}

func (g flankGeom) boardX() int { return g.dpadW + g.gap + g.wellsW }
func (g flankGeom) stripX() int { return g.boardX() + g.boardW/2 - g.stripW/2 }
func (g flankGeom) rowW() int   { return g.boardX() + g.boardW + g.gap + g.faceW }

// width is the whole layout's, the strip's spill past the row on either
// side included.
func (g flankGeom) width() int { return max(g.rowW(), g.stripX()+g.stripW) - min(0, g.stripX()) }

// fitBoardAndPad picks the board cell size and the pad's placement for a
// board column of the current constraints: the playfield of cols×rows
// visible cells (cols counting the side wells' previewCols when showSide),
// under it the move-buffer strip (player), and while the game is playable
// (pad) the control pad. The pad goes wherever the playfield ends up bigger:
// beside it, its whole height left to the board — the way a handheld keeps
// its controls off the screen's sides, and the only fit for a wide, short
// column — or under it in a tall, narrow (portrait) column. Either way it
// scales down, floored at padMinScale, rather than clip: beside the board
// the two share the width, the board's cell giving way to leave the pad its
// room (and a column too narrow for even that keeps the pad under the
// board); under it, the pad shrinks to the column's width and to whatever
// height is left over a minimum-cell playfield.
func (a *App) fitBoardAndPad(gtx C, cols, rows int, showSide, player, pad, hold bool) padPlan {
	m := padMouse
	if a.touchUI {
		m = padTouch
	}
	plan := padPlan{padSizer: padSizer{m: m, scale: 1}}
	reservedX := gtx.Dp(24)
	extra := 0
	if showSide {
		reservedX += gtx.Dp(18) // the side column's frame and the gap
		extra = previewCols
	}
	reservedY := 0
	if player {
		reservedY = gtx.Dp(90) // the move-buffer strip and its inset
	}
	fit := func(rx, ry int) int { return fitCellPx(gtx, cols, rows, 1, rx, ry, 14, 56) }
	if !pad {
		plan.cell = fit(reservedX, reservedY)
		return plan
	}
	natural := padSizer{m: m, scale: 1}.padSize(gtx, hold)
	scaleFloored := func(room, size int, floor float32) float32 {
		return min(1, max(floor, float32(room)/float32(size)))
	}
	scaleFor := func(room, size int) float32 { return scaleFloored(room, size, padMinScale) }
	shadow := gtx.Dp(3)
	availX := gtx.Constraints.Max.X - gtx.Dp(24)
	// Under the playfield: the pad scales to the column's width and to the
	// height left over a minimum-cell board, and takes its height (plus the
	// inset over it) from the board's.
	roomY := gtx.Constraints.Max.Y - reservedY - gtx.Dp(14)*(8*rows+2)/8 - shadow - gtx.Dp(10)
	under := padSizer{m: m, scale: min(scaleFor(availX-shadow, natural.X), scaleFloored(roomY, natural.Y, padMinScaleY))}
	underSz := under.padSize(gtx, hold)
	cellUnder := fit(reservedX, reservedY+underSz.Y+shadow+gtx.Dp(10))
	// Beside it: the board takes the column's height, the pad gets whatever
	// width is left of the playfield and its wells (drawBoard's and
	// holdWell/nextWell's frames) — and if the pad has to sit at its floor,
	// the board's cell gives way instead.
	geom := func(cell int, p padSizer) flankGeom {
		g := flankGeom{dpadW: p.dpadSize(gtx), faceW: p.faceSize(gtx, hold).X, gap: gtx.Dp(padSideGap),
			boardW: (cols-extra)*cell + 2*max(cell/8, 2), stripW: stripWidth(gtx)}
		if showSide {
			g.wellsW = previewCols*cell + 2*(max(cell/8, 2)+max(cell/3, 6)) + gtx.Dp(12)
		}
		return g
	}
	gaps := 2*gtx.Dp(padSideGap) + shadow
	g := geom(fit(reservedX, reservedY), padSizer{m: m, scale: 1})
	beside := padSizer{m: m, scale: scaleFor(availX-g.wellsW-g.boardW-gaps-gtx.Dp(2), natural.X)}
	cellBeside := fit(reservedX+gaps+beside.padSize(gtx, hold).X, reservedY)
	g = geom(cellBeside, beside)
	if over := g.width() + shadow - availX; over > 0 && beside.scale > padMinScale {
		// The pads' px rounding, or a cell at its floor: shave the scale by
		// the overflow (and a little), once.
		padW := beside.padSize(gtx, hold).X
		beside.scale = max(padMinScale, beside.scale*float32(padW-over-gtx.Dp(2))/float32(padW))
		cellBeside = fit(reservedX+gaps+beside.padSize(gtx, hold).X, reservedY)
		g = geom(cellBeside, beside)
	}
	if fits := g.width()+shadow <= availX; fits && cellBeside >= cellUnder {
		plan.padSizer, plan.beside, plan.cell, plan.width = beside, true, cellBeside, g.width()+shadow
	} else {
		plan.padSizer, plan.cell, plan.width = under, cellUnder, underSz.X+shadow
	}
	return plan
}

// controlPad lays out the pad in one row — the D-pad, the cluster gap, the
// face buttons — for a column that holds it under the playfield.
func (a *App) controlPad(gtx C, p padSizer, enabled, hold bool) D {
	gap := p.px(gtx, p.m.clusterGap)
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.dpad(gtx, p, enabled) }),
		layout.Rigid(func(gtx C) D { return D{Size: image.Pt(gap, 0)} }),
		layout.Rigid(func(gtx C) D { return a.faceButtons(gtx, p, enabled, hold) }),
	)
}

// dpad draws the D-pad: one cross-shaped plate in the pad's chrome — hard
// shadow, chunky border, solid fill, the hub's pivot dimple in the middle —
// with an arrow glyph on each arm. Each arm is its own click target (the
// whole arm, hub edge to tip); the hub is none. The top arm carries the
// clockwise-rotate glyph: it is the ↑ key.
func (a *App) dpad(gtx C, p padSizer, enabled bool) D {
	cell := p.px(gtx, p.m.btn)
	size := 3 * cell
	border, glyphCol := colAccent, colAccent
	if !enabled {
		border, glyphCol = colBorder, colMuted
	}
	// cross paints the plate's cross — the vertical bar, then the horizontal
	// one — inset from the arms' outline and offset by (dx, dy).
	cross := func(inset, dx, dy int, col colorN) {
		fillRect(gtx.Ops, image.Rect(cell+inset+dx, inset+dy, 2*cell-inset+dx, size-inset+dy), col)
		fillRect(gtx.Ops, image.Rect(inset+dx, cell+inset+dy, size-inset+dx, 2*cell-inset+dy), col)
	}
	off, bw := gtx.Dp(3), gtx.Dp(2)
	cross(0, off, off, colShadow)
	cross(0, 0, 0, border)
	cross(bw, 0, 0, colPanel)
	pivot := max(cell/6, 2)
	c := size/2 - pivot/2
	fillRect(gtx.Ops, image.Rect(c, c, c+pivot, c+pivot), colBorder)
	arm := func(btn *widget.Clickable, col, row int, bm []string) {
		defer op.Offset(image.Pt(col*cell, row*cell)).Push(gtx.Ops).Pop()
		gtx := gtx
		gtx.Constraints = layout.Exact(image.Pt(cell, cell))
		material.Clickable(gtx, btn, func(gtx C) D {
			return layout.Center.Layout(gtx, glyphWidget(bm, p.dp(p.m.glyph), glyphCol))
		})
	}
	arm(&a.padUp, 1, 0, glyphCW)
	arm(&a.padLeft, 0, 1, glyphLeft)
	arm(&a.padRight, 2, 1, glyphRight)
	arm(&a.padDown, 1, 2, glyphDown)
	return D{Size: image.Pt(size, size)}
}

// faceButtons draws the face cluster: ↺ ↻ side by side, the DROP bar under
// them and, with the hold rule, the HOLD bar under that — the bars as wide as
// the pair, each a labelled button with its glyph beside its pixel-face word.
func (a *App) faceButtons(gtx C, p padSizer, enabled, hold bool) D {
	btn, gap := p.px(gtx, p.m.btn), p.px(gtx, p.m.gap)
	barW := 2*btn + gap
	glyphCol := colAccent
	if !enabled {
		glyphCol = colMuted
	}
	hgap := func(gtx C) D { return D{Size: image.Pt(gap, 0)} }
	vgap := func(gtx C) D { return D{Size: image.Pt(0, gap)} }
	sq := func(b *widget.Clickable, bm []string) layout.Widget {
		return func(gtx C) D {
			return a.padButton(gtx, b, enabled, image.Pt(btn, btn), colPanel, glyphWidget(bm, p.dp(p.m.glyph), glyphCol))
		}
	}
	bar := func(b *widget.Clickable, bg, fg colorN, bm []string, label string) layout.Widget {
		return func(gtx C) D {
			return a.padButton(gtx, b, enabled, image.Pt(barW, btn), bg, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(glyphWidget(bm, p.dp(p.m.barGlyph), fg)),
					layout.Rigid(hgap),
					layout.Rigid(a.pixel(unit.Sp(p.m.label*p.scale), label, fg).Layout),
				)
			})
		}
	}
	dropBg, dropFg := colAccent, colBg
	if !enabled {
		dropBg, dropFg = colPanel, colMuted
	}
	rows := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(sq(&a.padCCW, glyphCCW)),
				layout.Rigid(hgap),
				layout.Rigid(sq(&a.padCW, glyphCW)),
			)
		}),
		layout.Rigid(vgap),
		layout.Rigid(bar(&a.padDrop, dropBg, dropFg, glyphDrop, "DROP")),
	}
	if hold {
		rows = append(rows,
			layout.Rigid(vgap),
			layout.Rigid(bar(&a.padHold, colPanel, glyphCol, glyphHold, "HOLD")),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// padButton renders one arcade pad button: hard shadow, chunky border, solid
// fill, centered glyph. The border mutes while disabled.
func (a *App) padButton(gtx C, btn *widget.Clickable, enabled bool, sz image.Point, bg colorN, content layout.Widget) D {
	border := colAccent
	if !enabled {
		border = colBorder
	}
	// The button is its size whatever the room around it (the Clickable
	// clamps its content to the constraints it is given).
	gtx.Constraints = layout.Exact(sz)
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: border, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			return material.Clickable(gtx, btn, func(gtx C) D {
				return background(gtx, bg, func(gtx C) D {
					return layout.Center.Layout(gtx, content)
				})
			})
		})
	})
}

// handlePadClicks drains the on-screen pad's clicks — and the HOLD box's,
// which holds when tapped like the phone versions' hold box — and dispatches
// them to the engine while the game is actually being played. Draining is
// unconditional so clicks made while the pad is disabled (pre-start, game
// over) die here instead of firing as moves once the game starts.
func (a *App) handlePadClicks(gtx C, eng *engine.Engine, active bool) {
	pads := [...]struct {
		btn  *widget.Clickable
		move func(*engine.Engine)
	}{
		{&a.padUp, (*engine.Engine).RotateCW},
		{&a.padLeft, (*engine.Engine).MoveLeft},
		{&a.padDown, (*engine.Engine).MoveDown},
		{&a.padRight, (*engine.Engine).MoveRight},
		{&a.padCCW, (*engine.Engine).RotateCCW},
		{&a.padCW, (*engine.Engine).RotateCW},
		{&a.padDrop, (*engine.Engine).HardDrop},
		{&a.padHold, (*engine.Engine).Hold},
		{&a.holdBoxBtn, (*engine.Engine).Hold},
	}
	for i := range pads {
		for pads[i].btn.Clicked(gtx) {
			if active && eng != nil {
				pads[i].move(eng)
			}
		}
	}
}
