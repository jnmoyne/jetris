package nativeui

// A rejected write is told in two halves (bridge.go, effects.go, drawBoard):
// the piece SNAPS BACK from where the optimistic board drew it and VIBRATES
// where the CAS failure put it, and the outline BLINKS where the lost step
// wanted it — the move that was taken away, drawn where it would have gone. A
// rejection with nothing headed anywhere (a lost spawn, lock or gravity step)
// keeps the plain rainbow border on the piece's cells. The recoil ends early
// the moment the board draws the piece somewhere else.

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

// The full recoil: for casSnapDur the piece is painted on its way back from
// `from` — starting exactly there, speeding up, arriving at rest — and then
// buzzes along the line it came in on, dying down to nothing by casRecoilDur.
// With nowhere to come from it is the plain buzz from the first frame.
func TestCASRecoilOffset(t *testing.T) {
	const cell = 32
	from := [2]float64{-1, 0} // drawn a column to the LEFT of where it stands now
	if got := casRecoilOffset(cell, from, 0); got != image.Pt(-cell, 0) {
		t.Fatalf("snap-back starts at %v, want a whole cell to the left", got)
	}
	prev := -cell
	for i := 1; i <= 4; i++ {
		since := time.Duration(i) * casSnapDur / 4
		got := casRecoilOffset(cell, from, since-time.Nanosecond)
		if got.Y != 0 {
			t.Fatalf("a horizontal snap-back drifted vertically: %v at %v", got, since)
		}
		if got.X < prev {
			t.Fatalf("snap-back went backwards: %d px after %d px", got.X, prev)
		}
		if step := got.X - prev; i > 1 && step <= 0 {
			t.Fatalf("snap-back must keep moving: step %d px at %v", step, since)
		}
		prev = got.X
	}
	if got := casRecoilOffset(cell, from, casSnapDur); got.X < -cell/6-1 || got.X > cell/6+1 {
		t.Fatalf("at the end of the snap the piece must be home (buzz amplitude at most): %v", got)
	}
	buzzed := false
	for i := 0; i < 60; i++ {
		since := casSnapDur + time.Duration(i)*casKickDur/60
		got := casRecoilOffset(cell, from, since)
		if got.Y != 0 {
			t.Fatalf("the buzz must run along the snap-back's line: %v at %v", got, since)
		}
		if got.X != 0 {
			buzzed = true
		}
	}
	if !buzzed {
		t.Fatal("the piece never buzzed after snapping back")
	}
	if got := casRecoilOffset(cell, from, casRecoilDur); got != (image.Point{}) {
		t.Fatalf("offset at the end of the recoil = %v, want none", got)
	}
	if got := casRecoilOffset(cell, from, -time.Millisecond); got != (image.Point{}) {
		t.Fatalf("offset before the kick = %v, want none", got)
	}
	// Nowhere to come from: the buzz, from the first frame.
	for i := 0; i < 30; i++ {
		since := time.Duration(i) * casKickDur / 30
		if got, want := casRecoilOffset(cell, [2]float64{}, since), casKickOffset(cell, since); got != want {
			t.Fatalf("with no snap-back the recoil = %v at %v, want the plain buzz %v", got, since, want)
		}
	}
}

// The layout runs the recoil against the piece as the board draws it: a kick
// fixes it on the piece where the rejection put it, snapping back from where
// the frame before drew it, and the first frame that draws the piece anywhere
// else ends it — for good, even back on the cells it buzzed on.
func TestRecoilFollowsTheDrawnPieceAndStopsWhenItMoves(t *testing.T) {
	boardWith := func(p game.Piece) engine.BoardSnapshot {
		snap := engine.BoardSnapshot{Width: 10, Height: 16, Rows: make([]game.Row, 16)}
		for r := range snap.Rows {
			snap.Rows[r] = game.Row{Cells: make([]game.Cell, 10)}
		}
		for _, rc := range p.Cells() {
			snap.Rows[rc[0]].Cells[rc[1]] = game.Cell{Active: true, PieceType: p.Type, PlayerIdx: 0}
		}
		return snap
	}
	a := newTestApp()
	frame := func(at time.Time) C {
		var ops op.Ops
		return C{Ops: &ops, Now: at, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(600, 800))}
	}
	base := time.Now()
	steered := game.Piece{Type: game.PieceT, Row: 3, Col: 2}  // the optimistic position the frames drew
	fallback := game.Piece{Type: game.PieceT, Row: 3, Col: 3} // where the rejection puts it
	moved := game.Piece{Type: game.PieceT, Row: 4, Col: 3}    // the player moving on

	if cells, _ := a.trackRecoil(frame(base), boardWith(steered), 0, time.Time{}); cells != nil {
		t.Fatalf("no kick, yet the recoil is on: %v", cells)
	}
	kick := base.Add(10 * time.Millisecond)
	at := kick.Add(5 * time.Millisecond)
	cells, from := a.trackRecoil(frame(at), boardWith(fallback), 0, kick)
	if !sameCells(cells, activePieceCells(boardWith(fallback), 0)) {
		t.Fatalf("recoil cells = %v, want the piece where the rejection put it", cells)
	}
	if from != ([2]float64{-1, 0}) {
		t.Fatalf("snap-back from %v, want a column to the left, where the frame before drew it", from)
	}
	at = at.Add(16 * time.Millisecond)
	if cells, from = a.trackRecoil(frame(at), boardWith(fallback), 0, kick); cells == nil || from != ([2]float64{-1, 0}) {
		t.Fatalf("the recoil must hold while the piece stands: cells %v from %v", cells, from)
	}
	at = at.Add(16 * time.Millisecond)
	if cells, _ = a.trackRecoil(frame(at), boardWith(moved), 0, kick); cells != nil {
		t.Fatalf("the piece moved on, yet the recoil is still on: %v", cells)
	}
	at = at.Add(16 * time.Millisecond)
	if cells, _ = a.trackRecoil(frame(at), boardWith(fallback), 0, kick); cells != nil {
		t.Fatalf("a recoil cut short must stay off, got %v", cells)
	}
	// A fresh kick starts a fresh recoil; a piece that has stood still for a
	// while came from nowhere.
	kick2 := at.Add(300 * time.Millisecond)
	if cells, from = a.trackRecoil(frame(kick2.Add(time.Millisecond)), boardWith(fallback), 0, kick2); cells == nil || from != ([2]float64{}) {
		t.Fatalf("a fresh kick on a piece that never left: cells %v from %v, want the buzz from nowhere", cells, from)
	}
	// And a recoil outlives nothing: past casRecoilDur it is over, moved or not.
	if cells, _ = a.trackRecoil(frame(kick2.Add(casRecoilDur)), boardWith(fallback), 0, kick2); cells != nil {
		t.Fatalf("the recoil ran past its window: %v", cells)
	}
}
