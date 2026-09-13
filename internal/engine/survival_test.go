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

// The rising floor (config.Survival): on a crew's board with company the
// raise is a write-once CAS on the crew's garbage register that every
// engine's clock races and exactly one wins, applied by the gated shrink
// like an attack; a crew of one raises on its journal. These tests run the
// real clocks — the Easy tier's floor rises 2.5 s after the game starts.

// startSurvivalGame brings up an in-progress cooperative survival game of
// the given seat count and tier on a private server; prefill, if any, writes
// cells to the board before any engine starts.
func startSurvivalGame(t *testing.T, seats int, tier config.Survival, prefill func(js jetstream.JetStream, gameID string)) (jetstream.JetStream, string) {
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
	gameID := "survival-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: seats, ExtraColumns: 4,
		Survival: tier, GarbageHoles: 1, Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	if prefill != nil {
		prefill(js, gameID)
	}
	return js, gameID
}

// publishCoopCell writes one cell of the crew's board directly.
func publishCoopCell(t *testing.T, js jetstream.JetStream, gameID string, row, col int, cell game.Cell) {
	t.Helper()
	data, err := cell.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := natspkg.PublishCellsAtomicallyNoCAS(context.Background(), js, []natspkg.CellUpdate{{
		Subject: config.CoopCellSubject(gameID, row, col),
		Payload: data,
	}}); err != nil {
		t.Fatal(err)
	}
}

// adversarialRow reports whether a board's row is a raised one: every cell
// but the hole columns adversarial, the holes empty.
func adversarialRow(row game.Row, holes []int) bool {
	hole := map[int]bool{}
	for _, c := range holes {
		hole[c] = true
	}
	for c, cell := range row.Cells {
		if hole[c] {
			if cell.Occupied {
				return false
			}
		} else if !cell.Occupied || !cell.Adversarial {
			return false
		}
	}
	return true
}

// Two seats, the Easy floor: the first raise lands 2.5 s after the start —
// once, though both clocks fire — the second 2.5 s after it, every engine
// applies the rows through the gate exactly once, and the holes are the
// seeded well's on both boards.
func TestSurvivalTwoSeatRaises(t *testing.T) {
	js, gameID := startSurvivalGame(t, 2, config.SurvivalEasy, nil)
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
	t0 := time.Now()
	a := startPlayer(t, js, gameID, "p0", 0, roster)
	b := startPlayer(t, js, gameID, "p1", 1, roster)
	if !a.survivalRegisters() || a.Survival() != config.SurvivalEasy {
		t.Fatal("a two-seat survival game should raise through the registers")
	}

	subject := config.CoopGarbageSubject(gameID)
	raises := func() int { reg, _ := fetchGarbageRegister(t, js, gameID, subject); return reg.Raises }
	waitUntil(t, 5*time.Second, func() bool { return raises() >= 1 }, "the first raise")
	if since := time.Since(t0); since < 2300*time.Millisecond {
		t.Errorf("the first raise landed %v after the start, before the Easy floor's 2.5 s", since)
	}
	time.Sleep(300 * time.Millisecond)
	if got := raises(); got != 1 {
		t.Fatalf("%d raises after one tick: both clocks landed theirs", got)
	}
	waitUntil(t, 5*time.Second, func() bool { return raises() >= 2 }, "the second raise")
	if since := time.Since(t0); since < 4700*time.Millisecond {
		t.Errorf("the second raise landed %v after the start, want ~5 s", since)
	}
	reg, _ := fetchGarbageRegister(t, js, gameID, subject)
	if reg.Raises != 2 || reg.Total != 2 {
		t.Errorf("register = %+v, want 2 raises of one row", reg)
	}

	// Applied exactly once, on both boards, with the well's holes.
	waitUntil(t, 5*time.Second, func() bool {
		return fetchTxnRegister(t, js, gameID, config.CoopTxnSubject(gameID)).Applied == 2 &&
			a.SurvivalRaises() == 2 && b.SurvivalRaises() == 2
	}, "both rows applied")
	holes := game.SurvivalHoles(14, 1, 0, 2, false, 5)
	for name, e := range map[string]*Engine{"p0": a, "p1": b} {
		waitUntil(t, 3*time.Second, func() bool {
			pf := e.Playfield()
			return adversarialRow(pf.Rows[pf.Height-2], holes[0]) && adversarialRow(pf.Rows[pf.Height-1], holes[1])
		}, name+"'s raised rows")
		pf := e.Playfield()
		if pf.AdversarialRowCount() != 2 {
			t.Errorf("%s: %d adversarial rows, want 2", name, pf.AdversarialRowCount())
		}
		if pf.ActivePieceForPlayer(e.playerIdx) == nil {
			t.Errorf("%s lost their piece to the raise", name)
		}
	}
	if a.Survived() < 4*time.Second || b.Survived() < 4*time.Second {
		t.Errorf("survived %v / %v, want the game's age", a.Survived(), b.Survived())
	}
}

// A raise that pushes the locked stack past the top ends the game for the
// whole crew: the applier tops out on the spot, the peer off the txn echo,
// and the meta reaches finished.
func TestSurvivalRaiseTopsOutCrew(t *testing.T) {
	js, gameID := startSurvivalGame(t, 2, config.SurvivalEasy, func(js jetstream.JetStream, gameID string) {
		publishCoopCell(t, js, gameID, 0, 0, game.Cell{Occupied: true, PieceType: game.PieceL})
	})
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
	a := startPlayer(t, js, gameID, "p0", 0, roster)
	b := startPlayer(t, js, gameID, "p1", 1, roster)
	waitUntil(t, 6*time.Second, func() bool {
		_, overA := a.GameOutcome()
		_, overB := b.GameOutcome()
		return overA && overB
	}, "the crew's game over")
	if won, _ := a.GameOutcome(); won {
		t.Error("a floor that won crowned the crew")
	}
	waitMetaStatus(t, js, gameID, config.GameStatusFinished, 5*time.Second)
	if a.Survived() <= 0 || a.Survived() > 10*time.Second {
		t.Errorf("survived %v", a.Survived())
	}
	end := a.Survived()
	time.Sleep(200 * time.Millisecond)
	if a.Survived() != end {
		t.Error("the clock kept running after the game ended")
	}
}

// On a survival board the crew's line clear takes the gate: the txn register
// records the clear, so it can never race the floor's shrink from a stale
// snapshot.
func TestSurvivalCoopClearIsGated(t *testing.T) {
	// The Hard floor first rises at 5 s: room for the drop and its clear.
	js, gameID := startSurvivalGame(t, 2, config.SurvivalHard, func(js jetstream.JetStream, gameID string) {
		// Seat 0 spawns at column 3 with the house full; seed 5's first
		// piece is a horizontal I over columns 3-6.
		for c := 0; c < 14; c++ {
			if c >= 3 && c <= 6 {
				continue
			}
			publishCoopCell(t, js, gameID, 23, c, game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 1})
		}
	})
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
	a := startPlayer(t, js, gameID, "p0", 0, roster)
	a.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		return fetchTxnRegister(t, js, gameID, config.CoopTxnSubject(gameID)).Op == txnOpClear
	}, "the gated clear")
	waitUntil(t, 3*time.Second, func() bool { return a.OwnLines() == 1 }, "the line to count")
	pf := a.Playfield()
	for c, cell := range pf.Rows[23].Cells {
		if cell.Occupied {
			t.Errorf("bottom row col %d still occupied after the clear: %+v", c, cell)
		}
	}
}

// A crew of one raises its floor on the journal: the rows come up on the
// local board and the stream converges on it, with the seeded well's holes.
func TestSurvivalSoloRaise(t *testing.T) {
	js, gameID := startSurvivalGame(t, 1, config.SurvivalEasy, nil)
	e := startPlayer(t, js, gameID, "p0", 0, []Seat{{PlayerID: "p0"}})
	if !e.solo() || e.survivalRegisters() {
		t.Fatal("a one-seat survival game should raise on the journal")
	}
	waitUntil(t, 5*time.Second, func() bool { return e.SurvivalRaises() >= 1 }, "the first raise")
	holes := game.SurvivalHoles(10, 1, 0, 1, false, 5)
	waitUntil(t, 3*time.Second, func() bool {
		pf := e.Playfield()
		return adversarialRow(pf.Rows[pf.Height-1], holes[0])
	}, "the raised row")
	streamConverges(t, e, js, gameID)
	if e.Survived() < 2*time.Second {
		t.Errorf("survived %v", e.Survived())
	}
	if reg, _ := fetchGarbageRegister(t, js, gameID, config.CoopGarbageSubject(gameID)); reg.Raises != 0 {
		t.Errorf("a solo game wrote the register: %+v", reg)
	}
}

// A solo raise into a full stack is the top-out: the game ends, the meta
// reaches finished, and the journal shows the risen board.
func TestSurvivalSoloRaiseTopsOut(t *testing.T) {
	js, gameID := startSurvivalGame(t, 1, config.SurvivalEasy, func(js jetstream.JetStream, gameID string) {
		publishCoopCell(t, js, gameID, 0, 0, game.Cell{Occupied: true, PieceType: game.PieceL})
	})
	e := startPlayer(t, js, gameID, "p0", 0, []Seat{{PlayerID: "p0"}})
	waitUntil(t, 6*time.Second, func() bool { _, over := e.GameOutcome(); return over }, "the top-out")
	waitMetaStatus(t, js, gameID, config.GameStatusFinished, 5*time.Second)
	if e.SurvivalRaises() != 1 {
		t.Errorf("%d raises, want the fatal one", e.SurvivalRaises())
	}
}

// A spectator of a survival game folds the crew's registers — it counts the
// raises and the time — and never raises anything itself.
func TestSurvivalSpectatorFollows(t *testing.T) {
	js, gameID := startSurvivalGame(t, 2, config.SurvivalEasy, nil)
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
	s := New(js, gameID, "watcher", "", config.ModeCooperative, ModeSpectator, 0, 0, 0)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.SetRoster(roster)
	a := startPlayer(t, js, gameID, "p0", 0, roster)
	waitUntil(t, 5*time.Second, func() bool { return s.SurvivalRaises() >= 1 }, "the spectator to see the raise")
	reg, _ := fetchGarbageRegister(t, js, gameID, config.CoopGarbageSubject(gameID))
	if reg.By != a.playerIdx {
		t.Errorf("the raise was %+v, want p0's", reg)
	}
	if s.Survived() <= 0 {
		t.Error("the spectator's clock never started")
	}
}
