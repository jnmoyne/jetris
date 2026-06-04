package engine

import (
	"context"
	"testing"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/game"
)

// TestCoopHardDropClearsCompletingLineImmediately reproduces the bug where a line
// completed by a HARD DROP was not cleared at that drop, but only at the next
// piece's lock. Root cause: ProjectHardDrop teleports the piece (old active rows
// cleared + dest locked rows set as separate messages); the consumer applies them
// incrementally and fires lock-in (and the CompletedRows check) before the dest
// rows are applied, so the completion is missed until the next lock-in.
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
	rowData, err := (game.Row{Cells: cells}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	subj := config.CoopRowSubject(gameID, bottom) // coop: shared board, no player token
	if _, err := js.Publish(context.Background(), subj, rowData); err != nil {
		t.Fatal(err)
	}

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
		return len(game.CompletedRows(e.Playfield())) == 0 && e.score > 0
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
