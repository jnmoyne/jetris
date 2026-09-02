package nativeui

// A rejected write is told in two halves (bridge.go, effects.go, drawBoard):
// the piece VIBRATES where the CAS failure put it back, and the outline BLINKS
// where the lost step wanted it — the move that was taken away, drawn where it
// would have gone. A rejection with nothing headed anywhere (a lost spawn,
// lock or gravity step) keeps the plain rainbow border on the piece's cells.

import (
	"context"
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

// pumpOne runs pumpEngine over one update and returns once it has been
// folded into the App — an RTT update queued behind it is the receipt.
func pumpOne(t *testing.T, a *App, e *engine.Engine, u engine.EngineUpdate) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.pumpEngine(ctx, e); close(done) }()
	defer func() { cancel(); <-done }()
	e.Updates <- u
	e.Updates <- engine.EngineUpdate{Kind: engine.UpdateRTT, RTT: time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		pumped := a.rtt == time.Millisecond
		a.mu.Unlock()
		if pumped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pumpEngine never processed the updates")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCASRejectionKicksThePieceAndBlinksTheLostTarget(t *testing.T) {
	a := newTestApp()
	e := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	pumpOne(t, a, e, engine.EngineUpdate{
		Kind:             engine.UpdateCASFlash,
		FlashCells:       [][2]int{{5, 4}, {5, 5}},
		FlashTargetCells: [][2]int{{5, 3}, {5, 4}},
	})

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.casKickAt.IsZero() {
		t.Fatal("a rejected write must kick the recoil")
	}
	for _, rc := range [][2]int{{5, 3}, {5, 4}} {
		if _, ok := a.casWant[rc]; !ok {
			t.Fatalf("casWant %v misses the target cell %v", a.casWant, rc)
		}
	}
	if len(a.casWant) != 2 {
		t.Fatalf("casWant = %v, want exactly the two target cells", a.casWant)
	}
	if len(a.flash) != 0 {
		t.Fatalf("flash = %v, want none: the piece vibrates instead of flashing", a.flash)
	}
}

func TestCASRejectionWithoutATargetKeepsTheRainbowOnThePiece(t *testing.T) {
	a := newTestApp()
	e := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	// A lost spawn, lock or gravity step: nothing was headed anywhere.
	pumpOne(t, a, e, engine.EngineUpdate{
		Kind:       engine.UpdateCASFlash,
		FlashCells: [][2]int{{5, 4}, {5, 5}},
	})

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.casKickAt.IsZero() {
		t.Fatal("a rejected write must kick the recoil, target or not")
	}
	if len(a.casWant) != 0 {
		t.Fatalf("casWant = %v, want none: there was no target to point at", a.casWant)
	}
	for _, rc := range [][2]int{{5, 4}, {5, 5}} {
		if _, ok := a.flash[rc]; !ok {
			t.Fatalf("flash %v misses the rejected cell %v", a.flash, rc)
		}
	}
}

func TestSpectatorFlashNeitherKicksNorBlinks(t *testing.T) {
	a := newTestApp()
	e := engine.New(nil, "g1", "watcher", "", config.ModeCompetitive, engine.ModeSpectator, 0, 0, 0)
	pumpOne(t, a, e, engine.EngineUpdate{
		Kind:             engine.UpdateCASFlash,
		FlashCells:       [][2]int{{5, 4}},
		FlashTargetCells: [][2]int{{5, 3}},
		FlashPlayerIdx:   2,
	})

	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.casKickAt.IsZero() || len(a.casWant) != 0 {
		t.Fatal("the recoil is the local player's own: a watched board never gets it")
	}
	if _, ok := a.specFlash[2][[2]int{5, 4}]; !ok {
		t.Fatalf("specFlash = %v, want player 2's rejected cell", a.specFlash)
	}
}

// The recoil settles: it is still outside its window (an idle zero epoch
// included), moves inside it, and decays as it runs.
func TestCASKickOffset(t *testing.T) {
	const cell = 32
	if got := casKickOffset(cell, -time.Millisecond); got != (image.Point{}) {
		t.Fatalf("offset before the kick = %v, want none", got)
	}
	if got := casKickOffset(cell, casKickDur); got != (image.Point{}) {
		t.Fatalf("offset at the end of the kick = %v, want none", got)
	}
	if got := casKickOffset(cell, time.Since(time.Time{})); got != (image.Point{}) {
		t.Fatalf("offset at the idle zero epoch = %v, want none", got)
	}
	moved := false
	var early, late int
	for i := 0; i < 60; i++ {
		since := time.Duration(i) * casKickDur / 60
		off := casKickOffset(cell, since)
		if off != (image.Point{}) {
			moved = true
		}
		if abs(off.X) > early && since < casKickDur/4 {
			early = abs(off.X)
		}
		if abs(off.X) > late && since > 3*casKickDur/4 {
			late = abs(off.X)
		}
	}
	if !moved {
		t.Fatal("the piece never moved during the kick")
	}
	if late >= early {
		t.Fatalf("the kick must decay: %d px late vs %d px early", late, early)
	}
}

// The vibrating cells are the piece as the board DRAWS it — every active cell
// of the local player's, and nobody else's.
func TestActivePieceCells(t *testing.T) {
	snap := sampleBoard()
	got := activePieceCells(snap, 0)
	want := [][2]int{{3, 4}, {4, 3}, {4, 4}, {4, 5}}
	if len(got) != len(want) {
		t.Fatalf("active cells = %v, want %v", got, want)
	}
	for _, rc := range want {
		if !got[rc] {
			t.Fatalf("active cells %v misses %v", got, rc)
		}
	}
	if got := activePieceCells(snap, 1); got != nil {
		t.Fatalf("player 1 has no piece on this board, got %v", got)
	}
	if got := activePieceCells(engine.BoardSnapshot{Width: 10, Height: 20}, 0); got != nil {
		t.Fatalf("empty board: active cells = %v, want none", got)
	}
}

// The recoil and the blinking outline are pure paint: the board reports the
// same size mid-vibration as at rest, whatever the frame's judder.
func TestRecoilIsPaintOnly(t *testing.T) {
	snap := sampleBoard()
	const cell = 24
	now := time.Now()
	var ops op.Ops
	draw := func(fx *boardFX, at time.Time) layout.Dimensions {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
		}
		return drawBoard(gtx, snap, 0, cell, true, fx, at)
	}
	rest := draw(&boardFX{}, now)
	fx := &boardFX{
		kick:   activePieceCells(snap, 0),
		kickAt: now,
		want:   map[[2]int]time.Time{{4, 2}: now, {5, 3}: now},
	}
	for i := 0; i < 24; i++ {
		at := now.Add(time.Duration(i) * casKickDur / 12) // runs past both windows
		if got := draw(fx, at); got != rest {
			t.Fatalf("board size mid-recoil = %v, at rest %v", got, rest)
		}
	}
}

// A game screen laying out with a live recoil, in both display positions.
func TestGameScreenLaysOutDuringRecoil(t *testing.T) {
	for _, v := range []string{labSync, labAsync} {
		a := newTestApp()
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.eng.SeedActivePiece(game.Piece{Type: game.PieceL, Row: 2, Col: 3})
		a.labEnum.Value = v
		a.casKickAt = time.Now()
		a.casWant = map[[2]int]time.Time{{2, 2}: a.casKickAt}
		renderOnce(t, a)
	}
}
