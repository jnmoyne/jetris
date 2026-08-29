package nativeui

import (
	"context"
	"encoding/json"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// Rapid touch input through the real router: a player hammering the screen
// as fast as the frames come must never lose a tap or a swipe — every input
// lands, in order, no matter how many in a row. (The stall a tablet showed
// — touches dead for a few seconds while the finger kept coming — is what
// these guard against on the Go side of the browser.)

// rapidGame is the router-level fixture: the game screen at thumb size with
// eng as the engine, one frame laid out so the playfield can be found.
func rapidGame(t *testing.T, eng *engine.Engine) (*App, *input.Router, image.Rectangle, float32) {
	t.Helper()
	a := newTestApp()
	a.touchUI = true
	a.eng = eng
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	r := new(input.Router)
	gameFrame(a, r)
	field, ok := semanticBounds(r, playfieldLabel)
	if !ok {
		t.Fatal("no playfield")
	}
	return a, r, field, float32(a.gest.cell)
}

func queuedEngine() *engine.Engine {
	return engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
}

// TestRapidTouchTapsAllLand: taps on the playfield as fast as the frames
// come — every tap a new finger, as iOS numbers them — each queue a rotate.
func TestRapidTouchTapsAllLand(t *testing.T) {
	a, r, field, cell := rapidGame(t, queuedEngine())
	cx, cy := float32(field.Min.X+field.Dx()/2)+cell, float32(field.Min.Y+field.Dy()/2)
	at := time.Duration(0)
	for i := 0; i < 40; i++ {
		id := pointer.ID(100 + i)
		touch(r, pointer.Press, id, cx, cy, at)
		gameFrame(a, r)
		touch(r, pointer.Release, id, cx, cy, at+40*ms)
		gameFrame(a, r)
		at += 80 * ms
		if got := len(a.eng.BufferedMoves()); got != i+1 {
			t.Fatalf("after tap %d: %d moves buffered, want %d", i+1, got, i+1)
		}
	}
}

// TestRapidPadTapsAllLand: the same on a pad button — a Clickable, which
// takes the keyboard focus on every press and hands it back the next frame.
func TestRapidPadTapsAllLand(t *testing.T) {
	a, r, field, _ := rapidGame(t, queuedEngine())
	btn, ok := nearestButton(r, image.Pt(field.Min.X, field.Min.Y+field.Dy()/2))
	if !ok {
		t.Fatal("no button")
	}
	bx, by := float32(btn.Min.X+btn.Dx()/2), float32(btn.Min.Y+btn.Dy()/2)
	at := time.Duration(0)
	for i := 0; i < 40; i++ {
		id := pointer.ID(100 + i)
		touch(r, pointer.Press, id, bx, by, at)
		gameFrame(a, r)
		touch(r, pointer.Release, id, bx, by, at+40*ms)
		gameFrame(a, r)
		at += 80 * ms
		if got := len(a.eng.BufferedMoves()); got != i+1 {
			t.Fatalf("after pad tap %d: %d moves buffered, want %d", i+1, got, i+1)
		}
	}
}

// TestRapidSwipesAllLand: back-and-forth three-column swipes, one finger
// after another, against a LIVE engine (the recognizer queues at most
// gestureBufferCap moves ahead, so the queue must drain): after each swipe
// the piece sits three columns from where it was.
func TestRapidSwipesAllLand(t *testing.T) {
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
	const gameID = "rapid-swipes"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2, NextCount: 1, Seed: 7,
		Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(js, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	leftmost := func() int {
		p := eng.Playfield().ActivePieceForPlayer(0)
		if p == nil {
			return -1
		}
		col := -1
		for _, c := range p.Cells() {
			if col < 0 || c[1] < col {
				col = c[1]
			}
		}
		return col
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("the first piece", func() bool { return leftmost() >= 0 })

	a, r, field, cell := rapidGame(t, eng)
	cx, cy := float32(field.Min.X+field.Dx()/2), float32(field.Min.Y+field.Dy()/2)
	at := time.Duration(0)
	col := leftmost()
	for i := 0; i < 12; i++ {
		id := pointer.ID(100 + i)
		dir := float32(1)
		if i%2 == 1 {
			dir = -1
		}
		x0 := cx - dir*1.5*cell
		touch(r, pointer.Press, id, x0, cy, at)
		gameFrame(a, r)
		for k := 1; k <= 3; k++ {
			touch(r, pointer.Move, id, x0+dir*float32(k)*cell, cy, at+time.Duration(k)*16*ms)
			gameFrame(a, r)
		}
		touch(r, pointer.Release, id, x0+dir*3*cell, cy, at+70*ms)
		gameFrame(a, r)
		at += 100 * ms
		// The recognizer may still owe steps (queued at most gestureBufferCap
		// ahead): frames without events catch up as the engine drains.
		want := col + 3*int(dir)
		waitFor("the swipe to land", func() bool {
			gameFrame(a, r)
			return len(eng.BufferedMoves()) == 0 && !a.gest.pending() && leftmost() == want
		})
		col = want
	}
}
