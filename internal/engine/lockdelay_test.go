package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/config"
)

// The lock delay under test: short enough to keep the tests quick, long
// enough that a 20 ms poll cannot mistake "not yet" for "never".
const testLockDelay = 400 * time.Millisecond

// walkToFloor soft-drops the engine's piece until it rests on the floor and
// returns (a bound on) the moment it landed.
func walkToFloor(t *testing.T, e *Engine, playerIdx int) time.Time {
	t.Helper()
	bottom := e.Playfield().Height - 1
	var landed time.Time
	waitUntil(t, 10*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(playerIdx)
		if p == nil {
			return false
		}
		if lowestCellRow(p) >= bottom {
			landed = time.Now()
			return true
		}
		e.MoveDown()
		return false
	}, "the piece to reach the floor")
	return landed
}

// A piece that lands does not lock on the spot: it rests for the lock delay
// (a soft drop into the floor is not a lock), then locks where it lies.
func TestLockDelayHoldsThenLocks(t *testing.T) {
	const gameID = "lock-delay-hold"
	e, js := setupCompetitiveEngine(t, gameID)
	e.lockDelay = testLockDelay
	if _, err := js.Publish(context.Background(), config.RosterSubject(gameID, "p1"), []byte(`{"player_id":"p1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")

	landed := walkToFloor(t, e, 0)
	for i := 0; i < 3; i++ {
		e.MoveDown() // soft-dropping into the floor must not lock the piece
	}
	time.Sleep(testLockDelay / 3)
	if got := e.PieceIdx(); got != 0 {
		t.Fatalf("piece locked %v after landing; want it to rest for the %v lock delay", time.Since(landed), testLockDelay)
	}
	if e.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("piece vanished during the lock delay")
	}

	waitUntil(t, 3*time.Second, func() bool { return e.PieceIdx() == 1 }, "the lock delay to lock the piece")
	if since := time.Since(landed); since < testLockDelay-testLockDelay/4 {
		t.Fatalf("piece locked %v after landing, before the %v lock delay", since, testLockDelay)
	}
	if got := len(lockedCells(e.Playfield())); got != 4 {
		t.Fatalf("locked cells after the delayed lock = %d, want the piece's 4", got)
	}
}

// Shifting a resting piece restarts the lock delay (move reset), so a piece
// kept moving outlives the delay many times over — until the player stops.
func TestLockDelayMoveReset(t *testing.T) {
	e, _, _ := setupEngine(t)
	e.lockDelay = testLockDelay
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")

	landed := walkToFloor(t, e, 0)
	const shifts = 8 // well under the reset cap; 8 × 150 ms = 3× the delay
	for i := 0; i < shifts; i++ {
		if i%2 == 0 {
			e.MoveLeft()
		} else {
			e.MoveRight()
		}
		time.Sleep(testLockDelay * 3 / 8)
		if e.PieceIdx() != 0 {
			t.Fatalf("piece locked after %d shifts (%v after landing); shifts must restart the lock delay", i+1, time.Since(landed))
		}
	}
	stopped := time.Now()
	waitUntil(t, 3*time.Second, func() bool { return e.PieceIdx() == 1 }, "the piece to lock once the player stops moving it")
	if since := time.Since(stopped); since < testLockDelay/2 {
		t.Fatalf("piece locked %v after the last shift; want a full lock delay", since)
	}
}

// The move reset is capped: after config.LockDelayMoveResets shifts on the
// same row, further shifts no longer restart the timer and the piece locks
// even though the player keeps moving it.
func TestLockDelayMoveResetCap(t *testing.T) {
	e, _, _ := setupEngine(t)
	e.lockDelay = testLockDelay
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")

	landed := walkToFloor(t, e, 0)
	const gap = 100 * time.Millisecond
	deadline := time.Now().Add(10 * time.Second)
	shifts := 0
	for e.PieceIdx() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("piece never locked through %d shifts; the move reset must be capped at %d", shifts, config.LockDelayMoveResets)
		}
		if shifts%2 == 0 {
			e.MoveLeft()
		} else {
			e.MoveRight()
		}
		shifts++
		time.Sleep(gap)
	}
	// The cap allows LockDelayMoveResets restarts spaced `gap` apart, then
	// one last full delay: the lock can't come much sooner than that.
	minimum := time.Duration(config.LockDelayMoveResets)*gap + testLockDelay
	if since := time.Since(landed); since < minimum*3/4 {
		t.Fatalf("piece locked %v after landing (%d shifts); the cap of %d resets should have kept it alive about %v", since, shifts, config.LockDelayMoveResets, minimum)
	}
}
