package config

import (
	"strings"
	"testing"
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
		{"EventsSubject", EventsSubject(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.events"},
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
