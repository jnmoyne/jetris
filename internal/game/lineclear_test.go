package game

import (
	"testing"
	"time"
)

// GravityInterval follows the Guideline speed curve,
// (0.8 − (L − 1) × 0.007)^(L − 1) seconds with L = level + 1, floored at one
// 60 Hz frame once the curve drops below it.
func TestGravityIntervalGuidelineCurve(t *testing.T) {
	want := []time.Duration{1000, 793, 618, 473, 355, 262, 190, 135, 94, 64, 43, 28, 18}
	for level, ms := range want {
		if got := GravityInterval(level); got != ms*time.Millisecond {
			t.Errorf("GravityInterval(%d) = %v, want %v", level, got, ms*time.Millisecond)
		}
	}
	for level := len(want); level <= 25; level++ {
		if got := GravityInterval(level); got != time.Second/60 {
			t.Errorf("GravityInterval(%d) = %v, want the one-frame floor %v", level, got, time.Second/60)
		}
	}
	if got := GravityInterval(-1); got != GravityInterval(0) {
		t.Errorf("GravityInterval(-1) = %v, want level 0's %v", got, GravityInterval(0))
	}
	for level := 1; level < 20; level++ {
		if GravityInterval(level) > GravityInterval(level-1) {
			t.Errorf("curve not monotonic at level %d", level)
		}
	}
}
