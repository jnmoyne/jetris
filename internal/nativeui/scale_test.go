package nativeui

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// The display-adaptive scale: 1 at (or below) the design window, the
// smaller of the two dimensions' excess above it — measured in dp, so a
// HiDPI window of the design size is not stretched — and applied to both the
// dp and the sp metric.
func TestUIScale(t *testing.T) {
	ctx := func(w, h int, pxPerDp float32) C {
		var ops op.Ops
		return layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: pxPerDp, PxPerSp: pxPerDp},
			Constraints: layout.Exact(image.Pt(w, h)),
		}
	}
	near := func(got, want float32) bool { return got > want-0.01 && got < want+0.01 }

	cases := []struct {
		name    string
		w, h    int
		pxPerDp float32
		want    float32
	}{
		{"design window is 1:1", designWinW, designWinH, 1, 1},
		{"smaller window never shrinks", 1000, 700, 1, 1},
		{"wider only is limited by the height", 2560, designWinH, 1, 1},
		{"1920×1080 stretches by the height's excess", 1920, 1080, 1, 1080.0 / designWinH},
		{"2560×1440 stretches by the height's excess", 2560, 1440, 1, 1440.0 / designWinH},
		{"tall and narrow is limited by the width", designWinW, 3000, 1, 1},
		{"Retina at the design size is 1:1", 2 * designWinW, 2 * designWinH, 2, 1},
		{"Retina twice the design size doubles", 4 * designWinW, 4 * designWinH, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := uiScale(ctx(tc.w, tc.h, tc.pxPerDp)); !near(got, tc.want) {
				t.Fatalf("uiScale(%d×%d @%v) = %v, want %v", tc.w, tc.h, tc.pxPerDp, got, tc.want)
			}
		})
	}

	// The scaled context stretches dp and sp alike: the login card, 560 dp by
	// design, is twice as wide on a window twice the design size.
	plain, big := ctx(designWinW, designWinH, 1), scaledContext(ctx(2*designWinW, 2*designWinH, 1))
	if got, want := big.Dp(loginCardW), 2*plain.Dp(loginCardW); got != want {
		t.Fatalf("login card at 2× = %d px, want %d", got, want)
	}
	if got, want := big.Sp(unit.Sp(12)), 2*plain.Sp(unit.Sp(12)); got != want {
		t.Fatalf("12 sp at 2× = %d px, want %d", got, want)
	}
	if got := scaledContext(plain).Dp(loginCardW); got != plain.Dp(loginCardW) {
		t.Fatalf("design-size window must not be rescaled: %d vs %d", got, plain.Dp(loginCardW))
	}
}
