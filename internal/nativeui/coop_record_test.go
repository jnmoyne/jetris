package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// TestCoopScoreIsRecord: the co-op fireworks trigger fires only when the
// finished game's shared score strictly beats the best archived co-op
// TotalScore for the SAME seat count, ignoring other modes and player counts,
// never counting a zero score, and never comparing a game against its own
// (possibly already-arrived) archive record.
func TestCoopScoreIsRecord(t *testing.T) {
	history := []config.ArchiveRecord{
		{GameID: "coop2-a", Mode: config.ModeCooperative, PlayerCount: 2, TotalScore: 1000},
		{GameID: "coop2-b", Mode: config.ModeCooperative, PlayerCount: 2, TotalScore: 1400},
		{GameID: "coop3-a", Mode: config.ModeCooperative, PlayerCount: 3, TotalScore: 5000},
		// Competitive record with a huge per-player score: must be ignored.
		{GameID: "comp2-a", Mode: config.ModeCompetitive, PlayerCount: 2, Players: []config.PlayerResult{{Score: 9999}}},
	}

	cases := []struct {
		name        string
		recs        []config.ArchiveRecord
		score       int
		playerCount int
		gameID      string
		want        bool
	}{
		{"beats best for its seat count", history, 1500, 2, "g-new", true},
		{"equals best is not a record", history, 1400, 2, "g-new", false},
		{"below best", history, 1200, 2, "g-new", false},
		{"other seat count's higher best ignored", history, 1500, 2, "g-new", true},
		{"held to its own seat count's best", history, 1500, 3, "g-new", false},
		{"competitive records ignored", history, 100, 4, "g-new", true},
		{"first ever co-op game is a record", nil, 1, 2, "g-new", true},
		{"zero score is never a record", nil, 0, 2, "g-new", false},
		{"own already-archived record excluded", []config.ArchiveRecord{
			{GameID: "g-new", Mode: config.ModeCooperative, PlayerCount: 2, TotalScore: 1500},
		}, 1500, 2, "g-new", true},
	}
	for _, c := range cases {
		if got := coopScoreIsRecord(c.recs, c.score, c.playerCount, c.gameID); got != c.want {
			t.Errorf("%s: coopScoreIsRecord = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSurvivalTimeIsRecord: a survival game's fireworks fire only when its
// time strictly beats the best archived survival time of the SAME tier and
// seat count — other tiers, seat counts, modes and score runs ignored, a zero
// time never a record, and the game's own record excluded.
func TestSurvivalTimeIsRecord(t *testing.T) {
	t0 := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	rec := func(id string, tier config.Survival, seats int, d time.Duration) config.ArchiveRecord {
		return config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative, Survival: tier, PlayerCount: seats, StartedAt: t0.Add(-d), FinishedAt: t0}
	}
	history := []config.ArchiveRecord{
		rec("n2-a", config.SurvivalNormal, 2, 3*time.Minute),
		rec("n2-b", config.SurvivalNormal, 2, 5*time.Minute),
		rec("h2-a", config.SurvivalHard, 2, 20*time.Minute),
		rec("n3-a", config.SurvivalNormal, 3, 30*time.Minute),
		rec("coop-a", config.SurvivalNone, 2, time.Hour),
		{GameID: "comp", Mode: config.ModeCompetitive, Survival: config.SurvivalNormal, PlayerCount: 2, StartedAt: t0.Add(-time.Hour), FinishedAt: t0},
	}
	for _, c := range []struct {
		name     string
		recs     []config.ArchiveRecord
		survived time.Duration
		tier     config.Survival
		seats    int
		gameID   string
		want     bool
	}{
		{"beats the tier's best for its seat count", history, 6 * time.Minute, config.SurvivalNormal, 2, "new", true},
		{"equals the best is not a record", history, 5 * time.Minute, config.SurvivalNormal, 2, "new", false},
		{"below the best", history, 4 * time.Minute, config.SurvivalNormal, 2, "new", false},
		{"another tier's longer run ignored", history, 6 * time.Minute, config.SurvivalNormal, 2, "new", true},
		{"held to its own seat count", history, 6 * time.Minute, config.SurvivalNormal, 3, "new", false},
		{"score runs and other modes ignored", history, time.Second, config.SurvivalEasy, 2, "new", true},
		{"the first ever run is a record", nil, time.Second, config.SurvivalHard, 1, "new", true},
		{"a zero time never is", nil, 0, config.SurvivalHard, 1, "new", false},
		{"no floor, no record", history, time.Hour, config.SurvivalNone, 2, "new", false},
		{"its own archived record excluded", []config.ArchiveRecord{rec("new", config.SurvivalNormal, 2, 9*time.Minute)}, 9 * time.Minute, config.SurvivalNormal, 2, "new", true},
	} {
		if got := survivalTimeIsRecord(c.recs, c.survived, c.tier, c.seats, c.gameID); got != c.want {
			t.Errorf("%s: survivalTimeIsRecord = %v, want %v", c.name, got, c.want)
		}
	}
	for d, want := range map[time.Duration]string{0: "0:00", 61 * time.Second: "1:01", 3*time.Minute + 42*time.Second: "3:42", 3723 * time.Second: "1:02:03", -time.Second: "0:00"} {
		if got := formatSurvived(d); got != want {
			t.Errorf("formatSurvived(%v) = %q, want %q", d, got, want)
		}
	}
}
