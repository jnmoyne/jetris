package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// Gravity is a clock that follows the level in every mode (move.go
// runInput): its rows are queued as MoveGravity steps — as many as the piece
// can fall — aggregated with the player's moves, replayed if their batch is
// lost, and never queued into another player's falling piece.

// The level starts at 1 (game.MinLevel) in every mode, before any clear.
func TestLevelStartsAtOne(t *testing.T) {
	for _, mode := range []config.GameMode{config.ModeCompetitive, config.ModeCooperative, config.ModeTeams} {
		e := New(nil, "g", "me", "", mode, ModePlayer, 0, 0, 0)
		if e.Level() != game.MinLevel {
			t.Errorf("mode %v: level = %d at the start, want %d", mode, e.Level(), game.MinLevel)
		}
	}
}

// Own lines drive the level in competitive too: ten of them make level 2.
func TestCompetitiveLevelFollowsOwnLines(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	e.totalLines.Store(25)
	e.refreshLevel()
	if e.Level() != 3 {
		t.Fatalf("level = %d after 25 own lines, want 3", e.Level())
	}
	select {
	case <-e.levelChanged:
	default:
		t.Fatal("refreshLevel did not tell the gravity clock the level moved")
	}
}

func gravityQueued(e *Engine) (gravity, others int) {
	for _, m := range e.BufferedMoves() {
		if m == MoveGravity {
			gravity++
		} else {
			others++
		}
	}
	return gravity, others
}

// queueGravity queues the rows the clock owes, as many as the piece can
// still fall — measured from where the queued rows already take it — and
// nothing once it rests on the stack or the floor.
func TestQueueGravityTakesTheRoom(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.SeedActivePiece(game.Piece{Type: game.PieceO, Row: 2, Col: 4})
	if n := e.queueGravity(3); n != 3 {
		t.Fatalf("queued %d rows, want 3", n)
	}
	if n := e.queueGravity(2); n != 2 {
		t.Fatalf("queued %d more rows, want 2: the piece as headed still has room", n)
	}
	if g, o := gravityQueued(e); g != 5 || o != 0 {
		t.Fatalf("queue holds %d gravity rows and %d other moves, want 5 and 0", g, o)
	}
	p, ok := e.IntentPiece()
	if !ok || p.Row != 2+5 {
		t.Fatalf("intent piece at row %d (ok %v), want %d: the queued rows are played out", p.Row, ok, 2+5)
	}

	floor := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	h := floor.Playfield().Height
	floor.SeedActivePiece(game.Piece{Type: game.PieceO, Row: h - 2, Col: 4}) // an O piece: two rows, resting on the floor
	if n := floor.queueGravity(3); n != 0 {
		t.Fatalf("queued %d rows for a piece on the floor, want 0", n)
	}
}

// Another player's falling piece blocks gravity: nothing queues into it, and
// the rows fall once it is gone.
func TestQueueGravityWaitsOnTeammatePiece(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.SeedActivePiece(game.Piece{Type: game.PieceO, Row: 2, Col: 4})
	w := e.Playfield().Width
	block := func(active bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		for c := 0; c < w; c++ {
			cell := game.Cell{}
			if active {
				cell = game.Cell{Active: true, PieceType: game.PieceO, PlayerIdx: 1, AnchorRow: 4, AnchorCol: c}
			}
			e.playfield.Apply(4, c, cell, 0) // seq 0: past the replica's same-or-lower guard, and no CAS expectation raised
		}
	}
	block(true)
	if n := e.queueGravity(3); n != 0 {
		t.Fatalf("queued %d rows into a teammate's piece, want 0", n)
	}
	block(false)
	if n := e.queueGravity(3); n != 3 {
		t.Fatalf("queued %d rows once the teammate's piece left, want 3", n)
	}
}

// A hard drop or a hold in the queue decides the piece's fate: no row of
// gravity queues behind it (it would fall on the next piece).
func TestQueueGravityNotBehindBarrier(t *testing.T) {
	for _, barrier := range []MoveType{MoveHardDrop, MoveHold} {
		e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
		e.SeedActivePiece(game.Piece{Type: game.PieceO, Row: 2, Col: 4})
		e.dispatch(barrier)
		if n := e.queueGravity(2); n != 0 {
			t.Fatalf("queued %d rows behind %v, want 0", n, barrier)
		}
	}
}

// dropQueuedGravity forgets the rows queued for a piece that is gone (a
// spawn, a hold) and keeps the player's moves.
func TestDropQueuedGravityKeepsPlayerMoves(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.SeedActivePiece(game.Piece{Type: game.PieceO, Row: 2, Col: 4})
	if n := e.queueGravity(3); n != 3 {
		t.Fatalf("queued %d rows, want 3", n)
	}
	e.dispatch(MoveLeft)
	if e.QueuedPlayerMoves() != 1 {
		t.Fatalf("QueuedPlayerMoves = %d, want 1: gravity rows are not the player's", e.QueuedPlayerMoves())
	}
	e.dropQueuedGravity()
	if got := e.BufferedMoves(); len(got) != 1 || got[0] != MoveLeft {
		t.Fatalf("queue after the purge = %v, want [MoveLeft]", got)
	}
}

// An ack that lands a batch elsewhere than predicted re-chains the guesses
// of the steps still in flight from the actual sequence, so the next step's
// expectations on their cells — and its own guess — start from the truth.
func TestResolveStepRebasesPredictions(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.SetPublishMode(PublishOptimistic)
	cellAt := func(r, c int) game.CellPos { return game.CellPos{Row: r, Col: c} }
	first := &inflightStep{
		keys:    []game.CellPos{cellAt(5, 1), cellAt(5, 2), cellAt(4, 1)},
		cells:   map[game.CellPos]game.Cell{cellAt(5, 1): {Active: true}, cellAt(5, 2): {Active: true}, cellAt(4, 1): {}},
		predEnd: 10, // predicted 8, 9, 10
	}
	second := &inflightStep{
		keys:    []game.CellPos{cellAt(6, 1), cellAt(5, 1)},
		cells:   map[game.CellPos]game.Cell{cellAt(6, 1): {Active: true}, cellAt(5, 1): {}},
		predEnd: 12, // predicted 11, 12 — chained off the first's guess
	}
	e.mu.Lock()
	e.inflight = []*inflightStep{first, second}
	for _, s := range e.inflight {
		for i, k := range s.keys {
			e.inflightCells[k]++
			e.inflightSeq[k] = s.predEnd - uint64(len(s.keys)-1-i)
		}
	}
	e.pipePredictedEnd = 12
	e.mu.Unlock()

	// The first batch really landed at 13, 14, 15: five messages of other
	// traffic came first.
	e.resolveStep(first, 15, nil)

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.inflight) != 1 || e.inflight[0] != second {
		t.Fatalf("in flight = %d steps, want the second alone", len(e.inflight))
	}
	if second.predEnd != 17 {
		t.Fatalf("second step's predicted end = %d, want 17 (re-chained from 15)", second.predEnd)
	}
	if e.pipePredictedEnd != 17 {
		t.Fatalf("pipePredictedEnd = %d, want 17", e.pipePredictedEnd)
	}
	if got := e.inflightSeq[cellAt(6, 1)]; got != 16 {
		t.Fatalf("predicted sequence of the second step's first cell = %d, want 16", got)
	}
	if got := e.inflightSeq[cellAt(5, 1)]; got != 17 {
		t.Fatalf("predicted sequence of the cell both steps wrote = %d, want the second's 17", got)
	}
	if got := e.playfield.CellLastSeq(5, 2); got != 14 {
		t.Fatalf("acked replica's sequence for the first step's second cell = %d, want 14 (from the ack)", got)
	}
	if _, guessed := e.inflightSeq[cellAt(5, 2)]; guessed {
		t.Fatal("a cell no step has in flight anymore still carries a guess")
	}
}

// ackedRow is the row of the player's piece on the acked replica.
func ackedRow(e *Engine) int {
	p := e.Playfield().ActivePieceForPlayer(0)
	if p == nil {
		return -1
	}
	return p.Row
}

// A level change re-arms the clock at once: at level 11 (43 ms a row) the
// piece falls several rows in the second a level-1 tick would have taken.
func TestLevelChangeReArmsGravity(t *testing.T) {
	e, _, _ := startedEngine(t)
	r0 := ackedRow(e)
	e.totalLines.Store(100)
	e.refreshLevel()
	if e.Level() != 11 {
		t.Fatalf("level = %d, want 11", e.Level())
	}
	waitUntil(t, time.Second, func() bool { return ackedRow(e) >= r0+5 }, "the piece to fall five rows within a second at level 11")
}

// Competitive gravity follows the player's own level: an engine on its own
// board falls at level 11's speed once its own lines are there.
func TestCompetitiveGravityFollowsOwnLevel(t *testing.T) {
	e := slowCompetitiveEngine(t, "gravity-competitive", 0)
	r0 := ackedRow(e)
	e.totalLines.Store(100)
	e.refreshLevel()
	waitUntil(t, time.Second, func() bool { return ackedRow(e) >= r0+5 }, "the competitive piece to fall five rows within a second at level 11")
}

// The rows that come due while a batch is in flight queue up as ONE group
// of MoveGravity — one batch when the slot frees — and take the piece down
// as many rows at once.
func TestOwedGravityRowsAggregate(t *testing.T) {
	e, _, _ := startedEngine(t)
	r0 := ackedRow(e)
	release := make(chan struct{})
	var sent atomic.Int32
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) { sent.Add(1) }
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishAsync)
	e.SetInflightLimit(1)
	e.totalLines.Store(100) // level 11: 43 ms a row
	e.refreshLevel()

	e.MoveLeft() // the batch in flight, its ack held
	waitUntil(t, 2*time.Second, func() bool {
		g, o := gravityQueued(e)
		return sent.Load() == 1 && g >= 3 && o == 0 && len(e.BufferedBatches()) == 1
	}, "three or more rows of gravity queued as one group behind the held batch")
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return ackedRow(e) >= r0+3 }, "the aggregated rows to land")
}

// A batch of gravity lost to a CAS race is replayed at once — the row lands
// well before the next tick would have moved the piece — and, carrying no
// player move, flashes nothing.
func TestLostGravityRowIsRetried(t *testing.T) {
	e, _, _ := startedEngine(t)
	ctx := context.Background()
	start := *e.Playfield().ActivePieceForPlayer(0)
	one := start
	one.Row++
	guarded := cellsMinus(one.Cells(), start.Cells())
	if len(guarded) == 0 {
		t.Fatal("no CAS-guarded cell on the first row of gravity")
	}
	empty, _ := game.Cell{}.Marshal()

	flashes := make(chan EngineUpdate, 16)
	drainCtx, drainCancel := context.WithCancel(ctx)
	defer drainCancel()
	go func() {
		for {
			select {
			case <-drainCtx.Done():
				return
			case u := <-e.Updates:
				if u.Kind == UpdateCASFlash {
					flashes <- u
				}
			}
		}
	}()

	var lossAt atomic.Value
	var sent atomic.Int32
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) {
		if sent.Add(1) != 1 {
			return
		}
		// The first batch is the first tick's row: land a competing write
		// on a cell it guards before its commit goes out.
		for _, rc := range guarded {
			if _, err := e.js.Publish(ctx, e.cellSubject(rc[0], rc[1]), empty); err != nil {
				t.Error(err)
			}
		}
		lossAt.Store(time.Now())
	}
	e.SetPublishMode(PublishAsync)

	waitUntil(t, 3*time.Second, func() bool { return ackedRow(e) >= start.Row+1 && e.InflightSteps() == 0 && !e.PipelineBroken() }, "the lost row of gravity to be replayed and land")
	at, _ := lossAt.Load().(time.Time)
	if at.IsZero() {
		t.Fatal("the first batch never went out")
	}
	if since := time.Since(at); since > 700*time.Millisecond {
		t.Fatalf("the row landed %v after the loss: the next tick's work, not a replay", since)
	}
	select {
	case u := <-flashes:
		t.Fatalf("a flash %v for a lost row of gravity: only a player's move flashes", u.FlashTargetCells)
	default:
	}
}

// A piece resting on another player's falling piece waits: it neither falls
// nor locks (the lock delay is not for a transient obstacle), and falls once
// the obstacle is gone.
func TestTeammatePieceBlocksGravityWithoutLocking(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	bottom := 0
	for _, c := range start.Cells() {
		bottom = max(bottom, c[0])
	}
	w := e.Playfield().Width
	block := func(active bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		for c := 0; c < w; c++ {
			cell := game.Cell{}
			if active {
				cell = game.Cell{Active: true, PieceType: game.PieceO, PlayerIdx: 1, AnchorRow: bottom + 1, AnchorCol: c}
			}
			e.playfield.Apply(bottom+1, c, cell, 0) // seq 0: past the replica's same-or-lower guard, and no CAS expectation raised
		}
	}
	block(true)
	e.totalLines.Store(100) // level 11: 43 ms a row
	e.refreshLevel()

	time.Sleep(3 * config.LockDelay)
	snap := e.Snapshot()
	if p := snapshotPiece(snap, 0); p == nil || p.Row != start.Row || activeCellCount(snap, 0) != 4 {
		t.Fatalf("piece = %v with %d active cells, want it waiting at row %d, unlocked", p, activeCellCount(snap, 0), start.Row)
	}
	block(false)
	waitUntil(t, time.Second, func() bool { return ackedRow(e) >= start.Row+1 }, "the piece to fall once the teammate's piece is gone")
}
