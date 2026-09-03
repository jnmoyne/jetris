package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// A cooperative game can be played alone: one seat on the standard 10-column
// board, played for the high score. The solo player's top-out is the crew's
// top-out — it ends the game and moves the meta to finished, exactly as any
// crew member's would — and the points scored on the way are the shared
// score the archive will rank against other solo games.
func TestSoloCoopTopOutFinishesGame(t *testing.T) {
	e, js, gameID := setupEngine(t) // a one-seat cooperative game
	defer e.Stop()
	ctx := context.Background()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if got := e.Playfield().Width; got != config.StandardWidth {
		t.Fatalf("solo co-op board is %d columns, want %d", got, config.StandardWidth)
	}

	// Walk the first piece out of the spawn rows, wall those rows off (the
	// first column left open, so the rows never complete and clear), then
	// hard-drop: the next spawn lands on locked cells — a top-out.
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(0)
		if p == nil {
			return false
		}
		if lowestCellRow(p) >= 8 {
			return true
		}
		e.MoveDown()
		return false
	}, "piece to descend below the spawn area")
	for _, row := range []int{2, 3} {
		cells := make([]game.Cell, config.StandardWidth)
		for c := 1; c < config.StandardWidth; c++ {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL}
		}
		publishCoopRowCells(t, js, gameID, row, cells)
	}
	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().Rows[3].Cells[3].Occupied
	}, "spawn wall to apply")

	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return e.Mode() == ModeGameOver }, "the solo player to top out")
	waitUntil(t, 5*time.Second, func() bool {
		m, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
		return err == nil && (m.Status == config.GameStatusFinished || m.Status == config.GameStatusArchived)
	}, "the game meta to move to finished")
	if e.Score() <= 0 {
		t.Fatalf("the solo run scored %d; the drops alone should have scored", e.Score())
	}
}
