package nativeui

import (
	"context"
	"image"
	"sync/atomic"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
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

	// A replay leaves the room; the way back rejoins it — muted, since the
	// unmute above was the session's, not the mic button's (micOn).
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

// The microphone follows the player from room to room: opened by the mic
// button in the lobby, it is open on the game screen joined next and in the
// lobby come back to; muted in the game, it is muted in the lobby after. One
// session at a time: the lobby's is stopped before the game's opens, so a
// room is never heard from the other. A fresh connection to a lobby starts
// muted (initLobby resets micOn).
func TestMicFollowsThePlayerBetweenRooms(t *testing.T) {
	g := newLobbyRig(t, image.Pt(1280, 820), deviceDesktop)
	a := g.a
	dev := &voice.FakeDevice{}
	a.voiceDevice = func() voice.Device { return dev }
	a.stopVoice() // the rig's first frame started one on the default fake
	g.frame()
	waitFor(t, "the lobby session to start", func() bool { return a.getVoice() != nil })
	lobbySess := a.getVoice()
	if !lobbySess.Muted() {
		t.Fatal("a fresh lobby's microphone is open")
	}
	g.frame()
	g.tap(barMicX(), g.barY())
	if lobbySess.Muted() || !dev.Capturing() {
		t.Fatal("the lobby's mic button did not open the microphone")
	}
	t.Cleanup(a.stopVoice)

	// A game joined: the game's own session, the microphone open on it, the
	// lobby's session stopped and its device closed before the game's opened
	// its own (the same fake here, so the game's open is the one that
	// counts).
	eng := engine.New(nil, "mic-rooms", "tester", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.mu.Lock()
	a.eng, a.screen = eng, screenGame
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "tester", Name: "tester"}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.mu.Unlock()
	a.startVoice(eng, context.Background(), nil)
	gameSess := a.getVoice()
	if gameSess == lobbySess || gameSess.Config().GameID != "mic-rooms" {
		t.Fatal("the game screen kept the lobby's session")
	}
	if gameSess.Muted() || !dev.Capturing() {
		t.Fatal("the game screen did not open the microphone the lobby had open")
	}
	lobbySess.Receive(config.VoiceSubject(config.LobbyVoiceRoom, "carol"), voicePacket(0, voice.FlagStart), time.Now())
	if len(gameSess.Snapshot().Speakers) != 0 {
		t.Fatal("a lobby packet showed up as a speaker on the game screen")
	}

	// Muted in the game, then back to the lobby: its new session muted.
	a.toggleMic()
	if !gameSess.Muted() || dev.Capturing() {
		t.Fatal("the game's mic button did not mute")
	}
	a.returnToLobby()
	g.frame()
	waitFor(t, "the lobby session to start again", func() bool { return a.getVoice() != nil && a.getVoice() != gameSess })
	if !a.getVoice().Muted() || dev.Capturing() {
		t.Fatal("the lobby came back with a microphone the game had muted")
	}

	// Opened in the lobby again, a spectated game has it open too.
	a.toggleMic()
	if a.getVoice().Muted() {
		t.Fatal("the lobby's second unmute failed")
	}
	spec := engine.New(nil, "mic-rooms-2", "tester", "", config.ModeCooperative, engine.ModeSpectator, 0, 0, 0)
	a.mu.Lock()
	a.eng, a.screen = spec, screenGame
	a.mu.Unlock()
	a.startVoice(spec, context.Background(), nil)
	if a.getVoice().Muted() || !dev.Capturing() {
		t.Fatal("spectating did not carry the open microphone over")
	}
	a.returnToLobby()
}

// holdDevice is a fake whose Open can be held — the speakers taking their
// time — and which counts the devices open at once.
type holdDevice struct {
	voice.FakeDevice
	live   *atomic.Int32
	opened chan struct{} // closed as Open is entered, when held
	hold   chan struct{} // Open waits for it, when not nil
	open   atomic.Bool
}

func (d *holdDevice) Open(io voice.DeviceIO) error {
	if d.hold != nil {
		close(d.opened)
		<-d.hold
	}
	if err := d.FakeDevice.Open(io); err != nil {
		return err
	}
	d.open.Store(true)
	d.live.Add(1)
	return nil
}

func (d *holdDevice) Close() {
	if d.open.Swap(false) {
		d.live.Add(-1)
	}
	d.FakeDevice.Close()
}

// Back to Lobby pressed while a game's speakers are still opening: the
// lobby's session takes the slot, and the game's, once open, is stopped
// rather than written over it — which would have left the lobby's playing
// the lobby room with nobody to stop it, and the lobby heard twice from
// the next visit on.
func TestVoiceStartOutrunByBack(t *testing.T) {
	g := newLobbyRig(t, image.Pt(1280, 820), deviceDesktop)
	a := g.a
	var live atomic.Int32
	var made atomic.Int32
	opened, hold := make(chan struct{}), make(chan struct{})
	a.voiceDevice = func() voice.Device {
		d := &holdDevice{live: &live}
		if made.Add(1) == 1 {
			d.opened, d.hold = opened, hold
		}
		return d
	}
	a.stopVoice() // the rig's first frame started one on the default fake

	// Joining: the game's speakers held open.
	eng := engine.New(nil, "outrun", "tester", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.startGameScreen(eng, ctx, cancel, []lobby.PlayerSummary{{PlayerID: "tester", Name: "tester"}}, "")
	}()
	<-opened

	// Back to the lobby meanwhile: its own session comes up.
	a.returnToLobby()
	g.frame()
	waitFor(t, "the lobby session to start", func() bool { return a.getVoice() != nil })
	lobbySess := a.getVoice()

	close(hold)
	<-done
	if v := a.getVoice(); v != lobbySess || v.Config().GameID != config.LobbyVoiceRoom {
		t.Fatalf("the slot holds %+v after the late game session opened, want the lobby's", v.Config())
	}
	waitFor(t, "the game's device to close", func() bool { return live.Load() == 1 })
	t.Cleanup(a.stopVoice)
}
