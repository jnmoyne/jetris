package nativeui

// Opt-in README screenshot capture: runs a REAL 2v2 teams game against an
// embedded JetStream server (four player engines plus a spectator, live
// gravity, pre-filled stacks and garbage rows published cell by cell, a fresh
// garbage row delivered through the real register → gated-shrink protocol)
// and renders the spectator and player game screens at 2x via a headless GPU
// window, frozen on the moment Team A clears a line and Team B receives the
// garbage row that clear sends. Skipped unless FW_SNAPSHOT_DIR is set (needs a
// GPU):
//
//	FW_SNAPSHOT_DIR=. go test ./internal/nativeui/ -run TestCaptureREADMEScreenshots
//
// Writes Jetris-screenshot-1.png (spectator view) and
// Jetris-screenshot-2.png (player view) into FW_SNAPSHOT_DIR.

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math/rand"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
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

const shotW, shotH = 2400, 1640 // 1200x820 dp at 2x for crisp README images

// The staged moment: Alice (team A, player 0) has just dropped an I piece flat
// into the 4-wide slot at clearCol, completing the row right above team A's
// garbage. That row strobes white on team A's board while the garbage row her
// clear sends lands — and strobes in her color — on team B's board.
const (
	clearCol   = 8 // first column of the slot the I piece drops into
	clearWidth = 4
)

// publishCell publishes one cell of a team board.
func publishCell(t *testing.T, js jetstream.JetStream, gameID string, team, row, col int, c game.Cell) {
	t.Helper()
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(context.Background(), config.TeamCellSubject(gameID, team, row, col), data); err != nil {
		t.Fatal(err)
	}
}

// prefillTeamBoard publishes a plausible mid-game state onto one team board:
// permanent garbage rows at the bottom (one per entry of garbageBy, bottom
// row first, each stamped with the attacker whose clear sent it) and, above
// them, a ragged stack — per-column random heights with holes, random piece
// colors, cells owned by that team's two players (global roster indices
// team*2 and team*2+1). The lowest stack row never comes out complete on its
// own: with slot set it is full except for the clearCol slot, which stays
// open all the way up — the well the staged I piece drops into; without it, a
// hole is forced somewhere along it.
func prefillTeamBoard(t *testing.T, js jetstream.JetStream, gameID string, team int, garbageBy []int, slot bool, rng *rand.Rand) {
	t.Helper()
	width := config.TeamBoardWidth(2)
	bottom := config.TeamTotalRows(2) - 1
	for i, by := range garbageBy {
		for col := 0; col < width; col++ {
			publishCell(t, js, gameID, team, bottom-i, col, game.Cell{
				Occupied: true, PieceType: game.PieceO, Adversarial: true, PlayerIdx: by,
			})
		}
	}
	floor := bottom - len(garbageBy) // lowest stack row, right above the garbage
	forcedHole := rng.Intn(width)
	for col := 0; col < width; col++ {
		if slot && col >= clearCol && col < clearCol+clearWidth {
			continue
		}
		h := 2 + rng.Intn(5) // stack height 2..6 above the garbage
		for d := 0; d < h; d++ {
			switch {
			case d == 0 && slot:
				// the floor row is the one the I piece completes: no holes
			case d == 0 && col == forcedHole, rng.Intn(100) < 18:
				continue // holes keep the rows incomplete and the stack ragged
			}
			publishCell(t, js, gameID, team, floor-d, col, game.Cell{
				Occupied:  true,
				PieceType: game.PieceType(rng.Intn(7)),
				PlayerIdx: team*2 + rng.Intn(2),
			})
		}
	}
}

// boardOf returns an engine's replica of one team board: its own board, or
// the one it follows through the opposing-team consumer (the spectator engine
// consumes team 0 as its "own" board).
func boardOf(e *engine.Engine, team int) (engine.BoardSnapshot, bool) {
	if team == e.TeamIdx() {
		return e.Snapshot(), true
	}
	snap, ok := e.OpponentSnapshots()[engine.TeamBoardKey(team)]
	return snap, ok
}

// garbageRowsOf is the bottom-anchored garbage row count of a board replica,
// -1 while the replica has not loaded.
func garbageRowsOf(e *engine.Engine, team int) int {
	snap, ok := boardOf(e, team)
	if !ok {
		return -1
	}
	return adversarialRows(snap)
}

// completedRowsOf lists the complete rows of a board replica.
func completedRowsOf(e *engine.Engine, team int) []int {
	snap, ok := boardOf(e, team)
	if !ok {
		return nil
	}
	return game.CompletedRows(&game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows})
}

// waitFor polls cond until it holds, failing the test after ten seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// frameCtx builds the 2x layout context of one frame drawn at now.
func frameCtx(ops *op.Ops, now time.Time) layout.Context {
	return layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 2, PxPerSp: 2},
		Constraints: layout.Exact(image.Pt(shotW, shotH)),
		Now:         now,
	}
}

// layoutOnly runs one layout pass without rendering it, so the game screen's
// per-frame detection (landed garbage → row strobes and impact shake) gets to
// observe the current board state exactly as a live frame would.
func layoutOnly(a *App, now time.Time) {
	var ops op.Ops
	a.layout(frameCtx(&ops, now))
}

// shootApp renders one frame of the app at 2x, as of now, and writes it to
// dir/name.
func shootApp(t *testing.T, w *headless.Window, a *App, now time.Time, dir, name string) {
	t.Helper()
	var ops op.Ops
	a.layout(frameCtx(&ops, now))
	if err := w.Frame(&ops); err != nil {
		t.Fatalf("frame %s: %v", name, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, shotW, shotH))
	if err := w.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", name, err)
	}
	f, err := os.Create(dir + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureREADMEScreenshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to capture README screenshots")
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
	ctx := context.Background()
	gameID := "readme-shots"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeTeams, PlayerCount: 4, TeamSize: 2,
		NextCount: 3, // the player view shows the HUD's NEXT preview panel
		Seed:      7, Status: config.GameStatusInProgress,
		CreatorID: "Alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// Both teams already carry a few rows of garbage from earlier exchanges:
	// team A (Alice + Chris) three rows sent by team B — David's, Bob's and
	// David's again, bottom up, so the per-sender framing shows — and team B
	// (Bob + David) three rows, all sent by Chris, so the row about to land
	// from Alice stands out in her color. Team A's stack keeps the slot open
	// for the staged clear.
	garbageA, garbageB := []int{3, 2, 3}, []int{1, 1, 1}
	rng := rand.New(rand.NewSource(3))
	prefillTeamBoard(t, js, gameID, 0, garbageA, true, rng)
	prefillTeamBoard(t, js, gameID, 1, garbageB, false, rng)
	height := config.TeamTotalRows(2)
	floorA := height - 1 - len(garbageA) // team A's lowest stack row: the one the I piece completes

	// Four player engines (team A: Alice+Chris, team B: Bob+David) and a
	// spectator engine, all consuming the real game stream.
	specs := []struct {
		id              string
		idx, team, slot int
	}{
		{"Alice", 0, 0, 0},
		{"Chris", 1, 0, 1},
		{"Bob", 2, 1, 0},
		{"David", 3, 1, 1},
	}
	engines := make([]*engine.Engine, len(specs))
	for i, s := range specs {
		e := engine.New(js, gameID, s.id, "", config.ModeTeams, engine.ModePlayer, s.idx, s.team, s.slot)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		engines[i] = e
	}
	spec := engine.New(js, gameID, "watcher", "", config.ModeTeams, engine.ModeSpectator, 0, 0, 0)
	if err := spec.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(spec.Stop)
	bob := engines[2]

	// Let the first pieces spawn. All engines share the game seed, so at piece
	// index 0 everyone holds the same tetromino — have some players hard-drop
	// (locking onto the prefilled stacks, at their spawn columns: well clear
	// of team A's slot) so their next, different pieces are the ones in
	// flight. Then stagger some real moves so the live pieces sit at
	// different heights and orientations.
	time.Sleep(1500 * time.Millisecond)
	engines[0].HardDrop()
	engines[2].HardDrop()
	time.Sleep(600 * time.Millisecond)
	engines[3].HardDrop()
	engines[0].HardDrop() // Alice again: two locks put her on piece index 2
	time.Sleep(600 * time.Millisecond)
	for round := 0; round < 3; round++ {
		for i, e := range engines {
			if (i+round)%2 == 0 {
				e.RotateCW()
			}
			for d := 0; d <= i%3; d++ {
				e.MoveDown()
			}
			if i%2 == 0 {
				e.MoveLeft()
			} else {
				e.MoveRight()
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)

	// Every replica we draw from must hold the pre-attack boards before the
	// screens seed their garbage detection — and no row may be complete yet
	// (the prefill is seeded, so a complete row here means the seed changed).
	waitFor(t, "pre-attack boards", func() bool {
		return garbageRowsOf(spec, 0) == len(garbageA) && garbageRowsOf(spec, 1) == len(garbageB) &&
			garbageRowsOf(bob, 0) == len(garbageA) && garbageRowsOf(bob, 1) == len(garbageB)
	})
	for team := 0; team < config.TeamCount; team++ {
		if rows := completedRowsOf(spec, team); len(rows) > 0 {
			t.Fatalf("team %d prefill produced complete rows %v before the staged clear", team, rows)
		}
	}

	players := []lobby.PlayerSummary{
		{PlayerID: "Alice", Name: "Alice", Team: 0, TeamSlot: 0},
		{PlayerID: "Chris", Name: "Chris", Team: 0, TeamSlot: 1},
		{PlayerID: "Bob", Name: "Bob", Team: 1, TeamSlot: 0},
		{PlayerID: "David", Name: "David", Team: 1, TeamSlot: 1},
	}
	chat := []lobby.ChatMessage{
		{Name: "Erin", Text: "who's winning?"}, // lobby line, folded in as @lobby
		{Name: "Bob", Text: "good luck!", GameID: gameID},
		{Name: "Alice", Text: "you'll need it :)", GameID: gameID},
		{Name: "David", Text: "ouch!", GameID: gameID},
	}
	teamScores := [config.TeamCount]int{16, 9} // team A's clear just scored its two players
	teamLevels := [config.TeamCount]int{1, 0}

	w, err := headless.NewWindow(shotW, shotH)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	// The spectator's app and Bob's (team B) app, each seeded with one
	// unrendered layout pass so their per-frame garbage detection knows the
	// pre-attack boards and will strobe exactly the row about to land.
	specApp := newTestApp()
	specApp.eng = spec
	specApp.screen = screenGame
	specApp.gameStatus = string(config.GameStatusInProgress)
	specApp.gamePlayers = players
	specApp.teamScores = teamScores
	specApp.teamLevels = teamLevels
	specApp.chatLog = chat
	layoutOnly(specApp, time.Now())

	bobApp := newTestApp()
	bobApp.eng = bob
	bobApp.screen = screenGame
	bobApp.gameStatus = string(config.GameStatusInProgress)
	bobApp.gamePlayers = players
	bobApp.teamScores = teamScores
	bobApp.teamLevels = teamLevels
	bobApp.level = 0
	bobApp.rtt = 2500 * time.Microsecond
	bobApp.chatLog = chat
	layoutOnly(bobApp, time.Now())

	// The moment. Alice's I piece lands flat in the slot and completes team
	// A's floor row (its cells published locked, as her hard drop's lock
	// would be), and her clear's attack advances team B's garbage register
	// by one row — which Bob's and David's engines apply through the real
	// gated shrink cascade: the stack rises, the new row fills the bottom.
	for col := clearCol; col < clearCol+clearWidth; col++ {
		publishCell(t, js, gameID, 0, floorA, col, game.Cell{Occupied: true, PieceType: game.PieceI, PlayerIdx: 0})
	}
	reg, _ := json.Marshal(engine.GarbageRegister{Total: 1, By: 0})
	if _, err := js.Publish(ctx, config.TeamGarbageSubject(gameID, 1), reg); err != nil {
		t.Fatal(err)
	}
	landedB := len(garbageB) + 1
	waitFor(t, "the clear and the landed garbage row on every replica", func() bool {
		return len(completedRowsOf(spec, 0)) == 1 && len(completedRowsOf(bob, 0)) == 1 &&
			garbageRowsOf(spec, 1) == landedB && garbageRowsOf(bob, 1) == landedB
	})

	// Screenshot 1: the spectator's view of both team boards. The landed row
	// strobes in Alice's color through the spectator screen's own detection
	// (one layout pass to observe it, then the frame drawn at the strobe's
	// epoch, i.e. lit). The line-clear strobe is the clearing team's own
	// celebration — the screen only strobes it for players, never on a
	// spectator's boards — so it is staged on the completed row for the
	// picture.
	layoutOnly(specApp, time.Now())
	specApp.mu.Lock()
	landed, ok := specApp.specRowStrobes[1][height-1]
	if ok {
		specApp.specRowStrobes[0] = map[int]rowStrobe{floorA: {start: landed.start, col: colStrobe}}
	}
	specApp.mu.Unlock()
	if !ok {
		t.Fatal("spectator screen did not strobe the landed garbage row")
	}
	shootApp(t, w, specApp, landed.start, dir, "Jetris-screenshot-1.png")

	// Screenshot 2: Bob's (team B) player view at the impact, with the
	// opposing-team sidebar. His own board's detection strobes the landed
	// row and kicks the shake; the frame is drawn at that epoch — strobe
	// lit, shake at its zero crossing — so the well sits straight.
	layoutOnly(bobApp, time.Now())
	bobApp.mu.Lock()
	impact := bobApp.shakeStart
	bobApp.mu.Unlock()
	if impact.IsZero() {
		t.Fatal("player screen did not register the landed garbage row")
	}
	shootApp(t, w, bobApp, impact, dir, "Jetris-screenshot-2.png")
}
