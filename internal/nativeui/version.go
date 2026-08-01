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

// versionLabel is the badge text: an arcade-cabinet "VER" plate, with the
// leading "v" of a release tag folded into the label ("v1.2.3" → "VER 1.2.3").
func versionLabel() string {
	return "VER " + strings.ToUpper(strings.TrimPrefix(version, "v"))
}

// versionBadge draws the version plate in the window's top-right corner, over
// whatever screen is showing: pixel-face text on its own panel chip so it stays
// readable above a board, framed like the rest of the 8-bit chrome.
func (a *App) versionBadge(gtx C) {
	lbl := a.pixel(unit.Sp(8), versionLabel(), colMuted)
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
