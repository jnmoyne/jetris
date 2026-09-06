package nativeui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/render"
)

// boardFX bundles the client-local overlays drawn over ONE board: rainbow
// borders on CAS-rejected cells, the recoil a rejected write gives the piece,
// full-row strobes (line clears and arriving garbage), and the hard-drop
// ghost. Every field may be nil, and a nil *boardFX draws no overlays at all
// (opponent thumbnails, archived boards). None of it is ever published — pure
// local decoration over committed state.
type boardFX struct {
	flash map[[2]int]time.Time      // CAS-rejected cells → rainbow border
	rows  map[int]rowStrobe         // absolute row index → strobe state
	ghost map[[2]int]game.PieceType // hard-drop ghost cells (drawn on empty squares only)
	// intent is the pre-rendered move: the cells of the piece where the
	// player is steering it (Engine.IntentPiece — the in-flight batches and
	// the queued moves played out), drawn as a white outline over whatever
	// is there, gone once the drawn board has the piece there itself
	// (intentCells). Nil in the consumer-only display mode.
	intent map[[2]int]bool
	// acked is where the ACKS have the player's piece while the board draws
	// it elsewhere (Optimistic async, optimisticBoard): outlined in grey, so
	// the white outline on the piece being steered keeps the focus.
	acked map[[2]int]bool
	// want is where a rejected step wanted the piece — the move the CAS
	// failure took away: a rainbow frame blinks on those squares, over
	// whatever is on them, for flashDur from the epoch stored per cell.
	want map[[2]int]time.Time
	// kick/kickAt/kickFrom are the recoil that goes with it: the cells of the
	// piece as the board draws it, painted from kickAt at casRecoilOffset —
	// snapping back from kickFrom (where the board drew it before the
	// rejection, in cells; zero: it never left) and vibrating where the
	// rejection put it, instead of going where it was steered. Set on the
	// local player's own board only.
	kick     map[[2]int]bool
	kickAt   time.Time
	kickFrom [2]float64
	tint     color.NRGBA // washes the EMPTY squares (fill + grid lines) toward a team/player color; zero = none
	frame    color.NRGBA // overrides the arcade-well frame color (the keyboard-focus outline); zero = the usual colBorder
}

// Board tint strength: how far an empty square's fill and its grid line are
// lerped toward boardFX.tint — a little color on the ground, the way the
// teams rendering lights Team A's well cyan and Team B's magenta (and on
// past them, each team's own color), while the
// pieces keep their own palette. The grid lines take more than the fill so
// the color reads as the board's own rather than as a haze over it.
const (
	boardTintFill = 0.10
	boardTintGrid = 0.26
)

// rowStrobe is one flashing row of 80's arcade feedback: the row blinks hard
// on/off in a solid color — white for rows just cleared on the board (by the
// local player, or by a teammate on a shared board), the attacker's player
// color for a garbage row that just landed.
type rowStrobe struct {
	start time.Time
	col   color.NRGBA
}

// Row-strobe timing: rowStrobeDur total, lit for the first half of every
// rowStrobeBlink cycle — four square-wave blinks, no easing, exactly the
// classic arcade line-clear flash. rowStrobeAlpha is the lit band's opacity
// over the cells beneath it.
const (
	rowStrobeDur   = 640 * time.Millisecond
	rowStrobeBlink = 160 * time.Millisecond
	rowStrobeAlpha = 0.7
)

// active reports whether any overlay in fx still needs animation frames.
func (fx *boardFX) rowsActive(now time.Time) bool {
	if fx == nil {
		return false
	}
	for _, rs := range fx.rows {
		if now.Sub(rs.start) < rowStrobeDur {
			return true
		}
	}
	return false
}

// fillRect paints r with c in absolute widget coordinates.
func fillRect(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	defer clip.Rect(r).Push(ops).Pop()
	paint.Fill(ops, c)
}

// strokeRect paints a w-px border just inside r, leaving what is already
// there showing through the middle.
func strokeRect(ops *op.Ops, r image.Rectangle, w int, c color.NRGBA) {
	if w <= 0 || r.Dx() <= 2*w || r.Dy() <= 2*w {
		return
	}
	fillRect(ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
	fillRect(ops, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
	fillRect(ops, image.Rect(r.Min.X, r.Min.Y+w, r.Min.X+w, r.Max.Y-w), c)
	fillRect(ops, image.Rect(r.Max.X-w, r.Min.Y+w, r.Max.X, r.Max.Y-w), c)
}

// rainbow returns the CAS-flash border color for a given progress through the
// flash, reproducing the web's 7-stop keyframe palette.
func rainbow(elapsed time.Duration) color.NRGBA {
	stops := []color.NRGBA{
		{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
		{R: 0xff, G: 0x7f, B: 0x00, A: 0xff},
		{R: 0xff, G: 0xff, B: 0x00, A: 0xff},
		{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
		{R: 0x00, G: 0x00, B: 0xff, A: 0xff},
		{R: 0x4b, G: 0x00, B: 0x82, A: 0xff},
		{R: 0x94, G: 0x00, B: 0xd3, A: 0xff},
	}
	i := int(float64(elapsed) / float64(flashDur) * float64(len(stops)))
	if i < 0 {
		i = 0
	}
	if i >= len(stops) {
		i = len(stops) - 1
	}
	return stops[i]
}

// clampF clamps v to [lo, hi].
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// easeOutBack eases 0→1 with a slight overshoot past 1 near the end, giving the
// countdown number a "pop" as it settles. t is clamped to [0,1] by the caller.
func easeOutBack(t float64) float64 {
	const c1 = 1.70158
	const c3 = c1 + 1
	u := t - 1
	return 1 + c3*u*u*u + c1*u*u
}

// withAlpha returns c with its alpha scaled to a (0..1).
func withAlpha(c color.NRGBA, a float64) color.NRGBA {
	c.A = uint8(clampF(a, 0, 1) * 255)
	return c
}

// lighten lerps c toward white by t (0..1).
func lighten(c color.NRGBA, t float64) color.NRGBA {
	return lerpColor(c, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: c.A}, t)
}

// darken lerps c toward black by t (0..1).
func darken(c color.NRGBA, t float64) color.NRGBA {
	return lerpColor(c, color.NRGBA{A: c.A}, t)
}

// drawCell paints a single board square: the whole cell is filled with the
// outline color, then an inner rectangle (inset by the outline width) is filled
// with the cell color — giving a colored frame around the fill, matching the
// web's outline-offset:-1px look. With bevel set (filled cells) the inner fill
// gets the 8-bit block shading: a lighter strip along the top and left edges, a
// darker strip along the bottom and right, and a gloss pixel in the top-left.
func drawCell(ops *op.Ops, x, y, size int, fill, outline color.NRGBA, outlineW int, bevel bool) {
	fillRect(ops, image.Rect(x, y, x+size, y+size), outline)
	if outlineW < 0 {
		outlineW = 0
	}
	inner := image.Rect(x+outlineW, y+outlineW, x+size-outlineW, y+size-outlineW)
	if inner.Dx() <= 0 || inner.Dy() <= 0 {
		return
	}
	fillRect(ops, inner, fill)
	bw := size / 8
	if bw < 1 {
		bw = 1
	}
	if !bevel || inner.Dx() <= 3*bw || inner.Dy() <= 3*bw {
		return
	}
	hi, lo := lighten(fill, 0.4), darken(fill, 0.45)
	// Lit from the upper-left: top + left highlight, bottom + right shadow.
	fillRect(ops, image.Rect(inner.Min.X, inner.Min.Y, inner.Max.X-bw, inner.Min.Y+bw), hi)
	fillRect(ops, image.Rect(inner.Min.X, inner.Min.Y, inner.Min.X+bw, inner.Max.Y-bw), hi)
	fillRect(ops, image.Rect(inner.Min.X+bw, inner.Max.Y-bw, inner.Max.X, inner.Max.Y), lo)
	fillRect(ops, image.Rect(inner.Max.X-bw, inner.Min.Y+bw, inner.Max.X, inner.Max.Y), lo)
	// Gloss pixel just inside the highlight corner.
	fillRect(ops, image.Rect(inner.Min.X+bw, inner.Min.Y+bw, inner.Min.X+2*bw, inner.Min.Y+2*bw), lighten(fill, 0.75))
}

// emptyCellStyle is how an unoccupied square on this board is painted — what
// the squares a vibrating piece juddered out of are filled with for the frame,
// so the recoil shows against the board rather than a smear of the piece
// itself.
func emptyCellStyle(localIdx int, showOutline bool, tint color.NRGBA) render.CellAppearance {
	ap := render.CellStyle(game.Cell{}, localIdx, showOutline)
	if tint.A != 0 {
		ap.Fill = lerpColor(ap.Fill, tint, boardTintFill)
		ap.Outline = lerpColor(ap.Outline, tint, boardTintGrid)
	}
	return ap
}

// recoilCell is one square of a vibrating piece, held back from the cell loop
// and painted last at the frame's judder offset.
type recoilCell struct {
	x, y     int
	ap       render.CellAppearance
	outline  color.NRGBA
	outlineW int
}

// boardRows is how many rows drawBoard paints of snap: the visible region
// below the headroom, or the whole board — the headroom rows behind smoked
// glass over the playfield — when the game shows its hidden rows
// (Engine.ShowHeadroom). Every layout that sizes a board by its row count
// asks here, so the cell fits the board that is actually drawn.
func boardRows(snap engine.BoardSnapshot, headroom bool) int {
	if headroom {
		return snap.Height
	}
	return snap.Height - snap.VisibleStart
}

// The smoked glass over a board's headroom rows (drawBoard, headroom):
// colSmoke is the pane's tint, laid over the rows at smokeAlpha so what is
// behind it — a piece the moment it spawns, a stack climbing out of the
// playfield — shows through dark and dim rather than not at all; the sheen
// is a diagonal highlight fading off the pane's top-left corner, the
// reflection that says "glass"; and the pane's lower edge, where it meets the
// playfield, catches the light as a thin bright line. Alpha values, 0..1.
const (
	smokeAlpha     = 0.66
	smokeSheen     = 0.10
	smokeEdgeAlpha = 0.28
)

var colSmoke = color.NRGBA{R: 0x0a, G: 0x09, B: 0x12, A: 0xff}

// smokedGlass lays the pane over band (absolute widget coordinates): the tint,
// the sheen, the lit edge along the bottom.
func smokedGlass(ops *op.Ops, band image.Rectangle, cellPx int) {
	if band.Empty() {
		return
	}
	fillRect(ops, band, withAlpha(colSmoke, smokeAlpha))
	func() {
		defer clip.Rect(band).Push(ops).Pop()
		paint.LinearGradientOp{
			Stop1:  f32.Pt(float32(band.Min.X), float32(band.Min.Y)),
			Stop2:  f32.Pt(float32(band.Min.X)+float32(band.Dx())*0.45, float32(band.Max.Y)),
			Color1: withAlpha(color.NRGBA{R: 0xff, G: 0xff, B: 0xff}, smokeSheen),
			Color2: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0},
		}.Add(ops)
		paint.PaintOp{}.Add(ops)
	}()
	edge := max(1, cellPx/16)
	fillRect(ops, image.Rect(band.Min.X, band.Max.Y-edge, band.Max.X, band.Max.Y), withAlpha(color.NRGBA{R: 0xff, G: 0xff, B: 0xff}, smokeEdgeAlpha))
}

// drawBoard renders a playfield snapshot at the current transform origin and
// returns its pixel dimensions (cells plus the surrounding "well" frame).
// localIdx is the viewer's player index (-1 for spectators). fx (may be nil)
// carries the client-local overlays: CAS-rejection rainbow borders and the
// blinking outline where a rejected step wanted the piece, the recoil that
// vibrates the piece it was taken from, the hard-drop ghost (empty squares
// only — real cells always win), and the clear/garbage row strobes painted
// over the finished cells. headroom draws the hidden rows above the visible
// region too — the whole board, its headroom behind smoked glass painted
// over everything up there, effects included (boardRows sizes it).
func drawBoard(gtx C, snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, fx *boardFX, now time.Time, headroom bool) D {
	fw := cellPx / 8 // chunky arcade-well frame around the playfield
	if fw < 2 {
		fw = 2
	}
	// top is the first row painted: the visible region's, or row 0 with the
	// headroom shown.
	top := snap.VisibleStart
	if headroom {
		top = 0
	}
	w := snap.Width*cellPx + 2*fw
	h := boardRows(snap, headroom)*cellPx + 2*fw
	if h < 2*fw {
		h = 2 * fw
	}
	frame := colBorder
	if fx != nil && fx.frame.A != 0 {
		frame = fx.frame // lit up: this board holds the keyboard
	}
	fillRect(gtx.Ops, image.Rect(0, 0, w, fw), frame)
	fillRect(gtx.Ops, image.Rect(0, h-fw, w, h), frame)
	fillRect(gtx.Ops, image.Rect(0, 0, fw, h), frame)
	fillRect(gtx.Ops, image.Rect(w-fw, 0, w, h), frame)
	// The CAS recoil: while it runs, the piece's own squares are held back
	// from the loop and painted last at the frame's offset — on their way
	// back from where the piece was steered, then juddering — over the empty
	// squares they are drawn out of.
	var kick image.Point
	var recoil []recoilCell
	if fx != nil && len(fx.kick) > 0 {
		kick = casRecoilOffset(cellPx, fx.kickFrom, now.Sub(fx.kickAt))
	}
	for r := top; r < snap.Height && r < len(snap.Rows); r++ {
		row := snap.Rows[r]
		y := fw + (r-top)*cellPx
		for c := 0; c < snap.Width && c < len(row.Cells); c++ {
			cell := row.Cells[c]
			ap := render.CellStyle(cell, localIdx, showOutline)
			if fx != nil && !cell.Occupied && !cell.Active {
				if pt, ok := fx.ghost[[2]int{r, c}]; ok {
					ap = render.GhostStyle(pt)
				} else if fx.tint.A != 0 && !cell.Adversarial {
					ap.Fill = lerpColor(ap.Fill, fx.tint, boardTintFill)
					ap.Outline = lerpColor(ap.Outline, fx.tint, boardTintGrid)
				}
			}
			outline, outlineW := ap.Outline, ap.OutlineW
			if fx != nil && fx.acked[[2]int{r, c}] {
				// Where the acks have the piece: a grey frame, trailing the
				// piece being steered.
				outline, outlineW = colAckedOutline, 2
			}
			if fx != nil && fx.intent[[2]int{r, c}] {
				// The pre-rendered move: a white frame on the piece where the
				// player is steering it (over the grey where the two meet). A
				// CAS flash on the same cell (below) wins — the move it
				// announced was just lost.
				outline, outlineW = colIntent, 2
			}
			if fx != nil && fx.flash != nil {
				if start, ok := fx.flash[[2]int{r, c}]; ok {
					if el := now.Sub(start); el < flashDur {
						outline, outlineW = rainbow(el), 2
					}
				}
			}
			x := fw + c*cellPx
			if kick != (image.Point{}) && fx.kick[[2]int{r, c}] {
				recoil = append(recoil, recoilCell{x: x, y: y, ap: ap, outline: outline, outlineW: outlineW})
				e := emptyCellStyle(localIdx, showOutline, fx.tint)
				drawCell(gtx.Ops, x, y, cellPx, e.Fill, e.Outline, e.OutlineW, false)
				continue
			}
			drawCell(gtx.Ops, x, y, cellPx, ap.Fill, outline, outlineW, ap.Bevel)
		}
	}
	if len(recoil) > 0 {
		// Clipped to the playfield, so a piece juddering against a wall bumps
		// into the well's frame rather than over it.
		st := clip.Rect(image.Rect(fw, fw, w-fw, h-fw)).Push(gtx.Ops)
		for _, rc := range recoil {
			drawCell(gtx.Ops, rc.x+kick.X, rc.y+kick.Y, cellPx, rc.ap.Fill, rc.outline, rc.outlineW, rc.ap.Bevel)
		}
		st.Pop()
	}
	// Where the rejected step wanted the piece: a thick rainbow frame blinking
	// hard on/off over whatever is on those squares — the loudest thing on the
	// board, and painted after the recoil rather than inside it, because it
	// marks a PLACE the piece never reached, not the piece. The half of every
	// cycle it is dark, the squares are simply themselves again.
	if fx != nil {
		for rc, start := range fx.want {
			r, c := rc[0], rc[1]
			if r < top || r >= snap.Height || c < 0 || c >= snap.Width {
				continue
			}
			el := now.Sub(start)
			if el < 0 || el >= flashDur || el%casWantBlink >= casWantBlink/2 {
				continue
			}
			x, y := fw+c*cellPx, fw+(r-top)*cellPx
			strokeRect(gtx.Ops, image.Rect(x, y, x+cellPx, y+cellPx), max(2, cellPx/10), rainbow(el))
		}
	}
	// Row strobes paint LAST, a translucent solid band across the row during
	// the lit half of each blink cycle — hard on/off, the arcade way.
	if fx != nil {
		for r, rs := range fx.rows {
			if r < top || r >= snap.Height {
				continue
			}
			el := now.Sub(rs.start)
			if el < 0 || el >= rowStrobeDur || el%rowStrobeBlink >= rowStrobeBlink/2 {
				continue
			}
			y := fw + (r-top)*cellPx
			fillRect(gtx.Ops, image.Rect(fw, y, w-fw, y+cellPx), withAlpha(rs.col, rowStrobeAlpha))
		}
	}
	// The smoked glass over the headroom paints last of all: everything up
	// there — cells, ghost, outlines, strobes — is behind it.
	if snap.VisibleStart > top {
		smokedGlass(gtx.Ops, image.Rect(fw, fw, w-fw, fw+(snap.VisibleStart-top)*cellPx), cellPx)
	}
	return D{Size: image.Pt(w, h)}
}

// scanlines draws the subtle CRT overlay across the whole window: one thin
// dark line every few pixels. Paint-only and drawn last, so it dims chrome and
// boards alike without intercepting input.
func scanlines(gtx C) {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	step := gtx.Dp(3)
	if step < 3 {
		step = 3
	}
	th := step / 3
	col := color.NRGBA{A: 0x12}
	for y := 0; y < h; y += step {
		fillRect(gtx.Ops, image.Rect(0, y, w, y+th), col)
	}
}

// hardShadow lays w over a solid offset copy of its bounds — the chunky
// "sticker" drop shadow of the 8-bit chrome. The shadow bleeds a few pixels
// past the reported size; neighbors simply overlap it.
func hardShadow(gtx C, w layout.Widget) D {
	off := gtx.Dp(3)
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	fillRect(gtx.Ops, image.Rect(off, off, dims.Size.X+off, dims.Size.Y+off), colShadow)
	call.Add(gtx.Ops)
	return dims
}

// fitCellPx picks the cell size (px) at which `boards` side-by-side playfields
// of cols×rows visible cells — each with its arcade-well frame, ≈cell/4 of
// extra width/height per board — fill the current constraints, after reserving
// reservedX/reservedY px for surrounding chrome. The result is clamped to
// [minDp, maxDp]: boards never shrink into unreadability (below the minimum
// the strips fall back to horizontal scrolling instead), and never blow up
// past the chunky-pixel look on a huge window. This is what makes every board
// view window-size reactive.
func fitCellPx(gtx C, cols, rows, boards, reservedX, reservedY int, minDp, maxDp unit.Dp) int {
	lo, hi := gtx.Dp(minDp), gtx.Dp(maxDp)
	if cols <= 0 || rows <= 0 || boards <= 0 {
		return lo
	}
	availX := gtx.Constraints.Max.X - reservedX
	availY := gtx.Constraints.Max.Y - reservedY
	// Frame per board: 2*fw with fw = cell/8, so width = cell*(8*cols+2)/8.
	cw := availX * 8 / (boards * (8*cols + 2))
	ch := availY * 8 / (8*rows + 2)
	return min(max(min(cw, ch), lo), hi)
}

// boardWidget wraps drawBoard as a layout.Widget for placement in a Flex/Stack.
func (a *App) boardWidget(snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, fx *boardFX, now time.Time, headroom bool) layout.Widget {
	return func(gtx C) D {
		return drawBoard(gtx, snap, localIdx, cellPx, showOutline, fx, now, headroom)
	}
}
