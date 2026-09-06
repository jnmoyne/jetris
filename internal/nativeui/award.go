package nativeui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/engine"
	"jetris/internal/game"
)

// awardDur is how long the banner naming a scored clear floats over the
// board: it pops in, holds, and rises away as it fades through the second
// half.
const awardDur = 1500 * time.Millisecond

// awardBanner is the last lock that scored a clear or a T-spin on the
// player's board — their own, or a teammate's on a shared board — as the
// engine announced it (engine.UpdateAward): the Guideline's account of it
// (game.Clear), the lock's points, who made it, and when it arrived (zero:
// none yet).
type awardBanner struct {
	clear  game.Clear
	points int
	player string
	own    bool
	at     time.Time
}

func (b awardBanner) visible(now time.Time) bool {
	return !b.at.IsZero() && now.Sub(b.at) < awardDur
}

// lines is the banner, top to bottom: a teammate's name (their clear, not
// ours), the clear's name — "T-SPIN DOUBLE", "JETRIS", "MINI T-SPIN"… — the
// qualifiers that apply ("B2B", "COMBO n", "PERFECT CLEAR"), and the points.
// Each on its own line, so a narrow board gets a tall banner rather than a
// wide one.
func (b awardBanner) lines() []string {
	var out []string
	if !b.own && b.player != "" {
		out = append(out, b.player)
	}
	if n := b.clear.Name(); n != "" {
		out = append(out, n)
	}
	var quals []string
	if b.clear.BackToBack {
		quals = append(quals, "B2B")
	}
	if b.clear.Combo > 0 {
		quals = append(quals, fmt.Sprintf("COMBO %d", b.clear.Combo))
	}
	if b.clear.Perfect {
		quals = append(quals, "PERFECT CLEAR")
	}
	if len(quals) > 0 {
		out = append(out, strings.Join(quals, " · "))
	}
	return append(out, fmt.Sprintf("+%d", b.points))
}

// awardOverlay draws the banner centered over the playfield — its lines
// stacked, the points last and largest — popping in like the countdown,
// then rising a little as it fades out. Gold for the clears the Guideline
// calls difficult (and a perfect clear), the plain foreground otherwise. The
// type is sized to the board and shrunk until the widest line fits its
// width, so the banner never runs out over the wells beside it.
func (a *App) awardOverlay(gtx C, b awardBanner) D {
	t := clampF(float64(gtx.Now.Sub(b.at))/float64(awardDur), 0, 1)
	in := clampF(t/0.15, 0, 1)
	out := clampF((t-0.55)/0.45, 0, 1)
	alpha := in * (1 - out)
	scale := 0.7 + 0.3*easeOutBack(in)

	// A fifth of the countdown's size, then fitted to the board's width.
	base := 16.0
	if pps := gtx.Metric.PxPerSp; pps > 0 {
		if m := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y); m > 0 {
			base = clampF(float64(m)/40/float64(pps), 10, 30)
		}
	}
	lines := b.lines()
	if maxW := gtx.Constraints.Max.X - gtx.Dp(6); maxW > 0 && maxW < 1<<16 {
		for _, l := range lines {
			if w := a.pixelWidth(gtx, unit.Sp(float32(base)), l); w > maxW {
				base = base * float64(maxW) / float64(w)
			}
		}
	}
	col := colFg
	if b.clear.Difficult() || b.clear.Perfect {
		col = colGold
	}
	kids := make([]layout.FlexChild, 0, 2*len(lines))
	for i, l := range lines {
		size, c := base*scale, withAlpha(col, alpha)
		if i == len(lines)-1 {
			size, c = base*1.35*scale, withAlpha(colFg, alpha) // the points
			kids = append(kids, layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout))
		}
		kids = append(kids, layout.Rigid(a.pixel(unit.Sp(float32(size)), l, c).Layout))
	}
	defer op.Offset(image.Pt(0, -int(float64(gtx.Dp(28))*out))).Push(gtx.Ops).Pop()
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, kids...)
}

// playfieldOverlayVisible reports whether anything floats over the playfield
// this frame: the pre-game countdown, or the award banner.
func playfieldOverlayVisible(view gameView, mode engine.Mode, now time.Time) bool {
	return countdownVisible(view, mode) || view.award.visible(now)
}

// playfieldOverlay is what floats over the playfield: the pre-game countdown
// while it runs, else the banner naming the last scored clear.
func (a *App) playfieldOverlay(gtx C, view gameView, mode engine.Mode) D {
	if countdownVisible(view, mode) {
		return a.countdownOverlay(gtx, view.countdown, view.countdownAt)
	}
	return a.awardOverlay(gtx, view.award)
}
