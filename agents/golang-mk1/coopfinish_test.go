package main

import "testing"

// A peer's top-out ends the crew's game for everyone, and the finish is
// every player's to make — the topper's own may never land (its connection
// went with it) — so the agent finishes the game too once its play loop
// returns. A rival's top-out in a competitive game, or our own echo, is not
// that.
func TestPeerCoopGameOverFinishes(t *testing.T) {
	newGame := func(mode int) *Game {
		g := &Game{mode: mode, eliminated: map[string]bool{}, results: map[string]event{}, senderTotals: map[string][2]int{}, senderTeams: map[string]int{}, started: make(chan struct{}), ended: make(chan struct{})}
		g.a = &Agent{name: "me"}
		return g
	}
	crew := newGame(modeCooperative)
	crew.onGameOver(event{Kind: "game_over", PlayerID: "other", TotalScore: 10, TotalLines: 1})
	if !crew.isEnded() || !crew.finishOnEnd() {
		t.Fatalf("crew: ended %v finish %v; want the peer's top-out to end and finish the game", crew.isEnded(), crew.finishOnEnd())
	}
	own := newGame(modeCooperative)
	own.onGameOver(event{Kind: "game_over", PlayerID: "me"})
	if own.finishOnEnd() {
		t.Fatal("our own echo is the topper's finish, not a peer's")
	}
	comp := newGame(modeCompetitive)
	comp.onGameOver(event{Kind: "game_over", PlayerID: "rival"})
	if comp.isEnded() || comp.finishOnEnd() {
		t.Fatalf("competitive: ended %v finish %v; a rival's top-out decides nothing", comp.isEnded(), comp.finishOnEnd())
	}
}
