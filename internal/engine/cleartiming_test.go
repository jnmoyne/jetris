package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
)

// publishCoopRowCells pre-fills one row of the shared coop board by publishing
// one message per non-empty cell (the playfield is stored one message per cell).
func publishCoopRowCells(t *testing.T, js jetstream.JetStream, gameID string, row int, cells []game.Cell) {
	t.Helper()
	ctx := context.Background()
	for col, c := range cells {
		if c == (game.Cell{}) {
			continue
		}
		data, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, row, col), data); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCoopHardDropClearsCompletingLineImmediately reproduces the bug where a line
// completed by a HARD DROP was not cleared at that drop, but only at the next
// piece's lock. Root cause: ProjectHardDrop teleports the piece (old active cells
// vacated + dest locked cells set as separate messages); the consumer applies them
// incrementally, and if the vacates were applied first, lock-in (and the
// CompletedRows check) would fire before the landing cells were applied and the
// completion missed until the next lock-in. The orderedCellKeys publish order
// (locked cells before vacates) guarantees the landing cells are in place when
// lock-in fires.
func TestCoopHardDropClearsCompletingLineImmediately(t *testing.T) {
	e, js, gameID := setupEngine(t) // coop, seed 42 -> piece 0 = T, width 10
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the first piece (T) to spawn.
	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(0) != nil
	}, "first piece to spawn")

	pf := e.Playfield()
	width := pf.Width
	bottom := pf.Height - 1 // last row

	// The T spawns at column 3 (cols 3,4,5 on its bottom row). Fill the bottom
	// row completely EXCEPT cols 3,4,5 so a straight hard drop completes it.
	gap := map[int]bool{3: true, 4: true, 5: true}
	cells := make([]game.Cell, width)
	for c := 0; c < width; c++ {
		if !gap[c] {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}
		}
	}
	publishCoopRowCells(t, js, gameID, bottom, cells) // coop: shared board, no player token

	// Wait for the consumer to apply the pre-filled bottom row.
	waitUntil(t, 3*time.Second, func() bool {
		r := e.Playfield().Rows[bottom]
		filled := 0
		for _, c := range r.Cells {
			if c.Occupied {
				filled++
			}
		}
		return filled == width-len(gap)
	}, "pre-filled bottom row to apply")

	// Sanity: a piece must still be active above the gap before we drop.
	if e.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("no active piece before hard drop")
	}

	// Hard drop: the T's bottom cells fill cols 3,4,5 of the bottom row, completing it.
	e.HardDrop()

	// After the drop settles, the completing line MUST already be cleared.
	// With the bug, the full bottom row sits uncleared (CompletedRows non-empty)
	// until the next piece locks.
	waitUntil(t, 2*time.Second, func() bool {
		return len(game.CompletedRows(e.Playfield())) == 0 && e.Score() > 0
	}, "completing line to clear at the hard drop")
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
