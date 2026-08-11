package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// TestSpectatorReplaysFullHistory pins the spectator join path that
// full-history retention enables: a spectator starting mid-game consumes every
// board from the BEGINNING of the stream (no snapshot fetch), so it replays
// the whole game — including cells that were later overwritten — and converges
// on the live board.
func TestSpectatorReplaysFullHistory(t *testing.T) {
	const gameID = "spectator-replay"
	player, js := setupCompetitiveEngine(t, gameID)
	ctx := context.Background()

	// The stream must retain full history for the replay (and so ordered
	// consumers can never miss an overwritten write): no per-subject cap.
	stream, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	if got := stream.CachedInfo().Config.MaxMsgsPerSubject; got > 0 {
		t.Fatalf("game stream MaxMsgsPerSubject = %d, want unlimited", got)
	}

	// A roster entry is what the spectator's roster consumer discovers p1 by.
	if _, err := js.Publish(ctx, config.RosterSubject(gameID, "p1"), []byte(`{"player_id":"p1"}`)); err != nil {
		t.Fatal(err)
	}

	if err := player.Start(); err != nil {
		t.Fatal(err)
	}
	defer player.Stop()
	waitUntil(t, 3*time.Second, func() bool {
		return player.Playfield().ActivePieceForPlayer(0) != nil
	}, "first piece to spawn")

	// Walk the piece around so cell subjects are written and then overwritten
	// (vacated) — history that a last-per-subject snapshot would not contain.
	player.MoveDown()
	player.MoveDown()
	player.MoveLeft()
	player.HardDrop()

	// Wait for the drop to lock, then capture the locked cells the spectator
	// must reproduce (locked cells never move; gravity may spawn and advance
	// more pieces meanwhile, which only adds cells).
	var locked map[game.CellPos]game.Cell
	waitUntil(t, 3*time.Second, func() bool {
		locked = lockedCells(player.Playfield())
		return len(locked) > 0
	}, "hard drop to lock")

	// A mid-game spectator on the same game. Track everything its consumers
	// deliver to prove the history REPLAYED: some cell subject must arrive
	// more than once, which a last-per-subject snapshot could never produce.
	spec := New(js, gameID, "spec", "", config.ModeCompetitive, ModeSpectator, 0, 0, 0)
	var mu sync.Mutex
	perSubject := make(map[string]int)
	cellPrefix := "jetris.game." + gameID + ".player.p1.playfield.cell."
	spec.OnStreamMsg = func(_ time.Time, subject string, _ []byte, _ string) {
		if !strings.HasPrefix(subject, cellPrefix) {
			return
		}
		mu.Lock()
		perSubject[subject]++
		mu.Unlock()
	}
	if err := spec.Start(); err != nil {
		t.Fatal(err)
	}
	defer spec.Stop()

	waitUntil(t, 5*time.Second, func() bool {
		mu.Lock()
		replayed := false
		for _, n := range perSubject {
			if n > 1 {
				replayed = true
				break
			}
		}
		mu.Unlock()
		if !replayed {
			return false
		}
		pf, ok := spec.OpponentPlayfields()["p1"]
		if !ok {
			return false
		}
		for pos, want := range locked {
			got := pf.Rows[pos.Row].Cells[pos.Col]
			if !got.Occupied || got.Active || got.PieceType != want.PieceType {
				return false
			}
		}
		return true
	}, "spectator to replay history and converge on the locked cells")
}

// lockedCells returns the board's occupied non-active cells keyed by position.
func lockedCells(pf *game.Playfield) map[game.CellPos]game.Cell {
	out := make(map[game.CellPos]game.Cell)
	for r := range pf.Rows {
		for c, cell := range pf.Rows[r].Cells {
			if cell.Occupied && !cell.Active {
				out[game.CellPos{Row: r, Col: c}] = cell
			}
		}
	}
	return out
}
