package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// setupCompetitiveGame starts n competitive engines ("p1".."pn", playerIdx
// 0..n-1) in one in-progress game, each consuming every other player's board.
// Seed 5 makes everyone's first piece a horizontal I (cols 3-6).
func setupCompetitiveGame(t *testing.T, gameID string, n int) (jetstream.JetStream, []*Engine) {
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
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: n,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p1", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, n)
	for i := range ids {
		ids[i] = "p" + string(rune('1'+i))
	}
	engines := make([]*Engine, n)
	for i, id := range ids {
		firstOpp := ids[(i+1)%n]
		e := New(js, gameID, id, firstOpp, config.ModeCompetitive, ModePlayer, i, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		// Attach consumers for the remaining opponents (the lobby's roster
		// consumer would discover them in a real game).
		for j, opp := range ids {
			if j != i && opp != firstOpp {
				e.startOpponentConsumer(e.ctx, opp)
			}
		}
		engines[i] = e
	}
	waitUntil(t, 5*time.Second, func() bool {
		for i, e := range engines {
			if e.Playfield().ActivePieceForPlayer(i) == nil {
				return false
			}
		}
		return true
	}, "all first pieces to spawn")
	return js, engines
}

// prefillBottomForI fills a player's bottom row except the I's drop columns
// 3-6, so their first hard drop completes exactly one line.
func prefillBottomForI(t *testing.T, js jetstream.JetStream, gameID, playerID string, bottom int) {
	t.Helper()
	for c := 0; c < config.StandardWidth; c++ {
		if c >= 3 && c <= 6 {
			continue
		}
		publishCompetitiveCell(t, js, gameID, playerID, bottom, c,
			game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0})
	}
}

func fetchGarbageRegister(t *testing.T, js jetstream.JetStream, gameID, subject string) (GarbageRegister, uint64) {
	t.Helper()
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, []string{subject})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		return GarbageRegister{}, 0
	}
	var reg GarbageRegister
	if err := json.Unmarshal(msgs[0].Payload, &reg); err != nil {
		t.Fatal(err)
	}
	return reg, msgs[0].Seq
}

func fetchTxnRegister(t *testing.T, js jetstream.JetStream, gameID, subject string) TxnRegister {
	t.Helper()
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, []string{subject})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		return TxnRegister{}
	}
	var txn TxnRegister
	if err := json.Unmarshal(msgs[0].Payload, &txn); err != nil {
		t.Fatal(err)
	}
	return txn
}

// TestCompetitiveRaiseLedgerFlow drives the full attack path end-to-end: A's
// line clear CAS-adds B's garbage register, B applies the deficit as a gated
// cascade transform, and the garbage lands exactly once.
func TestCompetitiveRaiseLedgerFlow(t *testing.T) {
	gameID := "garbage-ledger-flow"
	js, engines := setupCompetitiveGame(t, gameID, 2)
	a, b := engines[0], engines[1]
	bottom := config.CompetitiveTotalRows(2) - 1

	prefillBottomForI(t, js, gameID, "p1", bottom)
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().Rows[bottom].Cells[0].Occupied
	}, "pre-fill to apply on a's replica")

	a.HardDrop()
	waitUntil(t, 3*time.Second, func() bool { return a.Score() == 1 }, "a's clear to score")

	// B receives exactly one garbage row.
	waitUntil(t, 3*time.Second, func() bool {
		return b.Playfield().AdversarialRowCount() == 1
	}, "b's board to gain one garbage row")

	// Give a double-application time to surface, then re-assert.
	time.Sleep(500 * time.Millisecond)
	if got := b.Playfield().AdversarialRowCount(); got != 1 {
		t.Fatalf("b has %d adversarial rows, want exactly 1", got)
	}
	if got := a.Playfield().AdversarialRowCount(); got != 0 {
		t.Fatalf("attacker's own board has %d adversarial rows, want 0", got)
	}

	// Registers converge: owed == applied == 1, attributed to a (playerIdx 0).
	reg, _ := fetchGarbageRegister(t, js, gameID, config.CompetitiveGarbageSubject(gameID, "p2"))
	if reg.Total != 1 || reg.By != 0 {
		t.Fatalf("b's garbage register = %+v, want total 1 by 0", reg)
	}
	txn := fetchTxnRegister(t, js, gameID, config.CompetitiveTxnSubject(gameID, "p2"))
	if txn.Applied != 1 || txn.Op != "shrink" {
		t.Fatalf("b's txn register = %+v, want applied 1 op shrink", txn)
	}
	// B keeps its falling piece (garbage rose beneath it, no conflict).
	if b.Playfield().ActivePieceForPlayer(1) == nil {
		t.Fatal("b lost its active piece during the raise")
	}
}

// TestMultiLineClearSendsAllGarbage: one drop that completes TWO rows must
// clear both rows from the attacker's board and owe the victim two garbage
// rows — the attack strength is the full clear count, not one row per lock.
func TestMultiLineClearSendsAllGarbage(t *testing.T) {
	gameID := "garbage-multiline"
	js, engines := setupCompetitiveGame(t, gameID, 2)
	a, b := engines[0], engines[1]
	bottom := config.CompetitiveTotalRows(2) - 1

	// Fill the bottom TWO rows except column 5 — the column the seed-5 I
	// occupies after one clockwise rotation (spawn col 3 + vertical offset 2).
	for _, r := range []int{bottom - 1, bottom} {
		for c := 0; c < config.StandardWidth; c++ {
			if c == 5 {
				continue
			}
			publishCompetitiveCell(t, js, gameID, "p1", r, c,
				game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0})
		}
	}
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().Rows[bottom].Cells[0].Occupied &&
			a.Playfield().Rows[bottom-1].Cells[0].Occupied
	}, "two-row pre-fill to apply on a's replica")

	a.RotateCW()
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Orientation == 1
	}, "a's I to rotate vertical")

	a.HardDrop()
	// Competitive score = lines cleared, so a double is worth exactly 2.
	waitUntil(t, 3*time.Second, func() bool { return a.Score() == 2 }, "a's double clear to score 2")

	// Both rows are gone from a's board: the pre-fill vanished and only the
	// I's two leftover cells (column 5) shifted down into the bottom rows.
	pf := a.Playfield()
	for _, r := range []int{bottom - 1, bottom} {
		for c := 0; c < config.StandardWidth; c++ {
			cell := pf.Rows[r].Cells[c]
			if c == 5 {
				if !cell.Occupied || cell.PieceType != game.PieceI {
					t.Fatalf("row %d col 5 should hold the I remnant, got %+v", r, cell)
				}
			} else if cell.Occupied {
				t.Fatalf("row %d col %d still occupied after the double clear: %+v", r, c, cell)
			}
		}
	}

	// The victim owes and applies BOTH rows, exactly once.
	waitUntil(t, 5*time.Second, func() bool {
		return b.Playfield().AdversarialRowCount() == 2
	}, "b's board to gain both garbage rows")
	time.Sleep(500 * time.Millisecond)
	if got := b.Playfield().AdversarialRowCount(); got != 2 {
		t.Fatalf("b has %d adversarial rows, want exactly 2", got)
	}
	reg, _ := fetchGarbageRegister(t, js, gameID, config.CompetitiveGarbageSubject(gameID, "p2"))
	if reg.Total != 2 || reg.By != 0 {
		t.Fatalf("b's garbage register = %+v, want total 2 by 0", reg)
	}
	txn := fetchTxnRegister(t, js, gameID, config.CompetitiveTxnSubject(gameID, "p2"))
	if txn.Applied != 2 || txn.Op != "shrink" {
		t.Fatalf("b's txn register = %+v, want applied 2 op shrink", txn)
	}
}

// TestCompetitiveSimultaneousAttacksSum is the high-RTT double-clear scenario:
// two players clear at nearly the same instant, and every victim's register
// must converge to the SUM of the attacks — the CAS-add serializes the bumps,
// so no attack is lost (the old event-based path collapsed them into one).
func TestCompetitiveSimultaneousAttacksSum(t *testing.T) {
	gameID := "garbage-simultaneous"
	js, engines := setupCompetitiveGame(t, gameID, 3)
	a, b, c := engines[0], engines[1], engines[2]
	bottom := config.CompetitiveTotalRows(3) - 1

	prefillBottomForI(t, js, gameID, "p1", bottom)
	prefillBottomForI(t, js, gameID, "p2", bottom)
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().Rows[bottom].Cells[0].Occupied &&
			b.Playfield().Rows[bottom].Cells[0].Occupied
	}, "pre-fills to apply")

	// Both clear as close to simultaneously as the harness allows.
	a.HardDrop()
	b.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return a.Score() == 1 && b.Score() == 1 }, "both clears to score")

	// The bystander owes 2 — one from each attacker — and applies both.
	waitUntil(t, 5*time.Second, func() bool {
		reg, _ := fetchGarbageRegister(t, js, gameID, config.CompetitiveGarbageSubject(gameID, "p3"))
		return reg.Total == 2
	}, "c's register to reach the attack sum")
	waitUntil(t, 5*time.Second, func() bool {
		return c.Playfield().AdversarialRowCount() == 2
	}, "c's board to gain both garbage rows")

	// The attackers hit each other exactly once each.
	waitUntil(t, 5*time.Second, func() bool {
		return a.Playfield().AdversarialRowCount() == 1 &&
			b.Playfield().AdversarialRowCount() == 1
	}, "attackers to receive each other's row")

	time.Sleep(500 * time.Millisecond)
	if got := c.Playfield().AdversarialRowCount(); got != 2 {
		t.Fatalf("c has %d adversarial rows, want exactly 2 (lost or doubled attack)", got)
	}
	txn := fetchTxnRegister(t, js, gameID, config.CompetitiveTxnSubject(gameID, "p3"))
	if txn.Applied != 2 {
		t.Fatalf("c's txn applied = %d, want 2", txn.Applied)
	}
}

// TestShrinkCascadeTopsOutSqueezedPlayer: a raise big enough to drive the
// stack over the victim's hovering piece must push the piece off the top and
// eliminate the victim — the piece is never merged into the garbage.
func TestShrinkCascadeTopsOutSqueezedPlayer(t *testing.T) {
	gameID := "garbage-topout"
	js, engines := setupCompetitiveGame(t, gameID, 2)
	victim := engines[1]
	height := config.CompetitiveTotalRows(2)

	// Fill victim rows 4..bottom except column 0 (never completable, and the
	// I at cols 3-6 can never rest anywhere inside it).
	for r := 4; r < height; r++ {
		for col := 1; col < config.StandardWidth; col++ {
			publishCompetitiveCell(t, js, gameID, "p2", r, col,
				game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 1})
		}
	}
	waitUntil(t, 3*time.Second, func() bool {
		return victim.Playfield().Rows[4].Cells[1].Occupied
	}, "victim wall to apply")

	// Owe 4 rows directly (as if an opponent cleared a Tetris): the stack
	// rises to the very top, the hovering I has nowhere to go, and the victim
	// is squeezed out.
	payload, _ := json.Marshal(GarbageRegister{Total: 4, By: 0})
	if _, err := js.Publish(context.Background(), config.CompetitiveGarbageSubject(gameID, "p2"), payload); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, 5*time.Second, func() bool { return victim.Mode() == ModeGameOver }, "victim to top out from the raise")

	pf := victim.Playfield()
	if p := pf.ActivePieceForPlayer(1); p != nil {
		t.Fatalf("squeezed piece should be gone, found anchor row %d", p.Row)
	}
	for r := height - 4; r < height; r++ {
		for col := 0; col < config.StandardWidth; col++ {
			if !pf.Rows[r].Cells[col].Adversarial {
				t.Fatalf("row %d col %d should be garbage after the raise", r, col)
			}
		}
	}
	txn := fetchTxnRegister(t, js, gameID, config.CompetitiveTxnSubject(gameID, "p2"))
	if txn.Applied != 4 || len(txn.Topped) != 1 || txn.Topped[0] != 1 {
		t.Fatalf("victim txn = %+v, want applied 4 topped [1]", txn)
	}
}

// TestLateJoinAppliesOwedGarbage: rows owed BEFORE an engine starts are
// reconciled from the snapshot — the durable-register fix for attacks lost at
// high RTT or across reconnects.
func TestLateJoinAppliesOwedGarbage(t *testing.T) {
	gameID := "garbage-latejoin"
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
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p1", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// The attack lands while p1 is "offline".
	payload, _ := json.Marshal(GarbageRegister{Total: 2, By: 1})
	if _, err := js.Publish(ctx, config.CompetitiveGarbageSubject(gameID, "p1"), payload); err != nil {
		t.Fatal(err)
	}

	e := New(js, gameID, "p1", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	waitUntil(t, 5*time.Second, func() bool {
		return e.Playfield().AdversarialRowCount() == 2
	}, "the owed rows to apply from the snapshot reconcile")
	txn := fetchTxnRegister(t, js, gameID, config.CompetitiveTxnSubject(gameID, "p1"))
	if txn.Applied != 2 {
		t.Fatalf("txn applied = %d, want 2", txn.Applied)
	}
}

// TestSpectatorNeverAppliesGarbage: garbage application is structural to the
// victim's own runInput goroutine, which spectators never run — a spectator
// (or an eliminated player) must not write cells anywhere, even when a
// register in its namespace is bumped (the old event path had this hole).
func TestSpectatorNeverAppliesGarbage(t *testing.T) {
	gameID := "garbage-spectator"
	js, engines := setupCompetitiveGame(t, gameID, 2)
	_ = engines

	spec := New(js, gameID, "watcher", "p1", config.ModeCompetitive, ModeSpectator, 0, 0, 0)
	if err := spec.Start(); err != nil {
		t.Fatal(err)
	}
	defer spec.Stop()

	payload, _ := json.Marshal(GarbageRegister{Total: 3, By: 0})
	if _, err := js.Publish(context.Background(), config.CompetitiveGarbageSubject(gameID, "watcher"), payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	// Nothing may have been written to the spectator's phantom board.
	subjects := []string{
		config.CompetitiveTxnSubject(gameID, "watcher"),
		config.CompetitiveCellSubject(gameID, "watcher", config.CompetitiveTotalRows(2)-1, 0),
	}
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, subjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("spectator wrote %d board messages (first: %s)", len(msgs), msgs[0].Subject)
	}
}
