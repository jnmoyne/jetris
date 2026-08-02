package nativeui

// Opt-in README screenshot capture: runs a REAL 2v2 teams game against an
// embedded JetStream server (four player engines plus a spectator, live
// gravity, pre-filled stacks published cell by cell) and renders the
// spectator and player game screens at 2x via a headless GPU window.
// Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
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

// prefillTeamBoard publishes a plausible mid-game stack onto one team board:
// per-column random heights with holes, random piece colors, cells owned by
// that team's two players (global roster indices team*2 and team*2+1).
func prefillTeamBoard(t *testing.T, js jetstream.JetStream, gameID string, team int, rng *rand.Rand) {
	t.Helper()
	ctx := context.Background()
	width := config.TeamBoardWidth(2)
	bottom := config.TeamTotalRows(2) - 1
	for col := 0; col < width; col++ {
		h := 2 + rng.Intn(6) // stack height 2..7
		for d := 0; d < h; d++ {
			if rng.Intn(100) < 18 {
				continue // holes keep every row incomplete and the stack ragged
			}
			c := game.Cell{
				Occupied:  true,
				PieceType: game.PieceType(rng.Intn(7)),
				PlayerIdx: team*2 + rng.Intn(2),
			}
			data, err := c.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := js.Publish(ctx, config.TeamCellSubject(gameID, team, bottom-d, col), data); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// shootApp renders one frame of the app at 2x and writes it to dir/name.
func shootApp(t *testing.T, w *headless.Window, a *App, dir, name string) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 2, PxPerSp: 2},
		Constraints: layout.Exact(image.Pt(shotW, shotH)),
		Now:         time.Now(),
	}
	a.layout(gtx)
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

	rng := rand.New(rand.NewSource(3))
	prefillTeamBoard(t, js, gameID, 0, rng)
	prefillTeamBoard(t, js, gameID, 1, rng)

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

	// Let the first pieces spawn. All engines share the game seed, so at piece
	// index 0 everyone holds the same tetromino — have some players hard-drop
	// (locking onto the prefilled stacks) so their next, different pieces are
	// the ones in flight. Then stagger some real moves so the live pieces sit
	// at different heights and orientations.
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
	}

	w, err := headless.NewWindow(shotW, shotH)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	// Screenshot 1: the spectator's view of both team boards.
	a := newTestApp()
	a.eng = spec
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.gamePlayers = players
	a.teamScores = [config.TeamCount]int{14, 9}
	a.teamLevels = [config.TeamCount]int{1, 0}
	a.chatLog = chat
	shootApp(t, w, a, dir, "Jetris-screenshot-1.png")

	// Screenshot 2: Bob's (team B) player view with the opposing-team sidebar.
	a = newTestApp()
	a.eng = engines[2]
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.gamePlayers = players
	a.teamScores = [config.TeamCount]int{14, 9}
	a.teamLevels = [config.TeamCount]int{1, 0}
	a.level = 0
	a.rtt = 2500 * time.Microsecond
	a.chatLog = chat
	shootApp(t, w, a, dir, "Jetris-screenshot-2.png")
}
