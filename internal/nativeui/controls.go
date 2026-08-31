package nativeui

// On-screen arcade controls (the D-pad and face buttons) and the buffered-moves
// ("MOVE BUFFER") strip for the game screen. Everything here is drawn in the 8-bit chrome: blocky
// bitmap glyphs built from fillRect squares, the pixel face for labels, chunky
// borders and hard shadows — no smooth icon fonts, keeping the 80's low-res
// look at any window size.

import (
	"fmt"
	"image"
	"strconv"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/engine"
)

// The D-pad arms' semantic labels (what the tests find the buttons by, the
// way the playfield is found by playfieldLabel).
const (
	padLeftLabel   = "pad left"
	padRightLabel  = "pad right"
	padDownLabel   = "pad down"
	padRotateLabel = "pad rotate"
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
	// The game bar's switch icons (gamescreen.go), in the same blocky
	// language: the three-bar menu that shows the menu column, a speech
	// bubble for the chat strip, the D-pad cross for the on-screen pad. None
	// of them closes a window — every one is a switch that shows what it
	// hides, so there is no ✕ among them.
	glyphMenu = []string{
		"XXXXXXXX",
		"XXXXXXXX",
		"........",
		"XXXXXXXX",
		"XXXXXXXX",
		"........",
		"XXXXXXXX",
		"XXXXXXXX",
	}
	glyphChat = []string{
		".XXXXXX.",
		"XXXXXXXX",
		"XX....XX",
		"XXXXXXXX",
		"XX....XX",
		"XXXXXXXX",
		".XXXXXX.",
		"..XX....",
	}
	glyphPad = []string{
		"..XXXX..",
		"..XXXX..",
		"XXXXXXXX",
		"XXXXXXXX",
		"XXXXXXXX",
		"XXXXXXXX",
		"..XXXX..",
		"..XXXX..",
	}
	// glyphTrophy marks a winner (winnerMark, lobby.go). Drawn rather than
	// typed: a text trophy needs a font that has U+1F3C6, and since Gio
	// v0.10 none within reach does — it shapes to .notdef and comes out a
	// tofu box — while a bitmap is the same on every platform and in the
	// same language as the rest of this app's icons. Nine rows like the
	// rotate arrows: the rim, a bowl with a handle either side, the stem,
	// and the base.
	glyphTrophy = []string{
		"XXXXXXXXX",
		"XXXXXXXXX",
		"X.XXXXX.X",
		"X.XXXXX.X",
		"..XXXXX..",
		"...XXX...",
		"....X....",
		"..XXXXX..",
		".XXXXXXX.",
	}
	// glyphPlayers switches the lobby's players column on and off (lobby.go):
	// two figures side by side, which is what the column beside the games
	// actually lists — everyone else in here.
	glyphPlayers = []string{
		"XXX.XXX",
		"XXX.XXX",
		".X...X.",
		".......",
		"XXX.XXX",
		"XXX.XXX",
		"XXX.XXX",
		"XXX.XXX",
	}
	// glyphBoards switches the opponents' playfields on and off (gamescreen.go):
	// two wells side by side, the second one part-filled, which is what the
	// strip it opens actually shows.
	glyphBoards = []string{
		"XXX.XXX.",
		"X.X.X.X.",
		"X.X.X.X.",
		"X.X.X.X.",
		"X.X.XXX.",
		"X.X.XXX.",
		"XXX.XXX.",
		"........",
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

// flipped mirrors a glyph bitmap vertically (derives the fold-down arrow from
// the fold-up one).
func flipped(bm []string) []string {
	out := make([]string, len(bm))
	for i := range bm {
		out[i] = bm[len(bm)-1-i]
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
	// capCountGap is the gap in dp between the strip's centered MOVE BUFFER
	// label and the count that hangs off its right.
	capCountGap = 8
)

// stripChip is the move-buffer strip's chip side and inter-chip gap in px.
// The compact screen (formfactor.go) uses a smaller chip: the full-size row
// is 392 dp wide — wider than a phone — and the board is centered on the
// strip, so an oversized row would both spill off the screen and pull the
// playfield off it.
func (a *App) stripChip(gtx C) (chip, gap int) {
	if a.form.compact {
		return gtx.Dp(30), gtx.Dp(5)
	}
	return gtx.Dp(42), gtx.Dp(7)
}

// stripWidth is the move-buffer strip's width in px: its row of chip slots
// (the label over them is narrower). fitBoardAndPad plans around it, as the
// strip is wider than the playfield at most cell sizes.
func (a *App) stripWidth(gtx C) int {
	chip, gap := a.stripChip(gtx)
	return bufferedSlots * (chip + gap)
}

// stripReservedY is the vertical room the move-buffer strip and its inset
// take under the playfield, which fitBoardAndPad keeps for it.
func (a *App) stripReservedY(gtx C) int {
	if a.form.compact {
		return gtx.Dp(66)
	}
	return gtx.Dp(90)
}

// bufferedMovesStrip is the player's move-buffer readout under the board: a
// row of chunky slots that fill with arrow glyphs as inputs queue behind the
// in-flight batch publish (very visible on a high-RTT server) and drain as
// each buffered move's own publish starts. The moves that will go out
// together as one atomic batch (Engine.BufferedBatches) are shown the way
// the NATS messages panel shows a transaction's rows: a wash of the batch's
// color behind them, spanning the group, and a bracket in that color along
// the bottom, closed by a stub at either end — successive batches taking
// successive colors of msgGroupPalette, numbered from the batches the
// engine has taken so far (firstOrdinal) so a batch keeps its color as the
// queue drains. The dim empty slots are always there while playing, so a
// filling buffer is impossible to miss, and the strip is always exactly as
// wide as the slot row — the board is centered on it, and a caption or an
// overflow count that changed its width would shift the playfield with
// every queued move; past the slots the last one counts the rest — and a
// freshly queued glyph pops in with an overshoot. Over the slots sits the
// one caption the strip ever shows: MOVE BUFFER and the number queued.
func (a *App) bufferedMovesStrip(gtx C, batches [][]engine.MoveType, firstOrdinal int) D {
	chip, gap := a.stripChip(gtx)
	now := gtx.Now
	// The queue flat: each move with its batch's color and whether it opens
	// or closes its batch.
	type slot struct {
		m           engine.MoveType
		col         colorN
		first, last bool
	}
	var slots []slot
	for i, b := range batches {
		c := msgGroupPalette[(firstOrdinal+i)%len(msgGroupPalette)]
		for j, m := range b {
			slots = append(slots, slot{m: m, col: c, first: j == 0, last: j == len(b)-1})
		}
	}
	n := len(slots)
	if n != a.bufN {
		if n > a.bufN {
			a.bufGrewAt = now // a new glyph landed: run its pop-in
		}
		a.bufN = n
	}
	popping := now.Sub(a.bufGrewAt) < bufPopDur
	if popping {
		animate(gtx) // keep the pop-in animating
	}

	// The caption says the same thing whatever the queue is doing — the label,
	// then how many moves are buffered, "0" when none. The LABEL is what is
	// centered on the slot row, and the count hangs off its right, outside
	// that centering: the words never move, and a queue going from 9 to 10
	// merely reaches a glyph further right. (Centering the line as a whole
	// would shift the words half a glyph on every tenth move, and wording
	// that came and went — "EMPTY" against "3 QUEUED", an IN FLIGHT count
	// behind them — used to slide them about on every key press.) The count
	// is never empty either: an empty pixel run takes the fallback face's
	// line height (11 px against the 9 px of real text, on another baseline),
	// which used to make the row — and the centered board over it — jump by
	// two pixels.
	capSp := unit.Sp(9)
	if a.form.compact {
		capSp = unit.Sp(8)
	}
	countCol := colMuted
	if n > 0 {
		countCol = colGold
	}
	head, count := bufferedCaption(n)
	// The strip is always exactly the slot row wide, its content centered in
	// that width: the board over it is centered on the strip, and a caption
	// or a count that widened the strip would shift the whole playfield
	// with every move the player queues.
	width := a.stripWidth(gtx)
	macro := op.Record(gtx.Ops)
	dims := layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// The caption row claims the whole strip width, and the label is
			// laid in at the offset that centers IT on the row — so the count
			// after it grows to the right without ever moving the words.
			gtx.Constraints.Max.X = max(gtx.Constraints.Max.X, width)
			gtx.Constraints.Min.X = width
			lead := max(0, (width-a.pixelWidth(gtx, capSp, head))/2)
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: gtx.Metric.PxToDp(lead)}.Layout(gtx, a.pixel(capSp, head, colMuted).Layout)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: unit.Dp(capCountGap)}.Layout(gtx, a.pixel(capSp, count, countCol).Layout)
				}),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			var kids []layout.FlexChild
			for i := 0; i < bufferedSlots; i++ {
				i := i
				kids = append(kids, layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: gtx.Metric.PxToDp(gap)}.Layout(gtx, func(gtx C) D {
						if i >= n {
							return emptySlot(gtx, chip)
						}
						s := slots[i]
						scale := 1.0
						if popping && i == n-1 {
							t := clampF(float64(now.Sub(a.bufGrewAt))/float64(bufPopDur), 0, 1)
							scale = 0.4 + 0.6*easeOutBack(t)
						}
						// The batch under the glyph, the panel's way: its wash
						// and its bottom bracket span the gap to the next slot
						// while the batch continues there, and a stub closes
						// the bracket at the batch's first and last glyph.
						w := chip
						if !s.last && i+1 < bufferedSlots {
							w += gap
						}
						bar, stub, th := gtx.Dp(3), gtx.Dp(9), max(gtx.Dp(2), 2)
						fillRect(gtx.Ops, image.Rect(0, 0, w, chip), withAlpha(s.col, 0.10))
						fillRect(gtx.Ops, image.Rect(0, chip-bar, w, chip), withAlpha(s.col, 0.85))
						if s.first {
							fillRect(gtx.Ops, image.Rect(0, chip-stub, th, chip), withAlpha(s.col, 0.85))
						}
						if s.last {
							fillRect(gtx.Ops, image.Rect(chip-th, chip-stub, chip, chip), withAlpha(s.col, 0.85))
						}
						gtx.Constraints = layout.Exact(image.Pt(chip, chip))
						// The glyph keeps its share of the chip whatever size
						// the chip is (the compact strip's is smaller).
						glyph := float32(gtx.Metric.PxToDp(chip * 4 / 7))
						if i == bufferedSlots-1 && n > bufferedSlots {
							// The queue runs past the strip: the last slot
							// counts the rest instead of showing its move.
							layout.Center.Layout(gtx, a.pixel(gtx.Metric.PxToSp(chip*11/42), fmt.Sprintf("+%d", n-bufferedSlots+1), colFg).Layout)
						} else {
							layout.Center.Layout(gtx, a.moveGlyph(s.m, unit.Dp(glyph*float32(clampF(scale, 0, 1.15))), colFg))
						}
						return D{Size: image.Pt(chip, chip)}
					})
				}))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		}),
	)
	call := macro.Stop()
	defer op.Offset(image.Pt((width-dims.Size.X)/2, 0)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return D{Size: image.Pt(width, dims.Size.Y)}
}

// bufferedCaption is everything the strip's caption ever says for a queue of
// n moves: the fixed label, then the count — "0" when nothing is queued. It
// takes no other state on purpose. Wording that varied with the queue moved
// the line about under a board that has to sit still. The label is centered
// on the slot row and the count sits capCountGap to its right, so only the
// count's own width ever changes.
func bufferedCaption(n int) (head, count string) {
	return "MOVE BUFFER", strconv.Itoa(n)
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
// padSideGap (dp) is the gap between a flanking column — a side well over
// its pad — and the playfield; wellPadGap (dp) the gap between a well and
// the pad under it; wellGap (dp) the gap between a well and the playfield
// when no pad flanks it.
const (
	padMinScale  = 0.55
	padMinScaleY = 0.4
	padSideGap   = 24
	wellPadGap   = 12
	wellGap      = 12
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

// The HOLD box and the NEXT well normally draw at exactly the board's cell,
// so a preview reads like the pieces on the board. On a NARROW screen they
// cannot: a pair of full-size wells is 168 dp of a phone's 382 dp column,
// which drops the playfield from 280 dp wide to 170. So under
// narrowWellW they take narrowWellPct of the board's cell instead and keep
// their place beside the playfield, which is what they are for.
//
// The test is the screen's width, not whether it is the compact screen: what
// makes the wells unaffordable is width being the scarce dimension. A phone
// held LANDSCAPE is compact too, but its board is bound by height and there
// is width to spare — full-size wells cost it nothing — and the same goes
// for a tablet held portrait. narrowWellMinCell keeps a tile legible where
// the fraction lands small.
const (
	narrowWellW       = 600
	narrowWellPct     = 55
	narrowWellMinCell = 7
)

// narrowWells reports whether the wells shrink to fit this screen's width.
func (a *App) narrowWells() bool { return a.form.w > 0 && a.form.w < narrowWellW }

// wellCell is the cell size the HOLD box and the NEXT well draw at, given
// the board's (see narrowWellPct).
func (a *App) wellCell(boardCell int) int {
	if a.narrowWells() {
		return max(narrowWellMinCell, boardCell*narrowWellPct/100)
	}
	return boardCell
}

// wellWidth is a side well's full width in px at a given BOARD cell — the
// preview tile plus the frame and the padding around it, exactly as
// nextWell and holdWellBox lay themselves out. fitBoardAndPad plans the
// columns beside the playfield with it.
func (a *App) wellWidth(boardCell int) int {
	c := a.wellCell(boardCell)
	return previewCols*c + 2*(max(c/8, 2)+max(c/3, 6))
}

// sideWells is what flanks a player's playfield, for fitBoardAndPad's plan:
// whether the HOLD box (off the playfield's left) and the NEXT well (off its
// right) show, and each one's height at a given board cell — measured by
// laying the well out (a label's font metrics are not worth guessing at),
// so the pad placed under a well never runs past the playfield.
type sideWells struct {
	hold, next   bool
	holdH, nextH func(cell int) int
}

func (w sideWells) count() int {
	n := 0
	if w.hold {
		n++
	}
	if w.next {
		n++
	}
	return n
}

// flankGeom is the flanked layout's geometry: the playfield's left column
// (the HOLD box over the D-pad), the playfield, its right column (the NEXT
// well over the face buttons) — each column as wide as its wider member,
// its well and pad centered on each other, a pad centered on the
// playfield's height but never over its well — with a gap on either side
// of the playfield and the strip under it. Its parts come from
// fitBoardAndPad's plan; game.go's flanked layout realizes the same model
// from the widgets' measured sizes.
type flankGeom struct {
	dpadW, dpadH, faceW, faceH int // the pads
	holdW, holdH, nextW, nextH int // the wells (zero when not shown)
	boardW, boardH, stripW     int
	gap, vgap                  int  // beside the playfield; between a well and its pad
	lowPads                    bool // the pads hug the playfield's bottom edge (padsAtBottom)
}

func (g flankGeom) leftW() int  { return max(g.dpadW, g.holdW) }
func (g flankGeom) rightW() int { return max(g.faceW, g.nextW) }
func (g flankGeom) boardX() int { return g.leftW() + g.gap }
func (g flankGeom) stripX() int { return g.boardX() + g.boardW/2 - g.stripW/2 }
func (g flankGeom) rowW() int   { return g.boardX() + g.boardW + g.gap + g.rightW() }

// width is the whole layout's, the strip's spill past the row on either
// side included.
func (g flankGeom) width() int { return max(g.rowW(), g.stripX()+g.stripW) - min(0, g.stripX()) }

// padTop is a pad's top under its well (wellH zero without one): centered on
// the playfield — or hard against its bottom edge where the screen is held in
// two hands and the thumbs are at the foot of it, which is the compact screen
// in portrait (padsAtBottom) — and pushed down under the well when the two
// would overlap.
func padTop(boardH, wellH, vgap, padH int, bottom bool) int {
	y := max(0, (boardH-padH)/2)
	if bottom {
		y = max(0, boardH-padH)
	}
	if wellH > 0 {
		y = max(y, wellH+vgap)
	}
	return y
}

// padsAtBottom reports whether flanking pads hug the playfield's bottom edge
// rather than its middle: a phone or a tablet held upright in two hands.
func (a *App) padsAtBottom() bool { return a.form.compact && a.form.portrait }

// height is the columns' — the playfield's, or a pad's bottom past it.
func (g flankGeom) height() int {
	return max(g.boardH,
		padTop(g.boardH, g.holdH, g.vgap, g.dpadH, g.lowPads)+g.dpadH,
		padTop(g.boardH, g.nextH, g.vgap, g.faceH, g.lowPads)+g.faceH)
}

// besideScale is the largest pad scale, floored at padMinScale, at which
// the layout (its pads at their natural size in g) fits: the row within
// availX — each column as wide as its wider member, so a well wider than
// its pad leaves the pad's scale alone — and each pad within the
// playfield's height under its well.
func (g flankGeom) besideScale(availX int) float32 {
	room := float32(availX - g.boardW - 2*g.gap)
	dpadW, faceW, holdW, nextW := float32(g.dpadW), float32(g.faceW), float32(g.holdW), float32(g.nextW)
	rowW := func(s float32) float32 { return max(dpadW*s, holdW) + max(faceW*s, nextW) }
	s := float32(1)
	if rowW(1) > room {
		// rowW is piecewise linear and nondecreasing in the scale: the
		// largest scale that fits is where one of its pieces meets room.
		s = 0
		for _, c := range []float32{room / (dpadW + faceW), (room - holdW) / faceW, (room - nextW) / dpadW} {
			if c > s && rowW(c) <= room+0.5 {
				s = c
			}
		}
	}
	for _, col := range [][2]int{{g.holdH, g.dpadH}, {g.nextH, g.faceH}} {
		wellH, padH := col[0], col[1]
		roomH := g.boardH - wellH
		if wellH > 0 {
			roomH -= g.vgap
		}
		s = min(s, float32(roomH)/float32(padH))
	}
	return min(1, max(padMinScale, s))
}

// fitBoardAndPad picks the board cell size and the pad's placement for a
// board column of the current constraints: the playfield of cols×rows
// visible cells, the side wells beside it, under it the move-buffer strip
// (player), and while the game is playable (pad) the control pad. The pad
// goes wherever the playfield ends up bigger: beside it, its whole height
// left to the board — the D-pad under the HOLD box off the playfield's
// left, the face buttons under the NEXT well off its right, the way a
// handheld keeps its controls off the screen's sides, and the only fit for
// a wide, short column — or under it in a tall, narrow (portrait) column.
// Either way it scales down, floored at padMinScale, rather than clip:
// beside the board the pads take the width left by the playfield and the
// height left under their wells, the board's cell giving way to leave the
// pads their room (and a column too narrow for even that keeps the pad
// under the board); under it, the pad shrinks to the column's width and to
// whatever height is left over a minimum-cell playfield.
func (a *App) fitBoardAndPad(gtx C, cols, rows int, wells sideWells, player, pad, hold bool) padPlan {
	m := padMouse
	if a.touchUI {
		m = padTouch
	}
	plan := padPlan{padSizer: padSizer{m: m, scale: 1}}
	reservedY := 0
	if player {
		reservedY = a.stripReservedY(gtx) // the move-buffer strip and its inset
	}
	// The wells beside the playfield, at its cell size: previewCols board
	// columns each, plus a slice for the frame and the gap after it.
	// The wells beside the playfield cost it columns of its own: previewCols
	// each at the well's cell, which the compact screen shrinks
	// (compactWellPct), plus a slice for each frame and the gap after it.
	wellCols := wells.count() * previewCols
	if a.narrowWells() {
		wellCols = wellCols * narrowWellPct / 100
	}
	wellsX := wells.count() * gtx.Dp(18)
	// The cell may go smaller on the compact screen: a phone held landscape
	// has barely 300 dp of height for a 25-row well, and a board that fits
	// whole — small, but with its move buffer and its controls on screen —
	// beats one held at the desktop floor and clipped off the bottom.
	floor := unit.Dp(14)
	if a.form.compact {
		floor = 10
	}
	fit := func(cols, rx, ry int) int { return fitCellPx(gtx, cols, rows, 1, gtx.Dp(24)+rx, ry, floor, 56) }
	if !pad {
		plan.cell = fit(cols+wellCols, wellsX, reservedY)
		return plan
	}
	natural := padSizer{m: m, scale: 1}
	scaleFloored := func(room, size int, floor float32) float32 {
		return min(1, max(floor, float32(room)/float32(size)))
	}
	shadow := gtx.Dp(3)
	availX, availY := gtx.Constraints.Max.X-gtx.Dp(24), gtx.Constraints.Max.Y-reservedY
	// Under the playfield: the pad scales to the column's width and to the
	// height left over a minimum-cell board, and takes its height (plus the
	// inset over it) from the board's.
	naturalSz := natural.padSize(gtx, hold)
	roomY := availY - gtx.Dp(14)*(8*rows+2)/8 - shadow - gtx.Dp(10)
	under := padSizer{m: m, scale: min(scaleFloored(availX-shadow, naturalSz.X, padMinScale), scaleFloored(roomY, naturalSz.Y, padMinScaleY))}
	underSz := under.padSize(gtx, hold)
	cellUnder := fit(cols+wellCols, wellsX, reservedY+underSz.Y+shadow+gtx.Dp(10))
	// Beside it: the board takes the column's height; the pads get the
	// width left of the playfield and the height left under their wells
	// (drawBoard's and holdWell/nextWell's frames counted).
	geom := func(cell int, p padSizer) flankGeom {
		fw := max(cell/8, 2)
		face := p.faceSize(gtx, hold)
		g := flankGeom{dpadW: p.dpadSize(gtx), dpadH: p.dpadSize(gtx), faceW: face.X, faceH: face.Y,
			boardW: cols*cell + 2*fw, boardH: rows*cell + 2*fw, stripW: a.stripWidth(gtx),
			gap: gtx.Dp(padSideGap), vgap: gtx.Dp(wellPadGap), lowPads: a.padsAtBottom()}
		wellW := a.wellWidth(cell)
		if wells.hold {
			g.holdW, g.holdH = wellW, wells.holdH(cell)
		}
		if wells.next {
			g.nextW, g.nextH = wellW, wells.nextH(cell)
		}
		return g
	}
	// The pads' scale and the board's cell depend on each other — bigger
	// pads leave the board less width, a smaller board leaves the pads less
	// height under the wells — so plan from the board alone and settle over
	// a few rounds; the exact geometry has the last word.
	cell := fit(cols+wellCols, wellsX, reservedY)
	beside := natural
	var g flankGeom
	for range 3 {
		beside.scale = geom(cell, natural).besideScale(availX - shadow)
		g = geom(cell, beside)
		cell = fit(cols, g.leftW()+g.rightW()+2*g.gap+shadow, reservedY)
	}
	g = geom(cell, beside)
	if over := g.width() + shadow - availX; over > 0 && beside.scale > padMinScale {
		// The pads' px rounding, or a cell at its floor: shave the scale by
		// the overflow (and a little), once.
		padW := g.dpadW + g.faceW
		beside.scale = max(padMinScale, beside.scale*float32(padW-over-gtx.Dp(2))/float32(padW))
		g = geom(cell, beside)
	}
	// The row within the column's width, the pads within the playfield's
	// height (a playfield at its cell floor may run past the column on its
	// own; that is no reason to move the pad).
	if fits := g.width()+shadow <= availX && g.height() <= max(availY, g.boardH); fits && cell >= cellUnder {
		plan.padSizer, plan.beside, plan.cell, plan.width = beside, true, cell, g.width()+shadow
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
	arm := func(btn *widget.Clickable, col, row int, bm []string, label string) {
		defer op.Offset(image.Pt(col*cell, row*cell)).Push(gtx.Ops).Pop()
		gtx := gtx
		gtx.Constraints = layout.Exact(image.Pt(cell, cell))
		material.Clickable(gtx, btn, func(gtx C) D {
			semantic.LabelOp(label).Add(gtx.Ops) // what the tests find the arm by
			return layout.Center.Layout(gtx, glyphWidget(bm, p.dp(p.m.glyph), glyphCol))
		})
	}
	arm(&a.padUp, 1, 0, glyphCW, padRotateLabel)
	arm(&a.padLeft, 0, 1, glyphLeft, padLeftLabel)
	arm(&a.padRight, 2, 1, glyphRight, padRightLabel)
	arm(&a.padDown, 1, 2, glyphDown, padDownLabel)
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
//
// The ← → arms are not here: they move on the PRESS, not the click's
// release — held, they auto-repeat with the keyboard's DAS/ARR tuning — and
// handlePadShift drains their clicks and feeds their edges.
func (a *App) handlePadClicks(gtx C, eng *engine.Engine, active bool) {
	pads := [...]struct {
		btn  *widget.Clickable
		move func(*engine.Engine)
	}{
		{&a.padUp, (*engine.Engine).RotateCW},
		{&a.padDown, (*engine.Engine).MoveDown},
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
