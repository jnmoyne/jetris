package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// TestTeamVacateSurvivesGateContention pins the persistence of the teams
// elimination vacate. A topped-out player's piece is removed by a gated
// transform; on a busy board that gate keeps losing to teammate moves and
// other transforms. The old 6-attempt bound gave up, stranding the dead
// piece's active cells as a ghost obstacle that every later falling piece
// hovered on forever (the "frozen piece" bug) — nobody ever locks against
// another player's active cells, and nothing else vacates a dead player's
// piece. The vacate must instead ride out sustained contention.
func TestTeamVacateSurvivesGateContention(t *testing.T) {
	js, gameID, engines := setupTeamsGame(t)
	p1 := engines[1]
	ctx := context.Background()

	// Sanity: p1 holds a live piece on the shared team-0 board.
	if p1.Playfield().ActivePieceForPlayer(1) == nil {
		t.Fatal("p1 has no active piece to vacate")
	}

	// Bump the txn register out from under the vacate on its first 8 commit
	// attempts — beyond the old 6-attempt bound that stranded the piece.
	var losses atomic.Int32
	p1.testHookBeforeGatedCommit = func(op string) {
		if op != txnOpVacate || losses.Add(1) > 8 {
			return
		}
		data, _ := json.Marshal(TxnRegister{Applied: 0, Op: "test-competitor", By: 9})
		if _, err := js.Publish(context.Background(), config.TeamTxnSubject(gameID, 0), data); err != nil {
			t.Errorf("competing txn bump: %v", err)
		}
	}

	// Eliminate p1 (a live-piece team top-out runs the gated vacate).
	p1.handleTeamTopOut(ctx, false)

	if got := losses.Load(); got <= 8 {
		t.Fatalf("vacate stopped after %d commit attempts — gave up inside the contention window", got)
	}

	// Server-side truth: no active cell owned by p1 may remain anywhere on the
	// shared team-0 board.
	waitUntil(t, 5*time.Second, func() bool {
		pf := p1.Playfield()
		var subjects []string
		for r := 0; r < pf.Height; r++ {
			for c := 0; c < pf.Width; c++ {
				subjects = append(subjects, config.TeamCellSubject(gameID, 0, r, c))
			}
		}
		msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, subjects)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			cell, err := game.UnmarshalCell(m.Payload)
			if err != nil {
				continue
			}
			if cell.Active && cell.PlayerIdx == 1 {
				return false
			}
		}
		return true
	}, "the dead piece's cells to be vacated from the stream")
}
