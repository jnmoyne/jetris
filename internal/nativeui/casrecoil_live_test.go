package nativeui

// Opt-in, end to end: a rejected write on a LIVE engine — a competing write
// landed on a cell the player's next shift guards, so its CAS fails — told
// through the real pump into the real game layout, on the optimistic
// display. Writes the frames after the kick as PNGs for inspection and
// checks the flight starts from the lost shift's column. Skipped unless FW_SNAPSHOT_DIR
// is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestCASRecoilLive

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestCASRecoilLive(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to run the live CAS-recoil check")
	}
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const gameID = "cas-recoil-live"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2, NextCount: 3, Seed: 7,
		Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	size := image.Pt(1200, 820)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	a := newTestApp()
	e := engine.New(js, gameID, "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	a.eng = e
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.labEnum.Value = labAsync
	pumped := make(chan struct{})
	go func() { a.pumpEngine(ctx, e); close(pumped) }()
	defer func() { cancel(); <-pumped }()
	waitUntil(t, 5*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")

	router := new(input.Router)
	frame := func(at time.Time) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Now: at, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(size), Source: router.Source()}
		a.layout(gtx)
	}
	frame(time.Now()) // applies the switch's modes to the engine and draws the piece where it stands
	settled := func() bool {
		return !e.PipelineBroken() && e.InflightSteps() == 0 && len(e.BufferedMoves()) == 0
	}
	kickAt := func() time.Time {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.casKickAt
	}
	empty, _ := game.Cell{}.Marshal()
	var kick time.Time
	for attempt := 0; attempt < 30 && kick.IsZero(); attempt++ {
		waitUntil(t, 5*time.Second, settled, "the pipeline to settle")
		p := *e.Playfield().ActivePieceForPlayer(0)
		next, move := p, e.MoveLeft
		next.Col--
		if p.Col <= 1 {
			next.Col, move = p.Col+1, e.MoveRight
		}
		// The competing write lands on the shift's guarded cells first; the
		// shift then goes out with an expectation built before the echo
		// reaches the engine — usually, so this may take a few tries.
		var guarded [][2]int
		have := map[[2]int]bool{}
		for _, rc := range p.Cells() {
			have[[2]int{rc[0], rc[1]}] = true
		}
		for _, rc := range next.Cells() {
			if !have[[2]int{rc[0], rc[1]}] {
				guarded = append(guarded, [2]int{rc[0], rc[1]})
			}
		}
		for _, rc := range guarded {
			if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, rc[0], rc[1]), empty); err != nil {
				t.Fatal(err)
			}
		}
		move()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if kick = kickAt(); !kick.IsZero() {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if kick.IsZero() {
		t.Fatal("never provoked a CAS loss")
	}
	t.Logf("CAS loss provoked, kick at %v", kick)

	offsets := []time.Duration{5, 40, 80, 120, 160, 240, 600, 1150}
	for i, off := range offsets {
		at := kick.Add(off * time.Millisecond)
		snapshotPNGSized(t, w, dir, fmt.Sprintf("cas_recoil_live_%d_%03dms", i, off), size, func(gtx C) {
			gtx.Now = at
			a.layout(gtx)
		})
		if kickAt() != kick {
			t.Fatalf("frame %v: the kick moved to %v", off, kickAt())
		}
	}
	a.mu.Lock()
	from := a.casKickFrom
	a.mu.Unlock()
	if from[1] != 0 || (from[0] != 1 && from[0] != -1) {
		t.Fatalf("snap-back from %v, want the lost shift's column", from)
	}
	t.Logf("recoil snap-back from %v (cells)", from)
}
