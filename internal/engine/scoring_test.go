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

// TestAccountLockComboAndBackToBack drives the Guideline bookkeeping of a
// player's locks — the combo run and the Back-to-Back chain — through a
// sequence of clears on a transport-less engine and checks what each lock
// was accounted as.
func TestAccountLockComboAndBackToBack(t *testing.T) {
	e := New(nil, "g", "p", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	stacked := []game.Row{{Cells: []game.Cell{{Occupied: true, PieceType: game.PieceJ}}}} // a board with something left on it
	empty := []game.Row{{Cells: []game.Cell{{}}}}

	steps := []struct {
		name   string
		lines  int
		award  lockAward
		after  []game.Row
		want   game.Clear
		points int
	}{
		{"a lock that clears nothing: its drop points only", 0, lockAward{dropPoints: 10}, stacked, game.Clear{}, 10},
		{"a single opens a combo run", 1, lockAward{}, stacked, game.Clear{Lines: 1}, 100},
		{"a jetris right after: combo 1, not b2b (a single came before)", 4, lockAward{dropPoints: 4}, stacked, game.Clear{Lines: 4, Combo: 1}, 854},
		{"another jetris: combo 2, back-to-back", 4, lockAward{}, stacked, game.Clear{Lines: 4, Combo: 2, BackToBack: true}, 1300},
		{"nothing cleared: the combo ends, the chain stays", 0, lockAward{}, stacked, game.Clear{}, 0},
		{"a t-spin with no lines scores and keeps the chain", 0, lockAward{spin: game.TSpinFull}, stacked, game.Clear{Spin: game.TSpinFull}, 400},
		{"a t-spin double: back-to-back after the jetris", 2, lockAward{spin: game.TSpinFull}, stacked, game.Clear{Lines: 2, Spin: game.TSpinFull, BackToBack: true}, 1800},
		{"a plain single breaks the chain (combo 1)", 1, lockAward{}, stacked, game.Clear{Lines: 1, Combo: 1}, 150},
		{"a mini t-spin single that empties the board: perfect, not b2b", 1, lockAward{spin: game.TSpinMini}, empty, game.Clear{Lines: 1, Spin: game.TSpinMini, Combo: 2, Perfect: true}, 1100},
		{"a jetris: back-to-back after the mini", 4, lockAward{}, stacked, game.Clear{Lines: 4, Combo: 3, BackToBack: true}, 1350},
	}
	for _, st := range steps {
		clear, points := e.accountLock(st.lines, st.award, st.after, 1)
		if clear != st.want {
			t.Errorf("%s: clear = %+v, want %+v", st.name, clear, st.want)
		}
		if points != st.points {
			t.Errorf("%s: points = %d, want %d", st.name, points, st.points)
		}
	}
	// The level multiplies the clear, not the drop.
	fresh := New(nil, "g", "q", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	if _, points := fresh.accountLock(1, lockAward{dropPoints: 7}, stacked, 5); points != 500+7 {
		t.Errorf("single at level 5 with 7 drop points = %d, want 507", points)
	}

}

// TestTSpinDoubleThenBackToBackJetris plays the Guideline's showpiece on a
// competitive board with guideline garbage: seed 0 deals a T then an I. The
// T is turned vertical, soft-dropped into a T-slot and rotated into it —
// the last move a rotation, three corners filled, both front corners: a
// T-Spin Double, 1200 points and four rows of garbage. The I then drops
// through the four-deep column left beside it: a Jetris right after a
// difficult clear — Back-to-Back (800 × 1.5) plus a combo of one, and the
// Jetris' four rows of garbage plus the Back-to-Back bonus of two.
func TestTSpinDoubleThenBackToBackJetris(t *testing.T) {
	gameID := "tspin-b2b"
	js, engines := setupCompetitiveGameWith(t, gameID, 2,
		func(m *config.GameMeta) { m.GuidelineGarbage = true; m.Seed = 0 }, nil)
	a, b := engines[0], engines[1]
	bottom := config.TotalRows - 1
	locked := game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}

	// The stack, bottom up: four rows open at column 5 (the I's column once
	// vertical), the T-slot's nub row open at column 4, its bar row open at
	// columns 3-5, and one overhang cell — the T-spin's third corner.
	for r := bottom - 3; r <= bottom; r++ {
		for c := 0; c < config.StandardWidth; c++ {
			if c != 5 {
				publishCompetitiveCell(t, js, gameID, "p1", r, c, locked)
			}
		}
	}
	for c := 0; c < config.StandardWidth; c++ {
		if c != 4 {
			publishCompetitiveCell(t, js, gameID, "p1", bottom-4, c, locked)
		}
		if c < 3 || c > 5 {
			publishCompetitiveCell(t, js, gameID, "p1", bottom-5, c, locked)
		}
	}
	publishCompetitiveCell(t, js, gameID, "p1", bottom-6, 3, locked)
	waitUntil(t, 3*time.Second, func() bool {
		pf := a.Playfield()
		return pf.Rows[bottom-6].Cells[3].Occupied && pf.Rows[bottom].Cells[0].Occupied && pf.Rows[bottom-5].Cells[0].Occupied
	}, "the stack to apply on a's replica")

	spawned := *a.Playfield().ActivePieceForPlayer(0)
	if spawned.Type != game.PieceT {
		t.Fatalf("seed 0 should deal a T first, got %v", spawned.Type)
	}
	a.RotateCW()
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Orientation == 1
	}, "the T to turn vertical")
	// Soft-drop it into the slot: it rests with its anchor on the overhang's
	// row (the bar row below open, the nub row's hole under its foot).
	restRow := bottom - 6
	fallen := restRow - spawned.Row
	for i := 0; i < fallen; i++ {
		a.MoveDown()
	}
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Row == restRow
	}, "the T to soft-drop into the slot")
	a.RotateCW()
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Orientation == 2 && p.Row == restRow
	}, "the T to turn into the slot")

	// The lock delay locks it there: a T-Spin Double at level 1, plus one
	// point per soft-dropped cell — every row it fell, less any a gravity
	// tick took meanwhile (allow two).
	tsd := game.Clear{Lines: 2, Spin: game.TSpinFull}.Points(1)
	waitUntil(t, 3*time.Second, func() bool { return a.Score() >= tsd }, "the T-spin double to score")
	if got := a.Score(); got < tsd+fallen-2 || got > tsd+fallen {
		t.Fatalf("score after the T-spin double = %d, want %d plus %d soft-drop points (less a gravity tick or two)", got, tsd, fallen)
	}
	if lines := a.OwnLines(); lines != 2 {
		t.Fatalf("own lines = %d, want 2", lines)
	}
	waitUntil(t, 5*time.Second, func() bool { return b.Playfield().AdversarialRowCount() == 4 }, "b's board to gain the T-spin double's four rows")
	afterTSD := a.Score()

	// The I: turned vertical it drops down column 5 — a Jetris, Back-to-Back
	// after the T-spin, the second clear of the combo.
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Type == game.PieceI
	}, "the I to spawn")
	a.RotateCW()
	waitUntil(t, 3*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		return p != nil && p.Orientation == 1
	}, "the I to turn vertical")
	vertical := *a.Playfield().ActivePieceForPlayer(0)
	fell := game.HardDropDestination(vertical, a.Playfield()).Row - vertical.Row
	a.HardDrop()
	jetris := game.Clear{Lines: 4, BackToBack: true, Combo: 1}.Points(1) // 1250
	waitUntil(t, 3*time.Second, func() bool { return a.Score() >= afterTSD+jetris }, "the back-to-back jetris to score")
	if got, want := a.Score()-afterTSD, jetris+game.DropPoints(0, fell); got < want-4 || got > want {
		t.Fatalf("the jetris scored %d, want %d (1200 back-to-back + 50 combo + 2 a cell dropped, less a gravity tick or two)", got, want)
	}
	if lines := a.OwnLines(); lines != 6 {
		t.Fatalf("own lines = %d, want 6", lines)
	}
	// Its level: the clear was scored at level 1 (two lines before it), and
	// the six lines since leave a competitive player at level 0 — the HUD's
	// stat now follows the player's own lines in competitive too.
	if a.Level() != 0 || a.AchievedLevel() != 0 {
		t.Fatalf("level = %d/%d, want 0 after 6 lines", a.Level(), a.AchievedLevel())
	}

	waitUntil(t, 5*time.Second, func() bool { return b.Playfield().AdversarialRowCount() == 10 }, "b's board to gain the jetris' four rows and the back-to-back bonus of two")
	time.Sleep(300 * time.Millisecond)
	if got := b.Playfield().AdversarialRowCount(); got != 10 {
		t.Fatalf("b has %d adversarial rows, want exactly 10 (4 + 4 + 2)", got)
	}
	reg, _ := fetchGarbageRegister(t, js, gameID, config.CompetitiveGarbageSubject(gameID, "p2"))
	if reg.Total != 10 {
		t.Fatalf("b's garbage register = %+v, want total 10", reg)
	}
}

// TestScoreOnlyLockReachesTeammate: on a shared board a lock that clears
// nothing but scores — a hard drop's two points a cell — is announced like a
// clear (a line_clear event with lines_cleared 0), so the crew's shared
// score converges on every point, not just the clears'.
func TestScoreOnlyLockReachesTeammate(t *testing.T) {
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
	gameID := "coop-score-only"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	a := New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	b := New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 1, 0, 0)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	defer b.Stop()
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(0) != nil && b.Playfield().ActivePieceForPlayer(1) != nil
	}, "both first pieces to spawn")

	// a hard-drops its I onto the empty floor: no line, two points a cell.
	p := *a.Playfield().ActivePieceForPlayer(0)
	fell := game.HardDropDestination(p, a.Playfield()).Row - p.Row
	a.HardDrop()
	want := game.DropPoints(0, fell)
	waitUntil(t, 3*time.Second, func() bool { return a.Score() > 0 }, "a's drop to score")
	if got := a.Score(); got < want-2 || got > want {
		t.Fatalf("a's score = %d, want %d (two points a cell, less a gravity tick)", got, want)
	}
	if a.OwnLines() != 0 {
		t.Fatalf("a cleared %d lines, want none", a.OwnLines())
	}
	waitUntil(t, 3*time.Second, func() bool { return b.Score() == a.Score() }, "b to fold a's drop points off the score-only line_clear event")
	if b.Level() != 0 || a.Level() != 0 {
		t.Fatalf("levels = %d/%d, want 0/0: no line was cleared", a.Level(), b.Level())
	}
}
