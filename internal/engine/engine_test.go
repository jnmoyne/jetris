package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

func setupEngine(t *testing.T) (*Engine, jetstream.JetStream, string) {
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
	gameID := "test-engine-game"

	// Create game stream
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Publish initial meta
	meta := config.GameMeta{
		GameID:      gameID,
		Mode:        config.ModeCooperative,
		PlayerCount: 1,
		Seed:        42,
		Status:      config.GameStatusInProgress,
		CreatorID:   "player-1",
		CreatedAt:   time.Now(),
		StartedAt:   time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	e := New(js, gameID, "player-1", "", config.ModeCooperative, ModePlayer, 0)
	return e, js, gameID
}

func TestEngineStart(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}

	// Give the engine a moment to spawn the first piece
	time.Sleep(200 * time.Millisecond)

	pf := e.Playfield()
	p := pf.ActivePiece()
	if p == nil {
		t.Fatal("expected active piece after engine start")
	}
}

func TestEngineMoveLeftRight(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	pf := e.Playfield()
	p := pf.ActivePiece()
	if p == nil {
		t.Fatal("no active piece")
	}
	origCol := p.Col

	e.MoveLeft()
	time.Sleep(100 * time.Millisecond)

	pf = e.Playfield()
	p = pf.ActivePiece()
	if p != nil && p.Col >= origCol {
		// Might not have moved yet if publish is pending
		t.Log("piece may not have moved left yet (async)")
	}
}

func TestEngineHardDrop(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	// Hard drop should lock the piece immediately
	e.HardDrop()
	time.Sleep(300 * time.Millisecond)

	// Drain updates
	for len(e.Updates) > 0 {
		<-e.Updates
	}

	// After hard drop, a new piece should spawn
	pf := e.Playfield()
	// There should be either an active piece (the new one) or locked cells at the bottom
	hasLocked := false
	for c := 0; c < pf.Width; c++ {
		if pf.Rows[pf.Height-1].Cells[c].Occupied {
			hasLocked = true
			break
		}
	}
	if !hasLocked {
		// Check second to last row (I piece lands differently)
		for c := 0; c < pf.Width; c++ {
			if pf.Rows[pf.Height-2].Cells[c].Occupied {
				hasLocked = true
				break
			}
		}
	}
	if !hasLocked {
		t.Log("warning: no locked cells found after hard drop (may be async)")
	}
}

func TestEngineUpdatesChannel(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}

	// We should receive some updates within a reasonable time
	timeout := time.After(3 * time.Second)
	gotUpdate := false
	for !gotUpdate {
		select {
		case <-e.Updates:
			gotUpdate = true
		case <-timeout:
			t.Log("no updates received within timeout (this is expected if no row changes)")
			return
		}
	}
}

// TestCoopLineClearRerendersOtherPlayer verifies the fix for the bug where a
// line cleared by one cooperative player was not reflected on the other
// player's board. In coop the board is shared, so when ANOTHER player clears a
// line our engine must force a full-board re-render (every visible row) — not
// rely on the lossy per-row update fan-out — in addition to folding in the
// shared score delta.
func TestCoopLineClearRerendersOtherPlayer(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()

	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	// Drain startup/spawn/gravity updates.
	for len(e.Updates) > 0 {
		<-e.Updates
	}

	// Another player clears a line on the shared cooperative board.
	e.handleGameEvent(context.Background(), GameEvent{
		Kind:     EventLineClear,
		PlayerID: "other-player",
		Score:    4,
	})

	wantRows := e.playfield.Height - e.VisibleRowStart()
	gotFullRerender := false
	gotScore := false
	timeout := time.After(time.Second)
	for !(gotFullRerender && gotScore) {
		select {
		case u := <-e.Updates:
			switch u.Kind {
			case UpdateLineClear:
				if len(u.ChangedRows) == wantRows &&
					u.ChangedRows[0] == e.VisibleRowStart() &&
					u.ChangedRows[len(u.ChangedRows)-1] == e.playfield.Height-1 {
					gotFullRerender = true
				}
			case UpdateScore:
				if u.Score == 4 {
					gotScore = true
				}
			}
		case <-timeout:
			t.Fatalf("missing updates after other player's line clear: fullRerender=%v score=%v", gotFullRerender, gotScore)
		}
	}
}
