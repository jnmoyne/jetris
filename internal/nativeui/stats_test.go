package nativeui

import (
	"image"
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

// statValue finds a stat line by its label.
func statValue(t *testing.T, lines []statLine, label string) string {
	t.Helper()
	for _, l := range lines {
		if l.label == label {
			return l.value
		}
	}
	t.Fatalf("no %q among %+v", label, lines)
	return ""
}

func statLabels(lines []statLine) string {
	labels := make([]string, 0, len(lines))
	for _, l := range lines {
		labels = append(labels, l.label)
	}
	return strings.Join(labels, " ")
}

// The stat lines: the figures every record has, then the tally — with the
// score and the level only where asked for, the pace from the pieces and
// the time, dashes for what a record does not know, the attack only where
// the game attacks, and no tally at all for a record from before it.
func TestPlayerStatLines(t *testing.T) {
	p := config.PlayerResult{PlayerID: "alice", Score: 4200, Level: 4, Lines: 31, PieceCount: 120,
		Stats: &config.PlayerStats{Singles: 9, Doubles: 5, Triples: 2, Quads: 3, TSpins: 2, MiniTSpins: 1,
			BackToBacks: 2, MaxCombo: 3, PerfectClears: 1, Attack: 21, PlayedMs: 240_000}}
	lines := playerStatLines(p, config.ModeCompetitive, true, true)
	want := "SCORE LEVEL LINES PIECES TIME PPS SINGLES DOUBLES TRIPLES QUADS T-SPINS MINI T-SPINS BACK-TO-BACK MAX COMBO PERFECT CLEARS GARBAGE SENT"
	if got := statLabels(lines); got != want {
		t.Errorf("labels = %q\nwant     %q", got, want)
	}
	for label, val := range map[string]string{
		"SCORE": "4200", "LEVEL": "4", "LINES": "31", "PIECES": "120", "TIME": "4:00", "PPS": "0.50",
		"SINGLES": "9", "DOUBLES": "5", "TRIPLES": "2", "QUADS": "3", "T-SPINS": "2", "MINI T-SPINS": "1",
		"BACK-TO-BACK": "2", "MAX COMBO": "3", "PERFECT CLEARS": "1", "GARBAGE SENT": "21",
	} {
		if got := statValue(t, lines, label); got != val {
			t.Errorf("%s = %q, want %q", label, got, val)
		}
	}

	// The game-over box has its own score line: no SCORE, no LEVEL.
	if got := statLabels(playerStatLines(p, config.ModeCompetitive, false, false)); strings.Contains(got, "SCORE") || strings.Contains(got, "LEVEL") {
		t.Errorf("without basics the lines still carry %q", got)
	}
	// The crew's board attacks nobody.
	if got := statLabels(playerStatLines(p, config.ModeCooperative, true, true)); strings.Contains(got, "GARBAGE") {
		t.Errorf("a co-op tally lists an attack: %q", got)
	}
	// A record from before the tally: the figures alone, the unknowns dashed.
	old := config.PlayerResult{PlayerID: "bob", Score: 3100, Level: 3}
	lines = playerStatLines(old, config.ModeCompetitive, true, true)
	if got := statLabels(lines); got != "SCORE LEVEL LINES PIECES TIME PPS" {
		t.Errorf("an old record's labels = %q, want the figures alone", got)
	}
	for _, label := range []string{"PIECES", "TIME", "PPS"} {
		if got := statValue(t, lines, label); got != statUnknown {
			t.Errorf("an old record's %s = %q, want %q", label, got, statUnknown)
		}
	}
	// Pieces known but no time: a pace nobody can compute.
	known := config.PlayerResult{PieceCount: 40, Stats: &config.PlayerStats{}}
	if got := statValue(t, playerStatLines(known, config.ModeCompetitive, false, false), "PPS"); got != statUnknown {
		t.Errorf("PPS without a time = %q, want %q", got, statUnknown)
	}
}

// The local player's own result for the game-over box is read live off the
// engine: their own totals, the tally their echoed events built, the pieces
// they drew.
func TestOwnResult(t *testing.T) {
	eng := engine.New(nil, "g", "me", "", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	eng.SetOwnTotalsForTest(950, 6)
	eng.HandleGameEventForTest(engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "me", LinesCleared: 4, Score: 800, TotalScore: 800, TotalLines: 4, PieceCount: 20})
	eng.HandleGameEventForTest(engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "me", LinesCleared: 2, BackToBack: false, Combo: 1, Score: 150, TotalScore: 950, TotalLines: 6, PieceCount: 21})
	p := ownResult(eng)
	if p.PlayerID != "me" || p.Score != 950 || p.Lines != 6 || p.PieceCount != 21 {
		t.Errorf("own result = %+v, want me / 950 / 6 lines / 21 pieces", p)
	}
	if p.Stats == nil || p.Stats.Quads != 1 || p.Stats.Doubles != 1 || p.Stats.MaxCombo != 1 || p.Stats.Attack != 6 {
		t.Errorf("own tally = %+v, want the quad and the double (combo 1, 6 rows sent)", p.Stats)
	}
}

// The replay's builder folds every seat's tally off the recording's events
// the way the engines did live: a line_clear that moves the sender's totals
// counts, a replay of one does not, a game_over marks where the seat's time
// ends and carries its pieces, the attack follows the recorded meta's rule,
// and a seat that never announced anything has no tally.
func TestReplayTallyFromEvents(t *testing.T) {
	const id = "g-tally"
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	evSubj := func(kind engine.EventKind, pid string) string {
		return config.ReplayCopySubject(id, config.EventKindSubject(id, string(kind), pid))
	}
	rec := config.ArchiveRecord{GameID: id, Mode: config.ModeCompetitive, PlayerCount: 3, Version: 3,
		Players: []config.PlayerResult{{PlayerID: "alice", Score: 1300}, {PlayerID: "bob", Score: 40}, {PlayerID: "carol"}}}
	f := newReplayFeed(rec, t0)
	f.at(-time.Minute, replayMetaSubject(id), config.GameMeta{GameID: id, Status: config.GameStatusCreated, GuidelineGarbage: true})
	f.at(0, replayCountdownSubject(id), map[string]int{"seconds": 0})
	f.at(2*time.Second, replayMetaSubject(id), config.GameMeta{GameID: id, Status: config.GameStatusInProgress, GuidelineGarbage: true})
	quad := engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "alice", LinesCleared: 4, Score: 800, TotalScore: 800, TotalLines: 4, PieceCount: 12}
	f.at(10*time.Second, evSubj(engine.EventLineClear, "alice"), quad)
	f.at(10*time.Second+time.Millisecond, evSubj(engine.EventLineClear, "alice"), quad) // read twice: counted once
	f.at(20*time.Second, evSubj(engine.EventLineClear, "alice"),
		engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "alice", LinesCleared: 2, TSpin: 2, BackToBack: true, Combo: 1, Score: 500, TotalScore: 1300, TotalLines: 6, PieceCount: 15})
	f.at(25*time.Second, evSubj(engine.EventLineClear, "bob"),
		engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "bob", Score: 40, TotalScore: 40, PieceCount: 9})
	f.at(32*time.Second, evSubj(engine.EventGameOver, "alice"),
		engine.GameEvent{Kind: engine.EventGameOver, PlayerID: "alice", Score: 1300, TotalScore: 1300, TotalLines: 6, PieceCount: 16})
	f.at(40*time.Second, config.ReplayCopySubject(id, config.CompetitiveCellSubject(id, "bob", 6, 2)), lockedCell())
	tl := f.b.finish()

	alice := tl.stats["alice"]
	if alice == nil {
		t.Fatal("no tally for alice")
	}
	// The quad once (4 rows), the Back-to-Back T-spin double (4+2).
	want := config.PlayerStats{Quads: 1, Doubles: 1, TSpins: 1, BackToBacks: 1, MaxCombo: 1, Attack: 10, Pieces: 16, PlayedMs: 30_000}
	if *alice != want {
		t.Errorf("alice's tally = %+v\nwant          %+v", *alice, want)
	}
	// Bob scored a drop's points and never went out: he played to the end.
	if bob := tl.stats["bob"]; bob == nil || bob.Pieces != 9 || bob.PlayedMs != 38_000 || bob.Singles != 0 {
		t.Errorf("bob's tally = %+v, want 9 pieces, 38s played, no clears", bob)
	}
	if _, ok := tl.stats["carol"]; ok {
		t.Error("carol, who announced nothing, has a tally")
	}

	// The ending's roster: an older record takes the fold's tally and piece
	// counts; a record that carries its own keeps it.
	rv := newReplayView(rec)
	rv.tl = tl
	results := replayResults(rv)
	byID := map[string]config.PlayerResult{}
	for _, p := range results {
		byID[p.PlayerID] = p
	}
	if a := byID["alice"]; a.Stats == nil || a.Stats.Quads != 1 || a.PieceCount != 16 {
		t.Errorf("alice's replay result = %+v, want the folded tally and 16 pieces", a)
	}
	if c := byID["carol"]; c.Stats != nil {
		t.Errorf("carol's replay result carries a tally: %+v", c.Stats)
	}
	own := rec
	own.Version = config.ArchiveRecordVersion
	own.Players[0].Stats = &config.PlayerStats{Singles: 99}
	rv = newReplayView(own)
	rv.tl = tl
	if a := replayResults(rv)[0]; a.Stats == nil || a.Stats.Singles != 99 {
		t.Errorf("a record with its own tally was overridden: %+v", a.Stats)
	}
}

// Every screen that shows the stats lays out, wide and phone-sized: the
// history's View board in every mode, the ending of a replay (the roster
// beside the boards, or under them where the screen is too narrow), and
// the game-over box with the local player's own tally.
func TestStatsScreensRender(t *testing.T) {
	sizes := []image.Point{{1200, 820}, {390, 800}}
	render := func(t *testing.T, a *App, sz image.Point, now time.Time) {
		t.Helper()
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(sz), Now: now}
		if d := a.layout(gtx); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("layout at %v produced zero dimensions", sz)
		}
	}
	recs := []config.ArchiveRecord{sampleReplayRecord(), sampleTeamsReplayRecord(), sampleCoopReplayRecord()}
	// And a record from before the tally, in every mode.
	for _, rec := range recs[:] {
		old := rec
		old.Version = 3
		for i := range old.Players {
			old.Players[i].Stats = nil
		}
		recs = append(recs, old)
	}
	for _, rec := range recs {
		for _, sz := range sizes {
			a := newTestApp()
			a.openArchive(rec)
			render(t, a, sz, time.Now())

			a = newTestApp()
			rv := loadedReplay(rec)
			rv.head = rv.tl.dur
			rv.done, rv.doneAt = true, time.Now()
			a.replayView, a.screen = rv, screenReplay
			render(t, a, sz, rv.doneAt.Add(2*time.Second))
			// Scrubbed back into the game: the roster packs away again.
			rv.head = rv.tl.dur / 2
			render(t, a, sz, rv.doneAt.Add(3*time.Second))
		}
	}
	for _, gmode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive, config.ModeTeams} {
		for _, sz := range sizes {
			a := finishedGameApp(engine.ModePlayer, gmode, config.GameStatusFinished)
			a.eng.SetOwnTotalsForTest(1200, 7)
			a.eng.HandleGameEventForTest(engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "alice", LinesCleared: 3, Score: 500, TotalScore: 500, TotalLines: 3, PieceCount: 30})
			a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob", Team: 1}}
			render(t, a, sz, time.Now())
		}
	}
}
