package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// setupCompetitiveEngine starts a single competitive engine ("p1", playerIdx
// 0) in a 2-seat game (board height 4+24+2 = 30). The second seat never joins
// — board physics don't need it.
func setupCompetitiveEngine(t *testing.T, gameID string) (*Engine, jetstream.JetStream) {
	t.Helper()
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	// Seed 5's first piece is a horizontal I (cols 3-6 on a 10-wide board) —
	// a one-row footprint that completes a pre-filled row in one hard drop.
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p1", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	e := New(js, gameID, "p1", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	return e, js
}

// publishCompetitiveCell writes one cell of a competitive board directly.
func publishCompetitiveCell(t *testing.T, js jetstream.JetStream, gameID, playerID string, row, col int, cell game.Cell) {
	t.Helper()
	data, err := cell.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := natspkg.PublishCellsAtomicallyNoCAS(context.Background(), js, []natspkg.CellUpdate{{
		Subject: config.CompetitiveCellSubject(gameID, playerID, row, col),
		Payload: data,
	}}); err != nil {
		t.Fatal(err)
	}
}

// fetchCompetitiveCell returns the last stream state of one cell (empty cell
// if the subject was never written).
func fetchCompetitiveCell(t *testing.T, js jetstream.JetStream, gameID, playerID string, row, col int) game.Cell {
	t.Helper()
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID,
		[]string{config.CompetitiveCellSubject(gameID, playerID, row, col)})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		return game.Cell{}
	}
	c, err := game.UnmarshalCell(msgs[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCompetitiveClearCollapsesHeadroom pins the full-range collapse: rows
// ABOVE a cleared line must shift down INCLUDING the headroom rows 0-3. The
// old visible-range-only diff left the projection for rows 0-3 unpublished,
// duplicating a headroom cell at rows 3 and 4 and never dropping row-2
// content at all.
func TestCompetitiveClearCollapsesHeadroom(t *testing.T) {
	e, js := setupCompetitiveEngine(t, "gated-clear-headroom")

	// Headroom markers: row 3 col 0 and row 2 col 1 (clear of the spawn
	// columns 3-6). After one line clears they must sit at row 4 col 0 and
	// row 3 col 1, with their old positions vacated.
	marker := game.Cell{Occupied: true, PieceType: game.PieceJ, PlayerIdx: 0}
	publishCompetitiveCell(t, js, "gated-clear-headroom", "p1", 3, 0, marker)
	publishCompetitiveCell(t, js, "gated-clear-headroom", "p1", 2, 1, marker)

	// Pre-fill the bottom row except the I's drop columns 3-6.
	bottom := config.TotalRows - 1
	for c := 0; c < config.StandardWidth; c++ {
		if c >= 3 && c <= 6 {
			continue
		}
		publishCompetitiveCell(t, js, "gated-clear-headroom", "p1", bottom, c,
			game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0})
	}

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(0) != nil
	}, "first piece to spawn")

	e.HardDrop()
	waitUntil(t, 3*time.Second, func() bool {
		return e.Score() > 0 && len(game.CompletedRows(e.Playfield())) == 0
	}, "the line clear to register")

	// Server-side truth: the headroom shifted down with everything else.
	if c := fetchCompetitiveCell(t, js, "gated-clear-headroom", "p1", 4, 0); !c.Occupied || c.PieceType != game.PieceJ {
		t.Errorf("row-3 marker did not drop to row 4: %+v", c)
	}
	if c := fetchCompetitiveCell(t, js, "gated-clear-headroom", "p1", 3, 0); c.Occupied {
		t.Errorf("row 3 col 0 still occupied after the collapse (duplicated cell): %+v", c)
	}
	if c := fetchCompetitiveCell(t, js, "gated-clear-headroom", "p1", 3, 1); !c.Occupied || c.PieceType != game.PieceJ {
		t.Errorf("row-2 marker did not drop to row 3: %+v", c)
	}
	// Replica agrees.
	pf := e.Playfield()
	if !pf.Rows[4].Cells[0].Occupied || pf.Rows[3].Cells[0].Occupied {
		t.Errorf("replica headroom mismatch: row4c0=%+v row3c0=%+v", pf.Rows[4].Cells[0], pf.Rows[3].Cells[0])
	}
}

// TestGatedClearRejectsStaleGateAndRecomputes drives the gate end-to-end: a
// competing writer bumps the txn register between the clear's projection and
// its publish, the server must reject the ENTIRE stale batch atomically, and
// the recompute must land the clear exactly once from fresh state.
func TestGatedClearRejectsStaleGateAndRecomputes(t *testing.T) {
	e, js := setupCompetitiveEngine(t, "gated-clear-race")

	// Marker above the bottom row: after exactly one collapse it sits ON the
	// bottom row; a double-applied collapse would destroy it.
	marker := game.Cell{Occupied: true, PieceType: game.PieceJ, PlayerIdx: 0}
	bottom := config.TotalRows - 1
	publishCompetitiveCell(t, js, "gated-clear-race", "p1", bottom-1, 0, marker)
	for c := 0; c < config.StandardWidth; c++ {
		if c >= 3 && c <= 6 {
			continue
		}
		publishCompetitiveCell(t, js, "gated-clear-race", "p1", bottom, c,
			game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0})
	}

	// On the clear's FIRST attempt only: bump the txn register out from under
	// the batch, simulating a concurrent transform winning the gate.
	var fired atomic.Int32
	e.testHookBeforeGatedCommit = func(op string) {
		if op != txnOpClear || !fired.CompareAndSwap(0, 1) {
			return
		}
		data, _ := json.Marshal(TxnRegister{Applied: 0, Op: "test-competitor", By: 9})
		if _, err := js.Publish(context.Background(), config.CompetitiveTxnSubject("gated-clear-race", "p1"), data); err != nil {
			t.Errorf("competing txn bump: %v", err)
		}
	}

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(0) != nil
	}, "first piece to spawn")

	// The clear is scored once: a single at level 1 plus the hard drop's two
	// points per cell fallen (Guideline scoring).
	spawned := *e.Playfield().ActivePieceForPlayer(0)
	fell := game.HardDropDestination(spawned, e.Playfield()).Row - spawned.Row
	wantScore := game.Clear{Lines: 1}.Points(1) + game.DropPoints(0, fell)

	e.HardDrop()
	waitUntil(t, 3*time.Second, func() bool {
		return e.Score() > 0 && len(game.CompletedRows(e.Playfield())) == 0
	}, "the line clear to land after the gate rejection")

	if fired.Load() != 1 {
		t.Fatal("test hook never fired")
	}

	// Exactly one collapse: the marker moved down exactly one row.
	if c := fetchCompetitiveCell(t, js, "gated-clear-race", "p1", bottom, 0); !c.Occupied || c.PieceType != game.PieceJ {
		t.Errorf("marker did not land on the bottom row after one collapse: %+v", c)
	}
	if c := fetchCompetitiveCell(t, js, "gated-clear-race", "p1", bottom-1, 0); c.Occupied {
		t.Errorf("marker duplicated at its old row (collapse applied from stale state): %+v", c)
	}
	if got := e.Score(); got != wantScore {
		t.Errorf("score = %d, want exactly %d (one single plus the drop, scored once)", got, wantScore)
	}

}
