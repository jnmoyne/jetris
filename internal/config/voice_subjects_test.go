package config

import "testing"

// The voice subjects live under jetris.voice, outside every stream's filter,
// and parse back to the sender and the room they were built for.
func TestVoiceSubjects(t *testing.T) {
	g := "550e8400-e29b-41d4-a716-446655440000"
	cases := []struct {
		subject string
		player  string
		team    int
		ok      bool
	}{
		{VoiceSubject(g, "alice"), "alice", -1, true},
		{VoiceTeamSubject(g, 2, "bob"), "bob", 2, true},
		{"jetris.voice." + g + ".all.", "", 0, false},
		{"jetris.voice." + g + ".team.x.bob", "", 0, false},
		{"jetris.voice." + g + ".team.-1.bob", "", 0, false},
		{"jetris.voice." + g + ".team.1", "", 0, false},
		{"jetris.flash." + g + ".alice", "", 0, false},
		{"jetris.voice." + g + ".hall.alice", "", 0, false},
	}
	for _, c := range cases {
		p, team, ok := ParseVoiceSubject(c.subject)
		if p != c.player || team != c.team || ok != c.ok {
			t.Errorf("ParseVoiceSubject(%q) = %q, %d, %v; want %q, %d, %v", c.subject, p, team, ok, c.player, c.team, c.ok)
		}
	}
	if got, want := VoiceSubject(g, "alice"), "jetris.voice."+g+".all.alice"; got != want {
		t.Fatalf("VoiceSubject = %q, want %q", got, want)
	}
	if got, want := VoiceTeamSubject(g, 0, "alice"), "jetris.voice."+g+".team.0.alice"; got != want {
		t.Fatalf("VoiceTeamSubject = %q, want %q", got, want)
	}
	if got, want := VoiceSubjectFilter(g), "jetris.voice."+g+".all.*"; got != want {
		t.Fatalf("VoiceSubjectFilter = %q, want %q", got, want)
	}
	if got, want := VoiceTeamSubjectFilter(g, 3), "jetris.voice."+g+".team.3.*"; got != want {
		t.Fatalf("VoiceTeamSubjectFilter = %q, want %q", got, want)
	}
	if got, want := VoiceAnySubjectFilter(g), "jetris.voice."+g+".>"; got != want {
		t.Fatalf("VoiceAnySubjectFilter = %q, want %q", got, want)
	}
	// A voice subject must never fall inside the game stream's filter.
	if s := VoiceSubject(g, "alice"); len(s) > 0 && s[:len("jetris.game.")] == "jetris.game." {
		t.Fatalf("voice subject %q is captured by the game stream", s)
	}
}
