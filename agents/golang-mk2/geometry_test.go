package main

import "testing"

// TestSharedHeight pins the shared board's height rule the agent plays on:
// the standard 24 rows (4 of headroom over 20 visible) for one seat and the
// game's extra_rows more for every seat after it, clamped like the engine
// clamps it; a Game that never read a meta answers the standard board.
func TestSharedHeight(t *testing.T) {
	for _, tc := range []struct{ seats, extra, want int }{
		{1, 0, 24}, {1, 10, 24}, {2, 0, 24}, {2, 5, 29}, {3, 5, 34}, {4, 4, 36},
		{2, 10, 34}, {2, -1, 24}, {2, 99, 34},
	} {
		if got := sharedHeight(tc.seats, tc.extra); got != tc.want {
			t.Errorf("sharedHeight(%d, %d) = %d, want %d", tc.seats, tc.extra, got, tc.want)
		}
	}
	for in, want := range map[int]int{0: 0, -1: 0, 5: 5, 10: 10, 11: 10} {
		if got := extraRows(in); got != want {
			t.Errorf("extraRows(%d) = %d, want %d", in, got, want)
		}
	}
	g := &Game{}
	if g.height() != standardHeight || standardHeight != 24 {
		t.Errorf("a fresh Game is %d rows tall, want %d", g.height(), 24)
	}
	g.h = 34
	if g.height() != 34 {
		t.Errorf("height() = %d, want the meta's 34", g.height())
	}
}
