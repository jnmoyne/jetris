package main

import (
	"slices"
	"testing"
)

// TestRedealRankParity: the re-deal among the seats present follows the
// GUI's engine rule — the seven types dealt out (pieceSets, the fixtures
// TestSplitParity pins) between the slots present, ranked in slot order,
// this seat drawing from its rank's ration; the same roster twice re-deals
// nothing, every slot present is the deal at creation, and a game that does
// not split ignores the roster.
func TestRedealRankParity(t *testing.T) {
	g := &Game{mode: modeCooperative, idx: 2, split: true, seatsOnPF: 3, metaSeed: 42}
	g.ration = pieceSetFor(42, 3, 2)
	if !g.redealLocked([]int{0, 2}) {
		t.Fatal("seat 1 gone: no re-deal")
	}
	if want := pieceSetFor(42, 2, 1); !slices.Equal(g.ration, want) {
		t.Fatalf("seat 2's ration = %v, want the second of the two-way deal %v", g.ration, want)
	}
	if g.redealLocked([]int{2, 0}) {
		t.Fatal("the same seats, in another order, re-dealt")
	}
	if !g.redealLocked([]int{0, 1, 2}) {
		t.Fatal("the full roster back: no re-deal")
	}
	if want := pieceSetFor(42, 3, 2); !slices.Equal(g.ration, want) {
		t.Fatalf("the full roster back: ration = %v, want the creation deal's %v", g.ration, want)
	}
	if !g.redealLocked(nil) && g.presentPF != nil {
		t.Fatal("no roster: the deal at creation")
	}
	teams := &Game{mode: modeTeams, team: 1, teamSlot: 0, split: true, seatsOnPF: 2, metaSeed: 7}
	roster := []playerSummary{{PlayerID: "a", Team: 0, TeamSlot: 0}, {PlayerID: "b", Team: 1, TeamSlot: 0}, {PlayerID: "c", Team: 1, TeamSlot: 1}}
	if got := teams.presentSlots(roster); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("team 1's present slots = %v, want [0 1]", got)
	}
	plain := &Game{mode: modeCooperative, seatsOnPF: 3}
	if plain.redealLocked([]int{0}) || plain.ration != nil {
		t.Fatal("a game without the split dealt a ration")
	}
	// Seats read by position for a listing written before the field.
	legacy := normalizeSeats([]playerSummary{{PlayerID: "a"}, {PlayerID: "b"}})
	if legacy[1].Seat != 1 {
		t.Errorf("legacy seats = %+v", legacy)
	}
}
