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

// A crew of one is the only writer to its game: the engine plays on its
// local board and journals every write to the stream as a batch with no
// expectation, sent and never waited for (solo.go). These tests pin the
// journal — the pipeline tests (pipeline_test.go) pin the shared board's CAS
// pipeline on a two-seat crew.

// soloEngine is setupEngine + Start on the one-seat game: the solo journal,
// with the first piece spawned and its batch acked.
func soloEngine(t *testing.T) (*Engine, jetstream.JetStream, string) {
	t.Helper()
	e, js, gameID := setupEngine(t)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	if !e.solo() {
		t.Fatal("a one-seat cooperative game is not solo")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(0) != nil && e.InflightSteps() == 0
	}, "first piece to spawn and its batch to ack")
	return e, js, gameID
}

// streamBoard is the own board as the STREAM holds it: the last message of
// every cell subject, the truth every other consumer reads.
func streamBoard(t *testing.T, js jetstream.JetStream, gameID string, width, height int) *game.Playfield {
	t.Helper()
	subjects := make([]string, 0, width*height)
	for r := 0; r < height; r++ {
		for c := 0; c < width; c++ {
			subjects = append(subjects, config.CoopCellSubject(gameID, r, c))
		}
	}
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, subjects)
	if err != nil {
		t.Fatal(err)
	}
	pf := game.NewPlayfieldWithHeight(width, height)
	for _, m := range msgs {
		c, err := game.UnmarshalCell(m.Payload)
		if err != nil {
			t.Fatal(err)
		}
		pf.Apply(m.Row, m.Col, c, m.Seq)
	}
	return pf
}

func sameRows(a, b []game.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for r := range a {
		if len(a[r].Cells) != len(b[r].Cells) {
			return false
		}
		for c := range a[r].Cells {
			if a[r].Cells[c] != b[r].Cells[c] {
				return false
			}
		}
	}
	return true
}

// streamConverges waits until the stream holds exactly the local board: the
// journal caught up with the game. The local board keeps moving (gravity),
// so the two are compared as taken together.
func streamConverges(t *testing.T, e *Engine, js jetstream.JetStream, gameID string) {
	t.Helper()
	waitUntil(t, 10*time.Second, func() bool {
		if e.InflightSteps() != 0 {
			return false
		}
		local := e.Playfield()
		stream := streamBoard(t, js, gameID, local.Width, local.Height)
		return sameRows(stream.Rows, local.Rows) && sameRows(e.Playfield().Rows, local.Rows)
	}, "the stream to converge on the local board")
}

// TestSoloIsTheOnlyWriter: the journal is for the player of a one-seat
// cooperative game and nobody else — not a crew of two, not a spectator of
// the solo game, not a game of another mode, not an engine never started.
func TestSoloIsTheOnlyWriter(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	if e.solo() {
		t.Fatal("a constructed engine is solo before it knows its seat count")
	}
	e.playerCount = 1
	if !e.solo() {
		t.Fatal("the player of a one-seat cooperative game is not solo")
	}
	e.playerCount = 2
	if e.solo() {
		t.Fatal("a crew of two is solo")
	}
	spec := New(nil, "g", "watcher", "", config.ModeCooperative, ModeSpectator, 0, 0, 0)
	spec.playerCount = 1
	if spec.solo() {
		t.Fatal("a spectator of a solo game is solo")
	}
	comp := New(nil, "g", "p0", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	comp.playerCount = 1
	if comp.solo() {
		t.Fatal("a one-seat competitive game is solo: its board has registers the journal does not keep")
	}
}

// TestSoloJournalsUnguarded: in the optimistic mode a burst of moves is
// played on the local board at once — before a single ack — and every
// batch goes out on its own with NO expectation, none held behind a limit;
// the acked replica trails until the acks come, then the acks and the echo
// converge on the local board. Nothing to repair, nothing to flash.
func TestSoloJournalsUnguarded(t *testing.T) {
	e, js, gameID := soloEngine(t)
	e.SetPublishMode(PublishOptimistic)
	start := *e.Playfield().ActivePieceForPlayer(0)
	if got, ok := e.AckedPiece(); !ok || got != start {
		t.Fatalf("AckedPiece = %+v, %v; want the spawned piece %+v", got, ok, start)
	}

	var guarded atomic.Int32
	e.testHookBeforeStepSend = func(updates []natspkg.CellUpdate) {
		for _, u := range updates {
			if u.Expect != natspkg.ExpectNone || u.ExpectLastSeq != 0 {
				guarded.Add(1)
			}
		}
	}
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }
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

	e.MoveLeft()
	e.MoveLeft()
	e.MoveRight()
	e.MoveDown()
	e.MoveDown()
	want := start
	want.Col--
	want.Row += 2
	// The local board has the whole burst while every ack is still held
	// (a gravity tick may add a row).
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && len(e.BufferedMoves()) == 0
	}, "the burst to play out on the local board")
	if got := e.InflightSteps(); got < 5 {
		t.Fatalf("%d batches in flight with the acks held, want every move's own (5)", got)
	}
	if got := e.BatchesTaken(); got != 5 {
		t.Fatalf("BatchesTaken = %d, want 5: the journal holds no move back to group it", got)
	}
	if got := guarded.Load(); got != 0 {
		t.Fatalf("%d cells went out with an expectation; a solo batch carries none", got)
	}

	// (The acked replica is not asserted while the acks are held: the
	// consumer's echo keeps it too, and the echo is not held.)
	close(release)
	waitUntil(t, 5*time.Second, func() bool {
		if e.InflightSteps() != 0 {
			return false
		}
		local := e.Playfield().ActivePieceForPlayer(0)
		acked, ok := e.AckedPiece()
		return local != nil && ok && acked == *local
	}, "the acks to catch up with the local board")
	streamConverges(t, e, js, gameID)
	waitUntil(t, 5*time.Second, func() bool {
		return sameRows(e.EchoSnapshot().Rows, e.Playfield().Rows)
	}, "the echo to converge")
	select {
	case u := <-flashes:
		t.Fatalf("a flash %v in a solo game: nothing can be lost", u.FlashTargetCells)
	default:
	}
}

// TestSoloLocksClearsAndSpawnsAtOnce: a hard drop locks the piece, clears
// the line it completes, scores it and spawns the next piece on the local
// board the moment it is made — with every ack held, not one round trip
// paid — and the journal carries the lock, the clear and the spawn out
// behind it, in that order, so the stream ends up holding the same board.
func TestSoloLocksClearsAndSpawnsAtOnce(t *testing.T) {
	e, js, gameID := soloEngine(t) // seed 42: the first piece is a T, spawned at column 3
	start := *e.Playfield().ActivePieceForPlayer(0)
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }
	var sent atomic.Int32
	e.testHookBeforeStepSend = func([]natspkg.CellUpdate) { sent.Add(1) }

	// The bottom row filled except under the T's three bottom cells: the
	// drop completes it. Planted on the local board — the board the solo
	// game is played on.
	pf := e.Playfield()
	bottom := pf.Height - 1
	e.mu.Lock()
	for c := 0; c < pf.Width; c++ {
		if c < 3 || c > 5 {
			e.playfield.Apply(bottom, c, game.Cell{Occupied: true, PieceType: game.PieceL}, 0)
		}
	}
	e.mu.Unlock()

	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return e.PieceIdx() == 1 }, "the drop to lock the piece on the local board")
	local := e.Playfield()
	if got := len(game.CompletedRows(local)); got != 0 {
		t.Fatalf("%d completed rows on the local board after the drop; the clear is made at the lock", got)
	}
	if e.Score() <= 0 {
		t.Fatal("the drop and the clear scored nothing")
	}
	if p := local.ActivePieceForPlayer(0); p == nil {
		t.Fatal("no next piece on the local board after the drop: the spawn waited for something")
	} else if p.Type == start.Type && *p == start {
		t.Fatalf("the piece after the drop is the dropped one, %+v", *p)
	}
	if got := sent.Load(); got < 3 {
		t.Fatalf("%d batches journaled for the drop, want the lock, the clear and the spawn (3)", got)
	}
	if got := e.InflightSteps(); got < 3 {
		t.Fatalf("%d batches in flight with the acks held, want at least 3", got)
	}

	close(release)
	streamConverges(t, e, js, gameID)
	stream := streamBoard(t, js, gameID, local.Width, local.Height)
	if got := len(game.CompletedRows(stream)); got != 0 {
		t.Fatalf("%d completed rows in the stream: the clear was not journaled", got)
	}
}

// TestSoloSyncOneBatchAtATime: the pessimistic mode lets one batch out at a
// time — the moves made while it is in flight wait in the queue and go out
// together behind its ack — still with no expectation on any cell.
func TestSoloSyncOneBatchAtATime(t *testing.T) {
	e, js, gameID := soloEngine(t)
	e.SetPublishMode(PublishSync)
	start := *e.Playfield().ActivePieceForPlayer(0)
	var guarded atomic.Int32
	e.testHookBeforeStepSend = func(updates []natspkg.CellUpdate) {
		for _, u := range updates {
			if u.Expect != natspkg.ExpectNone {
				guarded.Add(1)
			}
		}
	}
	release := make(chan struct{})
	e.testHookBeforeStepResolve = func() { <-release }

	// The left goes out alone; the downs, made while it is in flight, wait.
	e.MoveLeft()
	waitUntil(t, 5*time.Second, func() bool { return e.InflightSteps() == 1 }, "the left to go out")
	e.MoveDown()
	e.MoveDown()
	e.MoveDown()
	waitUntil(t, 5*time.Second, func() bool { return len(e.BufferedMoves()) == 3 }, "the three downs to queue")
	time.Sleep(100 * time.Millisecond) // long enough for a batch to have gone out, had one been let out
	if got := e.InflightSteps(); got != 1 {
		t.Fatalf("%d batches in flight with the first one's ack held, want 1: the sync mode lets one out at a time", got)
	}
	if b := e.BufferedBatches(); len(b) != 1 || len(b[0]) != 3 {
		t.Fatalf("held moves grouped as %v, want one batch of three", b)
	}
	one := start
	one.Col--
	if p := e.Playfield().ActivePieceForPlayer(0); p == nil || p.Col != one.Col || p.Row < one.Row {
		t.Fatalf("local piece = %+v with the first batch out; want the left played, %+v", p, one)
	}

	close(release)
	want := one
	want.Row += 3
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && p.Row >= want.Row && e.InflightSteps() == 0 && len(e.BufferedMoves()) == 0
	}, "the held moves to go out behind the ack and land")
	if got := e.BatchesTaken(); got != 2 {
		t.Fatalf("BatchesTaken = %d, want 2: the left alone, the three downs as one", got)
	}
	if got := guarded.Load(); got != 0 {
		t.Fatalf("%d cells went out with an expectation in the solo sync mode", got)
	}
	streamConverges(t, e, js, gameID)
}

// TestSoloLostBatchIsJournaledAgain: a batch the server does not commit is
// not a lost race but a lost write — the local board is never rolled back
// and nothing flashes; once the pipeline drains, the cells the stream is
// missing are journaled again, and the stream converges on the game.
func TestSoloLostBatchIsJournaledAgain(t *testing.T) {
	e, js, gameID := soloEngine(t)
	e.SetPublishMode(PublishOptimistic)
	start := *e.Playfield().ActivePieceForPlayer(0)
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

	var sent atomic.Int32
	e.testHookBeforeStepSend = func(updates []natspkg.CellUpdate) {
		if sent.Add(1) == 2 {
			// The second batch is made to fail at the server: an
			// expectation that cannot hold, standing in for a link that
			// dropped it. The journal never sends one of its own.
			updates[0].Expect = natspkg.ExpectOwnSubject
			updates[0].ExpectLastSeq = 1 << 40
		}
	}

	e.MoveLeft()
	e.MoveLeft()
	e.MoveLeft()
	want := start
	want.Col -= 3
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Col == want.Col && len(e.BufferedMoves()) == 0
	}, "the three lefts to play on the local board")
	// The stream converges on the local board only through the resync: the
	// lost batch's vacates never reached it, so without the resync the cells
	// the first left wrote would sit there as a stray piece forever.
	streamConverges(t, e, js, gameID)
	e.mu.Lock()
	lost := e.soloLost
	e.mu.Unlock()
	if lost {
		t.Fatal("the lost batch is still noted after the resync")
	}
	if p := e.Playfield().ActivePieceForPlayer(0); p == nil || p.Col != want.Col {
		t.Fatalf("local piece = %+v after the resync; want it where the three lefts put it, column %d — the local board is never rolled back", p, want.Col)
	}
	select {
	case u := <-flashes:
		t.Fatalf("a flash %v for a lost solo batch: there was no race to lose", u.FlashTargetCells)
	default:
	}
}
