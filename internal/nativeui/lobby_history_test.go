package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// The history controls: both sort modes group by agent composition first
// (agents-only, then mixed, then all-human) and rank within each group by the
// selected key; the agent filter drops only agents-only records — any game
// with a human seat stays.
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
	a.histAgentsCb.Value = true

	// Score sort: agents-only first (despite its low score), then the mixed
	// game, then the two human games by score.
	a.histSortEnum.Value = "score"
	got := a.archivesForDisplay(recs())
	if want := []string{"agents-only", "mixed", "human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("score sort = %v, want %v", ids(got), want)
	}

	// Date sort: same grouping, human games by finish time (newest first).
	a.histSortEnum.Value = "date"
	got = a.archivesForDisplay(recs())
	if want := []string{"agents-only", "mixed", "human-low", "human-high"}; !sameIDs(got, want) {
		t.Fatalf("date sort = %v, want %v", ids(got), want)
	}

	// Agent filter: only the agents-only game drops out — the mixed game has a
	// human seat, so it stays (still grouped ahead of the all-human games).
	a.histSortEnum.Value = "score"
	a.histAgentsCb.Value = false
	got = a.archivesForDisplay(recs())
	if want := []string{"mixed", "human-high", "human-low"}; !sameIDs(got, want) {
		t.Fatalf("agent filter = %v, want %v", ids(got), want)
	}
}

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
	if wins != [config.TeamCount]int{2, 1} {
		t.Fatalf("wins = %v, want [2 1]", wins)
	}
	if points != [config.TeamCount]int{170, 155} {
		t.Fatalf("points = %v, want [170 155]", points)
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
