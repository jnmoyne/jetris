package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// snapshotPiece is the player's piece as a BoardSnapshot shows it.
func snapshotPiece(snap BoardSnapshot, playerIdx int) *game.Piece {
	pf := &game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows}
	return pf.ActivePieceForPlayer(playerIdx)
}

// activeCellCount counts the player's active cells on a snapshot.
func activeCellCount(snap BoardSnapshot, playerIdx int) int {
	n := 0
	for _, row := range snap.Rows {
		for _, c := range row.Cells {
			if c.Active && c.PlayerIdx == playerIdx {
				n++
			}
		}
	}
	return n
}

// cellsMinus is the cells of a that no b has.
func cellsMinus(a [][2]int, bs ...[][2]int) [][2]int {
	var out [][2]int
	for _, c := range a {
		skip := false
		for _, b := range bs {
			for _, d := range b {
				if c == d {
					skip = true
				}
			}
		}
		if !skip {
			out = append(out, c)
		}
	}
	return out
}

func sameCells(a, b [][2]int) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[[2]int]bool, len(a))
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			return false
		}
	}
	return true
}

// startedEngine is setupEngine + Start, with the first piece on the board.
func startedEngine(t *testing.T) (*Engine, jetstream.JetStream, string) {
	t.Helper()
	e, js, gameID := setupEngine(t)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	waitUntil(t, 5*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")
	return e, js, gameID
}

// TestAsyncStepsLandInOrder: with the batches pipelined a burst of moves goes
// out without waiting on any ack, lands in order on the acked replica, and
// the consumer's echo converges to the same piece — nothing lost, nothing to
// repair.
func TestAsyncStepsLandInOrder(t *testing.T) {
	e, _, _ := startedEngine(t)
	e.SetPublishMode(PublishAsync)
	if e.PublishMode() != PublishAsync {
		t.Fatal("SetPublishMode did not stick")
	}
	start := *e.Playfield().ActivePieceForPlayer(0)

	e.MoveLeft()
	e.MoveLeft()
	e.MoveRight()
	e.MoveDown()
	want := start
	want.Col--
	want.Row++
	// Column is the deterministic part: a gravity tick may add a row.
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && e.InflightSteps() == 0
	}, "the burst to land on the acked board")
	waitUntil(t, 5*time.Second, func() bool {
		p := snapshotPiece(e.EchoSnapshot(), 0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row
	}, "the echo to converge")
	if e.PipelineBroken() {
		t.Fatal("pipeline broken after a clean burst")
	}
}

// TestAsyncLostStepRollsBackAndRepairs: three pipelined lefts, the second of
// which loses its CAS race (a competing write to a cell it guards, landed
// just before its commit) while the third — sent before that ack came back,
// on the assumption the second committed — goes through anyway. The engine
// flashes the outline of where the lost step wanted to be, drops it and the
// step behind it, and repairs the board: the piece back where it stood after
// the first left, no stray cell left, on the acked replica and in the stream.
func TestAsyncLostStepRollsBackAndRepairs(t *testing.T) {
	e, _, _ := startedEngine(t)
	ctx := context.Background()
	start := *e.Playfield().ActivePieceForPlayer(0)
	one, two := start, start
	one.Col--
	two.Col -= 2
	// The cells the second left writes with a CAS expectation: new to it,
	// and not in flight from the first (which wrote the first left's cells
	// and vacated the spawn's).
	guarded := cellsMinus(two.Cells(), one.Cells(), start.Cells())
	if len(guarded) == 0 {
		t.Fatal("no CAS-guarded cell on the second left")
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

	var sent atomic.Int32
	release := make(chan struct{})
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) {
		switch sent.Add(1) {
		case 2:
			// Land a competing write on the second left's guarded cells
			// before its commit goes out: the expectation it carries is
			// already built, so the batch is rejected.
			for _, rc := range guarded {
				if _, err := e.js.Publish(ctx, e.cellSubject(rc[0], rc[1]), empty); err != nil {
					t.Error(err)
				}
			}
		case 3:
			close(release) // the third left is out: let the acks through
		}
	}
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishAsync)

	e.MoveLeft()
	e.MoveLeft()
	e.MoveLeft()

	select {
	case u := <-flashes:
		if !sameCells(u.FlashTargetCells, two.Cells()) {
			t.Fatalf("flash target = %v, want the lost step's target %v", u.FlashTargetCells, two.Cells())
		}
		if !sameCells(u.FlashCells, one.Cells()) {
			t.Fatalf("flash cells = %v, want the piece as it stood %v", u.FlashCells, one.Cells())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no CAS flash for the lost step")
	}
	// The repair puts the piece back after the first left, whole, with no
	// stray cell from the poisoned third left — and then REPLAYS that third
	// left from there: the player loses exactly the lost move, so the piece
	// ends two columns left of the spawn (which happens to be where the lost
	// step was headed), on the acked replica and, once echoed, on the
	// stream's own. (A gravity tick may have added a row meanwhile; the
	// column is what counts.)
	waitUntil(t, 5*time.Second, func() bool {
		if e.PipelineBroken() || e.InflightSteps() != 0 || len(e.BufferedMoves()) != 0 {
			return false
		}
		snap := e.Snapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == two.Col && activeCellCount(snap, 0) == 4
	}, "the acked board to be repaired and the third left replayed")
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.EchoSnapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == two.Col && activeCellCount(snap, 0) == 4
	}, "the stream to converge on the repaired board")
	select {
	case u := <-flashes:
		t.Fatalf("a second flash %v: only the first loss of an episode flashes", u.FlashTargetCells)
	default:
	}
}

// TestHardDropIsABarrier: a hard drop behind pipelined steps waits for their
// acks — the piece lands from where it has been acked to be, never from an
// optimistic position.
func TestHardDropIsABarrier(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishAsync)

	e.MoveLeft()
	e.MoveLeft()
	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return e.InflightSteps() >= 2 && len(e.BufferedMoves()) == 0 }, "both lefts in flight and the drop taken")
	landed := func() bool {
		for _, row := range e.Snapshot().Rows {
			for _, c := range row.Cells {
				if c.Occupied && !c.Active {
					return true
				}
			}
		}
		return false
	}
	// With the acks held the drop must wait.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if landed() {
			t.Fatal("hard drop published while steps were still in flight")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	dest := start
	dest.Col -= 2
	dest = game.HardDropDestination(dest, e.Playfield())
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.Snapshot()
		for _, rc := range dest.Cells() {
			c := snap.Rows[rc[0]].Cells[rc[1]]
			if !c.Occupied || c.Active {
				return false
			}
		}
		return true
	}, "the piece to land two columns left of the spawn")
}

// TestIntentPieceAppliesQueuedMoves: the intent piece is the piece with the
// queued moves played out by the engine's rules — a hard drop lands it and
// ends the look-ahead — and there is none while a lost step is repaired.
func TestIntentPieceAppliesQueuedMoves(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	if _, ok := e.IntentPiece(); ok {
		t.Fatal("intent with no piece on the board")
	}
	p := game.Piece{Type: game.PieceT, Row: 2, Col: 4}
	e.playfield.SetActivePieceForPlayer(p, 0)

	e.MoveLeft()
	e.MoveDown()
	e.RotateCW()
	want := p
	want.Col--
	want.Row++
	want, _ = game.RotateCoop(want, true, e.playfield, 0)
	got, ok := e.IntentPiece()
	if !ok || got != want {
		t.Fatalf("IntentPiece = %+v, %v; want %+v", got, ok, want)
	}

	// A queued hard drop freezes the piece where it stands (the UI's ghost
	// marks the landing); the moves behind it are the next piece's.
	e.HardDrop()
	e.MoveLeft()
	if got, ok := e.IntentPiece(); !ok || got != want {
		t.Fatalf("IntentPiece with a drop queued = %+v, %v; want the piece where it stands %+v", got, ok, want)
	}
	// And so does a drop being committed, whatever the queue holds.
	e.takeMoveGroup(false) // the left
	e.takeMoveGroup(false) // the down
	e.takeMoveGroup(false) // the rotation
	e.takeMoveGroup(false) // the drop, "in flight"
	e.freezePiece(MoveHardDrop)
	e.MoveRight()
	if got, ok := e.IntentPiece(); !ok || got != p {
		t.Fatalf("IntentPiece while a drop commits = %+v, %v; want the piece where it stands %+v", got, ok, p)
	}
	e.freezePiece(MoveDown)

	// A lost step being repaired: the intent AND the acked piece are the
	// rollback point — the piece is on its way back there — whatever the
	// queue holds and whatever strays the replica shows.
	if got, ok := e.AckedPiece(); !ok || got != p {
		t.Fatalf("AckedPiece = %+v, %v; want the replica's piece %+v", got, ok, p)
	}
	back := game.Piece{Type: game.PieceT, Row: 5, Col: 6}
	e.mu.Lock()
	e.pipeBroken, e.pipeRollback = true, back
	e.mu.Unlock()
	if got, ok := e.IntentPiece(); !ok || got != back {
		t.Fatalf("intent during a repair = %+v, %v; want the rollback point %+v", got, ok, back)
	}
	if got, ok := e.AckedPiece(); !ok || got != back {
		t.Fatalf("AckedPiece during a repair = %+v, %v; want the rollback point %+v", got, ok, back)
	}
}

// TestHeldMovesReprojectAfterRepair: moves held behind a full pipeline when
// a step in it is lost go out only after the repair, computed from the
// repaired board — the rollback point, actual sequences — not from the
// optimistic position the lost step and the poisoned ones behind it had
// promised. The steps: down, LEFT (lost), down, down fill the pipeline; the
// held right, right, right must land from one row down, three columns right.
func TestHeldMovesReprojectAfterRepair(t *testing.T) {
	e, _, _ := startedEngine(t)
	ctx := context.Background()
	start := *e.Playfield().ActivePieceForPlayer(0)
	one := start
	one.Row++ // after the first step
	two := one
	two.Col-- // where the lost step wanted to go
	guarded := cellsMinus(two.Cells(), one.Cells(), start.Cells())
	if len(guarded) == 0 {
		t.Fatal("no CAS-guarded cell on the lost step")
	}
	empty, _ := game.Cell{}.Marshal()

	var sent atomic.Int32
	release := make(chan struct{})
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) {
		if sent.Add(1) == 2 {
			for _, rc := range guarded {
				if _, err := e.js.Publish(ctx, e.cellSubject(rc[0], rc[1]), empty); err != nil {
					t.Error(err)
				}
			}
		}
	}
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishAsync)

	e.MoveDown()
	e.MoveLeft()
	e.MoveDown()
	e.MoveDown()
	e.MoveRight()
	e.MoveRight()
	e.MoveRight()
	waitUntil(t, 5*time.Second, func() bool {
		return e.InflightSteps() == DefaultInflightLimit && len(e.BufferedMoves()) == 3
	}, "the pipeline to fill and the three rights to wait")
	close(release)

	// The lost LEFT is the one move the player pays for. The two downs
	// pipelined behind it are replayed from the repaired board, then the
	// three rights: one row down (the first step), two more (the replay),
	// three columns right — never two columns left of that, which is where
	// the lost step's assumptions would have taken the rights.
	want := one
	want.Row += 2
	want.Col += 3
	waitUntil(t, 5*time.Second, func() bool {
		if e.PipelineBroken() || e.InflightSteps() != 0 || len(e.BufferedMoves()) != 0 {
			return false
		}
		snap := e.Snapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && activeCellCount(snap, 0) == 4
	}, "the replay and the held rights to land from the repaired position")
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.EchoSnapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && activeCellCount(snap, 0) == 4
	}, "the stream to converge")
}

// TestCASFlashCarriesTarget: a lost step's flash names both the piece as it
// stood and where it wanted to be.
func TestCASFlashCarriesTarget(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	pre := [][2]int{{1, 1}, {1, 2}}
	target := [][2]int{{1, 0}, {1, 1}}
	e.emitCASFlash(pre, target)
	select {
	case u := <-e.Updates:
		if u.Kind != UpdateCASFlash || !sameCells(u.FlashCells, pre) || !sameCells(u.FlashTargetCells, target) {
			t.Fatalf("flash = %+v, want cells %v and target %v", u, pre, target)
		}
	default:
		t.Fatal("no flash emitted")
	}
}

// TestOptimisticStepsPredictSequences: in the optimistic mode every cell of
// a pipelined step carries an expectation — the ones a step ahead wrote,
// still un-acked, carry that step's PREDICTED sequence — and on a quiet
// stream (nobody else writing) the predictions hold: a burst of dependent
// steps lands without a lost race.
func TestOptimisticStepsPredictSequences(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	one := start
	one.Col--

	flashes := make(chan EngineUpdate, 16)
	drainCtx, drainCancel := context.WithCancel(context.Background())
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

	// Hold the acks so the steps are certainly pipelined, and record the
	// second step's batch: it vacates cells the first step wrote.
	var sent atomic.Int32
	release := make(chan struct{})
	var second []natspkg.CellUpdate
	e.testHookBeforeStepSend = func(updates []natspkg.CellUpdate) {
		switch sent.Add(1) {
		case 2:
			second = append([]natspkg.CellUpdate(nil), updates...)
		case 3:
			close(release)
		}
	}
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishOptimistic)

	e.MoveLeft()
	e.MoveLeft()
	e.MoveDown()
	waitUntil(t, 5*time.Second, func() bool { return sent.Load() >= 3 }, "the three steps to go out")

	inFlight := make(map[string]bool)
	for _, rc := range cellsMinus(one.Cells(), start.Cells()) {
		inFlight[e.cellSubject(rc[0], rc[1])] = true // written by the first step, un-acked when the second went out
	}
	predicted := 0
	for _, u := range second {
		if u.Expect == natspkg.ExpectNone {
			t.Fatalf("optimistic step sent %s with no expectation", u.Subject)
		}
		if inFlight[u.Subject] {
			if u.ExpectLastSeq == 0 {
				t.Fatalf("in-flight cell %s carries no predicted sequence", u.Subject)
			}
			predicted++
		}
	}
	if predicted == 0 {
		t.Fatal("the second step guarded none of the first step's cells with a prediction")
	}

	want := one
	want.Col--
	want.Row++
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && e.InflightSteps() == 0 && !e.PipelineBroken()
	}, "the burst to land")
	select {
	case u := <-flashes:
		t.Fatalf("a prediction failed on a quiet stream: flash %v", u.FlashTargetCells)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestOptimisticMispredictionRepairs: a message from elsewhere landing in
// the stream just before the first step commits shifts that step's
// sequences off the prediction; the second step's expectations on the cells
// it wrote are therefore stale, the server rejects it, and the loss runs
// through the ordinary rollback — the outline of where the piece wanted to
// be flashes, and the piece settles where the first step put it.
func TestOptimisticMispredictionRepairs(t *testing.T) {
	e, js, gameID := startedEngine(t)
	ctx := context.Background()
	start := *e.Playfield().ActivePieceForPlayer(0)
	one, two := start, start
	one.Col--
	two.Col -= 2

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

	var sent atomic.Int32
	release := make(chan struct{})
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) {
		switch sent.Add(1) {
		case 1:
			// Something else lands in the stream before the first step's
			// commit: its sequences come out one past the prediction.
			if _, err := js.Publish(ctx, config.RosterSubject(gameID, "intruder"), []byte(`{}`)); err != nil {
				t.Error(err)
			}
		case 3:
			close(release)
		}
	}
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishOptimistic)

	e.MoveLeft()
	e.MoveLeft()
	e.MoveLeft()

	select {
	case u := <-flashes:
		if !sameCells(u.FlashTargetCells, two.Cells()) {
			t.Fatalf("flash target = %v, want the mispredicted step's target %v", u.FlashTargetCells, two.Cells())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a misprediction did not surface as a lost step")
	}
	// The mispredicted second left is dropped; the third, pipelined behind
	// it, is replayed from the repaired board: two columns left of the spawn.
	waitUntil(t, 5*time.Second, func() bool {
		if e.PipelineBroken() || e.InflightSteps() != 0 || len(e.BufferedMoves()) != 0 {
			return false
		}
		snap := e.Snapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == two.Col && activeCellCount(snap, 0) == 4
	}, "the acked board to settle after the repair and the replay")
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.EchoSnapshot()
		p := snapshotPiece(snap, 0)
		return p != nil && p.Col == two.Col && activeCellCount(snap, 0) == 4
	}, "the stream to converge")
}

// TestSplitBatches: coalescing groups runs of steps and keeps the barriers
// (a hard drop, a hold) alone; without it every move is its own batch.
func TestSplitBatches(t *testing.T) {
	queue := []MoveType{MoveLeft, MoveRight, MoveHardDrop, MoveDown, RotateCW, MoveHold, MoveDown}
	got := splitBatches(queue, true)
	want := [][]MoveType{{MoveLeft, MoveRight}, {MoveHardDrop}, {MoveDown, RotateCW}, {MoveHold}, {MoveDown}}
	if len(got) != len(want) {
		t.Fatalf("splitBatches = %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("batch %d = %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("batch %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
	if got := splitBatches(queue, false); len(got) != len(queue) {
		t.Fatalf("without coalescing: %d batches, want one per move (%d)", len(got), len(queue))
	}
	if got := splitBatches(nil, true); got != nil {
		t.Fatalf("empty queue: %v, want none", got)
	}

	// BufferedBatches groups in sync mode always (the moves that queue
	// during a round trip go out together), and in a pipelined mode only
	// once the pipeline is at its limit: a transport-less engine with steps
	// parked in flight.
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.MoveLeft()
	e.MoveLeft()
	e.HardDrop()
	if b := e.BufferedBatches(); len(b) != 2 || len(b[0]) != 2 || b[1][0] != MoveHardDrop {
		t.Fatalf("sync engine: %v, want [[left left] [drop]]", b)
	}
	e.SetPublishMode(PublishAsync)
	if b := e.BufferedBatches(); len(b) != 3 {
		t.Fatalf("async engine with an empty pipeline: %v, want one batch per move", b)
	}
	e.mu.Lock()
	for i := 1; i < DefaultInflightLimit; i++ {
		e.inflight = append(e.inflight, &inflightStep{})
	}
	e.mu.Unlock()
	if b := e.BufferedBatches(); len(b) != 3 {
		t.Fatalf("async engine under the limit: %v, want one batch per move", b)
	}
	e.mu.Lock()
	e.inflight = append(e.inflight, &inflightStep{})
	e.mu.Unlock()
	if b := e.BufferedBatches(); len(b) != 2 || len(b[0]) != 2 || b[1][0] != MoveHardDrop {
		t.Fatalf("async engine at the limit: %v, want [[left left] [drop]]", b)
	}
	// The knob's limit: a lower one fills sooner.
	e.SetInflightLimit(2)
	e.mu.Lock()
	e.inflight = e.inflight[:2]
	e.mu.Unlock()
	if b := e.BufferedBatches(); len(b) != 2 {
		t.Fatalf("async engine at a limit of 2 with 2 in flight: %v, want the lefts grouped", b)
	}
	if e.SetInflightLimit(0); e.InflightLimit() != 1 {
		t.Fatalf("SetInflightLimit(0) = %d, want the floor 1", e.InflightLimit())
	}
	if e.SetInflightLimit(99); e.InflightLimit() != MaxInflightLimit {
		t.Fatalf("SetInflightLimit(99) = %d, want the ceiling %d", e.InflightLimit(), MaxInflightLimit)
	}
}

// TestPipelineCoalescesWhenFull: with the acks held, the first batches go
// out one move each until the in-flight limit's worth are in flight; the moves
// made from then on wait in the queue, grouped, and go out as ONE batch the
// moment a slot frees — every move still lands, in order.
func TestPipelineCoalescesWhenFull(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	var sent atomic.Int32
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) { sent.Add(1) }
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishAsync)

	const burst = 7
	for i := 0; i < burst; i++ {
		e.MoveDown()
	}
	// The limit's worth of single-move batches fill the pipeline; the rest wait,
	// as one group.
	waitUntil(t, 5*time.Second, func() bool {
		return e.InflightSteps() == DefaultInflightLimit && len(e.BufferedMoves()) == burst-DefaultInflightLimit
	}, "the pipeline to fill and the rest of the burst to wait")
	if got := sent.Load(); got != DefaultInflightLimit {
		t.Fatalf("%d batches sent with the pipeline full, want %d", got, DefaultInflightLimit)
	}
	if b := e.BufferedBatches(); len(b) != 1 || len(b[0]) != burst-DefaultInflightLimit {
		t.Fatalf("waiting moves grouped as %v, want one batch of %d", b, burst-DefaultInflightLimit)
	}

	close(release)
	want := start
	want.Row += burst
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Row >= want.Row && e.InflightSteps() == 0 && len(e.BufferedMoves()) == 0
	}, "the whole burst to land")
	// The waiting moves went out as one batch: the group plus the singles,
	// and nothing else (a gravity tick within the test's few ms would add one).
	if got := sent.Load(); got != DefaultInflightLimit+1 {
		t.Fatalf("%d batches for %d moves, want %d (the group went out as one)", got, burst, DefaultInflightLimit+1)
	}
	if e.PipelineBroken() {
		t.Fatal("pipeline broken after a clean coalesced burst")
	}
}

// TestHardDropFlushesHeldMoves: a hard drop never waits behind a full
// pipeline. With the acks held and the pipeline full, the steps that piled
// up go out the moment the drop is queued — as one batch — and the drop
// commits right behind them once they are acked, from where they left the
// piece; the piece is frozen on the display from the moment of the drop.
func TestHardDropFlushesHeldMoves(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	var sent atomic.Int32
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) { sent.Add(1) }
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishOptimistic)

	// Fill the pipeline with downs, then three lefts that have to wait.
	for i := 0; i < DefaultInflightLimit; i++ {
		e.MoveDown()
	}
	e.MoveLeft()
	e.MoveLeft()
	e.MoveLeft()
	waitUntil(t, 5*time.Second, func() bool {
		return e.InflightSteps() == DefaultInflightLimit && len(e.BufferedMoves()) == 3
	}, "the pipeline to fill and the lefts to wait")

	// The drop: the lefts go out at once as one batch, the drop takes the
	// piece off the queue and waits for the acks; the display freezes the
	// piece where the lefts leave it, and a key for the next piece does not
	// move it.
	e.HardDrop()
	e.MoveRight()
	waitUntil(t, 5*time.Second, func() bool {
		return sent.Load() == DefaultInflightLimit+1 && e.InflightSteps() == DefaultInflightLimit+1
	}, "the held lefts to go out as one batch on the drop")
	frozen := start
	frozen.Row += DefaultInflightLimit
	frozen.Col -= 3
	waitUntil(t, 5*time.Second, func() bool {
		p, ok := e.IntentPiece()
		return ok && p == frozen && len(e.BufferedMoves()) == 1 // the right waits for the next piece
	}, "the piece to freeze where the drop takes it from")
	if landed(e) {
		t.Fatal("the drop committed before the steps ahead of it were acked")
	}

	close(release)
	dest := game.HardDropDestination(frozen, e.Playfield())
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.Snapshot()
		for _, rc := range dest.Cells() {
			c := snap.Rows[rc[0]].Cells[rc[1]]
			if !c.Occupied || c.Active {
				return false
			}
		}
		return true
	}, "the piece to land three columns left of where the pipeline had it")
}

// landed reports whether any cell of the board is locked.
func landed(e *Engine) bool {
	for _, row := range e.Snapshot().Rows {
		for _, c := range row.Cells {
			if c.Occupied && !c.Active {
				return true
			}
		}
	}
	return false
}

// TestPendingDrop: a queued hard drop reports where the piece — as the
// steps ahead of the drop leave it — is going to land; none queued, none
// pending; a repair in progress reverts it.
func TestPendingDrop(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	p := game.Piece{Type: game.PieceT, Row: 2, Col: 4}
	e.SeedActivePiece(p)
	if _, ok := e.PendingDrop(); ok {
		t.Fatal("a drop pending with none queued")
	}
	e.MoveLeft()
	e.HardDrop()
	e.MoveRight() // the next piece's
	left := p
	left.Col--
	want := game.HardDropDestinationCoop(left, e.playfield, 0)
	if got, ok := e.PendingDrop(); !ok || got != want {
		t.Fatalf("PendingDrop = %+v, %v; want the landing of the piece one left, %+v", got, ok, want)
	}
	// A hold queued first is not a drop.
	e2 := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e2.SeedActivePiece(p)
	e2.Hold()
	e2.HardDrop()
	if _, ok := e2.PendingDrop(); ok {
		t.Fatal("a drop pending behind a hold")
	}
	// The drop being committed still reports its landing; a repair reverts it.
	e.takeMoveGroup(false)
	e.takeMoveGroup(false)
	e.freezePiece(MoveHardDrop)
	if got, ok := e.PendingDrop(); !ok || got != game.HardDropDestinationCoop(p, e.playfield, 0) {
		t.Fatalf("PendingDrop while committing = %+v, %v; want the landing from where the piece stands", got, ok)
	}
	e.mu.Lock()
	e.pipeBroken = true
	e.mu.Unlock()
	if _, ok := e.PendingDrop(); ok {
		t.Fatal("a drop pending while a lost step is being repaired")
	}
}

// TestDropYieldsToReplay: a hard drop made behind a pipelined step that is
// lost lands from where the REPLAYED moves leave the piece — after them,
// never from the rollback point ahead of them, nor from the lost step's
// assumptions. Steps: down, LEFT (lost), down, down fill the pipeline; two
// rights wait, then the drop. The piece must land two columns right of the
// spawn: the lost left dropped, the two rights replayed before the drop.
func TestDropYieldsToReplay(t *testing.T) {
	e, _, _ := startedEngine(t)
	ctx := context.Background()
	start := *e.Playfield().ActivePieceForPlayer(0)
	one := start
	one.Row++
	two := one
	two.Col--
	guarded := cellsMinus(two.Cells(), one.Cells(), start.Cells())
	if len(guarded) == 0 {
		t.Fatal("no CAS-guarded cell on the lost step")
	}
	empty, _ := game.Cell{}.Marshal()

	var sent atomic.Int32
	release := make(chan struct{})
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) {
		if sent.Add(1) == 2 {
			for _, rc := range guarded {
				if _, err := e.js.Publish(ctx, e.cellSubject(rc[0], rc[1]), empty); err != nil {
					t.Error(err)
				}
			}
		}
	}
	e.testHookBeforeStepResolve = func() { <-release }
	e.SetPublishMode(PublishOptimistic)

	e.MoveDown()
	e.MoveLeft()
	e.MoveDown()
	e.MoveDown()
	e.MoveRight()
	e.MoveRight()
	waitUntil(t, 5*time.Second, func() bool {
		return e.InflightSteps() == DefaultInflightLimit && len(e.BufferedMoves()) == 2
	}, "the pipeline to fill and the rights to wait")
	e.HardDrop()
	// The drop flushes the rights and takes the piece off the queue.
	waitUntil(t, 5*time.Second, func() bool { return len(e.BufferedMoves()) == 0 }, "the rights and the drop to be taken")
	close(release)

	want := start
	want.Col += 2
	dest := game.HardDropDestination(want, game.NewPlayfieldWithHeight(e.playfield.Width, e.playfield.Height))
	waitUntil(t, 5*time.Second, func() bool {
		snap := e.Snapshot()
		for _, rc := range dest.Cells() {
			c := snap.Rows[rc[0]].Cells[rc[1]]
			if !c.Occupied || c.Active {
				return false
			}
		}
		return true
	}, "the piece to land two columns right of the spawn, after the replay")
	if e.PipelineBroken() {
		t.Fatal("pipeline still broken after the drop")
	}
}

// TestBatchesTaken: the strip's batch ordinal counts the batches taken off
// the queue — one per single move, one per coalesced group.
func TestBatchesTaken(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.MoveLeft()
	e.MoveLeft()
	e.MoveDown()
	e.HardDrop()
	if e.BatchesTaken() != 0 {
		t.Fatalf("BatchesTaken = %d before any take", e.BatchesTaken())
	}
	e.takeMoveGroup(true) // the run of three steps, as one
	if e.BatchesTaken() != 1 {
		t.Fatalf("BatchesTaken = %d after a coalesced group, want 1", e.BatchesTaken())
	}
	e.takeMoveGroup(true) // the drop
	e.takeMoveGroup(true) // nothing left: no batch
	if e.BatchesTaken() != 2 {
		t.Fatalf("BatchesTaken = %d after the drop and an empty take, want 2", e.BatchesTaken())
	}
}

// TestSyncMergesQueuedMoves: in sync mode the moves that queue up while a
// batch is out on its round trip go out together as one batch when it
// returns — a burst of twelve moves takes far fewer batches than twelve, and
// every move still lands, in order.
func TestSyncMergesQueuedMoves(t *testing.T) {
	e, _, _ := startedEngine(t)
	start := *e.Playfield().ActivePieceForPlayer(0)
	if e.PublishMode() != PublishSync {
		t.Fatal("the engine must start in sync mode")
	}
	moves := []MoveType{MoveDown, MoveDown, MoveDown, MoveDown, MoveLeft, MoveLeft, MoveLeft, MoveRight, MoveRight, MoveRight, MoveDown, MoveDown}
	for _, m := range moves {
		switch m {
		case MoveDown:
			e.MoveDown()
		case MoveLeft:
			e.MoveLeft()
		case MoveRight:
			e.MoveRight()
		}
	}
	want := start
	want.Row += 6
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && len(e.BufferedMoves()) == 0 && e.InflightSteps() == 0
	}, "the burst to land")
	if got := e.BatchesTaken(); got >= len(moves) {
		t.Fatalf("%d batches for %d moves: the moves queued during a round trip did not merge", got, len(moves))
	}
	waitUntil(t, 5*time.Second, func() bool {
		p := snapshotPiece(e.EchoSnapshot(), 0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row
	}, "the echo to converge")
}
