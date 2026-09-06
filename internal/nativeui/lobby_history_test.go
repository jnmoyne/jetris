package nativeui

import (
	"slices"
	"testing"
	"time"

	"jetris/internal/config"
)

// The history controls: the score sort groups by agent composition first
// (agents-only, then mixed, then all-human) and ranks within each group; the
// date sort is strictly chronological, newest first, regardless of crew; the
// three crew filter boxes each list or hide exactly their own composition.
func TestArchivesForDisplay(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	recs := func() []config.ArchiveRecord {
		return []config.ArchiveRecord{
			// Two all-human games (class: humans-only).
			{GameID: "human-high", Mode: config.ModeCompetitive, FinishedAt: t0,
				Players: []config.PlayerResult{{PlayerID: "alice", Score: 30}, {PlayerID: "bob"}}},
			{GameID: "human-low", Mode: config.ModeCompetitive, FinishedAt: t0.Add(time.Hour),
				Players: []config.PlayerResult{{PlayerID: "carol", Score: 5}, {PlayerID: "dan"}}},
			// One mixed human/agent game (class: mixed).
			{GameID: "mixed", Mode: config.ModeCompetitive, FinishedAt: t0.Add(30 * time.Minute),
				Players: []config.PlayerResult{{PlayerID: "eve", Score: 10}, {PlayerID: "pixel-3f-hard", Agent: true}}},
			// One agent-vs-agent game (class: agents-only) — the lowest score
			// of all, yet it must still sort to the very top of its group.
			{GameID: "agents-only", Mode: config.ModeCompetitive, FinishedAt: t0.Add(2 * time.Hour),
				Players: []config.PlayerResult{{PlayerID: "botA", Score: 3, Agent: true}, {PlayerID: "botB", Score: 1, Agent: true}}},
		}
	}

	a := newTestApp()
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = true, true, true

	// Score sort: agents-only first (despite its low score), then the mixed
	// game, then the two human games by score.
	a.histSortEnum.Value = "score"
	got := a.archivesForDisplay(recs(), nonePinned)
	if want := []string{"agents-only", "mixed", "human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("score sort = %v, want %v", ids(got), want)
	}

	// Date sort: strictly newest first — the agent game (t0+2h), then the
	// human game from t0+1h, the mixed game from t0+30m, the human game from
	// t0 — no crew grouping.
	a.histSortEnum.Value = "date"
	got = a.archivesForDisplay(recs(), nonePinned)
	if want := []string{"agents-only", "human-low", "mixed", "human-high"}; !sameIDs(got, want) {
		t.Fatalf("date sort = %v, want %v", ids(got), want)
	}

	// Crew filters: each box governs exactly its own composition — unchecking
	// "Agents only" keeps the mixed game (it has a human seat), and any single
	// box alone lists just that class; all off lists nothing.
	a.histSortEnum.Value = "score"
	a.histAgentsOnlyCb.Value = false
	got = a.archivesForDisplay(recs(), nonePinned)
	if want := []string{"mixed", "human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("without agents-only = %v, want %v", ids(got), want)
	}
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = true, false, false
	if got, want := a.archivesForDisplay(recs(), nonePinned), []string{"human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("players only = %v, want %v", ids(got), want)
	}
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = false, true, false
	if got, want := a.archivesForDisplay(recs(), nonePinned), []string{"mixed"}; !sameIDs(got, want) {
		t.Fatalf("agents and players = %v, want %v", ids(got), want)
	}
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = false, false, true
	if got, want := a.archivesForDisplay(recs(), nonePinned), []string{"agents-only"}; !sameIDs(got, want) {
		t.Fatalf("agents only = %v, want %v", ids(got), want)
	}
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = false, false, false
	if got := a.archivesForDisplay(recs(), nonePinned); len(got) != 0 {
		t.Fatalf("all filters off = %v, want nothing", ids(got))
	}

	// Pinned only narrows whatever the crew boxes list to the pinned
	// replays, in the same order; off, pins change nothing.
	a.histHumansCb.Value, a.histMixedCb.Value, a.histAgentsOnlyCb.Value = true, true, true
	pinned := func(id string) bool { return id == "human-low" || id == "agents-only" }
	a.histPinnedCb.Value = true
	if got, want := a.archivesForDisplay(recs(), pinned), []string{"agents-only", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("pinned only = %v, want %v", ids(got), want)
	}
	a.histAgentsOnlyCb.Value = false
	if got, want := a.archivesForDisplay(recs(), pinned), []string{"human-low"}; !sameIDs(got, want) {
		t.Fatalf("pinned only, without agents-only = %v, want %v", ids(got), want)
	}
	if got := a.archivesForDisplay(recs(), nonePinned); len(got) != 0 {
		t.Fatalf("pinned only with nothing pinned = %v, want nothing", ids(got))
	}
	a.histPinnedCb.Value = false
	if got, want := a.archivesForDisplay(recs(), pinned), []string{"mixed", "human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("pinned only off = %v, want the crew boxes' list", ids(got))
	}
}

// nonePinned is a history with no pinned replay.
func nonePinned(string) bool { return false }

// TestTeamStandings pins the all-time teams scoreboard fold: only teams games
// count, draws credit neither side, and points sum each team's final scores.
func TestTeamStandings(t *testing.T) {
	recs := []config.ArchiveRecord{
		{Mode: config.ModeTeams, WinningTeam: 0, TeamScores: []int{100, 40}},
		{Mode: config.ModeTeams, WinningTeam: 1, TeamScores: []int{10, 90}},
		{Mode: config.ModeTeams, WinningTeam: 0, TeamScores: []int{55, 20}},
		{Mode: config.ModeTeams, WinningTeam: -1, TeamScores: []int{5, 5}}, // draw
		{Mode: config.ModeCompetitive, WinningTeam: 0,
			Players: []config.PlayerResult{{PlayerID: "x", Score: 999}}}, // not a teams game
	}
	wins, points, games := teamStandings(recs)
	if games != 4 {
		t.Fatalf("games = %d, want 4", games)
	}
	if !slices.Equal(wins, []int{2, 1}) {
		t.Fatalf("wins = %v, want [2 1]", wins)
	}
	if !slices.Equal(points, []int{170, 155}) {
		t.Fatalf("points = %v, want [170 155]", points)
	}

	// A three-way game widens the board: TEAM C gets a column of its own,
	// which the two-team games above simply never scored in.
	wide := append(append([]config.ArchiveRecord(nil), recs...),
		config.ArchiveRecord{Mode: config.ModeTeams, TeamCount: 3, WinningTeam: 2, TeamScores: []int{1, 2, 300}})
	wins, points, games = teamStandings(wide)
	if games != 5 {
		t.Fatalf("games = %d, want 5", games)
	}
	if !slices.Equal(wins, []int{2, 1, 1}) {
		t.Fatalf("wins = %v, want [2 1 1]", wins)
	}
	if !slices.Equal(points, []int{171, 157, 300}) {
		t.Fatalf("points = %v, want [171 157 300]", points)
	}
	if d := newTestApp().teamStandingsLine(testCtx(1200, 60), wide); d.Size.X == 0 {
		t.Fatal("standings line should render a three-team history")
	}

	// The rendered line appears only when a teams game exists.
	a := newTestApp()
	if d := a.teamStandingsLine(testCtx(1200, 60), recs); d.Size.X == 0 {
		t.Fatal("standings line should render when teams games exist")
	}
	if d := a.teamStandingsLine(testCtx(1200, 60), recs[4:]); d.Size.X != 0 {
		t.Fatal("standings line should be empty without teams games")
	}
}

func ids(recs []config.ArchiveRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.GameID
	}
	return out
}

// sameIDs reports whether recs' GameIDs equal want in order.
func sameIDs(recs []config.ArchiveRecord, want []string) bool {
	got := ids(recs)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
