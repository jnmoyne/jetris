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
		{"GameStream", GameStream(gameID), "JETRICKS_GAME_550e8400-e29b-41d4-a716-446655440000"},
		{"GameSubjectFilter", GameSubjectFilter(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.>"},
		{"CompetitiveRowSubject", CompetitiveRowSubject(gameID, playerID, 12), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.row.12"},
		{"CoopRowSubject", CoopRowSubject(gameID, 12), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.playfield.row.12"},
		{"CompetitiveRowSubjectFilter", CompetitiveRowSubjectFilter(gameID, playerID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.row.>"},
		{"CoopRowSubjectFilter", CoopRowSubjectFilter(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.playfield.row.>"},
		{"MetaSubject", MetaSubject(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.meta"},
		{"RosterSubject", RosterSubject(gameID, playerID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.roster.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"EventsSubject", EventsSubject(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.events"},
		{"CountdownSubject", CountdownSubject(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.countdown"},
		{"ChatSubject", ChatSubject(gameID), "jetricks.game.550e8400-e29b-41d4-a716-446655440000.chat"},
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

func TestGameStreamValidName(t *testing.T) {
	name := GameStream("550e8400-e29b-41d4-a716-446655440000")
	if strings.ContainsAny(name, " .>*") {
		t.Errorf("stream name %q contains invalid characters", name)
	}
}
