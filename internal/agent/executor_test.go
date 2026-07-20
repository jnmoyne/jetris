package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"jetricks/internal/engine"
	"jetricks/internal/game"
)

// fakeMover applies moves synchronously to a local playfield with the same
// rules the engine uses (Rotate/CanPlace/HardDropDestination), standing in for
// the NATS round-trip. Everything runs on the test goroutine.
type fakeMover struct {
	pf         *game.Playfield
	playerIdx  int
	pieceIdx   uint64
	mode       engine.Mode
	rejectSpin bool      // reject all rotations (stall scenario)
	onDispatch func(int) // called before applying the nth dispatch
	dispatches int
}

func newFakeMover(pf *game.Playfield, playerIdx int) *fakeMover {
	return &fakeMover{pf: pf, playerIdx: playerIdx, mode: engine.ModePlayer}
}

func (f *fakeMover) apply(mv engine.MoveType) {
	f.dispatches++
	if f.onDispatch != nil {
		f.onDispatch(f.dispatches)
	}
	p := f.pf.ActivePieceForPlayer(f.playerIdx)
	if p == nil {
		return
	}
	switch mv {
	case engine.MoveLeft, engine.MoveRight:
		q := *p
		if mv == engine.MoveLeft {
			q.Col--
		} else {
			q.Col++
		}
		if game.CanPlace(q, f.pf) {
			f.pf.SetActivePieceForPlayer(q, f.playerIdx)
		}
	case engine.RotateCW, engine.RotateCCW:
		if f.rejectSpin {
			return
		}
		if q, ok := game.Rotate(*p, mv == engine.RotateCW, f.pf); ok {
			f.pf.SetActivePieceForPlayer(q, f.playerIdx)
		}
	case engine.MoveHardDrop:
		dest := game.HardDropDestination(*p, f.pf)
		f.pf.ClearActiveCellsForPlayer(f.playerIdx)
		for _, c := range dest.Cells() {
			f.pf.Rows[c[0]].Cells[c[1]] = game.Cell{Occupied: true, PieceType: dest.Type, PlayerIdx: f.playerIdx}
		}
		f.pieceIdx++
	}
}

func (f *fakeMover) MoveLeft()  { f.apply(engine.MoveLeft) }
func (f *fakeMover) MoveRight() { f.apply(engine.MoveRight) }
func (f *fakeMover) RotateCW()  { f.apply(engine.RotateCW) }
func (f *fakeMover) RotateCCW() { f.apply(engine.RotateCCW) }
func (f *fakeMover) HardDrop()  { f.apply(engine.MoveHardDrop) }

func (f *fakeMover) Playfield() *game.Playfield { return f.pf.Clone() }
func (f *fakeMover) PieceIdx() uint64           { return f.pieceIdx }
func (f *fakeMover) PlayerIdx() int             { return f.playerIdx }
func (f *fakeMover) Mode() engine.Mode          { return f.mode }

// fastTuning keeps executor timeouts tiny so failure paths resolve quickly.
func fastTuning() Tuning {
	return Tuning{MoveTimeout: 20 * time.Millisecond, DropTimeout: 50 * time.Millisecond}
}

// The executor must reach every enumerated placement on a fresh board.
func TestExecuteReachesTarget(t *testing.T) {
	base := newBoard()
	spawn := game.SpawnPiece(game.PieceJ, base.Width)

	for _, cand := range enumerate(base, Rules{}, spawn) {
		pf := base.Clone()
		pf.SetActivePieceForPlayer(spawn, 0)
		f := newFakeMover(pf, 0)

		err := Execute(context.Background(), f, Placement{Target: cand.dest}, fastTuning(), 0, 0)
		if err != nil {
			t.Fatalf("Execute to %+v: %v", cand.dest, err)
		}
		// The piece must be locked exactly at the target cells.
		for _, c := range cand.dest.Cells() {
			cell := f.pf.Rows[c[0]].Cells[c[1]]
			if !cell.Occupied || cell.Active {
				t.Fatalf("target %+v: cell %v not locked", cand.dest, c)
			}
		}
		if f.pieceIdx != 1 {
			t.Fatalf("target %+v: pieceIdx = %d, want 1", cand.dest, f.pieceIdx)
		}
	}
}

// A move that never takes effect must surface as ErrStalled, not a hang.
func TestExecuteStalls(t *testing.T) {
	pf := newBoard()
	spawn := game.SpawnPiece(game.PieceT, pf.Width)
	pf.SetActivePieceForPlayer(spawn, 0)
	f := newFakeMover(pf, 0)
	f.rejectSpin = true

	target := spawn
	target.Orientation = 1 // requires a rotation that will never apply
	err := Execute(context.Background(), f, Placement{Target: target}, fastTuning(), 0, 0)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled", err)
	}
}

// Garbage landing mid-script must abort with ErrBoardChanged so the caller
// re-plans against the risen stack.
func TestExecuteDetectsGarbage(t *testing.T) {
	pf := newBoard()
	spawn := game.SpawnPiece(game.PieceT, pf.Width)
	pf.SetActivePieceForPlayer(spawn, 0)
	f := newFakeMover(pf, 0)
	f.onDispatch = func(n int) {
		if n == 2 { // garbage arrives while the script is under way
			garbageRow(f.pf, f.pf.Height-1)
		}
	}

	target := spawn
	target.Col = 0
	err := Execute(context.Background(), f, Placement{Target: target}, fastTuning(), 0, 0)
	if !errors.Is(err, ErrBoardChanged) {
		t.Fatalf("err = %v, want ErrBoardChanged", err)
	}
}

// Leaving player mode mid-script must abort with ErrGameOver.
func TestExecuteDetectsGameOver(t *testing.T) {
	pf := newBoard()
	spawn := game.SpawnPiece(game.PieceT, pf.Width)
	pf.SetActivePieceForPlayer(spawn, 0)
	f := newFakeMover(pf, 0)
	f.onDispatch = func(n int) {
		if n == 2 {
			f.mode = engine.ModeGameOver
		}
	}

	target := spawn
	target.Col = 0
	err := Execute(context.Background(), f, Placement{Target: target}, fastTuning(), 0, 0)
	if !errors.Is(err, ErrGameOver) {
		t.Fatalf("err = %v, want ErrGameOver", err)
	}
}

// A piece locked by gravity before the script finishes counts as done.
func TestExecuteAcceptsGravityLock(t *testing.T) {
	pf := newBoard()
	spawn := game.SpawnPiece(game.PieceT, pf.Width)
	pf.SetActivePieceForPlayer(spawn, 0)
	f := newFakeMover(pf, 0)
	f.onDispatch = func(n int) {
		if n == 2 { // simulate gravity locking the piece elsewhere
			p := f.pf.ActivePieceForPlayer(0)
			dest := game.HardDropDestination(*p, f.pf)
			f.pf.ClearActiveCellsForPlayer(0)
			for _, c := range dest.Cells() {
				f.pf.Rows[c[0]].Cells[c[1]] = game.Cell{Occupied: true, PieceType: dest.Type}
			}
			f.pieceIdx++
		}
	}

	target := spawn
	target.Col = 0
	if err := Execute(context.Background(), f, Placement{Target: target}, fastTuning(), 0, 0); err != nil {
		t.Fatalf("err = %v, want nil (gravity lock is success)", err)
	}
}
