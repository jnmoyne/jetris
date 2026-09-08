package main

import "testing"

// A board scored per seat: another seat's line_clear folds the crew's lines
// (the shared level) but never their points; the ranking names the top
// scorer(s) — ourselves from our own score, a topped-out player from their
// game_over, everyone else from the last total their events carried.
func TestFoldIndividual(t *testing.T) {
	g := &Game{mode: modeCooperative, individual: true, senderTotals: map[string][2]int{}, results: map[string]event{}}
	g.a = &Agent{name: "me"}
	g.foldLineClear(event{Kind: "line_clear", PlayerID: "other", TotalScore: 300, TotalLines: 2})
	if g.sharedScore != 0 || g.totalLines != 2 {
		t.Fatalf("shared score %d, lines %d; want 0 and 2", g.sharedScore, g.totalLines)
	}
	shared := &Game{mode: modeCooperative, senderTotals: map[string][2]int{}, results: map[string]event{}}
	shared.a = &Agent{name: "me"}
	shared.foldLineClear(event{Kind: "line_clear", PlayerID: "other", TotalScore: 300, TotalLines: 2})
	if shared.sharedScore != 300 {
		t.Fatalf("a shared board folds the points: %d, want 300", shared.sharedScore)
	}

	g.roster = []playerSummary{{PlayerID: "me"}, {PlayerID: "other"}, {PlayerID: "third"}}
	g.score = 250
	g.results["third"] = event{Kind: "game_over", PlayerID: "third", TotalScore: 300}
	if w := g.topScorersLocked(); w["me"] || !w["other"] || !w["third"] {
		t.Fatalf("topScorers = %v, want other and third tied at 300", w)
	}
	g.score = 301
	if !g.individualWinnerLocked() {
		t.Fatal("the best score is not the winner")
	}
	for _, r := range g.playerResults() {
		want := r["player_id"] == "me"
		if _, got := r["winner"]; got != want {
			t.Errorf("%s: winner %v, want %v", r["player_id"], got, want)
		}
	}
}
