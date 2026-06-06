package nativeui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"jetricks/internal/engine"
	"jetricks/internal/render"
)

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

// drawCell paints a single board square: the whole cell is filled with the
// outline color, then an inner rectangle (inset by the outline width) is filled
// with the cell color — giving a colored frame around the fill, matching the
// web's outline-offset:-1px look.
func drawCell(ops *op.Ops, x, y, size int, fill, outline color.NRGBA, outlineW int) {
	fillRect(ops, image.Rect(x, y, x+size, y+size), outline)
	if outlineW < 0 {
		outlineW = 0
	}
	inner := image.Rect(x+outlineW, y+outlineW, x+size-outlineW, y+size-outlineW)
	if inner.Dx() > 0 && inner.Dy() > 0 {
		fillRect(ops, inner, fill)
	}
}

// drawBoard renders a playfield snapshot at the current transform origin and
// returns its pixel dimensions. localIdx is the viewer's player index (-1 for
// spectators). flash (may be nil) overlays a rainbow border on recently
// CAS-rejected cells, keyed by absolute (row, col).
func drawBoard(gtx C, snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, flash map[[2]int]time.Time, now time.Time) D {
	w := snap.Width * cellPx
	h := (snap.Height - snap.VisibleStart) * cellPx
	if h < 0 {
		h = 0
	}
	for r := snap.VisibleStart; r < snap.Height && r < len(snap.Rows); r++ {
		row := snap.Rows[r]
		y := (r - snap.VisibleStart) * cellPx
		for c := 0; c < snap.Width && c < len(row.Cells); c++ {
			ap := render.CellStyle(row.Cells[c], localIdx, showOutline)
			outline, outlineW := ap.Outline, ap.OutlineW
			if flash != nil {
				if start, ok := flash[[2]int{r, c}]; ok {
					if el := now.Sub(start); el < flashDur {
						outline, outlineW = rainbow(el), 2
					}
				}
			}
			drawCell(gtx.Ops, c*cellPx, y, cellPx, ap.Fill, outline, outlineW)
		}
	}
	return D{Size: image.Pt(w, h)}
}

// boardWidget wraps drawBoard as a layout.Widget for placement in a Flex/Stack.
func (a *App) boardWidget(snap engine.BoardSnapshot, localIdx, cellPx int, showOutline bool, flash map[[2]int]time.Time, now time.Time) layout.Widget {
	return func(gtx C) D {
		return drawBoard(gtx, snap, localIdx, cellPx, showOutline, flash, now)
	}
}
