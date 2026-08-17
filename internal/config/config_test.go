package config

import (
	"strings"
	"testing"
	"time"
)

func TestSubjectBuilders(t *testing.T) {
	gameID := "550e8400-e29b-41d4-a716-446655440000"
	playerID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	tests := []struct {
		name   string
		got    string
		expect string
	}{
		{"GameStream", GameStream(gameID), "JETRIS_GAME_550e8400-e29b-41d4-a716-446655440000"},
		{"GameSubjectFilter", GameSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.>"},
		{"CompetitiveCellSubject", CompetitiveCellSubject(gameID, playerID, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.cell.12.7"},
		{"CoopCellSubject", CoopCellSubject(gameID, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.playfield.cell.12.7"},
		{"CompetitiveCellSubjectFilter", CompetitiveCellSubjectFilter(gameID, playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.cell.>"},
		{"CoopCellSubjectFilter", CoopCellSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.playfield.cell.>"},
		{"TeamCellSubject", TeamCellSubject(gameID, 1, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.team.1.playfield.cell.12.7"},
		{"TeamCellSubjectFilter", TeamCellSubjectFilter(gameID, 0), "jetris.game.550e8400-e29b-41d4-a716-446655440000.team.0.playfield.cell.>"},
		{"MetaSubject", MetaSubject(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.meta"},
		{"RosterSubject", RosterSubject(gameID, playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.roster.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"EventKindSubject", EventKindSubject(gameID, "line_clear", playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.events.line_clear.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"EventsSubjectFilter", EventsSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.events.>"},
		{"CountdownSubject", CountdownSubject(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.countdown"},
		{"GameChatSubject", GameChatSubject(gameID), "jetris.chat.550e8400-e29b-41d4-a716-446655440000"},
		{"LobbyChatSubject", LobbyChatSubject, "jetris.chat.lobby"},
		{"LobbyPlayerKey", LobbyPlayerKey(playerID), "players.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"LobbyGameKey", LobbyGameKey(gameID), "games.550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expect {
				t.Errorf("got %q, want %q", tt.got, tt.expect)
			}
		})
	}
}

func TestTeamDimensions(t *testing.T) {
	for _, teamSize := range []int{1, 2, 3} {
		if got, want := TeamBoardWidth(teamSize), teamSize*StandardWidth; got != want {
			t.Errorf("TeamBoardWidth(%d) = %d, want %d", teamSize, got, want)
		}
		if got, want := TeamVisibleRows(teamSize), VisibleRows+teamSize; got != want {
			t.Errorf("TeamVisibleRows(%d) = %d, want %d", teamSize, got, want)
		}
		if got, want := TeamTotalRows(teamSize), HeadroomRows+VisibleRows+teamSize; got != want {
			t.Errorf("TeamTotalRows(%d) = %d, want %d", teamSize, got, want)
		}
		if got := TeamVisibleRowStart(teamSize); got != HeadroomRows {
			t.Errorf("TeamVisibleRowStart(%d) = %d, want %d", teamSize, got, HeadroomRows)
		}
	}
}

func TestModeTeamsString(t *testing.T) {
	if got := ModeTeams.String(); got != "teams" {
		t.Errorf("ModeTeams.String() = %q, want %q", got, "teams")
	}
}

func TestGameStreamValidName(t *testing.T) {
	name := GameStream("550e8400-e29b-41d4-a716-446655440000")
	if strings.ContainsAny(name, " .>*") {
		t.Errorf("stream name %q contains invalid characters", name)
	}
}

func TestGameIDFromChatSubject(t *testing.T) {
	if got := GameIDFromChatSubject(LobbyChatSubject); got != "" {
		t.Errorf("lobby subject: got %q, want empty", got)
	}
	if got := GameIDFromChatSubject(GameChatSubject("g-42")); got != "g-42" {
		t.Errorf("game subject: got %q, want g-42", got)
	}
	if got := GameIDFromChatSubject("jetris.chat."); got != "" {
		t.Errorf("empty game token: got %q, want empty", got)
	}
}

// TestArchiveRecordRanking pins the shared "By score" ordering (HeadlineScore
// per mode, then RankBefore's tie-breaks) that both the history list and the
// replay archiver's top-N cut rely on.
func TestArchiveRecordRanking(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// HeadlineScore: shared total (coop), best team (teams), best player (competitive).
	coop := ArchiveRecord{Mode: ModeCooperative, TotalScore: 40,
		Players: []PlayerResult{{PlayerID: "a", Score: 99}}}
	if got := coop.HeadlineScore(); got != 40 {
		t.Errorf("coop headline = %d, want 40", got)
	}
	teams := ArchiveRecord{Mode: ModeTeams, TeamScores: []int{10, 30}}
	if got := teams.HeadlineScore(); got != 30 {
		t.Errorf("teams headline = %d, want 30", got)
	}
	comp := ArchiveRecord{Mode: ModeCompetitive,
		Players: []PlayerResult{{PlayerID: "a", Score: 7}, {PlayerID: "b", Score: 12}}}
	if got := comp.HeadlineScore(); got != 12 {
		t.Errorf("competitive headline = %d, want 12", got)
	}

	// RankBefore: score desc, then shorter duration, then newer finish, then
	// game ID — a total order, so every client computes the identical top N.
	mk := func(id string, score int, dur time.Duration, fin time.Time) ArchiveRecord {
		return ArchiveRecord{GameID: id, Mode: ModeCooperative, TotalScore: score,
			StartedAt: fin.Add(-dur), FinishedAt: fin}
	}
	hi, lo := mk("a", 20, time.Minute, t0), mk("b", 10, time.Minute, t0)
	if !hi.RankBefore(lo) || lo.RankBefore(hi) {
		t.Error("higher score must rank first")
	}
	fast, slow := mk("a", 10, time.Minute, t0), mk("b", 10, 2*time.Minute, t0)
	if !fast.RankBefore(slow) || slow.RankBefore(fast) {
		t.Error("shorter game must break a score tie")
	}
	newer, older := mk("a", 10, time.Minute, t0.Add(time.Hour)), mk("b", 10, time.Minute, t0)
	if !newer.RankBefore(older) || older.RankBefore(newer) {
		t.Error("newer finish must break a duration tie")
	}
	x, y := mk("a", 10, time.Minute, t0), mk("b", 10, time.Minute, t0)
	if !x.RankBefore(y) || y.RankBefore(x) {
		t.Error("game ID must totally order full ties")
	}

	// SameReplayBucket: mode and agent presence both have to match.
	human := ArchiveRecord{Mode: ModeCooperative, Players: []PlayerResult{{PlayerID: "a"}}}
	agent := ArchiveRecord{Mode: ModeCooperative, Players: []PlayerResult{{PlayerID: "a", Agent: true}}}
	otherMode := ArchiveRecord{Mode: ModeTeams, Players: []PlayerResult{{PlayerID: "a"}}}
	if !human.SameReplayBucket(human) {
		t.Error("same mode + same crew should share a bucket")
	}
	if human.SameReplayBucket(agent) || human.SameReplayBucket(otherMode) {
		t.Error("agent presence and mode must both split buckets")
	}
}
