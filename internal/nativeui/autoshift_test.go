package nativeui

import (
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
)

// shiftRig drives an autoShift with synthetic key edges and frames — the
// machine alone, no router and no engine — and collects the shifts it emits
// (their dirs, in order). The frame clock is base + the given offset; base is
// nonzero except where a test wants the zero-time regime the layout tests run
// frames in.
type shiftRig struct {
	s        autoShift
	base     time.Time
	das, arr time.Duration
	dirs     []int
}

func newShiftRig(das, arr time.Duration) *shiftRig {
	return &shiftRig{base: time.Unix(2000, 0), das: das, arr: arr}
}

func (r *shiftRig) emit(dir int) { r.dirs = append(r.dirs, dir) }

func (r *shiftRig) press(dir int, at time.Duration) {
	r.s.press(dir, r.base.Add(at), r.das, r.arr, r.emit)
}

func (r *shiftRig) release(dir int, at time.Duration) {
	r.s.release(dir, r.base.Add(at), r.das, r.arr, r.emit)
}

// step is a frame at base+at with the given buffer room and slide space
// (possible < 0: no shift can land — the lock-to-spawn gap).
func (r *shiftRig) step(at time.Duration, room, possible int) (wake time.Time, every bool) {
	_, wake, every = r.s.step(r.base.Add(at), r.arr, room, func() int { return possible }, r.emit)
	return wake, every
}

func (r *shiftRig) want(t *testing.T, dirs ...int) {
	t.Helper()
	if len(dirs) == 0 {
		dirs = nil
	}
	if len(r.dirs) != len(dirs) {
		t.Fatalf("emitted %v, want %v", r.dirs, dirs)
	}
	for i := range dirs {
		if r.dirs[i] != dirs[i] {
			t.Fatalf("emitted %v, want %v", r.dirs, dirs)
		}
	}
}

// wantDue asserts the machine's invariant: after a step, nextDue is never in
// the past — repeats are dropped, not banked.
func (r *shiftRig) wantDue(t *testing.T, at time.Duration) {
	t.Helper()
	if r.s.dir != 0 && r.s.nextDue.Before(r.base.Add(at)) {
		t.Fatalf("nextDue %v is before now %v: banked repeats", r.s.nextDue, r.base.Add(at))
	}
}

func TestAutoShiftTapMovesOnce(t *testing.T) {
	r := newShiftRig(60*ms, 0)
	r.press(-1, 0)
	r.release(-1, 30*ms)
	r.step(100*ms, 6, 9)
	r.want(t, -1)

	// The layout tests' regime: frames at the zero time, press and release in
	// the same frame.
	r = newShiftRig(60*ms, 0)
	r.base = time.Time{}
	r.press(-1, 0)
	r.release(-1, 0)
	r.step(0, 6, 9)
	r.want(t, -1)
}

func TestAutoShiftDASThenARR(t *testing.T) {
	r := newShiftRig(60*ms, 50*ms)
	r.press(-1, 0)
	r.want(t, -1) // the press's immediate shift

	wake, every := r.step(30*ms, 6, 9) // DAS still charging
	r.want(t, -1)
	if every || !wake.Equal(r.base.Add(60*ms)) {
		t.Fatalf("charging: wake = %v, every = %v; want the DAS expiry", wake, every)
	}

	wake, _ = r.step(60*ms, 6, 9) // DAS expiry: the first repeat
	r.want(t, -1, -1)
	if !wake.Equal(r.base.Add(110 * ms)) {
		t.Fatalf("first repeat: wake = %v, want +110ms", wake)
	}

	r.step(160*ms, 6, 9) // a slow frame: floor catch-up, 2 owed
	r.want(t, -1, -1, -1, -1)
	r.wantDue(t, 160*ms)

	r.release(-1, 200*ms)
	r.step(300*ms, 6, 9) // released: nothing repeats
	r.want(t, -1, -1, -1, -1)
}

func TestAutoShiftARRZeroSnapsAndGlues(t *testing.T) {
	r := newShiftRig(60*ms, 0)
	r.press(-1, 0)
	r.want(t, -1)

	_, every := r.step(60*ms, 6, 9) // the slide: all the board allows, room-capped
	r.want(t, -1, -1, -1, -1, -1, -1, -1)
	if !every {
		t.Fatal("glued slide must re-measure every frame")
	}

	r.step(76*ms, 3, 3) // the re-measure is idempotent: only what remains
	r.want(t, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1)

	_, every = r.step(92*ms, 6, 0) // at the wall: glued, still watching
	r.want(t, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1)
	if !every {
		t.Fatal("at the wall the slide keeps watching for a gap")
	}

	r.step(108*ms, 6, 2) // a gap opens (a shared board): taken at once
	r.want(t, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1)
}

func TestAutoShiftInstantDAS(t *testing.T) {
	// DAS 0 / ARR 0: a press is the whole slide.
	r := newShiftRig(0, 0)
	r.press(1, 0)
	r.want(t, 1)
	r.step(0, 6, 4)
	r.want(t, 1, 1, 1, 1, 1)
}

func TestAutoShiftOSRepeatIgnored(t *testing.T) {
	r := newShiftRig(60*ms, 50*ms)
	r.press(-1, 0)
	r.press(-1, 33*ms) // the OS auto-repeat: no Release between
	r.press(-1, 47*ms)
	r.want(t, -1) // only the real press shifted

	wake, _ := r.step(50*ms, 6, 9)
	if !wake.Equal(r.base.Add(60 * ms)) {
		t.Fatalf("wake = %v, want the ORIGINAL press's DAS expiry", wake)
	}
}

func TestAutoShiftMostRecentPressWins(t *testing.T) {
	r := newShiftRig(60*ms, 50*ms)
	r.press(-1, 0)
	r.press(1, 20*ms) // both held: the newer press drives
	r.want(t, -1, 1)

	r.step(70*ms, 6, 9) // right's DAS (20+60=80) not expired yet
	r.want(t, -1, 1)

	r.release(-1, 75*ms) // the inactive key: no effect on the active one
	r.press(-1, 90*ms)   // re-press left: newer again, fresh charge
	r.want(t, -1, 1, -1)

	r.release(-1, 100*ms) // right (held since 20ms, charge long spent) resumes
	r.want(t, -1, 1, -1, 1)
	r.step(150*ms, 6, 9) // resumed at 100ms: next repeat one ARR later
	r.want(t, -1, 1, -1, 1, 1)

	r.release(1, 200*ms) // last key up: idle
	r.step(300*ms, 6, 9)
	r.want(t, -1, 1, -1, 1, 1)

	// A stray release (a focus regain mid-hold): ignored entirely.
	r.release(-1, 310*ms)
	r.step(400*ms, 6, 9)
	r.want(t, -1, 1, -1, 1, 1)
}

func TestAutoShiftGapRetainsCharge(t *testing.T) {
	// ARR 0: the charge survives the lock-to-spawn gap, and the NEXT piece
	// snaps over the frame it appears — the full slide, nothing banked.
	r := newShiftRig(60*ms, 0)
	r.press(-1, 0)
	_, every := r.step(60*ms, 6, -1) // the gap: no piece to shift
	r.want(t, -1)
	if !every {
		t.Fatal("the gap must be watched every frame for the spawn")
	}
	r.step(200*ms, 6, -1) // still no piece
	r.want(t, -1)
	r.step(216*ms, 6, 4) // the spawn: the slide, at once
	r.want(t, -1, -1, -1, -1, -1)

	// ARR > 0: the spawn gets ONE immediate shift, then the cadence — the
	// gap's worth of repeats was dropped, not banked.
	r = newShiftRig(60*ms, 50*ms)
	r.press(1, 0)
	r.step(60*ms, 6, -1)
	r.step(200*ms, 6, -1)
	r.want(t, 1)
	r.step(216*ms, 6, 9)
	r.want(t, 1, 1)
	r.wantDue(t, 216*ms)
}

func TestAutoShiftBackPressure(t *testing.T) {
	// ARR > 0 with a full buffer: the repeat waits without banking.
	r := newShiftRig(60*ms, 50*ms)
	r.press(-1, 0)
	_, every := r.step(60*ms, 0, 9) // due, but no room
	r.want(t, -1)
	if !every {
		t.Fatal("a full buffer must be retried every frame")
	}
	r.wantDue(t, 60*ms)
	r.step(76*ms, 3, 9) // room again: one repeat, not the backlog
	r.want(t, -1, -1)
}

// gameFrameAt is gameFrame with a frame clock — the DAS/ARR timing tests
// need Now to advance between frames.
func gameFrameAt(a *App, r *input.Router, now time.Time) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
		Now:         now,
		Source:      r.Source(),
	}
	a.layout(gtx)
	r.Frame(ops)
}

// TestAutoShiftKeyboardDAS drives the whole path through a real Gio router:
// a held ← (Press, never Released) shifts once, the OS repeat's duplicate
// Presses add nothing, the DAS expiry snaps the piece to the wall (ARR 0)
// where it stays glued, and losing the keys to the chat resets the machine.
func TestAutoShiftKeyboardDAS(t *testing.T) {
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs) // pinned: the timings below assume DAS 60 / ARR 0
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	// A T at Col 4 (cells in cols 4..6): exactly 4 columns of room to its left.
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(3000, 0)

	gameFrameAt(a, &r, base) // the start frame: the board takes the keys
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}

	buffered := func() int { return len(a.eng.BufferedMoves()) }

	r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the press: %d moves buffered, want 1", buffered())
	}

	// The OS auto-repeat: more Presses, no Release between.
	r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	gameFrameAt(a, &r, base.Add(40*ms))
	if buffered() != 1 {
		t.Fatalf("after the OS repeat's press: %d moves buffered, want still 1", buffered())
	}

	// DAS (60ms from the press's frame at +10ms) expires; ARR 0 slides the
	// piece the remaining 3 columns to the wall, in one frame.
	gameFrameAt(a, &r, base.Add(80*ms))
	if buffered() != 4 {
		t.Fatalf("after the DAS expiry: %d moves buffered, want 4 (the slide to the wall)", buffered())
	}

	// Glued at the wall: held frames add nothing.
	gameFrameAt(a, &r, base.Add(100*ms))
	gameFrameAt(a, &r, base.Add(200*ms))
	if buffered() != 4 {
		t.Fatalf("glued at the wall: %d moves buffered, want still 4", buffered())
	}

	// Tab hands the keys to the chat: the board's FocusEvent resets the
	// machine, so the still-unreleased ← repeats nothing ever again.
	r.Queue(input.SystemEvent{Event: key.Event{Name: key.NameTab, State: key.Press}})
	gameFrameAt(a, &r, base.Add(210*ms))
	gameFrameAt(a, &r, base.Add(300*ms))
	gameFrameAt(a, &r, base.Add(400*ms))
	if buffered() != 4 {
		t.Fatalf("after losing the keys: %d moves buffered, want still 4", buffered())
	}
	if a.shift.dir != 0 || a.shift.negDown {
		t.Fatal("losing the keys did not reset the auto-shift machine")
	}
}

// TestWASDKeysDriveThePiece drives the left hand's arrows through the same
// router the arrow keys go through: A shifts left, D right, W rotates
// clockwise and S soft-drops — the arrow keys' own actions, in the arrow
// keys' own order.
func TestWASDKeysDriveThePiece(t *testing.T) {
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs)
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(4000, 0)

	gameFrameAt(a, &r, base) // the start frame: the board takes the keys
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}

	// A tap of each letter, released before the next: one move apiece, and
	// none of them held long enough for DAS.
	at := base
	for _, k := range []key.Name{"A", "D", "W", "S"} {
		at = at.Add(10 * ms)
		r.Queue(key.Event{Name: k, State: key.Press})
		gameFrameAt(a, &r, at)
		at = at.Add(10 * ms)
		r.Queue(key.Event{Name: k, State: key.Release})
		gameFrameAt(a, &r, at)
	}
	want := []engine.MoveType{engine.MoveLeft, engine.MoveRight, engine.RotateCW, engine.MoveDown}
	if got := a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after tapping A D W S: BufferedMoves = %v, want %v", got, want)
	}
}

// TestWASDSharesTheArrowsAutoShift is the other half: because the letters are
// folded onto the arrow names before any of the dispatch (arrowForKey), A and
// ← are ONE key to the shift machine. A held A charges DAS and snaps to the
// wall exactly as a held ← does (TestAutoShiftKeyboardDAS, the same timings),
// a ← pressed while A is down is the duplicate press the OS repeat is —
// ignored, the DAS clock unmoved — and releasing A stops the repeat.
func TestWASDSharesTheArrowsAutoShift(t *testing.T) {
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs) // pinned: the timings below assume DAS 60 / ARR 0
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	// A T at Col 4 (cells in cols 4..6): exactly 4 columns of room to its left.
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(5000, 0)

	gameFrameAt(a, &r, base)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	r.Queue(key.Event{Name: "A", State: key.Press})
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the press of A: %d moves buffered, want 1", buffered())
	}

	// ← while A is down: the same key to the machine, so it is the OS
	// repeat's duplicate press — ignored, and the DAS clock unmoved.
	r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	gameFrameAt(a, &r, base.Add(40*ms))
	if buffered() != 1 {
		t.Fatalf("← pressed while A was held: %d moves buffered, want still 1 (one key, not two)", buffered())
	}

	// DAS (60ms from the press's frame at +10ms) expires: ARR 0 slides the
	// piece the remaining 3 columns to the wall in one frame.
	gameFrameAt(a, &r, base.Add(80*ms))
	if buffered() != 4 {
		t.Fatalf("after the DAS expiry: %d moves buffered, want 4 (the slide to the wall)", buffered())
	}

	// Releasing A stops it — and the arrow's own release, of a press the
	// machine folded away, leaves nothing running either.
	r.Queue(key.Event{Name: "A", State: key.Release})
	gameFrameAt(a, &r, base.Add(90*ms))
	r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Release})
	gameFrameAt(a, &r, base.Add(200*ms))
	gameFrameAt(a, &r, base.Add(400*ms))
	if buffered() != 4 {
		t.Fatalf("after releasing A: %d moves buffered, want still 4", buffered())
	}
	if a.shift.dir != 0 || a.shift.negDown {
		t.Fatal("releasing A did not stop the shift machine")
	}
}

// TestModifierKeysRotateAndHold drives the Guideline's two modifier
// controls through the router: Ctrl rotates counter-clockwise on its press,
// Shift holds on its RELEASE (see TestShiftTabHoldsNothing), each delivered
// — as the backends deliver them — carrying its own modifier bit. And the
// keys pressed WHILE one is held still arrive: a filter naming no modifier
// would drop them, which would make holding Shift a way to freeze the piece.
func TestModifierKeysRotateAndHold(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(6000, 0)

	gameFrameAt(a, &r, base)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}

	at := base
	for _, e := range []key.Event{
		{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press},
		{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Release},
		{Name: key.NameShift, Modifiers: key.ModShift, State: key.Press},
		// Held Shift, and the rest of the scheme still answering: a rotate,
		// a shift and a hard drop, each carrying the bit Shift puts on it.
		{Name: "Z", Modifiers: key.ModShift, State: key.Press},
		{Name: "Z", Modifiers: key.ModShift, State: key.Release},
		{Name: key.NameLeftArrow, Modifiers: key.ModShift, State: key.Press},
		{Name: key.NameLeftArrow, Modifiers: key.ModShift, State: key.Release},
		{Name: key.NameSpace, Modifiers: key.ModShift, State: key.Press},
		{Name: key.NameSpace, Modifiers: key.ModShift, State: key.Release},
		{Name: key.NameShift, Modifiers: key.ModShift, State: key.Release},
	} {
		at = at.Add(10 * ms)
		r.Queue(e)
		gameFrameAt(a, &r, at)
	}
	// The hold lands last, on Shift's release — after the three moves made
	// while it was down.
	want := []engine.MoveType{
		engine.RotateCCW,
		engine.Rotate180, engine.MoveLeft, engine.MoveHardDrop,
		engine.MoveHold,
	}
	if got := a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Ctrl, Shift, then X ← Space under a held Shift: BufferedMoves = %v, want %v", got, want)
	}
}

// TestShiftTabHoldsNothing is why Shift holds on its release: a shifted Tab
// is the board/chat switch, and a player reaching for the chat did not mean
// to spend the hold. The Tab disarms the armed press, so the keys move and
// the piece is not held — and the Shift release that lands afterwards (with
// the chat now holding the keys) does nothing either. C, the other hold key,
// is unaffected: it holds on the press.
func TestShiftTabHoldsNothing(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(7000, 0)

	gameFrameAt(a, &r, base)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}
	if !a.chatVisible() {
		t.Fatal("a 1200x820 window does not show the chat strip by default")
	}

	// Shift down, then Tab: the keys go to the chat, the hold is disarmed.
	r.Queue(key.Event{Name: key.NameShift, Modifiers: key.ModShift, State: key.Press})
	gameFrameAt(a, &r, base.Add(10*ms))
	if !a.holdArmed {
		t.Fatal("the press of Shift did not arm the hold")
	}
	r.Queue(input.SystemEvent{Event: key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}})
	gameFrameAt(a, &r, base.Add(20*ms))
	if a.holdArmed {
		t.Fatal("the shifted Tab left the hold armed")
	}
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("the shifted Tab did not hand the keys to the chat editor")
	}
	// The release, wherever it lands, spends nothing.
	r.Queue(key.Event{Name: key.NameShift, State: key.Release})
	gameFrameAt(a, &r, base.Add(30*ms))
	if got := a.eng.BufferedMoves(); len(got) != 0 {
		t.Fatalf("a shifted Tab moved the piece: BufferedMoves = %v, want none", got)
	}

	// Back on the board, a Shift tap on its own still holds.
	r.Queue(input.SystemEvent{Event: key.Event{Name: key.NameTab, State: key.Press}})
	gameFrameAt(a, &r, base.Add(40*ms))
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("Tab did not hand the keys back to the board")
	}
	r.Queue(key.Event{Name: key.NameShift, Modifiers: key.ModShift, State: key.Press})
	gameFrameAt(a, &r, base.Add(50*ms))
	if got := a.eng.BufferedMoves(); len(got) != 0 {
		t.Fatalf("the press alone held the piece: BufferedMoves = %v, want none until the release", got)
	}
	r.Queue(key.Event{Name: key.NameShift, State: key.Release})
	gameFrameAt(a, &r, base.Add(60*ms))
	want := []engine.MoveType{engine.MoveHold}
	if got := a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a Shift tap on the board: BufferedMoves = %v, want %v", got, want)
	}

	// Losing the keys with Shift still down disarms it too: the release the
	// board never sees must not hold the next piece the moment it comes back.
	r.Queue(key.Event{Name: key.NameShift, Modifiers: key.ModShift, State: key.Press})
	gameFrameAt(a, &r, base.Add(70*ms))
	press(&r, editorPressX, editorPressY) // a click into the chat, no Tab involved
	gameFrameAt(a, &r, base.Add(80*ms))
	gameFrameAt(a, &r, base.Add(90*ms))
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("the click in the chat strip did not hand it the keys")
	}
	if a.holdArmed {
		t.Fatal("losing the keys left the hold armed")
	}
	if got := a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("losing the keys spent the hold: BufferedMoves = %v, want %v", got, want)
	}
}

// TestAutoShiftPadHold is the touch device's version of the same guarantee:
// the on-screen pad's ← arm moves on the press, a held arm auto-repeats with
// the DAS/ARR tuning (snapping to the wall at ARR 0), the pad press's
// one-frame focus flap (the Clickable takes the keys, handleKeys hands them
// back) does not reset the hold, and lifting the finger neither double-fires
// the click nor leaves the repeat running.
func TestAutoShiftPadHold(t *testing.T) {
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs) // pinned: the timings below assume DAS 60 / ARR 0
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	// A T at Col 4 (cells in cols 4..6): exactly 4 columns of room to its left.
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.touchUI = true
	a.padShown = true // the pad on, whatever the window's default
	var r input.Router
	base := time.Unix(4000, 0)

	gameFrameAt(a, &r, base)
	pl, ok := semanticBounds(&r, padLeftLabel)
	if !ok {
		t.Fatal("no ← arm in the semantic tree — the pad is not on screen")
	}
	cx, cy := float32(pl.Min.X+pl.Dx()/2), float32(pl.Min.Y+pl.Dy()/2)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	touch(&r, pointer.Press, 1, cx, cy, 0) // finger down, and held
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the arm's press: %d moves buffered, want 1", buffered())
	}

	// The flap frame: the arm's Clickable holds the keys, and the machine
	// must ride it out — the DAS expiry below is the proof.
	gameFrameAt(a, &r, base.Add(40*ms))
	if buffered() != 1 {
		t.Fatalf("holding the arm before DAS: %d moves buffered, want still 1", buffered())
	}

	gameFrameAt(a, &r, base.Add(80*ms)) // DAS expiry: ARR 0 snaps the last 3 columns
	if buffered() != 4 {
		t.Fatalf("after the DAS expiry: %d moves buffered, want 4 (the slide to the wall)", buffered())
	}
	gameFrameAt(a, &r, base.Add(100*ms))
	if buffered() != 4 {
		t.Fatalf("glued at the wall: %d moves buffered, want still 4", buffered())
	}

	touch(&r, pointer.Release, 1, cx, cy, 110*ms) // finger up
	gameFrameAt(a, &r, base.Add(120*ms))
	gameFrameAt(a, &r, base.Add(300*ms))
	if buffered() != 4 {
		t.Fatalf("after the release: %d moves buffered, want still 4 (no click double-fire, no repeats)", buffered())
	}
	if a.shift.dir != 0 || a.shift.negDown {
		t.Fatal("lifting the finger did not release the machine")
	}
}

func TestAutoShiftResetClearsEverything(t *testing.T) {
	r := newShiftRig(60*ms, 0)
	r.press(-1, 0)
	r.want(t, -1)
	r.s.reset() // the board lost the keys mid-hold
	if r.s.dir != 0 || r.s.negDown {
		t.Fatal("reset must forget the held key")
	}
	r.step(100*ms, 6, 9) // no repeats from the forgotten hold
	r.want(t, -1)
	r.press(-1, 200*ms) // the next press is fresh, not an "OS repeat"
	r.want(t, -1, -1)
}

// TestHardDropOncePerPress: the hard drop has no DAS and no ARR. A held
// space drops once, however many Presses the OS auto-repeat sends behind it,
// and only a real re-press (a Release first) drops again — or a player
// leaning on the bar would drop every piece that spawned under it.
func TestHardDropOncePerPress(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(5000, 0)

	gameFrameAt(a, &r, base) // the start frame: the board takes the keys
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the press: %d moves buffered, want 1", buffered())
	}
	if m := a.eng.BufferedMoves()[0]; m != engine.MoveHardDrop {
		t.Fatalf("space buffered %v, want a hard drop", m)
	}

	// The OS auto-repeat: Presses with no Release between, and frames to
	// give any repeat machine its chance to run.
	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(60*ms))
	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(200*ms))
	gameFrameAt(a, &r, base.Add(400*ms))
	if buffered() != 1 {
		t.Fatalf("holding space: %d moves buffered, want still 1", buffered())
	}

	// Lifting and pressing again is a second drop.
	r.Queue(key.Event{Name: key.NameSpace, State: key.Release})
	gameFrameAt(a, &r, base.Add(410*ms))
	r.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	gameFrameAt(a, &r, base.Add(420*ms))
	if buffered() != 2 {
		t.Fatalf("after the re-press: %d moves buffered, want 2", buffered())
	}
}

// TestSoftDropAutoRepeat: ↓ repeats on its own knob — SDF times the level's
// gravity, the Guideline's soft drop — on its own axis and with no DAS to
// wait out (softDAS). The press drops one row, the OS repeat's duplicate
// Presses add nothing, and a row falls every interval from the press onward
// until the key comes up.
func TestSoftDropAutoRepeat(t *testing.T) {
	a := newTestApp()
	// Pinned: DAS 150 (which ↓ must ignore), ARR 20 (which is the shift's
	// and not the soft drop's), SDF 20 — 20x the level-1 gravity of 1000ms,
	// so one row every 50ms.
	a.SetHandling(150, 20, 20, defaultDropGuardMs)
	if got := a.softARR(nil); got != 50*ms {
		t.Fatalf("soft drop interval = %v, want 50ms (level 1 gravity / SDF 20)", got)
	}
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	// High on an empty board: rows to spare below it.
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	base := time.Unix(6000, 0)

	gameFrameAt(a, &r, base)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	r.Queue(key.Event{Name: key.NameDownArrow, State: key.Press})
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the press: %d moves buffered, want 1", buffered())
	}
	if m := a.eng.BufferedMoves()[0]; m != engine.MoveDown {
		t.Fatalf("↓ buffered %v, want a soft drop", m)
	}

	// The OS auto-repeat, inside the first interval: nothing of its own.
	r.Queue(key.Event{Name: key.NameDownArrow, State: key.Press})
	gameFrameAt(a, &r, base.Add(30*ms))
	if buffered() != 1 {
		t.Fatalf("inside the first interval: %d moves buffered, want still 1", buffered())
	}

	// The first repeat is one interval after the press (+60ms), NOT one DAS:
	// this frame is still 100ms short of the 150ms charge a shift would owe.
	gameFrameAt(a, &r, base.Add(70*ms))
	if buffered() != 2 {
		t.Fatalf("one interval after the press: %d moves buffered, want 2 (no DAS on ↓)", buffered())
	}
	// The rows due at +110ms and +160ms, both owed by this frame.
	gameFrameAt(a, &r, base.Add(160*ms))
	if buffered() != 4 {
		t.Fatalf("after two more rows: %d moves buffered, want 4", buffered())
	}

	// The key comes up: the repeat stops there.
	r.Queue(key.Event{Name: key.NameDownArrow, State: key.Release})
	gameFrameAt(a, &r, base.Add(170*ms))
	gameFrameAt(a, &r, base.Add(600*ms))
	if buffered() != 4 {
		t.Fatalf("after the release: %d moves buffered, want still 4", buffered())
	}
	if a.soft.dir != 0 || a.soft.posDown {
		t.Fatal("the release did not stop the soft-drop machine")
	}
	// The shift machine is a different axis and never saw a key.
	if a.shift.dir != 0 {
		t.Fatal("the ↓ key drove the ← → machine")
	}
}

// TestPadDownArmSoftDrops is the touch version: the pad's ↓ arm drops on the
// press and, held, repeats on the same SDF and with no DAS — and lifting the
// finger does not fire its click as a second drop (handlePadClicks no longer
// has it).
func TestPadDownArmSoftDrops(t *testing.T) {
	a := newTestApp()
	a.SetHandling(150, 20, 20, defaultDropGuardMs) // pinned: DAS 150 (ignored by ↓), SDF 20 = one row per 50ms
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.touchUI = true
	a.padShown = true // the pad on, whatever the window's default
	var r input.Router
	base := time.Unix(7000, 0)

	gameFrameAt(a, &r, base)
	pd, ok := semanticBounds(&r, padDownLabel)
	if !ok {
		t.Fatal("no ↓ arm in the semantic tree — the pad is not on screen")
	}
	cx, cy := float32(pd.Min.X+pd.Dx()/2), float32(pd.Min.Y+pd.Dy()/2)
	buffered := func() int { return len(a.eng.BufferedMoves()) }

	touch(&r, pointer.Press, 1, cx, cy, 0) // finger down, and held
	gameFrameAt(a, &r, base.Add(10*ms))
	if buffered() != 1 {
		t.Fatalf("after the arm's press: %d moves buffered, want 1", buffered())
	}
	// The focus flap frame: the arm's Clickable holds the keys, and the
	// machine must ride it out — the row below is the proof.
	gameFrameAt(a, &r, base.Add(40*ms))
	if buffered() != 1 {
		t.Fatalf("inside the first interval: %d moves buffered, want still 1", buffered())
	}
	gameFrameAt(a, &r, base.Add(70*ms)) // one interval past the press
	if buffered() != 2 {
		t.Fatalf("one interval after the arm's press: %d moves buffered, want 2", buffered())
	}

	touch(&r, pointer.Release, 1, cx, cy, 80*ms) // finger up
	gameFrameAt(a, &r, base.Add(90*ms))
	gameFrameAt(a, &r, base.Add(600*ms))
	if buffered() != 2 {
		t.Fatalf("after the release: %d moves buffered, want still 2 (no click double-fire)", buffered())
	}
	if a.soft.dir != 0 || a.soft.posDown {
		t.Fatal("lifting the finger did not release the soft-drop machine")
	}
}

// TestSoftARRFollowsGravity: the soft drop's rate is SDF times the level's
// gravity, the Guideline's way of putting it — so ↓ stays ahead of the fall
// at every level instead of running at a wall-clock rate the speed curve
// overtakes. The top of the knob is instant, and no setting can ever compute
// its way to 0 (the machine's "instant") by rounding.
func TestSoftARRFollowsGravity(t *testing.T) {
	for _, c := range []struct {
		sdf, level int
		want       time.Duration
	}{
		{sdf: 20, level: 1, want: 50 * ms},                      // 1000ms gravity / 20
		{sdf: 10, level: 1, want: 100 * ms},                     // the same level, half the factor
		{sdf: 1, level: 1, want: 1000 * ms},                     // the slowest setting: gravity itself
		{sdf: 20, level: 6, want: game.GravityInterval(6) / 20}, // the curve carried through
		{sdf: 20, level: game.MaxLevel, want: 1 * ms},           // the fastest level (7 ms a row): floored, never 0
		{sdf: maxSDF, level: 1, want: 0},                        // MAX: the machine's instant slide
		{sdf: maxSDF, level: game.MaxLevel, want: 0},            //
	} {
		if got := softInterval(c.sdf, c.level); got != c.want {
			t.Errorf("SDF %d at level %d: interval %v, want %v", c.sdf, c.level, got, c.want)
		}
	}
	// No engine yet (a frame before the game): level 1 stands in.
	a := newTestApp()
	a.SetHandling(defaultDASMs, defaultARRMs, 20, defaultDropGuardMs)
	if got := a.softARR(nil); got != 50*ms {
		t.Errorf("with no engine: interval %v, want the level-1 rate 50ms", got)
	}
}

// TestHandlingKnobsRoundTrip: each slider's position and its knob agree, so
// the HANDLING section opens showing the tuning that is actually in force —
// and the saved set carries all four.
func TestHandlingKnobsRoundTrip(t *testing.T) {
	a := newTestApp()
	var saved prefs.Handling
	a.handlingSave = func(h prefs.Handling) error { saved = h; return nil }

	a.SetHandling(85, 35, 12, 45)
	if a.dasMs != 85 || a.arrMs != 35 || a.sdf != 12 || a.dropGuardMs != 45 {
		t.Fatalf("knobs = %d/%d/%d/%d, want 85/35/12/45", a.dasMs, a.arrMs, a.sdf, a.dropGuardMs)
	}
	for _, c := range []struct {
		name string
		pos  float32
		r    knobRange
		want int
	}{
		{"DAS", a.dasFloat.Value, msRange, 85},
		{"ARR", a.arrFloat.Value, msRange, 35},
		{"SDF", a.sdfFloat.Value, sdfRange, 12},
		{"GUARD", a.dropGuardFloat.Value, msRange, 45},
	} {
		if got := c.r.value(c.pos); got != c.want {
			t.Errorf("%s slider at %v reads back as %d, want %d", c.name, c.pos, got, c.want)
		}
	}
	// Out of range on the way in: clamped, sliders and all.
	a.SetHandling(9000, -5, 9000, 9000)
	if a.dasMs != maxHandlingMs || a.arrMs != 0 || a.sdf != maxSDF || a.dropGuardMs != maxHandlingMs {
		t.Fatalf("clamped knobs = %d/%d/%d/%d, want %d/0/%d/%d",
			a.dasMs, a.arrMs, a.sdf, a.dropGuardMs, maxHandlingMs, maxSDF, maxHandlingMs)
	}
	if got := sdfRange.value(a.sdfFloat.Value); got != maxSDF {
		t.Errorf("SDF slider at the top reads back as %d, want %d", got, maxSDF)
	}

	a.persistHandling()
	want := prefs.Handling{DASMs: maxHandlingMs, ARRMs: 0, SDF: maxSDF, DropGuardMs: maxHandlingMs}
	if saved != want {
		t.Fatalf("persisted %+v, want %+v", saved, want)
	}
}
