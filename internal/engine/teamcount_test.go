package engine

import (
	"context"
	"testing"

	"jetris/internal/config"
)

// The garbage rotation (ledger.go). A duel has only one answer — every attack
// lands on the other team — and past two teams consecutive attacks rotate
// through the opponents, never hitting our own team and never piling every
// raise onto one victim.
func TestNextGarbageTargetRotates(t *testing.T) {
	for _, tc := range []struct {
		teams, me int
		want      []int
	}{
		{2, 0, []int{1, 1, 1, 1}},
		{2, 1, []int{0, 0, 0, 0}},
		{3, 0, []int{1, 2, 1, 2}},
		{3, 1, []int{2, 0, 2, 0}},
		{3, 2, []int{0, 1, 0, 1}},
		{4, 1, []int{2, 3, 0, 2, 3, 0}},
	} {
		e := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, tc.me, 0)
		e.teamCount = tc.teams
		for i, want := range tc.want {
			if got := e.nextGarbageTarget(); got != want {
				t.Fatalf("%d teams, seat on team %d: attack %d hit team %d, want %d", tc.teams, tc.me, i, got, want)
			}
		}
	}
}

// The endgame past two teams: a team falling decides nothing while more than
// one is still standing — only the last team left wins, and every team out
// together is a draw. A spectator engine takes the same decision path as a
// player's without any of its transitions.
func TestTeamEndgamePastTwoTeams(t *testing.T) {
	newSpectator := func(teams, teamSize int) *Engine {
		e := New(nil, "g", "watcher", "", config.ModeTeams, ModeSpectator, 0, 0, 0)
		e.teamCount, e.teamSize = teams, teamSize
		return e
	}
	out := func(e *Engine, id string, team int) {
		e.handleGameEvent(context.Background(), GameEvent{Kind: EventGameOver, PlayerID: id, Team: team})
	}
	decided := func(e *Engine) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.teamOutcomeDone
	}

	// Three teams of one: A falls, then C — B is the last standing.
	e := newSpectator(3, 1)
	out(e, "a", 0)
	if decided(e) {
		t.Fatal("three teams: the game was decided when only team A had fallen")
	}
	out(e, "c", 2)
	if !decided(e) {
		t.Fatal("three teams: the game was not decided with only team B left")
	}

	// Three teams of two: a team is out only once BOTH its members are.
	e = newSpectator(3, 2)
	for _, p := range []struct {
		id   string
		team int
	}{{"a1", 0}, {"a2", 0}, {"c1", 2}} {
		out(e, p.id, p.team)
	}
	if decided(e) {
		t.Fatal("three teams of two: decided with team C still holding a member")
	}
	out(e, "c2", 2)
	if !decided(e) {
		t.Fatal("three teams of two: not decided with only team B left")
	}

	// Every team out: a draw, decided exactly once.
	e = newSpectator(3, 1)
	out(e, "a", 0)
	out(e, "b", 1)
	out(e, "c", 2)
	if !decided(e) {
		t.Fatal("three teams: all out was not decided")
	}
}
