package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// The history controls: the agent filter drops any record with an agent seat,
// and the sort selector switches between headline-score and most-recent-first
// ordering.
func TestArchivesForDisplay(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	recs := func() []config.ArchiveRecord {
		return []config.ArchiveRecord{
			{GameID: "old-high", Mode: config.ModeCompetitive, FinishedAt: t0,
				Players: []config.PlayerResult{{PlayerID: "alice", Score: 30}, {PlayerID: "bob"}}},
			{GameID: "new-low", Mode: config.ModeCompetitive, FinishedAt: t0.Add(time.Hour),
				Players: []config.PlayerResult{{PlayerID: "carol", Score: 5}, {PlayerID: "dan"}}},
			{GameID: "agent-mid", Mode: config.ModeCompetitive, FinishedAt: t0.Add(30 * time.Minute),
				Players: []config.PlayerResult{{PlayerID: "eve", Score: 10}, {PlayerID: "pixel-3f-hard", Agent: true}}},
		}
	}

	a := newTestApp()
	a.histAgentsCb.Value = true
	a.histSortEnum.Value = "score"
	got := a.archivesForDisplay(recs())
	if len(got) != 3 || got[0].GameID != "old-high" || got[1].GameID != "agent-mid" || got[2].GameID != "new-low" {
		t.Fatalf("score sort = %v", ids(got))
	}

	a.histSortEnum.Value = "date"
	got = a.archivesForDisplay(recs())
	if len(got) != 3 || got[0].GameID != "new-low" || got[1].GameID != "agent-mid" || got[2].GameID != "old-high" {
		t.Fatalf("date sort = %v", ids(got))
	}

	a.histAgentsCb.Value = false
	got = a.archivesForDisplay(recs())
	if len(got) != 2 || got[0].GameID != "new-low" || got[1].GameID != "old-high" {
		t.Fatalf("agent filter = %v", ids(got))
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
