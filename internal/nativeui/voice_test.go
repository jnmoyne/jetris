package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/lobby"
	"jetris/internal/voice"
)

// The lobby's session comes and goes with the lobby screen: started muted
// when the lobby is up, listed in the lobby room, stopped on the archive
// screen, started afresh — muted again — on the way back; and a screen with
// no lobby starts nothing.
func TestLobbyVoiceFollowsTheScreen(t *testing.T) {
	a := newTestApp()
	dev := &voice.FakeDevice{}
	a.voiceDevice = func() voice.Device { return dev }
	a.screen = screenLobby
	a.reconcileVoice()
	if a.getVoice() != nil {
		t.Fatal("a lobby screen without a lobby started a session")
	}

	a.lobby = lobby.New(nil, nil, "tester", "tester")
	a.reconcileVoice()
	waitFor(t, "the lobby session to start", func() bool { return a.getVoice() != nil })
	s := a.getVoice()
	if !s.Muted() || s.Config().GameID != config.LobbyVoiceRoom || s.Config().PlayerID != "tester" || s.Config().Team != -1 {
		t.Fatalf("lobby session = %+v muted %v, want a muted session in the lobby room", s.Config(), s.Muted())
	}
	a.reconcileVoice() // a second frame starts nothing more
	if a.getVoice() != s {
		t.Fatal("a second frame replaced the lobby session")
	}
	s.SetMuted(false)
	if !dev.Capturing() {
		t.Fatal("unmuting in the lobby did not open the microphone")
	}
	// Someone in the lobby room is heard, and the screen lays out with the
	// mark — players beside the panel, then put away (the strip).
	a.lobbyPlayersShown = true
	s.Receive(config.VoiceSubject(config.LobbyVoiceRoom, "bob"), voicePacket(0, voice.FlagStart), time.Now())
	renderOnce(t, a)
	a.lobbyPlayersShown = false
	renderOnce(t, a)

	// A replay leaves the room; the way back rejoins it, muted.
	a.mu.Lock()
	a.screen = screenArchive
	a.mu.Unlock()
	a.reconcileVoice()
	waitFor(t, "the lobby session to stop on the archive screen", func() bool { return a.getVoice() == nil && !dev.Capturing() })
	a.mu.Lock()
	a.screen = screenLobby
	a.mu.Unlock()
	a.reconcileVoice()
	waitFor(t, "the lobby session to start again", func() bool { return a.getVoice() != nil })
	if !a.getVoice().Muted() {
		t.Fatal("the lobby session came back unmuted")
	}
	t.Cleanup(a.stopVoice)
}
