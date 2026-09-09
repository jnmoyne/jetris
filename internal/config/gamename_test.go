package config

import "testing"

// GameName cuts what a creator types down to something that can be all three
// of the things a game ID is at once: a NATS stream name, a subject token and
// a lobby KV key.
func TestGameName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"tournament", "tournament"},
		{"  friday  ", "friday"},
		{"Friday night!", "Friday-night"},
		{"co-op", "co-op"},
		{"a...b", "a-b"},
		{"--lead and trail--", "lead-and-trail"},
		{"under_score", "under_score"},
		{"7 pm", "7-pm"},
		// A subject, a stream name and a KV key take none of these.
		{"a.b", "a-b"},
		{"a>b", "a-b"},
		{"a*b", "a-b"},
		{"a/b", "a-b"},
		{"a\\b", "a-b"},
		{"a\tb", "a-b"},
		// Nothing usable: the game keeps a generated ID rather than failing.
		{"!!!", ""},
		{"パーティ", ""},
		// The cut never leaves a name trailing off in a dash.
		{"123456789012345678901234567890", "123456789012345678901234"},
		{"12345678901234567890123 four", "12345678901234567890123"},
		{"1234567890123456789012 four", "1234567890123456789012-f"},
	}
	for _, tc := range cases {
		if got := GameName(tc.in); got != tc.want {
			t.Errorf("GameName(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := GameName(tc.in); len([]rune(got)) > MaxGameNameLen {
			t.Errorf("GameName(%q) = %q: longer than MaxGameNameLen", tc.in, got)
		}
	}
}

// A name is normalized where every other setting is: the spec the create
// paths hand the lobby (GameSpec.Normalized).
func TestGameSpecNormalizesName(t *testing.T) {
	spec := GameSpec{Name: " Friday night! ", Mode: ModeCooperative, PlayerCount: 2}.Normalized()
	if spec.Name != "Friday-night" {
		t.Errorf("spec.Name = %q, want Friday-night", spec.Name)
	}
}

// "lobby" is the game ID the chat stream and the voice rooms give the lobby's
// own channels, whatever case it is typed in.
func TestGameNameReserved(t *testing.T) {
	for _, name := range []string{"lobby", "Lobby", "LOBBY"} {
		if !GameNameReserved(name) {
			t.Errorf("GameNameReserved(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "lobbys", "the-lobby", "tournament"} {
		if GameNameReserved(name) {
			t.Errorf("GameNameReserved(%q) = true, want false", name)
		}
	}
}

// IsGameName tells a name from the ID an unnamed game is dealt — the
// question every screen that puts a game in front of a player asks.
func TestIsGameName(t *testing.T) {
	generated := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6BA7B810-9DAD-11D1-80B4-00C04FD430C8",
	}
	for _, id := range generated {
		if IsGameName(id) {
			t.Errorf("IsGameName(%q) = true, want false", id)
		}
	}
	named := []string{"tournament", "Friday-night", "550e8400", "550e8400-e29b-41d4-a716-44665544000"}
	for _, id := range named {
		if !IsGameName(id) {
			t.Errorf("IsGameName(%q) = false, want true", id)
		}
	}
	if IsGameName("") {
		t.Error(`IsGameName("") = true, want false`)
	}
}

// A named game's meta says what the game is called; a generated ID is no
// name and reads as none.
func TestGameMetaName(t *testing.T) {
	if got := (GameMeta{GameID: "tournament"}).Name(); got != "tournament" {
		t.Errorf("Name() = %q, want tournament", got)
	}
	if got := (GameMeta{GameID: "550e8400-e29b-41d4-a716-446655440000"}).Name(); got != "" {
		t.Errorf("Name() = %q, want empty", got)
	}
}
