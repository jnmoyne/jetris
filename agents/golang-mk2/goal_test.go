package main

import "testing"

// TestPlayfieldLines mirrors the GUI's goal rule: the crew's board counts
// every seat's lines (ours included), a team's board its members', a
// competitive board its player's — and the crossing decides the verdict
// once: the crew wins together, a team by membership, a competitive board
// by ownership.
func TestPlayfieldLines(t *testing.T) {
	crew := &Game{mode: modeCooperative, lineGoal: 3, lines: 1, senderTotals: map[string][2]int{"a": {10, 1}, "b": {20, 1}}, senderTeams: map[string]int{}, started: make(chan struct{}), ended: make(chan struct{})}
	crew.a = &Agent{name: "me"}
	if got := crew.playfieldLinesLocked(event{PlayerID: "a"}); got != 3 {
		t.Fatalf("crew lines = %d, want 3", got)
	}
	crew.checkLineGoal(event{Kind: "line_clear", PlayerID: "a", TotalLines: 1})
	if !crew.goalDecided || !crew.goalWon || !crew.isEnded() {
		t.Fatalf("crew: decided %v won %v ended %v", crew.goalDecided, crew.goalWon, crew.isEnded())
	}

	team := &Game{mode: modeTeams, team: 0, lineGoal: 3, lines: 2, senderTotals: map[string][2]int{"b0": {0, 2}, "b1": {0, 1}, "mate": {0, 5}}, senderTeams: map[string]int{"b0": 1, "b1": 1, "mate": 0}, started: make(chan struct{}), ended: make(chan struct{})}
	team.a = &Agent{name: "me"}
	if got := team.playfieldLinesLocked(event{PlayerID: "b1", Team: 1}); got != 3 {
		t.Fatalf("team B lines = %d, want 3", got)
	}
	if got := team.playfieldLinesLocked(event{PlayerID: "mate", Team: 0}); got != 7 {
		t.Fatalf("team A lines = %d, want 7 (ours included)", got)
	}
	team.checkLineGoal(event{Kind: "line_clear", PlayerID: "b1", Team: 1, TotalLines: 1})
	if !team.goalDecided || team.goalWon {
		t.Fatalf("team: decided %v won %v, want a loss to team B", team.goalDecided, team.goalWon)
	}
	team.checkLineGoal(event{Kind: "line_clear", PlayerID: "mate", Team: 0, TotalLines: 5})
	if team.goalWon {
		t.Fatal("a later crossing moved the verdict")
	}

	comp := &Game{mode: modeCompetitive, lineGoal: 2, lines: 2, senderTotals: map[string][2]int{"rival": {0, 1}}, senderTeams: map[string]int{}, started: make(chan struct{}), ended: make(chan struct{})}
	comp.a = &Agent{name: "me"}
	comp.checkLineGoal(event{Kind: "line_clear", PlayerID: "rival", TotalLines: 1})
	if comp.goalDecided {
		t.Fatal("the rival's 1 line decided a goal of 2")
	}
	comp.checkLineGoal(event{Kind: "line_clear", PlayerID: "me", TotalLines: 2})
	if !comp.goalDecided || !comp.goalWon {
		t.Fatalf("our own crossing: decided %v won %v", comp.goalDecided, comp.goalWon)
	}
	if !comp.winCheck() {
		t.Fatal("winCheck ignores the goal's verdict")
	}
}
