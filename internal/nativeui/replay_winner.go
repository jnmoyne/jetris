package nativeui

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/game"
	"jetris/internal/render"
)

// The end of a replay is a reveal. While the game plays back nothing on the
// screen says who won: the summary line names the players in their board
// colors without scores or trophies. When the copy-complete marker lands the
// screen crowns the winning board(s) — a WINNER / WINNERS banner rises out of
// the well and floats over it under a trophy with the game's rank, the
// winning team's or player's name turns gold in bold italic, the beaten
// boards read OUT behind a wash, and the status line says who won. The
// trophy is graded by where the game stands in its replay bucket's all-time
// ranking (config.ReplayRank — the "By score" order behind the history's
// TOP 10 mark): the bucket's best game floats a LEGENDARY trophy (gold with a
// holographic rainbow shine, halo rings and a swarm of sparkles), the top
// three an EPIC one (gold in a purple aura), the top ten a RARE one (silver
// with a shine sweep), and any other game a plain bronze cup — and the cup
// holds a tetromino picked by the same rank from the internet's worst-to-best
// ranking of the seven pieces (prizePiece). Every frame of the show is a
// pure function of the frame time against the moment the replay finished —
// the fireworks idiom: layoutReplay keeps invalidating while the show is up
// and no per-frame state is mutated. A live game gets the same show on the
// spectator's screen, and on a winning player's own over their victory
// fireworks (spectator_reveal.go).

// trophyTier grades the trophy by the game's rank in its replay bucket.
type trophyTier int

const (
	tierPlain     trophyTier = iota // outside the bucket's top ten: a bronze cup
	tierRare                        // top ten: silver, a shine sweep, a few sparkles
	tierEpic                        // top three: gold in a purple aura
	tierLegendary                   // the bucket's best game: gold, prismatic, haloed
)

// trophyTierFor grades a game by its rank (1 = the bucket's best).
func trophyTierFor(rank int) trophyTier {
	switch {
	case rank <= 1:
		return tierLegendary
	case rank <= 3:
		return tierEpic
	case rank <= config.ReplayTopN:
		return tierRare
	}
	return tierPlain
}

// tierStyle is a tier's look: the cup's metal and the effects around it.
type tierStyle struct {
	name    string // caption word ("" for the plain cup)
	metal   colorN // cup body
	shade   colorN // cup's shaded side and base
	glint   colorN // cup's highlight strip
	aura    colorN // the board's frame pulse and the shine sweep's tint (A = 0: neither)
	caption colorN
	sparks  int  // twinkling stars around the cup
	glow    bool // a soft breathing glow behind the cup
	halo    bool // rings expanding out of the cup
	prism   bool // holographic: a rainbow shine sweep and a hue-cycling highlight
}

func (t trophyTier) style() tierStyle {
	switch t {
	case tierLegendary:
		return tierStyle{name: "LEGENDARY",
			metal: colGold, shade: colorN{R: 0xb0, G: 0x7c, B: 0x00, A: 0xff}, glint: colorN{R: 0xff, G: 0xf4, B: 0xb0, A: 0xff},
			aura: colorN{R: 0xff, G: 0xe0, B: 0x70, A: 0xff}, caption: colGold,
			sparks: 14, glow: true, halo: true, prism: true}
	case tierEpic:
		purple := colorN{R: 0xc0, G: 0x60, B: 0xff, A: 0xff}
		return tierStyle{name: "EPIC",
			metal: colGold, shade: colorN{R: 0xb0, G: 0x7c, B: 0x00, A: 0xff}, glint: colorN{R: 0xff, G: 0xf4, B: 0xb0, A: 0xff},
			aura: purple, caption: purple, sparks: 8, glow: true}
	case tierRare:
		return tierStyle{name: "RARE",
			metal: colorN{R: 0xd4, G: 0xdc, B: 0xec, A: 0xff}, shade: colorN{R: 0x7c, G: 0x88, B: 0xa4, A: 0xff}, glint: colorN{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
			aura: colAccent, caption: colAccent, sparks: 4}
	}
	return tierStyle{
		metal: colorN{R: 0xc4, G: 0x7a, B: 0x36, A: 0xff}, shade: colorN{R: 0x7e, G: 0x48, B: 0x1e, A: 0xff}, glint: colorN{R: 0xe8, G: 0xaa, B: 0x6a, A: 0xff},
		caption: colFg}
}

// trophyArt is the cup, 12×13 pixels: X the metal, h its highlight strip, d
// its shaded side and base, . empty. Drawn at a pixel size derived from the
// board's cell so it reads like the pieces beside it.
var trophyArt = []string{
	"..XXXXXXXX..",
	".XhXXXXXXdX.",
	"XXhXXXXXXdXX",
	"XXhXXXXXXdXX",
	".XhXXXXXXdX.",
	"..hXXXXXXd..",
	"...XXXXXX...",
	"....XXXX....",
	".....XX.....",
	".....XX.....",
	"....XXXX....",
	"...XXXXXX...",
	"..dddddddd..",
}

// sparkTable places the sparkles around the cup — positions as fractions of
// the cup's box (past its edges), each twinkling on its own phase.
var sparkTable = []struct{ fx, fy, phase float64 }{
	{-0.30, 0.10, 0.00}, {1.25, 0.25, 0.37}, {0.15, -0.30, 0.61}, {0.95, -0.22, 0.13},
	{-0.22, 0.75, 0.82}, {1.18, 0.85, 0.29}, {0.50, -0.42, 0.48}, {0.35, 1.15, 0.71},
	{-0.42, 0.42, 0.20}, {1.38, 0.55, 0.92}, {0.75, 1.08, 0.05}, {0.05, 0.55, 0.57},
	{1.05, 0.05, 0.76}, {-0.10, 1.05, 0.34},
}

// winnerFX is the crown on a finished game's winning board: the moment the
// game ended on this screen (the show's clock), the game's rank in its
// bucket (of that size) with the tier and prize piece it earns, and the
// banner word.
type winnerFX struct {
	doneAt   time.Time
	rank, of int
	tier     trophyTier
	piece    game.PieceType
	banner   string // WINNER, WINNERS, or GAME OVER (the cooperative board)
}

// newWinnerFX grades a game's rank into its crown.
func newWinnerFX(doneAt time.Time, rank, of int, banner string) winnerFX {
	return winnerFX{doneAt: doneAt, rank: rank, of: of, tier: trophyTierFor(rank), piece: prizePiece(rank), banner: banner}
}

// prizePiece is the tetromino the winner show sets in the trophy's mouth,
// chosen by the game's rank from the internet's consensus ranking of the
// seven pieces, best to worst: the I (four lines at once — the piece every
// stack waits for), the T (the T-spin's piece, the most versatile), the L and
// J (the dependable pair), the O (harmless, and no help in a crisis), and the
// S and Z at the bottom — the pieces nobody wants, the Z the most cursed of
// all. So the bucket's best game gets the I, and a game nobody ranks the Z.
func prizePiece(rank int) game.PieceType {
	switch {
	case rank <= 1:
		return game.PieceI
	case rank <= 2:
		return game.PieceT
	case rank <= 3:
		return game.PieceL
	case rank <= 6:
		return game.PieceJ
	case rank <= config.ReplayTopN:
		return game.PieceO
	case rank <= 2*config.ReplayTopN:
		return game.PieceS
	}
	return game.PieceZ
}

// crownWinners decorates a finished replay's board strip for the reveal: the
// winning boards get the winner show and a gold bold-italic label, and — when
// there IS a winner to beat them — the others get the OUT wash. UI goroutine.
func (a *App) crownWinners(boards []labeledBoard, rv *replayView, doneAt, now time.Time) {
	won, banner := replayWinners(rv)
	if len(won) == 0 {
		return // a draw: nobody is crowned and nobody is out
	}
	fx := newWinnerFX(doneAt, rv.rank, rv.of, banner)
	for i := range boards {
		if won[i] {
			boards[i].labelCol, boards[i].emph = colGold, true
			boards[i].wrap = a.crownBoard(fx, now)
		} else {
			boards[i].wrap = a.knockoutBoard
		}
	}
}

// replayWinners resolves a finished replay's outcome: the indexes (in
// rv.boards order) of the boards that won, and the banner they are crowned
// with — WINNERS for the winning team, WINNER for the last player standing,
// GAME OVER for the cooperative board (it is everyone's; the run's rank is
// the prize). A draw crowns nobody.
func replayWinners(rv *replayView) (won map[int]bool, banner string) {
	won = map[int]bool{}
	switch rv.rec.Mode {
	case config.ModeTeams:
		if t := rv.rec.WinningTeam; t >= 0 && t < len(rv.boards) {
			won[t], banner = true, "WINNERS"
		}
	case config.ModeCooperative:
		if len(rv.boards) > 0 {
			won[0], banner = true, "GAME OVER"
		}
	default:
		for _, p := range rv.rec.Players {
			if i, ok := rv.byPlayer[p.PlayerID]; ok && p.Winner {
				won[i], banner = true, "WINNER"
			}
		}
	}
	return won, banner
}

// replayVerdict is the status line's tail once the replay is complete: who won.
func replayVerdict(r config.ArchiveRecord) string {
	switch r.Mode {
	case config.ModeTeams:
		if r.WinningTeam >= 0 && r.WinningTeam < r.Teams() {
			return "TEAM " + r.TeamName(r.WinningTeam) + " WINS!"
		}
		return "DRAW"
	case config.ModeCooperative:
		return fmt.Sprintf("FINAL SCORE %d", r.TotalScore)
	}
	var winners []string
	for _, p := range r.Players {
		if p.Winner {
			winners = append(winners, p.PlayerID)
		}
	}
	return winnersVerdict(winners)
}

// winnersVerdict names the winning players: ALICE WINS!, ALICE & BOB WIN!,
// or DRAW when nobody won.
func winnersVerdict(names []string) string {
	up := make([]string, 0, len(names))
	for _, n := range names {
		up = append(up, strings.ToUpper(n))
	}
	sort.Strings(up)
	switch n := len(up); n {
	case 0:
		return "DRAW"
	case 1:
		return up[0] + " WINS!"
	default:
		return strings.Join(up[:n-1], ", ") + " & " + up[n-1] + " WIN!"
	}
}

// crownBoard wraps a winning board with the winner show, drawn over the
// board from the board's own size and the strip's cell size.
func (a *App) crownBoard(fx winnerFX, now time.Time) func(layout.Widget, int) layout.Widget {
	return a.crown(fx, now, false)
}

// crownBoardOnTop is crownBoard with the show painted at the very end of the
// frame instead of right over the board: op.Defer keeps the board's
// transform, so the show still floats over the well, but above whatever the
// screen paints after the board — the victory fireworks a winning player's
// screen lays over everything (layoutGame). The frame's own last layer, the
// CRT scanlines, is deferred too and later (App.layout), so it still covers
// the show.
func (a *App) crownBoardOnTop(fx winnerFX, now time.Time) func(layout.Widget, int) layout.Widget {
	return a.crown(fx, now, true)
}

func (a *App) crown(fx winnerFX, now time.Time, onTop bool) func(layout.Widget, int) layout.Widget {
	return func(board layout.Widget, cellPx int) layout.Widget {
		return func(gtx C) D {
			return layout.Stack{}.Layout(gtx,
				layout.Stacked(board),
				layout.Expanded(func(gtx C) D {
					if !onTop {
						a.drawWinnerShow(gtx, fx, cellPx, now)
						return D{Size: gtx.Constraints.Min}
					}
					macro := op.Record(gtx.Ops)
					a.drawWinnerShow(gtx, fx, cellPx, now)
					op.Defer(gtx.Ops, macro.Stop())
					return D{Size: gtx.Constraints.Min}
				}),
			)
		}
	}
}

// knockoutBoard washes a beaten board out and stamps it OUT — the live
// spectator view's language for an eliminated board.
func (a *App) knockoutBoard(board layout.Widget, _ int) layout.Widget {
	washed := func(gtx C) D {
		return layout.Stack{}.Layout(gtx,
			layout.Stacked(board),
			layout.Expanded(func(gtx C) D {
				fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Min}, withAlpha(colBg, 0.45))
				return D{Size: gtx.Constraints.Min}
			}),
		)
	}
	return a.boardOverlay(washed, "OUT", colErr)
}

// Show timing: the banner's rise, its fade-in, the float everything bobs on,
// the breathing of glows and frame pulses, the shine sweep, the sparkle
// twinkle, the halo rings' expansion, and the prismatic hue cycle.
const (
	showRise   = 900 * time.Millisecond
	showFade   = 350 * time.Millisecond
	showBob    = 2400 * time.Millisecond
	showBreath = 1600 * time.Millisecond
	showSweep  = 2200 * time.Millisecond
	showTwinkl = 1300 * time.Millisecond
	showHalo   = 1800 * time.Millisecond
	showPrism  = 3 * time.Second
)

// cyc is where a repeating show phase stands at t: the fraction of one period.
func cyc(t, period time.Duration) float64 {
	return math.Mod(t.Seconds()/period.Seconds(), 1)
}

// drawWinnerShow paints one frame of the show over a board of the current
// constraints' size: the frame pulse, then the banner rising out of the well
// to settle below its middle, the rank caption under it and the trophy above
// it, all floating on a slow bob once risen.
func (a *App) drawWinnerShow(gtx C, fx winnerFX, cellPx int, now time.Time) {
	st := fx.tier.style()
	w, h := gtx.Constraints.Min.X, gtx.Constraints.Min.Y
	el := now.Sub(fx.doneAt)
	if el < 0 {
		el = 0
	}
	pxPerSp := float64(gtx.Metric.PxPerSp)
	if pxPerSp <= 0 {
		pxPerSp = 1
	}
	spOf := func(px float64) unit.Sp { return unit.Sp(px / pxPerSp) }

	// Frame pulse: rings hugging the well's frame, breathing in the aura color.
	if st.aura.A != 0 {
		breath := 0.5 + 0.5*math.Sin(2*math.Pi*cyc(el, showBreath))
		for i := 1; i <= 3; i++ {
			g := i * max(cellPx/5, 2)
			alpha := (0.45 - 0.12*float64(i)) * (0.35 + 0.65*breath)
			drawRing(gtx.Ops, image.Rect(-g, -g, w+g, h+g), max(cellPx/5, 2), withAlpha(st.aura, alpha))
		}
	}

	rise := easeOutBack(clampF(float64(el)/float64(showRise), 0, 1))
	fade := clampF(float64(el)/float64(showFade), 0, 1)
	bob := math.Sin(2 * math.Pi * cyc(el, showBob))

	// The banner: a pixel chip like the live OUT / WINNER markers.
	banner := a.pixel(spOf(clampF(float64(cellPx)*0.75, 7, 16)), fx.banner, withAlpha(colGo, fade))
	inset := layout.UniformInset(unit.Dp(4))
	bannerW := func(gtx C) D {
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, banner.Layout)
		call := macro.Stop()
		fillRect(gtx.Ops, image.Rectangle{Max: dims.Size}, withAlpha(colBg, 0.85*fade))
		call.Add(gtx.Ops)
		return dims
	}
	bd := measure(gtx, bannerW)
	startY := float64(h + bd.Size.Y)
	restY := 0.45 * float64(h)
	cy := startY + (restY-startY)*rise + 5*bob
	bx, by := (w-bd.Size.X)/2, int(cy)-bd.Size.Y/2

	// The trophy above the banner, bobbing a beat behind it — painted first
	// so the banner and caption chips sit over its glow.
	px := max(cellPx/3, 3)
	tw, th := len(trophyArt[0])*px, len(trophyArt)*px
	ty := by - px*3 - th + int(3*math.Sin(2*math.Pi*cyc(el, showBob)+0.9))
	drawTrophy(gtx.Ops, image.Pt((w-tw)/2, ty), px, st, fx.piece, el, fade)

	drawAt(gtx, image.Pt(bx, by), bannerW)

	// The rank caption under the banner.
	caption := fmt.Sprintf("#%d OF %d", fx.rank, fx.of)
	capCol := st.caption
	if st.name != "" {
		caption = fmt.Sprintf("#%d · %s", fx.rank, st.name)
	}
	if st.prism {
		capCol = lerpColor(colGold, hueColor(cyc(el, showPrism)), 0.5)
	}
	cl := a.pixel(spOf(clampF(float64(cellPx)*0.45, 6, 10)), caption, withAlpha(capCol, fade))
	capW := func(gtx C) D {
		macro := op.Record(gtx.Ops)
		dims := inset.Layout(gtx, cl.Layout)
		call := macro.Stop()
		fillRect(gtx.Ops, image.Rectangle{Max: dims.Size}, withAlpha(colBg, 0.85*fade))
		call.Add(gtx.Ops)
		return dims
	}
	cd := measure(gtx, capW)
	drawAt(gtx, image.Pt((w-cd.Size.X)/2, by+bd.Size.Y+2), capW)
}

// drawTrophy paints the cup at the given pixel size with its tier's effects
// — the glow and halo rings behind it, the shine sweep through its metal,
// the sparkles around it — and the prize piece standing in its mouth.
func drawTrophy(ops *op.Ops, at image.Point, px int, st tierStyle, piece game.PieceType, el time.Duration, fade float64) {
	tw, th := len(trophyArt[0])*px, len(trophyArt)*px
	cx, cy := at.X+tw/2, at.Y+th/2
	if st.glow {
		// A soft glow: nested translucent squares stepping down in size, so
		// the aura fades out from the cup instead of sitting in one hard box.
		breath := 0.5 + 0.5*math.Sin(2*math.Pi*cyc(el, showBreath))
		for i := 5; i >= 1; i-- {
			half := int(float64(tw) * (0.5 + 0.14*float64(i)))
			fillRect(ops, image.Rect(cx-half, cy-half, cx+half, cy+half), withAlpha(st.aura, (0.05+0.04*breath)*fade))
		}
	}
	if st.halo {
		for k := 0; k < 2; k++ {
			ph := math.Mod(cyc(el, showHalo)+0.5*float64(k), 1)
			half := int(float64(tw) * (0.6 + 0.9*ph))
			drawRing(ops, image.Rect(cx-half, cy-half, cx+half, cy+half), max(px/2, 2), withAlpha(st.aura, 0.55*(1-ph)*fade))
		}
	}
	band := cyc(el, showSweep)*1.6 - 0.3 // the shine's position along the cup's diagonal
	diag := float64(len(trophyArt[0]) + len(trophyArt))
	for r, row := range trophyArt {
		for c, ch := range row {
			var col colorN
			switch ch {
			case 'X':
				col = st.metal
			case 'h':
				col = st.glint
			case 'd':
				col = st.shade
			default:
				continue
			}
			d := float64(c+r) / diag
			if st.prism && ch == 'h' {
				// The highlight strip cycles through the spectrum.
				col = lerpColor(col, hueColor(cyc(el, showPrism)), 0.55)
			}
			if st.aura.A != 0 {
				// The shine: a band sweeping down the cup's diagonal — white
				// light on silver and gold, a holographic rainbow on the
				// legendary cup (each hue at its own depth in the band).
				width := 0.10
				if st.prism {
					width = 0.14
				}
				if dd := math.Abs(d - band); dd < width {
					k := 1 - dd/width
					if st.prism {
						col = lerpColor(col, hueColor((d-band+width)/(2*width)), 0.75*k)
					} else {
						col = lighten(col, 0.85*k)
					}
				}
			}
			fillRect(ops, image.Rect(at.X+c*px, at.Y+r*px, at.X+(c+1)*px, at.Y+(r+1)*px), withAlpha(col, fade))
		}
	}
	// The prize piece: board-styled cells two trophy pixels square, the
	// piece centered over the cup's mouth (the rim is eight pixels wide,
	// exactly a four-cell preview box) with its bottom row over the rim,
	// so it stands in the cup.
	cell := 2 * px
	rows, _ := pieceBox(piece)
	drawPrizePiece(ops, at.X+2*px, at.Y+px-rows*cell, cell, piece, fade)
	for i := 0; i < st.sparks && i < len(sparkTable); i++ {
		s := sparkTable[i]
		tk := math.Sin(2 * math.Pi * (cyc(el, showTwinkl) + s.phase))
		if tk < 0.15 {
			continue
		}
		col := colorN{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		if i%2 == 1 {
			col = st.aura
		}
		size := max(2, int(float64(px)*(0.45+0.75*tk)))
		x, y := at.X+int(s.fx*float64(tw)), at.Y+int(s.fy*float64(th))
		drawStar(ops, image.Pt(x, y), size, withAlpha(col, tk*fade))
	}
}

// pieceBox is the size, in cells, of a piece's spawn orientation.
func pieceBox(pt game.PieceType) (rows, cols int) {
	cells := game.Piece{Type: pt}.Cells()
	minR, maxR, minC, maxC := cells[0][0], cells[0][0], cells[0][1], cells[0][1]
	for _, rc := range cells[1:] {
		minR, maxR = min(minR, rc[0]), max(maxR, rc[0])
		minC, maxC = min(minC, rc[1]), max(maxC, rc[1])
	}
	return maxR - minR + 1, maxC - minC + 1
}

// drawPrizePiece paints a piece in its spawn orientation, styled like locked
// cells of its type and centered in a previewCols-wide box at (x0, y0) —
// drawMiniPiece with the show's fade applied.
func drawPrizePiece(ops *op.Ops, x0, y0, cell int, pt game.PieceType, fade float64) {
	cells := game.Piece{Type: pt}.Cells()
	minR, minC, maxC := cells[0][0], cells[0][1], cells[0][1]
	for _, rc := range cells[1:] {
		minR, minC, maxC = min(minR, rc[0]), min(minC, rc[1]), max(maxC, rc[1])
	}
	shift := (previewCols-(maxC-minC+1))/2 - minC
	ap := render.CellStyle(game.Cell{Occupied: true, PieceType: pt}, -1, false)
	for _, rc := range cells {
		drawCell(ops, x0+(rc[1]+shift)*cell, y0+(rc[0]-minR)*cell, cell, withAlpha(ap.Fill, fade), withAlpha(ap.Outline, fade), ap.OutlineW, ap.Panels)
	}
}

// drawStar paints a four-pointed pixel star: a center block with an arm on
// each side, all of size px.
func drawStar(ops *op.Ops, c image.Point, px int, col colorN) {
	half := px / 2
	fillRect(ops, image.Rect(c.X-half, c.Y-half, c.X-half+px, c.Y-half+px), col)
	fillRect(ops, image.Rect(c.X-half, c.Y-half-px, c.X-half+px, c.Y-half), col)
	fillRect(ops, image.Rect(c.X-half, c.Y-half+px, c.X-half+px, c.Y-half+2*px), col)
	fillRect(ops, image.Rect(c.X-half-px, c.Y-half, c.X-half, c.Y-half+px), col)
	fillRect(ops, image.Rect(c.X-half+px, c.Y-half, c.X-half+2*px, c.Y-half+px), col)
}

// drawRing paints a hollow rectangle: r's outline, wpx thick, inside r.
func drawRing(ops *op.Ops, r image.Rectangle, wpx int, col colorN) {
	fillRect(ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+wpx), col)
	fillRect(ops, image.Rect(r.Min.X, r.Max.Y-wpx, r.Max.X, r.Max.Y), col)
	fillRect(ops, image.Rect(r.Min.X, r.Min.Y+wpx, r.Min.X+wpx, r.Max.Y-wpx), col)
	fillRect(ops, image.Rect(r.Max.X-wpx, r.Min.Y+wpx, r.Max.X, r.Max.Y-wpx), col)
}

// hueColor is the fully saturated color at hue t (0..1 around the wheel).
func hueColor(t float64) colorN {
	h := math.Mod(t, 1) * 6
	x := 1 - math.Abs(math.Mod(h, 2)-1)
	var r, g, b float64
	switch int(h) {
	case 0:
		r, g = 1, x
	case 1:
		r, g = x, 1
	case 2:
		g, b = 1, x
	case 3:
		g, b = x, 1
	case 4:
		r, b = x, 1
	default:
		r, b = 1, x
	}
	return colorN{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255), A: 0xff}
}

// measure lays w out off-screen (the recorded ops are discarded) and returns
// its dimensions, with no minimum imposed.
func measure(gtx C, w layout.Widget) D {
	m := gtx
	m.Constraints.Min = image.Point{}
	rec := op.Record(gtx.Ops)
	d := w(m)
	rec.Stop()
	return d
}

// drawAt lays w out translated to at, with no minimum imposed.
func drawAt(gtx C, at image.Point, w layout.Widget) D {
	defer op.Offset(at).Push(gtx.Ops).Pop()
	gtx.Constraints.Min = image.Point{}
	return w(gtx)
}

// pixelEmph is a pixel-face label in synthesized bold italic — the face has
// no such variants — leaning the glyphs with a shear and double-striking them
// a pixel apart, the way 8-bit title screens faked emphasis.
func (a *App) pixelEmph(size unit.Sp, txt string, col colorN) layout.Widget {
	return func(gtx C) D {
		l := a.pixel(size, txt, col)
		macro := op.Record(gtx.Ops)
		dims := l.Layout(gtx)
		call := macro.Stop()
		lean := float32(dims.Size.Y) * 0.2
		// Shear about the label's bottom edge so the top leans right.
		tr := op.Affine(f32.Affine2D{}.Shear(f32.Pt(0, float32(dims.Size.Y)), -0.2, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off := op.Offset(image.Pt(1, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		tr.Pop()
		return D{Size: image.Pt(dims.Size.X+int(lean)+1, dims.Size.Y), Baseline: dims.Baseline}
	}
}

// span is one run of the replay's summary line: text in a color, in bold
// italic for a revealed winner.
type span struct {
	text string
	col  colorN
	emph bool
}

// replaySummary is the replay screen's summary line: when the game was
// played and how long it took, the mode, then every player in the color of
// their board — teams grouped under their color-matched TEAM A / TEAM B / …,
// the
// cooperative crew plainly — with no scores and no trophies while the game
// plays back, so the ending stays a surprise. Revealed, the winners go gold
// with a trophy and in bold italic, the beaten keep their colors, and every
// score and level appears.
func replaySummary(r config.ArchiveRecord, reveal bool) []span {
	mode := "competitive"
	switch r.Mode {
	case config.ModeTeams:
		mode = "teams"
	case config.ModeCooperative:
		mode = "co-op"
	}
	head := mode + " · "
	if when := archiveWhen(r); when != "" {
		head = when + " · " + head
	}
	out := []span{{text: head, col: colMuted}}
	sep := span{text: " · ", col: colMuted}
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].PlayerID < players[j].PlayerID })
	switch r.Mode {
	case config.ModeTeams:
		for t := 0; t < r.Teams(); t++ {
			if t > 0 {
				out = append(out, sep)
			}
			col := render.PlayerColorRGBA(t)
			won := reveal && r.WinningTeam == t
			name := "TEAM " + r.TeamName(t)
			if won {
				name, col = winnerMark+name, colGold
			}
			if reveal && t < len(r.TeamScores) {
				name += fmt.Sprintf(" %d", r.TeamScores[t])
				if t < len(r.TeamLevels) {
					name += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
				}
			}
			out = append(out, span{text: name, col: col, emph: won})
			var members []string
			for _, p := range players {
				if p.Team == t {
					members = append(members, agentName(p.PlayerID, p.Agent))
				}
			}
			if len(members) > 0 {
				out = append(out, span{text: " — ", col: colMuted}, span{text: strings.Join(members, ", "), col: col})
			}
		}
	case config.ModeCooperative:
		names := make([]string, 0, len(players))
		for _, p := range players {
			names = append(names, agentName(p.PlayerID, p.Agent))
		}
		out = append(out, span{text: strings.Join(names, ", "), col: colFg})
		if reveal {
			out = append(out, sep, span{text: fmt.Sprintf("total %d (lvl %d)", r.TotalScore, r.FinalLevel), col: colGold, emph: true})
		}
	default:
		for i, p := range players {
			if i > 0 {
				out = append(out, sep)
			}
			txt, col := agentName(p.PlayerID, p.Agent), render.PlayerColorRGBA(i)
			if reveal {
				txt += fmt.Sprintf(" %d (lvl %d)", p.Score, p.Level)
			}
			won := reveal && p.Winner
			if won {
				txt, col = winnerMark+txt, colGold
			}
			out = append(out, span{text: txt, col: col, emph: won})
		}
	}
	return out
}

// spansLine lays spans out as one line of body text, each run in its own
// color and weight.
func (a *App) spansLine(spans []span) layout.Widget {
	return func(gtx C) D {
		kids := make([]layout.FlexChild, 0, len(spans))
		for _, s := range spans {
			kids = append(kids, layout.Rigid(a.markedSpan(s.text, s.col, s.emph)))
		}
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, kids...)
	}
}
