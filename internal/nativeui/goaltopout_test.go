package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// A crew that tops out before its line goal has lost: the game screen
// crowns nobody, and the game-over box says so.
func TestCoopGoalTopOutCrownsNobody(t *testing.T) {
	a := newTestApp()
	eng := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	eng.SetLineGoalForTest(100)
	players := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob", Seat: 1}}
	eng.SetRoster([]engine.Seat{{PlayerID: "alice"}, {PlayerID: "bob", Seat: 1}})
	// bob tops out at 12 of 100 lines: his game_over reaches alice's engine.
	eng.HandleGameEventForTest(engine.GameEvent{Kind: engine.EventGameOver, PlayerID: "bob", TotalLines: 12})

	view := gameView{gameOver: true, players: players, status: string(config.GameStatusFinished)}
	oc := a.resolveOutcome(eng, view, config.ModeCooperative, time.Now())
	if oc.decided || oc.wins("alice") || oc.wins("bob") {
		t.Fatalf("outcome = decided %v alice %v bob %v; want nobody crowned for a missed goal", oc.decided, oc.wins("alice"), oc.wins("bob"))
	}
	if won, over := eng.GameOutcome(); !over || won {
		t.Fatalf("GameOutcome() = won %v over %v, want a loss", won, over)
	}
}

// The game-over box's verdict line: the crew's missed goal is a loss, its
// reached goal and its classic run (no goal) say nothing beyond the score;
// the other modes' lines are unchanged.
func TestGameOverVerdictLine(t *testing.T) {
	cases := []struct {
		name                 string
		gmode                config.GameMode
		individual, won      bool
		goal                 int
		goalReached, playsOn bool
		want                 string
	}{
		{"crew missed goal", config.ModeCooperative, false, false, 100, false, false, "GOAL MISSED · YOU LOST"},
		{"crew reached goal", config.ModeCooperative, false, true, 100, true, false, ""},
		{"crew classic run", config.ModeCooperative, false, false, 0, false, false, ""},
		{"ranked seat won", config.ModeCooperative, true, true, 100, false, false, "YOU WON!"},
		{"ranked seat lost", config.ModeCooperative, true, false, 0, false, false, "YOU LOST"},
		{"competitive won", config.ModeCompetitive, false, true, 0, false, false, "YOU WON!"},
		{"team plays on", config.ModeTeams, false, false, 0, false, true, "Your team plays on"},
		{"team lost", config.ModeTeams, false, false, 40, false, false, "YOUR TEAM LOST"},
	}
	for _, c := range cases {
		if got, _ := gameOverVerdict(c.gmode, c.individual, c.won, c.goal, c.goalReached, c.playsOn); got != c.want {
			t.Errorf("%s: verdict %q, want %q", c.name, got, c.want)
		}
	}
}
