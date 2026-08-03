package nativeui

import (
	"testing"

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
