package game

import (
	"testing"
	"time"
)

// GravityInterval is the Tetris Worlds curve exactly
// (https://harddrop.com/wiki/Tetris_Worlds): (0.8 − (L − 1) × 0.007)^(L − 1)
// seconds per row at level L, levels 1 to 15, nothing rounded and no floor —
// the top two levels are faster than a 60 Hz frame.
func TestGravityIntervalTetrisWorldsCurve(t *testing.T) {
	wantMicros := []int64{1000000, 793000, 617796, 472729, 355197, 262004, 189677, 134735, 93882, 64152, 42976, 28218, 18153, 11439, 7059}
	if len(wantMicros) != MaxLevel-MinLevel+1 {
		t.Fatalf("table covers %d levels, want %d", len(wantMicros), MaxLevel-MinLevel+1)
	}
	for i, us := range wantMicros {
		level := MinLevel + i
		if got := GravityInterval(level).Round(time.Microsecond); got != time.Duration(us)*time.Microsecond {
			t.Errorf("GravityInterval(%d) = %v, want %dµs", level, got, us)
		}
	}
	frame := time.Second / 60
	if GravityInterval(MaxLevel-1) >= frame || GravityInterval(MaxLevel) >= frame {
		t.Errorf("levels %d and %d should be faster than a frame (%v): %v, %v", MaxLevel-1, MaxLevel, frame, GravityInterval(MaxLevel-1), GravityInterval(MaxLevel))
	}
	if got := GravityInterval(MinLevel - 1); got != GravityInterval(MinLevel) {
		t.Errorf("GravityInterval(%d) = %v, want level %d's %v", MinLevel-1, got, MinLevel, GravityInterval(MinLevel))
	}
	if got := GravityInterval(MaxLevel + 5); got != GravityInterval(MaxLevel) {
		t.Errorf("GravityInterval(%d) = %v, want level %d's %v", MaxLevel+5, got, MaxLevel, GravityInterval(MaxLevel))
	}
	for level := MinLevel + 1; level <= MaxLevel; level++ {
		if GravityInterval(level) >= GravityInterval(level-1) {
			t.Errorf("curve not strictly faster at level %d", level)
		}
	}
}
