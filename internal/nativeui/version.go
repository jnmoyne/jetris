package nativeui

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
)

// version is the build's version string, shown in the window's top-right
// corner. It stays "dev" for a plain `go build`; releases stamp
// -X main.version and cmd/jetris hands it here via SetVersion.
var version = "dev"

// SetVersion records the build version for the corner badge. Called once from
// main before the window opens.
func SetVersion(v string) {
	if v = strings.TrimSpace(v); v != "" {
		version = v
	}
}

// NotifyUpdate records that release tag (a newer build than this one, as found
// by cmd/jetris's startup check) is available at url: the version plate turns
// gold and names it, and the login screen says where to get it. Safe from any
// goroutine.
func (a *App) NotifyUpdate(tag, url string) {
	a.mu.Lock()
	a.updateTag, a.updateURL = tag, url
	a.mu.Unlock()
	a.invalidate()
}

// update returns the newer release's tag and page, "" when none is known.
func (a *App) update() (tag, url string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateTag, a.updateURL
}

// versionLabel is the badge text: an arcade-cabinet "VER" plate, with the
// leading "v" of a release tag folded into the label ("v1.2.3" → "VER 1.2.3").
// With a newer release known, the plate names it: "VER 1.2.3 · 1.3.0 AVAILABLE".
func versionLabel(update string) string {
	s := "VER " + strings.ToUpper(strings.TrimPrefix(version, "v"))
	if update != "" {
		s += " · " + strings.ToUpper(strings.TrimPrefix(update, "v")) + " AVAILABLE"
	}
	return s
}

// versionLabelShort is the plate where there is no room for the whole thing —
// a phone, where the badge sits in the same row as the brand banner and a
// two-line plate covers it. The build metadata goes ("0.11.1-13-GF206014-DIRTY"
// is a developer's string, not a player's), and once a newer release is known
// the plate says only that, which is the part worth the corner.
func versionLabelShort(update string) string {
	if update != "" {
		return "NEW " + strings.ToUpper(strings.TrimPrefix(update, "v"))
	}
	v := strings.ToUpper(strings.TrimPrefix(version, "v"))
	if i := strings.IndexByte(v, '-'); i > 0 {
		v = v[:i]
	}
	return "VER " + v
}

// versionBadge draws the version plate in the window's top-right corner, over
// whatever screen is showing: pixel-face text on its own panel chip so it stays
// readable above a board, framed like the rest of the 8-bit chrome. Muted
// normally; gold while a newer release is available, so the hint follows the
// player onto every screen.
func (a *App) versionBadge(gtx C) {
	update, _ := a.update()
	col := colMuted
	if update != "" {
		col = colGold
	}
	txt := versionLabel(update)
	if a.form.compact {
		txt = versionLabelShort(update)
	}
	lbl := a.pixel(unit.Sp(8), txt, col)
	lbl.MaxLines = 1
	inset := layout.Inset{Top: unit.Dp(6), Right: unit.Dp(8)}
	inset.Layout(gtx, func(gtx C) D {
		return layout.NE.Layout(gtx, func(gtx C) D {
			return layout.Background{}.Layout(gtx,
				func(gtx C) D {
					fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Min.X, gtx.Constraints.Min.Y), colPanel)
					fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Min.X, gtx.Dp(1)), colBorder)
					fillRect(gtx.Ops, image.Rect(0, gtx.Constraints.Min.Y-gtx.Dp(1), gtx.Constraints.Min.X, gtx.Constraints.Min.Y), colBorder)
					return D{Size: gtx.Constraints.Min}
				},
				func(gtx C) D {
					return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, lbl.Layout)
				},
			)
		})
	})
}
