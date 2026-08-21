package nativeui

// Display-adaptive scale. Every screen is designed for the default window
// (designWinW × designWinH dp — app.Size in Run): the login card's width, the
// connection panel's height, dialog widths, side panels, the boards' cell
// clamps, the type sizes. A larger display must not leave that layout
// floating in empty space, so the frame's dp/sp metric is stretched by the
// factor the window exceeds the design size — in BOTH dimensions, so nothing
// ever overflows — and every screen grows in proportion to fill the display:
// the connection dialog, the lobby's panels and rows, the wizard and invite
// dialogs, the playfields, the text. Never below 1: a window smaller than the
// design size keeps the 1:1 metric and the screens' own minimums (and the
// window's MinSize) take over. HiDPI displays already carry their own
// PxPerDp, so the comparison is made in dp — a Retina 2560×1600 window is
// 1280×800 dp and is not stretched.
const (
	designWinW = 1280
	designWinH = 820
)

// uiScale is the stretch factor for a frame whose constraints are the whole
// window: min(width/designWinW, height/designWinH) in dp, floored at 1.
func uiScale(gtx C) float32 {
	if gtx.Metric.PxPerDp <= 0 {
		return 1
	}
	sw := float32(gtx.Constraints.Max.X) / (gtx.Metric.PxPerDp * designWinW)
	sh := float32(gtx.Constraints.Max.Y) / (gtx.Metric.PxPerDp * designWinH)
	return max(1, min(sw, sh))
}

// scaledContext returns the frame context with its dp and sp metric
// stretched by uiScale — applied once, at the top of App.layout, so every
// gtx.Dp / gtx.Sp below it (and every material widget) sizes to the display.
func scaledContext(gtx C) C {
	s := uiScale(gtx)
	gtx.Metric.PxPerDp *= s
	gtx.Metric.PxPerSp *= s
	return gtx
}
