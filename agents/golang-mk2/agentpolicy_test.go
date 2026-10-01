package main

import (
	"context"
	"testing"
	"time"
)

// TestAgentPolicyPerTeam: max_agents caps the agents of EACH team of a
// teams game (guide §2) — a team whose agent seats are taken is full for an
// agent while a seat stays free for a human, and pickTeam steers round it;
// an invited join is exempt. Elsewhere the cap is on the whole game.
func TestAgentPolicyPerTeam(t *testing.T) {
	g := obj{}
	g.set("mode", modeTeams)
	g.set("status", "created")
	g.set("team_count", 2)
	g.set("team_size", 2)
	g.set("player_count", 4)
	g.set("max_agents", 1)
	g.set("players", []playerSummary{{PlayerID: "bot", Seat: 0, Team: 0, Agent: true}})
	if !joinable(g) {
		t.Error("a 2v2 with one agent seat per team and team B empty is not joinable")
	}
	if got := pickTeam(g, -1, true); got != 1 {
		t.Errorf("pickTeam = %d, want team B (team A's agent seat is taken)", got)
	}
	if got := pickTeam(g, 0, true); got != -1 {
		t.Errorf("pickTeam(team A) = %d, want -1 (its agent seat is taken)", got)
	}
	if got := pickTeam(g, 0, false); got != 0 {
		t.Errorf("an invited join to team A = %d, want 0 (the invitation is the permission)", got)
	}
	g.set("players", []playerSummary{{PlayerID: "bot", Seat: 0, Team: 0, Agent: true}, {PlayerID: "hal", Seat: 2, Team: 1, Agent: true}})
	if joinable(g) {
		t.Error("a 2v2 with an agent on each team (max 1 per team) is joinable with two seats free")
	}
	if got := pickTeam(g, -1, false); got < 0 {
		t.Error("an invited agent found no team with a free seat")
	}
	g.set("max_agents", 2)
	if !joinable(g) || pickTeam(g, -1, true) < 0 {
		t.Error("two agent seats per team, one taken on each: not joinable")
	}
	g.set("max_agents", 0)
	if joinable(g) {
		t.Error("a game closed to agents is joinable")
	}
	// Elsewhere the cap is on the whole game.
	coop := obj{}
	coop.set("mode", modeCooperative)
	coop.set("status", "created")
	coop.set("player_count", 3)
	coop.set("max_agents", 1)
	coop.set("players", []playerSummary{{PlayerID: "bot", Seat: 0, Agent: true}})
	if joinable(coop) {
		t.Error("a co-op game with its one agent seat taken is joinable")
	}
	coop.set("max_agents", 2)
	if !joinable(coop) {
		t.Error("a co-op game with an agent seat free is not joinable")
	}
}

// TestPauseWhileAlone: with the listing's agents_pause_alone, an agent left
// as the only player on the roster waits between pieces, and plays on the
// moment the roster holds someone else; the game ending releases it as a
// stop; without the rule it never waits.
func TestPauseWhileAlone(t *testing.T) {
	ctx := context.Background()
	mk := func(pause bool) *Game {
		g := newGame(&Agent{name: "me", stopCh: make(chan struct{})}, "g", 0)
		g.mode = modeCooperative
		g.pauseAlone = pause
		g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}})
		return g
	}
	if g := mk(false); !g.pauseWhileAlone(ctx) {
		t.Error("without the rule, an agent alone waited")
	}
	g := mk(true)
	g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 1}})
	if !g.pauseWhileAlone(ctx) {
		t.Error("with company, the agent waited")
	}

	g = mk(true)
	done := make(chan bool, 1)
	go func() { done <- g.pauseWhileAlone(ctx) }()
	select {
	case <-done:
		t.Fatal("an agent alone did not wait")
	case <-time.After(400 * time.Millisecond):
	}
	g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 1}})
	select {
	case ok := <-done:
		if !ok {
			t.Error("company arriving did not resume play")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the agent stayed paused after company arrived")
	}

	g = mk(true)
	go func() { done <- g.pauseWhileAlone(ctx) }()
	g.markEnded()
	select {
	case ok := <-done:
		if ok {
			t.Error("the game ending did not release the paused agent as a stop")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the agent stayed paused after the game ended")
	}
}
