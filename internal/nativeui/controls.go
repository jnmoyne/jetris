package nativeui

// On-screen arcade controls and the buffered-moves ("MOVE BUFFER") strip for
// the game screen. Everything here is drawn in the 8-bit chrome: blocky
// bitmap glyphs built from fillRect squares, the pixel face for labels, chunky
// borders and hard shadows — no smooth icon fonts, keeping the 80's low-res
// look at any window size.

import (
	"fmt"
	"image"
	"math"
	"time"

	"gioui.org/layout"
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
		a.invalidate() // keep the chase glow / pop-in animating
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

// controlPad is the mouse control row under the player's board — the whole
// keyboard scheme as chunky arcade buttons (rotate CCW/CW, shift left / down /
// right, and a wide DROP bar), so the game is fully playable without a
// keyboard. It renders dimmed until the game is running; handlePadClicks does
// the actual gating of clicks.
func (a *App) controlPad(gtx C, enabled bool) D {
	glyphCol := colAccent
	if !enabled {
		glyphCol = colMuted
	}
	sq := func(btn *widget.Clickable, content layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
				return a.padButton(gtx, btn, enabled, image.Pt(gtx.Dp(52), gtx.Dp(48)), colPanel, content)
			})
		})
	}
	dropBg, dropFg := colAccent, colBg
	if !enabled {
		dropBg, dropFg = colPanel, colMuted
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		sq(&a.padCCW, glyphWidget(glyphCCW, 18, glyphCol)),
		sq(&a.padLeft, glyphWidget(glyphLeft, 18, glyphCol)),
		sq(&a.padDown, glyphWidget(glyphDown, 18, glyphCol)),
		sq(&a.padRight, glyphWidget(glyphRight, 18, glyphCol)),
		sq(&a.padCW, glyphWidget(glyphCW, 18, glyphCol)),
		layout.Rigid(func(gtx C) D {
			return a.padButton(gtx, &a.padDrop, enabled, image.Pt(gtx.Dp(104), gtx.Dp(48)), dropBg, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(glyphWidget(glyphDrop, 16, dropFg)),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(a.pixel(unit.Sp(9), "DROP", dropFg).Layout),
				)
			})
		}),
	)
}

// padButton renders one arcade pad button: hard shadow, chunky border, solid
// fill, centered glyph. The border mutes while disabled.
func (a *App) padButton(gtx C, btn *widget.Clickable, enabled bool, sz image.Point, bg colorN, content layout.Widget) D {
	border := colAccent
	if !enabled {
		border = colBorder
	}
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: border, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			return material.Clickable(gtx, btn, func(gtx C) D {
				gtx.Constraints = layout.Exact(sz)
				return background(gtx, bg, func(gtx C) D {
					return layout.Center.Layout(gtx, content)
				})
			})
		})
	})
}

// handlePadClicks drains the on-screen pad's clicks and dispatches them to the
// engine while the game is actually being played. Draining is unconditional so
// clicks made while the pad is disabled (pre-start, game over) die here
// instead of firing as moves once the game starts.
func (a *App) handlePadClicks(gtx C, eng *engine.Engine, active bool) {
	pads := [...]struct {
		btn  *widget.Clickable
		move func(*engine.Engine)
	}{
		{&a.padCCW, (*engine.Engine).RotateCCW},
		{&a.padLeft, (*engine.Engine).MoveLeft},
		{&a.padDown, (*engine.Engine).MoveDown},
		{&a.padRight, (*engine.Engine).MoveRight},
		{&a.padCW, (*engine.Engine).RotateCW},
		{&a.padDrop, (*engine.Engine).HardDrop},
	}
	for i := range pads {
		for pads[i].btn.Clicked(gtx) {
			if active && eng != nil {
				pads[i].move(eng)
			}
		}
	}
}
