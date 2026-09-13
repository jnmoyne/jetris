package nativeui

import (
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/gamepad"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
)

// newGamepadGame is a game in progress with a fake controller: a T at Col 4
// (four columns of room to its left), DAS 60 / ARR 0 like the keyboard's
// tests, the start frame laid out. The pad's edges are dispatched on the
// same DAS/ARR machines as the keys, through the same frames.
func newGamepadGame(t *testing.T) (*App, *gamepad.FakeSource, *input.Router, time.Time) {
	t.Helper()
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs)
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	pad := a.gamepad.(*gamepad.FakeSource)
	r := new(input.Router)
	base := time.Unix(7000, 0)
	gameFrameAt(a, r, base)
	return a, pad, r, base
}

// TestGamepadDPadDAS: the D-pad's left is the ← key — one shift on the
// press, the slide to the wall when DAS expires (ARR 0), nothing more while
// held, and the release stops the machine.
func TestGamepadDPadDAS(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	pad.Press(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the press: %d moves buffered, want 1", buffered())
	}
	if m := a.eng.BufferedMoves()[0]; m != engine.MoveLeft {
		t.Fatalf("the D-pad's left buffered %v, want a move left", m)
	}
	gameFrameAt(a, r, base.Add(40*ms))
	if buffered() != 1 {
		t.Fatalf("before DAS: %d moves buffered, want still 1", buffered())
	}
	gameFrameAt(a, r, base.Add(80*ms))
	if buffered() != 4 {
		t.Fatalf("after the DAS expiry: %d moves buffered, want 4 (the slide to the wall)", buffered())
	}
	gameFrameAt(a, r, base.Add(200*ms))
	if buffered() != 4 {
		t.Fatalf("glued at the wall: %d moves buffered, want still 4", buffered())
	}
	pad.Release(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(210*ms))
	if a.shift.dir != 0 || a.shift.negDown {
		t.Fatal("the release did not stop the auto-shift machine")
	}
	if a.gamepadHeld[prefs.KeyMoveLeft] != 0 {
		t.Fatalf("move left still held %d times after the release", a.gamepadHeld[prefs.KeyMoveLeft])
	}
}

// TestGamepadStickAndDPadShareOneMove: the stick's left and the D-pad's are
// one move — the second press adds nothing, and the machine only lets go
// when both have.
func TestGamepadStickAndDPadShareOneMove(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	pad.Press(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(10*ms))
	pad.Press(gamepad.StickLeft)
	gameFrameAt(a, r, base.Add(20*ms))
	if buffered() != 1 {
		t.Fatalf("after both presses: %d moves buffered, want 1", buffered())
	}
	pad.Release(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(30*ms))
	if a.shift.dir != -1 {
		t.Fatal("letting the D-pad go while the stick is still pushed stopped the slide")
	}
	// DAS from the first press (+10) expires at +70: the slide.
	gameFrameAt(a, r, base.Add(80*ms))
	if buffered() != 4 {
		t.Fatalf("after the DAS expiry: %d moves buffered, want 4", buffered())
	}
	pad.Release(gamepad.StickLeft)
	gameFrameAt(a, r, base.Add(90*ms))
	if a.shift.dir != 0 {
		t.Fatal("the last release did not stop the machine")
	}
}

// TestGamepadButtons: the face buttons turn, the shoulders hold, the D-pad's
// down soft-drops — each on the press, once — and a press and release
// inside one frame still counts.
func TestGamepadButtons(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	steps := []struct {
		b    gamepad.Button
		want engine.MoveType
	}{
		{gamepad.East, engine.RotateCW},
		{gamepad.South, engine.RotateCCW},
		{gamepad.West, engine.Rotate180},
		{gamepad.LB, engine.MoveHold},
		{gamepad.RT, engine.MoveHold},
		{gamepad.DPadDown, engine.MoveDown},
		{gamepad.StickDown, engine.MoveDown},
	}
	for i, s := range steps {
		pad.Press(s.b)
		pad.Release(s.b)
		gameFrameAt(a, r, base.Add(time.Duration(i+1)*10*ms))
		got := a.eng.BufferedMoves()
		if len(got) != i+1 {
			t.Fatalf("%v: %d moves buffered, want %d", s.b, len(got), i+1)
		}
		if got[i] != s.want {
			t.Fatalf("%v buffered %v, want %v", s.b, got[i], s.want)
		}
	}
	// Back and Start make no move.
	pad.Press(gamepad.Start)
	pad.Press(gamepad.Back)
	gameFrameAt(a, r, base.Add(100*ms))
	if n := len(a.eng.BufferedMoves()); n != len(steps) {
		t.Fatalf("Start/Back: %d moves buffered, want still %d", n, len(steps))
	}
}

// TestGamepadHardDropOncePerPress: a held hard-drop button drops once; the
// D-pad's up and the North button both drop.
func TestGamepadHardDropOncePerPress(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	pad.Press(gamepad.North)
	gameFrameAt(a, r, base.Add(10*ms))
	if buffered() != 1 || a.eng.BufferedMoves()[0] != engine.MoveHardDrop {
		t.Fatalf("after the press: %v, want one hard drop", a.eng.BufferedMoves())
	}
	gameFrameAt(a, r, base.Add(200*ms))
	gameFrameAt(a, r, base.Add(400*ms))
	if buffered() != 1 {
		t.Fatalf("holding the button: %d moves buffered, want still 1", buffered())
	}
	pad.Release(gamepad.North)
	pad.Press(gamepad.DPadUp)
	gameFrameAt(a, r, base.Add(410*ms))
	if buffered() != 2 {
		t.Fatalf("the D-pad's up: %d moves buffered, want 2", buffered())
	}
}

// TestGamepadIgnoredWhileNotPlaying: before the start the pad's presses are
// spent and not answered, and the release that follows once the game is on
// is a stray.
func TestGamepadIgnoredWhileNotPlaying(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	a.gameStatus = string(config.GameStatusStarting)
	pad.Press(gamepad.East)
	pad.Press(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(10*ms))
	if n := len(a.eng.BufferedMoves()); n != 0 {
		t.Fatalf("before the start: %d moves buffered, want 0", n)
	}
	a.gameStatus = string(config.GameStatusInProgress)
	pad.Release(gamepad.East)
	pad.Release(gamepad.DPadLeft)
	gameFrameAt(a, r, base.Add(20*ms))
	gameFrameAt(a, r, base.Add(200*ms))
	if n := len(a.eng.BufferedMoves()); n != 0 {
		t.Fatalf("the strays' releases: %d moves buffered, want 0", n)
	}
	if a.shift.dir != 0 {
		t.Fatal("a stray release started the machine")
	}
}

// TestGamepadDrivesWhileChatFocused: the pad cannot type, so it keeps the
// piece while the chat has the keys.
func TestGamepadDrivesWhileChatFocused(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	a.chatShown = true
	r.Queue(input.SystemEvent{Event: key.Event{Name: key.NameTab, State: key.Press}})
	gameFrameAt(a, r, base.Add(10*ms))
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("Tab did not hand the keys to the chat")
	}
	pad.Press(gamepad.East)
	gameFrameAt(a, r, base.Add(20*ms))
	if got := a.eng.BufferedMoves(); len(got) != 1 || got[0] != engine.RotateCW {
		t.Fatalf("with the chat focused the pad buffered %v, want one rotate CW", got)
	}
}

// TestGamepadLegend: the GAMEPAD section joins the legend, last, once a pad
// has spoken; without the hold rule it has no hold line.
func TestGamepadLegend(t *testing.T) {
	a, pad, r, base := newGamepadGame(t)
	if secs := a.controlsSections(true, false); len(secs) != 2 {
		t.Fatalf("before any press: %d sections, want 2", len(secs))
	}
	pad.Press(gamepad.South)
	gameFrameAt(a, r, base.Add(10*ms))
	secs := a.controlsSections(true, false)
	if len(secs) != 3 || secs[2].header != "GAMEPAD" {
		t.Fatalf("after a press: %d sections, last %q; want 3 ending in GAMEPAD", len(secs), secs[len(secs)-1].header)
	}
	if n := len(secs[2].rows); n != 7 {
		t.Fatalf("with hold: %d rows, want 7", n)
	}
	if n := len(a.controlsSections(false, true)[2].rows); n != 6 {
		t.Fatalf("without hold: %d rows, want 6", n)
	}
}
