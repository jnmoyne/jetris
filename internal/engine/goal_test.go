package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// The line goal is decided from the ordered event stream, on every engine
// alike: the crew's board reaching it ends the game with the whole crew as
// winners; a board scored per seat crowns its top scorer(s); a team's board
// reaching it first wins for the team; a competitive board for its player.
// A second crossing decides nothing — the first verdict stands.
func TestLineGoalDecidesPerPlayfield(t *testing.T) {
	ctx := context.Background()
	clear := func(id string, team, lines, score int) GameEvent {
		return GameEvent{Kind: EventLineClear, PlayerID: id, Team: team, LinesCleared: 1, TotalLines: lines, TotalScore: score}
	}

	// The crew together.
	crew := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	crew.lineGoal = 3
	crew.handleGameEvent(ctx, clear("other", 0, 2, 100))
	if _, _, decided := crew.Winners(); decided {
		t.Fatal("decided at 2 of 3 lines")
	}
	if lines, goal := crew.GoalProgress(); lines != 2 || goal != 3 {
		t.Fatalf("GoalProgress() = %d/%d, want 2/3", lines, goal)
	}
	crew.handleGameEvent(ctx, clear("me", 0, 1, 50)) // our own echo crosses it
	winners, winTeam, decided := crew.Winners()
	if !decided || winTeam != -1 || !winners["me"] || !winners["other"] {
		t.Fatalf("crew verdict = %v %d %v, want everyone", winners, winTeam, decided)
	}
	if won, over := crew.GameOutcome(); !won || !over || !crew.GoalReached() {
		t.Fatalf("GameOutcome() = %v %v, GoalReached %v; want a won goal", won, over, crew.GoalReached())
	}
	crew.handleGameEvent(ctx, clear("other", 0, 9, 900))
	if w, _, _ := crew.Winners(); !w["me"] || !w["other"] {
		t.Fatal("a later crossing moved the verdict")
	}

	// A board scored per seat: the top scorer.
	ranked := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	ranked.lineGoal, ranked.individual = 3, true
	ranked.handleGameEvent(ctx, clear("me", 0, 2, 100))
	ranked.handleGameEvent(ctx, clear("other", 0, 1, 500))
	if w, _, d := ranked.Winners(); !d || !w["other"] || w["me"] {
		t.Fatalf("ranked verdict = %v %v, want other alone", w, d)
	}
	if won, _ := ranked.GameOutcome(); won {
		t.Fatal("the lower score won a board scored per seat")
	}

	// Teams: the first playfield there; our team's members win.
	team := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, 0, 0)
	team.lineGoal = 3
	team.handleGameEvent(ctx, clear("b0", 1, 2, 0))
	team.handleGameEvent(ctx, clear("me", 0, 2, 0))
	team.handleGameEvent(ctx, clear("b1", 1, 1, 0))
	winners, winTeam, decided = team.Winners()
	if !decided || winTeam != 1 || !winners["b0"] || !winners["b1"] || winners["me"] {
		t.Fatalf("teams verdict = %v %d %v, want team B", winners, winTeam, decided)
	}
	if won, over := team.GameOutcome(); won || !over {
		t.Fatal("the losing team's member did not lose")
	}
	ours := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, 0, 0)
	ours.lineGoal = 3
	ours.SetRoster([]Seat{{PlayerID: "me", Team: 0}, {PlayerID: "mate", Team: 0, TeamSlot: 1}, {PlayerID: "b0", Team: 1}})
	ours.handleGameEvent(ctx, clear("mate", 0, 3, 0))
	if w, wt, d := ours.Winners(); !d || wt != 0 || !w["me"] || !w["mate"] || w["b0"] {
		t.Fatalf("our team's verdict = %v %d %v, want team A with both members", w, wt, d)
	}
	if lines, _ := ours.TeamGoalProgress(0); lines != 3 {
		t.Fatalf("team A's progress = %d, want 3", lines)
	}

	// Competitive: the player whose board got there.
	comp := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	comp.lineGoal = 3
	comp.handleGameEvent(ctx, clear("rival", 0, 3, 0))
	if w, _, d := comp.Winners(); !d || !w["rival"] || w["me"] {
		t.Fatalf("competitive verdict = %v %v, want rival", w, d)
	}
	mine := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	mine.lineGoal = 3
	mine.handleGameEvent(ctx, clear("rival", 0, 2, 0))
	mine.handleGameEvent(ctx, clear("me", 0, 3, 0)) // our own echo
	if w, _, d := mine.Winners(); !d || !w["me"] || w["rival"] {
		t.Fatalf("own crossing verdict = %v %v, want me", w, d)
	}
	if won, _ := mine.GameOutcome(); !won {
		t.Fatal("our own crossing did not win")
	}

	// No goal: nothing ever decides.
	none := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	none.handleGameEvent(ctx, clear("other", 0, 99, 0))
	if _, _, d := none.Winners(); d || none.GoalReached() {
		t.Fatal("a game without a goal decided one")
	}
}

// On the wire: a competitive board now announces its clears, and the clear
// that reaches the goal ends the game on every engine — the clearer wins,
// the other loses, the meta moves to finished.
func TestCompetitiveLineGoalEndsGameOnTheWire(t *testing.T) {
	gameID := "goal-competitive"
	js, engines := setupCompetitiveGameWith(t, gameID, 2, func(m *config.GameMeta) { m.LineGoal = 1 }, nil)
	a, b := engines[0], engines[1]
	bottom := config.TotalRows - 1
	if a.LineGoal() != 1 || b.LineGoal() != 1 {
		t.Fatalf("engines read the goal as %d and %d, want 1", a.LineGoal(), b.LineGoal())
	}

	prefillBottomForI(t, js, gameID, "p1", bottom)
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().Rows[bottom].Cells[0].Occupied
	}, "pre-fill to apply on a's replica")

	a.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		_, _, da := a.Winners()
		_, _, db := b.Winners()
		return da && db
	}, "both engines to decide the goal")
	if w, _, _ := a.Winners(); !w["p1"] || w["p2"] {
		t.Errorf("a's verdict = %v, want p1", w)
	}
	if w, _, _ := b.Winners(); !w["p1"] || w["p2"] {
		t.Errorf("b's verdict = %v, want p1", w)
	}
	if won, over := a.GameOutcome(); !won || !over {
		t.Errorf("a: won %v over %v, want a win", won, over)
	}
	if won, over := b.GameOutcome(); won || !over {
		t.Errorf("b: won %v over %v, want a loss", won, over)
	}
	if got := b.PlayerScores()["p1"]; got <= 0 {
		t.Errorf("b never folded p1's competitive line_clear (score %d)", got)
	}
	waitUntil(t, 5*time.Second, func() bool {
		m, _, err := natspkg.FetchGameMeta(context.Background(), js, gameID)
		return err == nil && (m.Status == config.GameStatusFinished || m.Status == config.GameStatusArchived)
	}, "the meta to move to finished")
}
