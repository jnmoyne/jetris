package nativeui

import (
	"image"
	"image/color"
	"time"

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
// borders on CAS-rejected cells, full-row strobes (line clears and arriving
// garbage), and the hard-drop ghost. Every field may be nil, and a nil
// *boardFX draws no overlays at all (opponent thumbnails, archived boards).
// None of it is ever published — pure local decoration over committed state.
type boardFX struct {
	flash map[[2]int]time.Time      // CAS-rejected cells → rainbow border
	rows  map[int]rowStrobe         // absolute row index → strobe state
	ghost map[[2]int]game.PieceType // hard-drop ghost cells (drawn on empty squares only)
	tint  color.NRGBA               // washes the EMPTY squares (fill + grid lines) toward a team/player color; zero = none
	frame color.NRGBA               // overrides the arcade-well frame color (the keyboard-focus outline); zero = the usual colBorder
}

// Board tint strength: how far an empty square's fill and its grid line are
// lerped toward boardFX.tint — a little color on the ground, the way the
// teams rendering lights Team A's well cyan and Team B's magenta, while the
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

// drawBoard renders a playfield snapshot at the current transform origin and
// returns its pixel dimensions (cells plus the surrounding "well" frame).
// localIdx is the viewer's player index (-1 for spectators). fx (may be nil)
// carries the client-local overlays: CAS-rejection rainbow borders, the
// hard-drop ghost (empty squares only — real cells always win), and the
// clear/garbage row strobes painted over the finished cells.
func drawBoard(gtx C, snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, fx *boardFX, now time.Time) D {
	fw := cellPx / 8 // chunky arcade-well frame around the playfield
	if fw < 2 {
		fw = 2
	}
	w := snap.Width*cellPx + 2*fw
	h := (snap.Height-snap.VisibleStart)*cellPx + 2*fw
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
	for r := snap.VisibleStart; r < snap.Height && r < len(snap.Rows); r++ {
		row := snap.Rows[r]
		y := fw + (r-snap.VisibleStart)*cellPx
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
			if fx != nil && fx.flash != nil {
				if start, ok := fx.flash[[2]int{r, c}]; ok {
					if el := now.Sub(start); el < flashDur {
						outline, outlineW = rainbow(el), 2
					}
				}
			}
			drawCell(gtx.Ops, fw+c*cellPx, y, cellPx, ap.Fill, outline, outlineW, ap.Bevel)
		}
	}
	// Row strobes paint LAST, a translucent solid band across the row during
	// the lit half of each blink cycle — hard on/off, the arcade way.
	if fx != nil {
		for r, rs := range fx.rows {
			if r < snap.VisibleStart || r >= snap.Height {
				continue
			}
			el := now.Sub(rs.start)
			if el < 0 || el >= rowStrobeDur || el%rowStrobeBlink >= rowStrobeBlink/2 {
				continue
			}
			y := fw + (r-snap.VisibleStart)*cellPx
			fillRect(gtx.Ops, image.Rect(fw, y, w-fw, y+cellPx), withAlpha(rs.col, rowStrobeAlpha))
		}
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
func (a *App) boardWidget(snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, fx *boardFX, now time.Time) layout.Widget {
	return func(gtx C) D {
		return drawBoard(gtx, snap, localIdx, cellPx, showOutline, fx, now)
	}
}
