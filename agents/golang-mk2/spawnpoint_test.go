package main

import "testing"

// The spawn point follows the seats present, as the GUI's engine lays them
// out (config.SharedSpawnOffsetAmong): ranked in slot order, one
// extra-columns step apart, the group centred on the board — alone in the
// middle, a full house in the historical layout. Refreshed by every roster
// push.
func TestSpawnColumnFollowsSeatsPresent(t *testing.T) {
	// Seat 2 of a three-seat crew on an 18-wide board (extra 4).
	g := &Game{mode: modeCooperative, idx: 2, seatsOnPF: 3, extra: 4, eliminated: map[string]bool{}}
	g.a = &Agent{name: "me"}
	for _, tc := range []struct {
		what   string
		roster []playerSummary
		want   int
	}{
		{"alone", []playerSummary{{PlayerID: "me", Seat: 2}}, 7},
		{"two of three, the higher seat", []playerSummary{{PlayerID: "x", Seat: 0}, {PlayerID: "me", Seat: 2}}, 9},
		{"a full house", []playerSummary{{PlayerID: "x", Seat: 0}, {PlayerID: "y", Seat: 1}, {PlayerID: "me", Seat: 2}}, 11},
		{"a roster that lacks us", []playerSummary{{PlayerID: "x", Seat: 0}}, 9},
		{"no roster", nil, 11},
	} {
		g.onRoster(tc.roster)
		if g.spawnC != tc.want {
			t.Errorf("%s: spawn column %d, want %d", tc.what, g.spawnC, tc.want)
		}
	}
	if got := g.spawnColumn(nil); got != 11 {
		t.Errorf("no roster at all: spawn column %d, want the section's 11", got)
	}

	// Slot 2 of team B, three a side: only the team's own slots count.
	tm := &Game{mode: modeTeams, idx: 5, team: 1, teamSlot: 2, seatsOnPF: 3, extra: 4, eliminated: map[string]bool{}}
	tm.a = &Agent{name: "me"}
	tm.onRoster([]playerSummary{{PlayerID: "me", Seat: 5, Team: 1, TeamSlot: 2}, {PlayerID: "a0", Seat: 0, Team: 0}})
	if tm.spawnC != 7 {
		t.Errorf("alone on the team board: spawn column %d, want the middle, 7", tm.spawnC)
	}
	tm.onRoster([]playerSummary{{PlayerID: "me", Seat: 5, Team: 1, TeamSlot: 2}, {PlayerID: "b0", Seat: 3, Team: 1, TeamSlot: 0}, {PlayerID: "a0", Seat: 0, Team: 0}})
	if tm.spawnC != 9 {
		t.Errorf("two of the team's three: spawn column %d, want 9", tm.spawnC)
	}

	// A competitive board is private and standard.
	c := &Game{mode: modeCompetitive, idx: 1, eliminated: map[string]bool{}}
	c.a = &Agent{name: "me"}
	c.onRoster([]playerSummary{{PlayerID: "me", Seat: 1}})
	if c.spawnC != spawnCol {
		t.Errorf("competitive: spawn column %d, want %d", c.spawnC, spawnCol)
	}
}
