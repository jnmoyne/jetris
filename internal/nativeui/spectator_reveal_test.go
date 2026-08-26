package nativeui

// The spectator's live ending: the outcome resolved from what a spectator's
// engine knows, the provisional rank, the prize piece ladder, and the reveal
// rendering on every spectated mode.

import (
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
)

func outOf(ids ...string) func(string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

func TestSpectatorVerdict(t *testing.T) {
	trio := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob"}, {PlayerID: "carol", Name: "carol"}}
	// Competitive: undecided with one of three out; decided once all but one
	// are out (the survivor wins); everyone out at once is a draw.
	if d, _, _ := spectatorVerdict(config.ModeCompetitive, trio, outOf("bob"), false); d {
		t.Error("competitive: one of three out resolved as decided")
	}
	d, w, team := spectatorVerdict(config.ModeCompetitive, trio, outOf("bob", "carol"), false)
	if !d || !w["alice"] || len(w) != 1 || team != -1 {
		t.Errorf("competitive: two of three out → decided %v winners %v team %d, want alice alone", d, w, team)
	}
	if d, w, _ := spectatorVerdict(config.ModeCompetitive, trio, outOf("alice", "bob", "carol"), false); !d || len(w) != 0 {
		t.Errorf("competitive: all out → decided %v winners %v, want a decided draw", d, w)
	}
	// Teams: decided once a team is fully out — the other team's members win.
	teams := []lobby.PlayerSummary{{PlayerID: "alice", Team: 0}, {PlayerID: "bob", Team: 0}, {PlayerID: "carol", Team: 1}, {PlayerID: "dave", Team: 1}}
	if d, _, _ := spectatorVerdict(config.ModeTeams, teams, outOf("alice", "carol"), false); d {
		t.Error("teams: one out per team resolved as decided")
	}
	d, w, team = spectatorVerdict(config.ModeTeams, teams, outOf("alice", "bob"), false)
	if !d || team != 1 || !w["carol"] || !w["dave"] || len(w) != 2 {
		t.Errorf("teams: team A out → decided %v team %d winners %v, want team B's members", d, team, w)
	}
	if d, w, team := spectatorVerdict(config.ModeTeams, teams, outOf("alice", "bob", "carol", "dave"), false); !d || team != -1 || len(w) != 0 {
		t.Errorf("teams: both out → decided %v team %d winners %v, want a decided draw", d, team, w)
	}
	// Co-op: the shared game over decides, and the crew shares the board.
	if d, _, _ := spectatorVerdict(config.ModeCooperative, trio, outOf(), false); d {
		t.Error("co-op: resolved as decided before the game over")
	}
	if d, w, team := spectatorVerdict(config.ModeCooperative, trio, outOf(), true); !d || len(w) != 3 || team != -1 {
		t.Errorf("co-op game over → decided %v winners %v team %d, want the whole crew", d, w, team)
	}
}

func TestLiveVerdictAndBanner(t *testing.T) {
	duo := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob"}}
	cases := []struct {
		gmode   config.GameMode
		winners map[string]bool
		team    int
		score   int
		want    string
	}{
		{config.ModeCompetitive, map[string]bool{"alice": true}, -1, 0, "ALICE WINS!"},
		{config.ModeCompetitive, map[string]bool{"alice": true, "bob": true}, -1, 0, "ALICE & BOB WIN!"},
		{config.ModeCompetitive, nil, -1, 0, "DRAW"},
		{config.ModeTeams, nil, 1, 0, "TEAM B WINS!"},
		{config.ModeTeams, nil, -1, 0, "DRAW"},
		{config.ModeCooperative, nil, -1, 5200, "FINAL SCORE 5200"},
	}
	for _, tc := range cases {
		if got := liveVerdict(tc.gmode, duo, tc.winners, tc.team, tc.score); got != tc.want {
			t.Errorf("liveVerdict(%v, %v, %d, %d) = %q, want %q", tc.gmode, tc.winners, tc.team, tc.score, got, tc.want)
		}
	}
	for gmode, want := range map[config.GameMode]string{config.ModeCompetitive: "WINNER", config.ModeTeams: "WINNERS", config.ModeCooperative: "GAME OVER"} {
		if got := liveBanner(gmode); got != want {
			t.Errorf("liveBanner(%v) = %q, want %q", gmode, got, want)
		}
	}
}

// The provisional record ranks like the archive's will: the same headline
// score, bucket (agent seats) and winners.
func TestLiveRecordRanksLikeTheArchive(t *testing.T) {
	now := time.Now()
	roster := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Agent: true}, {PlayerID: "bob", Name: "bob", Team: 1}}
	eng := engine.New(nil, "g1", "spec", "", config.ModeCompetitive, engine.ModeSpectator, 0, 0, 0)
	oc := liveOutcome{decided: true, winners: map[string]bool{"alice": true}, winTeam: -1, scores: map[string]int{"alice": 4200, "bob": 3100}}
	rec := liveRecord(eng, gameView{players: roster}, oc, config.ModeCompetitive, now)
	if rec.GameID != "g1" || rec.HeadlineScore() != 4200 || !rec.HasAgents() || len(rec.Players) != 2 {
		t.Errorf("competitive provisional record = %+v, want g1, headline 4200, agent seats, two players", rec)
	}
	for _, p := range rec.Players {
		if p.Winner != (p.PlayerID == "alice") || p.Score != oc.scores[p.PlayerID] {
			t.Errorf("player %+v: winner/score do not match the outcome", p)
		}
	}
	teamsOC := liveOutcome{decided: true, winTeam: 1, winners: map[string]bool{"bob": true}}
	rec = liveRecord(eng, gameView{players: roster, teamScores: [config.TeamCount]int{3100, 4200}}, teamsOC, config.ModeTeams, now)
	if rec.HeadlineScore() != 4200 || rec.WinningTeam != 1 || len(rec.TeamScores) != config.TeamCount {
		t.Errorf("teams provisional record = %+v, want headline 4200, team B", rec)
	}
	rec = liveRecord(eng, gameView{players: roster, score: 5200}, liveOutcome{decided: true, winTeam: -1}, config.ModeCooperative, now)
	if rec.HeadlineScore() != 5200 || rec.TotalScore != 5200 {
		t.Errorf("co-op provisional record = %+v, want the shared total", rec)
	}
	// The record ranks in its own bucket only, like the archive record will.
	others := []config.ArchiveRecord{
		{GameID: "h1", Mode: config.ModeCompetitive, Players: []config.PlayerResult{{PlayerID: "x", Score: 9000}}},              // all-human: another bucket
		{GameID: "a1", Mode: config.ModeCompetitive, Players: []config.PlayerResult{{PlayerID: "x", Score: 5000, Agent: true}}}, // beats it
		{GameID: "a2", Mode: config.ModeCompetitive, Players: []config.PlayerResult{{PlayerID: "x", Score: 1000, Agent: true}}},
	}
	rec = liveRecord(eng, gameView{players: roster}, oc, config.ModeCompetitive, now)
	if rank, of := config.ReplayRank(others, rec); rank != 2 || of != 3 {
		t.Errorf("provisional rank = %d of %d, want 2 of 3 (the mixed bucket)", rank, of)
	}
}

// The prize piece follows the internet's worst-to-best ladder down the ranks.
func TestPrizePieceLadder(t *testing.T) {
	want := map[int]game.PieceType{1: game.PieceI, 2: game.PieceT, 3: game.PieceL, 4: game.PieceJ, 6: game.PieceJ,
		7: game.PieceO, 10: game.PieceO, 11: game.PieceS, 20: game.PieceS, 21: game.PieceZ, 99: game.PieceZ}
	for rank, pt := range want {
		if got := prizePiece(rank); got != pt {
			t.Errorf("rank %d: piece %v, want %v", rank, got, pt)
		}
	}
	if fx := newWinnerFX(time.Now(), 1, 5, "WINNER"); fx.piece != game.PieceI || fx.tier != tierLegendary {
		t.Errorf("winnerFX for #1 = %+v, want the I piece on a legendary cup", fx)
	}
}

// A spectated co-op game: nothing until the shared game over; then the
// decision is stamped once (the show's clock holds across frames), the crew
// wins, and — with no lobby to rank against — the game stands alone. A
// player's screen never resolves an outcome. The finished screen renders.
func TestResolveOutcomeStampsTheDecision(t *testing.T) {
	a := newTestApp()
	eng := engine.New(nil, "g1", "spec", "", config.ModeCooperative, engine.ModeSpectator, 0, 0, 0)
	roster := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob"}}
	a.eng, a.gamePlayers, a.screen = eng, roster, screenGame
	t0 := time.Now()
	if oc := a.resolveOutcome(eng, a.snapshotGame(t0), engine.ModeSpectator, config.ModeCooperative, t0); oc.decided {
		t.Fatalf("undecided co-op resolved as %+v", oc)
	}
	a.mu.Lock()
	a.gameOver, a.score = true, 5200
	a.mu.Unlock()
	oc := a.resolveOutcome(eng, a.snapshotGame(t0), engine.ModeSpectator, config.ModeCooperative, t0)
	if !oc.decided || !oc.at.Equal(t0) || oc.banner != "GAME OVER" || oc.verdict != "FINAL SCORE 5200" || len(oc.winners) != 2 || oc.rank != 1 || oc.of != 1 {
		t.Fatalf("decided co-op = %+v, want GAME OVER at t0, the crew winning, #1 of 1", oc)
	}
	if later := a.resolveOutcome(eng, a.snapshotGame(t0.Add(5*time.Second)), engine.ModeSpectator, config.ModeCooperative, t0.Add(5*time.Second)); !later.at.Equal(t0) {
		t.Errorf("the show's clock moved: %v, want %v", later.at, t0)
	}
	if oc := a.resolveOutcome(eng, a.snapshotGame(t0), engine.ModePlayer, config.ModeCooperative, t0); oc.decided {
		t.Errorf("a player's screen resolved an outcome: %+v", oc)
	}
	for _, at := range []time.Duration{0, 200 * time.Millisecond, 3 * time.Second, time.Minute} {
		renderAt(t, a, t0.Add(at))
	}
}

// The competitive and teams reveals render — the boards strip, the legend
// and the result box — from a decided outcome (the strip's boards read
// "Loading…" without a stream, which still runs the label and result paths).
func TestSpectatorRevealRenders(t *testing.T) {
	roster := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Team: 0}, {PlayerID: "bob", Name: "bob", Team: 1, Agent: true}}
	now := time.Now()
	for _, gmode := range []config.GameMode{config.ModeCompetitive, config.ModeTeams} {
		for _, oc := range []liveOutcome{
			{decided: true, winners: map[string]bool{"alice": true}, winTeam: 0, scores: map[string]int{"alice": 4200, "bob": 3100}, banner: "WINNER", verdict: "ALICE WINS!", at: now.Add(-3 * time.Second), rank: 1, of: 12},
			{decided: true, winners: map[string]bool{}, winTeam: -1, banner: "WINNER", verdict: "DRAW", at: now, rank: 30, of: 40},
		} {
			a := newTestApp()
			eng := engine.New(nil, "g1", "spec", "", gmode, engine.ModeSpectator, 0, 0, 0)
			a.eng, a.gamePlayers, a.screen = eng, roster, screenGame
			view := a.snapshotGame(now)
			view.outcome = oc
			var ops op.Ops
			gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 820)), Now: now}
			if d := a.gameBoardArea(gtx, eng, view, engine.ModeSpectator, gmode); d.Size.X == 0 {
				t.Errorf("%v board area laid out nothing", gmode)
			}
			if d := a.legend(gtx, eng, view, gmode); d.Size.Y == 0 {
				t.Errorf("%v legend laid out nothing", gmode)
			}
			if d := a.spectatorResultBox(gtx, view, oc, gmode); d.Size.Y == 0 {
				t.Errorf("%v result box laid out nothing", gmode)
			}
		}
	}
}
