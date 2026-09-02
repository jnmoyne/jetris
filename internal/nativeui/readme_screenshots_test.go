package nativeui

// Opt-in README screenshot capture: runs a REAL 2v2 teams game with
// Guideline-style garbage — one hole per garbage row, clean (the rows of one
// raise share their hole column, so a raise's holes line up into a well) and
// attacks by the Guideline table — against an embedded JetStream server
// (four player engines plus a spectator, live gravity, pre-filled stacks and
// garbage raises published cell by cell, a fresh garbage row delivered
// through the real register → gated-shrink protocol that punches its hole)
// and renders the spectator and player game screens at 2x via a headless GPU
// window, frozen on the moment Team A clears a double and the garbage row
// that clear sends lands on Team B. Skipped unless FW_SNAPSHOT_DIR is set
// (needs a GPU):
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

const shotW, shotH = 2400, 1640 // 1200x820 dp at 2x for crisp README images

// The staged moment: Alice (team A, player 0) has just dropped an O piece
// into the two-wide, two-deep slot at clearCol, completing the two rows right
// above team A's garbage — a double, the smallest clear that attacks under
// the Guideline table (a single sends nothing). Both rows strobe white on
// team A's board while the garbage row her double sends lands — and strobes
// in her color — on team B's board.
const (
	clearCol   = 8 // first column of the slot the O piece drops into
	clearWidth = 2 // the slot is two columns wide...
	clearRows  = 2 // ...and two rows deep: the O piece completes both, a double
)

// The staged game is one the create wizard would make today: a 2v2 with the
// board-width slider at its default, so each team's board is the standard 10
// columns plus shotExtraCols for the second teammate — the width a README
// reader gets when they create a teams game themselves.
const shotExtraCols = config.DefaultExtraColumns

// shotBoardWidth is the staged team board's width.
func shotBoardWidth() int { return config.TeamBoardWidth(2, shotExtraCols) }

// raise is one garbage attack from an earlier exchange as it sits on a team
// board: rows rows sent by a clear of player by. The game raises clean
// Guideline garbage — one hole per row — and the rows of one raise share
// their hole column, so a raise's holes line up into a well.
type raise struct{ by, rows int }

// raisedRows is the number of garbage rows a list of raises stacks up.
func raisedRows(raises []raise) int {
	n := 0
	for _, r := range raises {
		n += r.rows
	}
	return n
}

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
// garbage raises at the bottom (bottom — the most recent — first, each
// stamped with the attacker whose clear sent it, each of its rows punched
// with the raise's one shared hole) and, above them, a ragged stack —
// per-column random heights with holes, random piece colors, cells owned by
// that team's two players (global roster indices team*2 and team*2+1). The
// lowest stack rows never come out complete on their own: with slot set the
// bottom clearRows rows are full except for the clearCol slot, which stays
// open all the way up — the slot the staged O piece drops into; without it,
// a hole is forced somewhere along the floor row.
func prefillTeamBoard(t *testing.T, js jetstream.JetStream, gameID string, team int, raises []raise, slot bool, rng *rand.Rand) {
	t.Helper()
	width := shotBoardWidth()
	row := config.TeamTotalRows(2, 2) - 1 // the bottom row
	prevHole := -1
	for _, r := range raises {
		// One hole column per raise — never the column of the raise right
		// below it, so the per-raise draw shows, and on the slotted board
		// outside the slot, so the staged piece lands on garbage, not over
		// a hole.
		hole := rng.Intn(width)
		for hole == prevHole || (slot && hole >= clearCol && hole < clearCol+clearWidth) {
			hole = rng.Intn(width)
		}
		prevHole = hole
		for k := 0; k < r.rows; k++ {
			for col := 0; col < width; col++ {
				if col == hole {
					continue
				}
				publishCell(t, js, gameID, team, row, col, game.Cell{
					Occupied: true, PieceType: game.PieceO, Adversarial: true, PlayerIdx: r.by,
				})
			}
			row--
		}
	}
	floor := row // lowest stack row, right above the garbage
	forcedHole := rng.Intn(width)
	for col := 0; col < width; col++ {
		if slot && col >= clearCol && col < clearCol+clearWidth {
			continue
		}
		h := 3 + rng.Intn(5) // stack height 3..7 above the garbage: the game has been going on for a while
		for d := 0; d < h; d++ {
			switch {
			case slot && d < clearRows:
				// the slot rows are the ones the O piece completes: no holes
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

// frameCtx builds the 2x layout context of one frame drawn at now, with a
// live (if eventless) input source: a zero Source is a DISABLED one since Gio
// v0.10, and every material widget in a disabled context paints its greyed
// look — not what the app shows anyone (see snapshotPNGSized).
func frameCtx(ops *op.Ops, now time.Time) layout.Context {
	return layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 2, PxPerSp: 2},
		Constraints: layout.Exact(image.Pt(shotW, shotH)),
		Now:         now,
		Source:      new(input.Router).Source(),
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
		ExtraColumns: shotExtraCols,
		NextCount:    3, // the player view shows the HUD's NEXT preview panel
		// Guideline-style garbage: every garbage row comes with one hole,
		// the rows of one raise share it (RandomGarbageHoles off), and a
		// clear attacks by the Guideline table — the victims' engines raise
		// the staged attack's row under exactly these rules.
		GarbageHoles: 1, GuidelineGarbage: true,
		Seed: 7, Status: config.GameStatusInProgress,
		CreatorID: "Alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// Both teams already carry garbage from earlier exchanges, listed bottom
	// (the most recent raise) up: team A (Alice + Chris) four rows — David's
	// double, Bob's double, then David's triple, whose two rows share their
	// hole — so the per-sender framing and the per-raise well both show; and
	// team B (Bob + David) three rows, all Chris's — a double over a triple —
	// so the row about to land from Alice stands out in her color. Team A's
	// stack keeps the slot open for the staged double.
	garbageA := []raise{{3, 1}, {2, 1}, {3, 2}}
	garbageB := []raise{{1, 1}, {1, 2}}
	rng := rand.New(rand.NewSource(3))
	prefillTeamBoard(t, js, gameID, 0, garbageA, true, rng)
	prefillTeamBoard(t, js, gameID, 1, garbageB, false, rng)
	height := config.TeamTotalRows(2, 2)
	floorA := height - 1 - raisedRows(garbageA) // team A's lowest stack row: the bottom row of the slot

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
	rowsA, rowsB := raisedRows(garbageA), raisedRows(garbageB)
	waitFor(t, "pre-attack boards", func() bool {
		return garbageRowsOf(spec, 0) == rowsA && garbageRowsOf(spec, 1) == rowsB &&
			garbageRowsOf(bob, 0) == rowsA && garbageRowsOf(bob, 1) == rowsB
	})
	for team := 0; team < config.DefaultTeamCount; team++ {
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
		{Name: "Alice", Text: "we are :)", GameID: gameID},
		{Name: "Bob", Text: "not for long", GameID: gameID},
		{Name: "David", Text: "ouch!", GameID: gameID},
	}
	// A game well under way: teams score two points a line (one per
	// teammate) and climb a level every ten lines — team A is 34 lines in,
	// its double just counted, team B 27.
	teamScores := []int{68, 54}
	teamLevels := []int{3, 2}

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
	bobApp.level = teamLevels[1] // a team board's level is the team's
	bobApp.rtt = 2500 * time.Microsecond
	bobApp.chatLog = chat
	layoutOnly(bobApp, time.Now())

	// The moment. Alice's O piece lands in the slot and completes both of
	// team A's floor rows — a double (its cells published locked, as her
	// hard drop's lock would be) — and the attack the Guideline table owes
	// for it advances team B's garbage register by one row, which Bob's and
	// David's engines apply through the real gated shrink cascade: the stack
	// rises, the new row fills the bottom, minus the one hole the cascade
	// punches in it.
	for d := 0; d < clearRows; d++ {
		for col := clearCol; col < clearCol+clearWidth; col++ {
			publishCell(t, js, gameID, 0, floorA-d, col, game.Cell{Occupied: true, PieceType: game.PieceO, PlayerIdx: 0})
		}
	}
	attack := game.AttackRows(clearRows, meta.GuidelineGarbage)
	reg, _ := json.Marshal(engine.GarbageRegister{Total: attack, By: 0})
	if _, err := js.Publish(ctx, config.TeamGarbageSubject(gameID, 1), reg); err != nil {
		t.Fatal(err)
	}
	landedB := rowsB + attack
	waitFor(t, "the clear and the landed garbage row on every replica", func() bool {
		return len(completedRowsOf(spec, 0)) == clearRows && len(completedRowsOf(bob, 0)) == clearRows &&
			garbageRowsOf(spec, 1) == landedB && garbageRowsOf(bob, 1) == landedB
	})

	// Screenshot 1: the spectator's view of both team boards. The landed row
	// strobes in Alice's color through the spectator screen's own detection
	// (one layout pass to observe it, then the frame drawn at the strobe's
	// epoch, i.e. lit). The line-clear strobe is the clearing team's own
	// celebration — the screen only strobes it for players, never on a
	// spectator's boards — so it is staged on the completed rows for the
	// picture.
	layoutOnly(specApp, time.Now())
	specApp.mu.Lock()
	landed, ok := specApp.specRowStrobes[1][height-1]
	if ok {
		cleared := make(map[int]rowStrobe, clearRows)
		for d := 0; d < clearRows; d++ {
			cleared[floorA-d] = rowStrobe{start: landed.start, col: colStrobe}
		}
		specApp.specRowStrobes[0] = cleared
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
