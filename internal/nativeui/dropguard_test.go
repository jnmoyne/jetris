package nativeui

// The accidental-drop guard (dropguard.go): the machine on its own, then the
// space bar going through it on a real frame.

import (
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
)

// guardWindow is the knob these tests run at: the default.
const guardWindow = defaultDropGuardMs * time.Millisecond

// TestDropGuardSelfLock: a piece that locks on its own takes the hard drop
// with it — through the lock-to-spawn gap, and for the knob's window after
// the next piece is on the board — and gives it back when the window is out.
func TestDropGuardSelfLock(t *testing.T) {
	var g dropGuard
	t0 := time.Unix(1000, 0)

	// A piece on the board: the drop is the player's.
	g.observe(7, true, t0, guardWindow)
	if g.blocked(t0, guardWindow) {
		t.Fatal("a piece on the board: the drop must not be refused")
	}

	// It locks on its own — off the board, its lock-in still round-tripping.
	g.observe(7, false, t0.Add(10*ms), guardWindow)
	if !g.blocked(t0.Add(10*ms), guardWindow) {
		t.Fatal("the lock-to-spawn gap: the drop must be refused")
	}
	// A gap of any length keeps refusing: the piece it was meant for is down.
	g.observe(7, false, t0.Add(500*ms), guardWindow)
	if !g.blocked(t0.Add(500*ms), guardWindow) {
		t.Fatal("a long gap: the drop must still be refused")
	}

	// The next piece spawns: the window starts here, not at the lock.
	spawn := t0.Add(500 * ms)
	g.observe(8, true, spawn, guardWindow)
	if !g.blocked(spawn.Add(guardWindow-time.Millisecond), guardWindow) {
		t.Fatal("inside the window: the drop must be refused")
	}
	if g.blocked(spawn.Add(guardWindow), guardWindow) {
		t.Fatal("past the window: the drop must be the player's again")
	}
}

// TestDropGuardLockAndSpawnInOneFrame: the lock and the spawn behind it share
// one critical section in the engine, so a frame may never see the board
// without a piece. The piece INDEX moving on is enough on its own.
func TestDropGuardLockAndSpawnInOneFrame(t *testing.T) {
	var g dropGuard
	t0 := time.Unix(2000, 0)
	g.observe(3, true, t0, guardWindow)

	frame := t0.Add(16 * ms)
	g.observe(4, true, frame, guardWindow) // a new piece, the gap never seen
	if !g.blocked(frame, guardWindow) {
		t.Fatal("a lock and a spawn inside one frame: the drop must be refused")
	}
	if g.blocked(frame.Add(guardWindow), guardWindow) {
		t.Fatal("past the window: the drop must be the player's again")
	}
}

// TestDropGuardOwnDropIsNotGuarded: the guard is for the lock nobody asked
// for. A piece the player hard dropped themselves guards nothing — neither
// the gap behind it nor the piece that follows — so a deliberate stream of
// drops runs exactly as it did before the knob existed.
func TestDropGuardOwnDropIsNotGuarded(t *testing.T) {
	var g dropGuard
	t0 := time.Unix(3000, 0)
	g.observe(1, true, t0, guardWindow)

	g.spend() // the player's own hard drop
	g.observe(1, false, t0.Add(5*ms), guardWindow)
	if g.blocked(t0.Add(5*ms), guardWindow) {
		t.Fatal("the gap after the player's own drop: the drop must not be refused")
	}
	g.observe(2, true, t0.Add(40*ms), guardWindow)
	if g.blocked(t0.Add(40*ms), guardWindow) {
		t.Fatal("the piece after the player's own drop: the drop must not be refused")
	}

	// And the very next lock, which they did not make, is guarded again.
	g.observe(3, true, t0.Add(900*ms), guardWindow)
	if !g.blocked(t0.Add(900*ms), guardWindow) {
		t.Fatal("the lock after that one: the drop must be refused")
	}
}

// TestDropGuardOff: at 0 the knob is off — no gap and no lock ever refuses a
// drop, whatever the machine has seen.
func TestDropGuardOff(t *testing.T) {
	var g dropGuard
	t0 := time.Unix(4000, 0)
	g.observe(1, true, t0, 0)
	g.observe(1, false, t0.Add(10*ms), 0)
	if g.blocked(t0.Add(10*ms), 0) {
		t.Fatal("guard off: the gap must not refuse a drop")
	}
	g.observe(2, true, t0.Add(20*ms), 0)
	if g.blocked(t0.Add(20*ms), 0) {
		t.Fatal("guard off: a lock must not refuse a drop")
	}
}

// TestDropGuardFirstPieceIsFree: the first board a frame ever sees arms
// nothing — a game opens with the drop available.
func TestDropGuardFirstPieceIsFree(t *testing.T) {
	var g dropGuard
	t0 := time.Unix(5000, 0)
	g.observe(11, true, t0, guardWindow) // joined mid-game: any index at all
	if g.blocked(t0, guardWindow) {
		t.Fatal("the first frame: the drop must not be refused")
	}
}

// TestDropGuardRefusesTheSpaceBar drives the whole path through a real Gio
// router: with the guard up (a piece has just locked on its own and the next
// one is on the board) the space bar buffers nothing, and the same press once
// the window is out is a hard drop again.
func TestDropGuardRefusesTheSpaceBar(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(6000, 0)

	gameFrameAt(a, &r, base) // the start frame: the board takes the keys
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	// A lock the player did not make, with the next piece already up: the
	// frame turns it into the knob's window (the engine's own index is what
	// arms this in play — here the state is set as that frame leaves it).
	a.dropGuard.armed = true
	gameFrameAt(a, &r, base.Add(100*ms))

	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(110*ms))
	if buffered() != 0 {
		t.Fatalf("inside the guard: %d moves buffered, want none", buffered())
	}

	// Past the window, and on a fresh press (the refused one was spent).
	r.Queue(key.Event{Name: key.NameSpace, State: key.Release})
	gameFrameAt(a, &r, base.Add(150*ms))
	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(160*ms))
	if buffered() != 1 {
		t.Fatalf("past the guard: %d moves buffered, want 1", buffered())
	}
	if m := a.eng.BufferedMoves()[0]; m != engine.MoveHardDrop {
		t.Fatalf("space buffered %v, want a hard drop", m)
	}
}

// TestDropGuardHeldSpaceIsSpent: a press the guard refuses is spent all the
// same — a space still held down when the window runs out does not fire the
// drop it was refused. One drop per press, guard or no guard.
func TestDropGuardHeldSpaceIsSpent(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(7000, 0)

	gameFrameAt(a, &r, base)
	a.dropGuard.armed = true
	gameFrameAt(a, &r, base.Add(10*ms))

	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(20*ms))
	// Held: the OS auto-repeat's Presses, and frames well past the window.
	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(200*ms))
	gameFrameAt(a, &r, base.Add(400*ms))
	if n := len(a.eng.BufferedMoves()); n != 0 {
		t.Fatalf("a held space refused by the guard: %d moves buffered, want none", n)
	}
}
