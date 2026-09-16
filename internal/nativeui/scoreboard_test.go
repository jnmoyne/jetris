package nativeui

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// frameScores is the folded scoreboard with the local player's own row read
// live off their engine (their own totals reach the folded map a round trip
// later, with their echo) — and no row of a spectator's own.
func TestFrameScoresOwnRow(t *testing.T) {
	player := engine.New(nil, "g", "me", "", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	player.SetOwnTotalsForTest(420, 4)
	scores, lines, me := frameScores(player)
	if me != "me" {
		t.Errorf("me = %q, want the player", me)
	}
	if scores["me"] != 420 || lines["me"] != 4 {
		t.Errorf("own row = %d/%d, want the engine's live 420/4", scores["me"], lines["me"])
	}
	spec := engine.New(nil, "g", "spec", "", config.ModeCompetitive, engine.ModeSpectator, 0, 0, 0)
	scores, _, me = frameScores(spec)
	if me != "" {
		t.Errorf("a spectator's me = %q, want none", me)
	}
	if _, ok := scores["spec"]; ok {
		t.Error("a spectator got a row of their own")
	}
}

// The scoreboard renders on every screen that lists it: a competitive
// player's legend and game-over box (everyone ranked, the ranking line
// under "Your score"), a competitive spectator's HUD (no SCORE of their own),
// legend and boards (a score beside each name), a crew's legend (each
// member's share) and game-over box, and a teams spectator's team boards.
func TestScoreboardRenders(t *testing.T) {
	now := time.Now()
	roster := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob", Team: 1, Agent: true}}
	scores := map[string]int{"alice": 4200, "bob": 3100}
	lines := map[string]int{"alice": 12, "bob": 9}
	gtxFor := func() layout.Context {
		var ops op.Ops
		return layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 820)), Now: now}
	}
	for _, tc := range []struct {
		name  string
		gmode config.GameMode
		mode  engine.Mode
	}{
		{"competitive player", config.ModeCompetitive, engine.ModePlayer},
		{"competitive spectator", config.ModeCompetitive, engine.ModeSpectator},
		{"co-op player", config.ModeCooperative, engine.ModePlayer},
		{"co-op spectator", config.ModeCooperative, engine.ModeSpectator},
		{"teams spectator", config.ModeTeams, engine.ModeSpectator},
	} {
		a := newTestApp()
		eng := engine.New(nil, "g1", "alice", "", tc.gmode, tc.mode, 0, 0, 0)
		a.eng, a.gamePlayers, a.screen = eng, roster, screenGame
		view := a.snapshotGame(now)
		view.playerScores, view.playerLines = scores, lines
		if tc.mode == engine.ModePlayer {
			view.me = "alice"
		}
		view.perSeat = perSeatScored(tc.gmode, false)
		view.teamScores, view.teamLevels = []int{4200, 3100}, []int{2, 1}
		view.status = string(config.GameStatusInProgress)
		if d := a.legend(gtxFor(), eng, view, tc.gmode); d.Size.Y == 0 {
			t.Errorf("%s: legend laid out nothing", tc.name)
		}
		if d := a.gameHUD(gtxFor(), eng, view, tc.mode, tc.gmode, 0); d.Size.Y == 0 {
			t.Errorf("%s: HUD laid out nothing", tc.name)
		}
		if d := a.barStats(gtxFor(), view, tc.mode, tc.gmode); d.Size.X == 0 {
			t.Errorf("%s: bar stats laid out nothing", tc.name)
		}
		if tc.mode == engine.ModeSpectator {
			if d := a.gameBoardArea(gtxFor(), eng, view, tc.mode, tc.gmode); d.Size.X == 0 {
				t.Errorf("%s: board area laid out nothing", tc.name)
			}
			continue
		}
		view.gameOver = true
		if d := a.gameOverBox(gtxFor(), eng, tc.gmode, view); d.Size.Y == 0 {
			t.Errorf("%s: game-over box laid out nothing", tc.name)
		}
	}
	// The competitive game-over ranking, best first, from the frame's scoreboard.
	if got := rankingLine(roster, scores); got != "alice 4200 · bob [agent] 3100" {
		t.Errorf("rankingLine = %q", got)
	}
}

// The history's PLAYERS column for the crew's shared run: each member's
// share of the score, best first, once the record keeps it (version 3), the
// names alone before; the crew's board scored per seat ranks its seats
// winner first, the way a competitive game's row does.
func TestCoopRosterLinesShares(t *testing.T) {
	rec := config.ArchiveRecord{Version: 3, Mode: config.ModeCooperative, TotalScore: 2140,
		Players: []config.PlayerResult{{PlayerID: "bob", Score: 940}, {PlayerID: "alice", Score: 1200, Agent: true}}}
	if got := archiveRosterLines(rec); len(got) != 1 || got[0].text != "alice [agent] 1200 · bob 940" {
		t.Errorf("v3 co-op roster = %+v, want the shares best first", got)
	}
	old := rec
	old.Version = 2
	if got := archiveRosterLines(old); len(got) != 1 || got[0].text != "alice [agent], bob" {
		t.Errorf("v2 co-op roster = %+v, want the names alone", got)
	}
	individual := old
	individual.Scoring = config.ScoringIndividual
	individual.Players = []config.PlayerResult{{PlayerID: "bob", Score: 940}, {PlayerID: "alice", Score: 1200, Winner: true}}
	got := archiveRosterLines(individual)
	if len(got) != 2 || !strings.HasPrefix(got[0].text, winnerMark) || !strings.Contains(got[0].text, "alice 1200") || !strings.Contains(got[1].text, "bob 940") {
		t.Errorf("individual roster = %+v, want the winner first with their score, then the rest", got)
	}
}

// The replay's per-board scoreboard lines at the playhead — and none at all
// for a recording that carries no totals.
func TestReplayBoardSubs(t *testing.T) {
	players := []config.PlayerResult{{PlayerID: "alice"}, {PlayerID: "bob", Team: 1, Agent: true}}
	marks := map[string]scoreMark{"alice": {score: 1200, lines: 14}, "bob": {score: 940, lines: 9}}
	for _, tc := range []struct {
		rec  config.ArchiveRecord
		want []string
	}{
		{config.ArchiveRecord{Mode: config.ModeCompetitive, PlayerCount: 2, Players: players}, []string{"1200 · lvl 2", "940 · lvl 1"}},
		{config.ArchiveRecord{Mode: config.ModeCooperative, PlayerCount: 2, Players: players}, []string{"2140 · lvl 3 — alice 1200 · bob [agent] 940"}},
		{config.ArchiveRecord{Mode: config.ModeCooperative, PlayerCount: 2, Scoring: config.ScoringIndividual, Players: players}, []string{"alice 1200 · bob [agent] 940 · lvl 3"}},
		{config.ArchiveRecord{Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, Players: players}, []string{"1200 · lvl 2 — alice 1200", "940 · lvl 1 — bob [agent] 940"}},
	} {
		rv := newReplayView(tc.rec)
		rv.tl = &replayTimeline{scores: []scoreMark{{player: "alice"}}} // the recording carries totals
		rv.scores = marks
		if got := rv.boardSubs(); !slices.Equal(got, tc.want) {
			t.Errorf("%v: subs = %q, want %q", tc.rec.Mode, got, tc.want)
		}
	}
	rv := newReplayView(config.ArchiveRecord{Mode: config.ModeCompetitive, PlayerCount: 2, Players: players})
	rv.tl = &replayTimeline{}
	for _, s := range rv.boardSubs() {
		if s != "" {
			t.Errorf("a recording with no totals got a scoreboard line %q", s)
		}
	}
}
