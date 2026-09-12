package nativeui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// A beta player on a far server (Batch RTT 189 ms, Optimistic async, the
// GUARD knob at its default) playing at their own top speed: the space bar
// drops the piece, and the next piece's keys — a rotation, the next drop —
// are pressed before that piece is on the board. Through the real frame
// loop (the keys, the accidental-drop guard, the engine's queue) every one
// of them must land on the next piece.

// farGame is the game screen on a real engine over a 190 ms round trip, one
// frame laid out so the board holds the keys.
func farGame(t *testing.T, gameID string) (*App, *input.Router, *engine.Engine) {
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
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2, Seed: 5,
		Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	slowNC, err := nats.Connect(testutil.StartLagProxy(t, url, 95*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slowNC.Close)
	slowJS, err := jetstream.New(slowNC)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(slowJS, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	e.SetPublishMode(engine.PublishOptimistic)
	e.SetInflightLimit(1)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)

	a := newTestApp()
	a.eng = e
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	r := new(input.Router)
	deadline := time.Now().Add(10 * time.Second)
	for !e.HasActivePiece() {
		if time.Now().After(deadline) {
			t.Fatal("no first piece")
		}
		time.Sleep(10 * time.Millisecond)
	}
	gameFrameAt(a, r, time.Now()) // the board takes the keys
	return a, r, e
}

// frames runs the frame loop at 60 Hz until cond holds or the deadline is
// out; reports whether it held.
func frames(a *App, r *input.Router, d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		gameFrameAt(a, r, time.Now())
		if cond() {
			return true
		}
		time.Sleep(16 * time.Millisecond)
	}
	return cond()
}

// tap presses and releases a key, a frame each.
func tap(a *App, r *input.Router, name key.Name) {
	r.Queue(key.Event{Name: name, State: key.Press})
	gameFrameAt(a, r, time.Now())
	r.Queue(key.Event{Name: name, State: key.Release})
	gameFrameAt(a, r, time.Now())
}

// TestFarServerRotationPressedBehindTheDropRotatesTheNextPiece: space, then
// ↑ while the drop's commit is still out — the next piece spawns rotated.
func TestFarServerRotationPressedBehindTheDropRotatesTheNextPiece(t *testing.T) {
	a, r, e := farGame(t, "far-rotate")
	// Seed 5 deals I, O, L: the O turns into itself, so the L is the piece
	// whose rotation shows.
	tap(a, r, key.NameSpace)
	if !frames(a, r, 5*time.Second, func() bool { return e.PieceIdx() == 1 && e.HasActivePiece() }) {
		t.Fatal("no second piece")
	}
	tap(a, r, key.NameSpace)
	time.Sleep(40 * time.Millisecond) // inside the drop's round trip
	tap(a, r, key.NameUpArrow)
	if !frames(a, r, 5*time.Second, func() bool { return e.PieceIdx() == 2 && e.HasActivePiece() }) {
		t.Fatal("no third piece")
	}
	frames(a, r, 300*time.Millisecond, func() bool { return false }) // the held rotation's own round trip
	p := e.Playfield().ActivePieceForPlayer(0)
	if p == nil {
		t.Fatal("no piece after the spawn")
	}
	if p.Orientation != 1 {
		t.Fatalf("the rotation pressed behind the drop was lost: next piece orientation %d, want 1", p.Orientation)
	}
}

// TestFarServerSpacePressedBehindTheDropDropsTheNextPiece: space, then space
// again while the first drop's commit is still out — the guard lets it
// through (a drop behind a drop is the player's), and it drops the next
// piece the moment it is there.
func TestFarServerSpacePressedBehindTheDropDropsTheNextPiece(t *testing.T) {
	a, r, e := farGame(t, "far-drop")
	tap(a, r, key.NameSpace)
	time.Sleep(40 * time.Millisecond) // inside the drop's round trip
	tap(a, r, key.NameSpace)
	// Gravity alone (a row a second at level 1) cannot lock a piece in this
	// time: only the second drop can bring the index to 2.
	if !frames(a, r, 3*time.Second, func() bool { return e.PieceIdx() >= 2 }) {
		t.Fatalf("the drop pressed behind the drop was lost: piece index %d, want 2", e.PieceIdx())
	}
}

// TestFarServerSpacePressedInTheGapDropsTheNextPiece: space pressed after
// the drop has taken the piece off the board, the next one still on its way
// — the frames see the gap, then the index moving on without a piece — is
// the next piece's too.
func TestFarServerSpacePressedInTheGapDropsTheNextPiece(t *testing.T) {
	a, r, e := farGame(t, "far-gap-drop")
	tap(a, r, key.NameSpace)
	if !frames(a, r, 2*time.Second, func() bool { return !e.HasActivePiece() }) {
		t.Fatal("the drop never took the piece off the board")
	}
	tap(a, r, key.NameSpace)
	if !frames(a, r, 3*time.Second, func() bool { return e.PieceIdx() >= 2 }) {
		t.Fatalf("the drop pressed in the gap was lost: piece index %d, want 2", e.PieceIdx())
	}
}
