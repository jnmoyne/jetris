package nativeui

import (
	"context"
	"encoding/json"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/pointer"
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

// liveEngine starts an embedded NATS server, publishes a game and starts a
// player engine on it. The board fitting is only honest against a real one:
// a transport-less engine never learns its visible row start and draws all
// 40 rows, which floors every cell size the layout would pick.
func liveEngine(t *testing.T, gameID string, gmode config.GameMode) *engine.Engine {
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
		GameID: gameID, Mode: gmode, PlayerCount: 2, NextCount: 3, Hold: true, Seed: 7,
		Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(js, gameID, "alice", "bob", gmode, engine.ModePlayer, 0, 0, 0)
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	time.Sleep(300 * time.Millisecond) // the first piece spawns and the preview fills
	return eng
}

// screenRig drives a whole game screen of a given size through a real Gio
// router, so the bar's buttons and the playfield's gesture surface are
// hit-tested exactly as they are in the app.
type screenRig struct {
	a  *App
	r  *input.Router
	sz image.Point
}

func newScreenRig(t *testing.T, sz image.Point, dev deviceKind, eng *engine.Engine) *screenRig {
	t.Helper()
	a := newTestApp()
	a.touchUI = dev != deviceDesktop
	a.deviceHint, a.deviceHinted = dev, true
	a.eng = eng
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	g := &screenRig{a: a, r: new(input.Router), sz: sz}
	g.frame()
	return g
}

// frame lays the screen out through the router and commits it, the way the
// window loop does.
func (g *screenRig) frame() {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(g.sz),
		Source:      g.r.Source(),
	}
	g.a.layout(gtx)
	g.r.Frame(ops)
}

// tap queues a touch press and release at a window position and runs the
// frames that dispatch it.
func (g *screenRig) tap(x, y float32) {
	touch(g.r, pointer.Press, 1, x, y, 0)
	touch(g.r, pointer.Release, 1, x, y, 60*time.Millisecond)
	g.frame()
	g.frame()
}

// field is the touch-gesture surface on screen.
func (g *screenRig) field(t *testing.T) image.Rectangle {
	t.Helper()
	r, ok := semanticBounds(g.r, playfieldLabel)
	if !ok {
		t.Fatal("no playfield in the semantic tree")
	}
	return r
}

// boardPx is the playfield's own drawn width and height, from the cell size
// the layout settled on (drawBoard's own arithmetic).
func (g *screenRig) boardPx() image.Point {
	snap := g.a.eng.Snapshot()
	cell := g.a.gest.cell
	fw := max(cell/8, 2)
	return image.Pt(snap.Width*cell+2*fw, (snap.Height-snap.VisibleStart)*cell+2*fw)
}

// The bar's button positions in a 1 px/dp test window: the menu is the
// leftmost, then (right to left) the chat button and the pad switch.
func barCenterY() float32    { return compactBarH / 2 }
func barMenuX() float32      { return 6 + (compactBarH-14)/2 }
func barChatX(w int) float32 { return float32(w) - barMenuX() }
func barPadX(w int) float32  { return float32(w) - barMenuX() - (compactBarH - 14) - 6 }

// TestCompactWellsAndPadBuyTheBoardRoom pins what the compact screen trades
// away and what the playfield gets for it: with the HOLD/NEXT wells moved
// into the top bar and the on-screen pad switched off, the same board column
// fits a distinctly bigger cell than it does carrying both.
func TestCompactWellsAndPadBuyTheBoardRoom(t *testing.T) {
	const cols, rows = 10, 24
	a := newTestApp()
	a.touchUI = true
	a.form = screenForm{device: devicePhone, w: 390, h: 844, portrait: true, compact: true}
	wells := sideWells{hold: true, next: true,
		holdH: func(cell int) int { return a.holdWellBox(looseCtx(400, 400), game.PieceI, false, false, cell).Size.Y },
		nextH: func(cell int) int {
			return a.nextWell(looseCtx(400, 400), []game.PieceType{game.PieceT, game.PieceT, game.PieceT}, cell).Size.Y
		},
	}
	// The board column a phone's compact screen hands the playfield.
	col := looseCtx(382, 780)
	full := a.fitBoardAndPad(col, cols, rows, wells, true, true, true)
	bare := a.fitBoardAndPad(col, cols, rows, sideWells{}, true, false, true)
	if bare.cell <= full.cell {
		t.Fatalf("cell without the wells and the pad %d px, with them %d px: the compact screen bought nothing",
			bare.cell, full.cell)
	}
	if got, want := bare.cell*cols, 382*2/3; got < want {
		t.Errorf("the bare playfield is %d px wide in a %d px column, under two thirds of it", got, 382)
	}
}

// TestCompactBoardSpansThePhone: on a phone the playfield gets the width of
// the screen, and the surface a swipe lands on gets ALL of it — wider than
// the well itself, out to the edges of the board column.
func TestCompactBoardSpansThePhone(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-span", config.ModeCompetitive))
	if !g.a.form.compact {
		t.Fatalf("a %dx%d phone did not get the compact screen: %+v", w, h, g.a.form)
	}
	if g.a.padVisible() {
		t.Fatal("the on-screen pad is on by default on a phone held portrait")
	}
	board, field := g.boardPx(), g.field(t)
	if board.X*2 <= w {
		t.Errorf("playfield %d px wide on a %d px phone: under half the screen", board.X, w)
	}
	if field.Dx() <= board.X {
		t.Errorf("swipe surface %d px, playfield %d px: no room gained", field.Dx(), board.X)
	}
	// It reaches the edges: the board column's own 4 dp inset, and the 1 px
	// the surface leaves itself, are all that is missing on either side.
	if slack := w - field.Dx(); slack > 16 {
		t.Errorf("swipe surface %d px on a %d px phone: %d px of it unreachable", field.Dx(), w, slack)
	}
	if g.a.gest.fieldW != field.Dx() {
		t.Errorf("recognizer width %d, surface on screen %v", g.a.gest.fieldW, field)
	}
}

// TestSwipeSurfaceRotateSplitFollowsTheWell: the surface is wider than the
// playfield, so its middle has to be the PLAYFIELD's — a tap rotates by
// which side of the well it fell, including out in the margin where there is
// no board under the finger at all.
func TestSwipeSurfaceRotateSplitFollowsTheWell(t *testing.T) {
	// A transport-less engine here on purpose: it never drains its move
	// queue, so BufferedMoves is the exact list of what a tap produced.
	newEng := func() *engine.Engine {
		return engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	}
	probe := newScreenRig(t, image.Pt(390, 844), devicePhone, newEng())
	field := probe.field(t)
	cell := probe.a.gest.cell
	cx := float32(field.Min.X + field.Dx()/2)
	cy := float32(field.Min.Y + field.Dy()/2)
	for _, c := range []struct {
		name string
		x    float32
		want engine.MoveType
	}{
		{"just left of the well's middle", cx - float32(cell), engine.RotateCCW},
		{"just right of the well's middle", cx + float32(cell), engine.RotateCW},
		{"off the board, at the surface's left edge", float32(field.Min.X) + 3, engine.RotateCCW},
		{"off the board, at the surface's right edge", float32(field.Max.X) - 3, engine.RotateCW},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newScreenRig(t, image.Pt(390, 844), devicePhone, newEng())
			g.tap(c.x, cy)
			if got := g.a.eng.BufferedMoves(); !reflect.DeepEqual(got, []engine.MoveType{c.want}) {
				t.Fatalf("tap %s: moves %v, want exactly [%v]", c.name, got, c.want)
			}
		})
	}
}

// TestCompactBarOpensAndClosesThePanels drives the bar's buttons for real:
// the menu opens the HUD panel, the chat button swaps to the chat panel, a
// tap on the board beside a panel closes it, a tap inside the panel does not
// — and no gesture under an open panel ever reaches the piece.
func TestCompactBarOpensAndClosesThePanels(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-panels", config.ModeCompetitive))
	field := g.field(t)
	fieldY := float32(field.Min.Y + field.Dy()/2)

	g.tap(barMenuX(), barCenterY())
	if !g.a.hudDrawer {
		t.Fatal("the menu button did not open the HUD panel")
	}
	// A tap on the board under the open panel neither moves the piece nor
	// (the panel covers that point) closes anything.
	queued := len(g.a.eng.BufferedMoves())
	g.tap(float32(field.Min.X+field.Dx()/4), fieldY)
	if got := g.a.eng.BufferedMoves(); len(got) != queued {
		t.Fatalf("a tap under the open panel queued %v", got[queued:])
	}
	if !g.a.hudDrawer {
		t.Fatal("a tap inside the panel closed it")
	}
	// A tap on the board BESIDE the panel closes it, and still queues nothing.
	g.tap(float32(w)-6, fieldY)
	if g.a.hudDrawer {
		t.Fatal("a tap beside the panel did not close it")
	}
	if got := g.a.eng.BufferedMoves(); len(got) != queued {
		t.Fatalf("the closing tap also queued %v", got[queued:])
	}

	// The chat button swaps panels rather than stacking them, and closes
	// the one it opened.
	g.tap(barMenuX(), barCenterY())
	g.tap(barChatX(w), barCenterY())
	if g.a.hudDrawer || !g.a.chatDrawer {
		t.Fatalf("chat button: hud=%v chat=%v, want only the chat open", g.a.hudDrawer, g.a.chatDrawer)
	}
	g.tap(barChatX(w), barCenterY())
	if g.a.drawerOpen() {
		t.Fatal("the chat button did not close the panel it had opened")
	}
}

// TestCompactPadButtonTogglesThePad: the bar's pad button is the player's
// standing answer on the on-screen controls — it brings the pad back on a
// phone held portrait, at the price in board the default was avoiding, and
// takes it away again.
func TestCompactPadButtonTogglesThePad(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-pad", config.ModeCompetitive))
	if g.a.padVisible() {
		t.Fatal("the pad starts on for a phone held portrait")
	}
	without := g.a.gest.cell
	g.tap(barPadX(w), barCenterY())
	if !g.a.padVisible() {
		t.Fatal("the pad button did not switch the pad on")
	}
	if g.a.gest.cell >= without {
		t.Fatalf("with the pad on the cell is %d px, not under the %d px it had without", g.a.gest.cell, without)
	}
	g.tap(barPadX(w), barCenterY())
	if g.a.padVisible() {
		t.Fatal("the pad button did not switch the pad back off")
	}
	if g.a.gest.cell != without {
		t.Fatalf("cell back to %d px, want the %d px it had before the pad came and went", g.a.gest.cell, without)
	}
}

// TestFoldedPanelsGiveTheBoardTheirRoom: on the full screen the HUD column
// and the chat strip fold away, and what they give up the playfield takes —
// a bigger cell, and a wider surface to swipe on.
func TestFoldedPanelsGiveTheBoardTheirRoom(t *testing.T) {
	eng := liveEngine(t, "fold-room", config.ModeCooperative)
	sz := image.Pt(1100, 820)
	open := newScreenRig(t, sz, deviceDesktop, eng)
	if open.a.form.compact {
		t.Fatalf("a %v window went compact", sz)
	}
	folded := newScreenRig(t, sz, deviceDesktop, eng)
	folded.a.hudFold, folded.a.chatFold = true, true
	folded.frame()
	if folded.a.gest.cell <= open.a.gest.cell {
		t.Fatalf("folded cell %d px, unfolded %d px: folding bought the board nothing",
			folded.a.gest.cell, open.a.gest.cell)
	}
	if fo, op := folded.field(t).Dx(), open.field(t).Dx(); fo <= op {
		t.Fatalf("folded swipe surface %d px, unfolded %d px", fo, op)
	}
}

// TestCompactStripFitsTheScreen: the move-buffer strip is what the playfield
// is centered on, so a row wider than the display would push the board off
// it. The compact chip keeps the row inside the narrowest phone.
func TestCompactStripFitsTheScreen(t *testing.T) {
	a := newTestApp()
	for _, w := range []int{320, 360, 390} {
		gtx := testCtx(w, 780)
		a.form = screenForm{device: devicePhone, w: w, h: 780, portrait: true, compact: true}
		if sw := a.stripWidth(gtx); sw > w {
			t.Errorf("compact strip %d px wide in a %d px display", sw, w)
		}
		// The strip itself is exactly the row's width, caption or no.
		batches := [][]engine.MoveType{{engine.MoveLeft, engine.MoveDown}, {engine.MoveHardDrop}}
		if d := a.bufferedMovesStrip(testCtx(w, 780), batches, 2, 1); d.Size.X != a.stripWidth(gtx) {
			t.Errorf("compact strip laid out %d px wide, want %d", d.Size.X, a.stripWidth(gtx))
		}
	}
	// The full screen keeps the roomier chip.
	wide := testCtx(1280, 820)
	a.form = screenForm{device: deviceDesktop, w: 1280, h: 820, compact: true}
	compact := a.stripWidth(wide)
	a.form.compact = false
	if full := a.stripWidth(wide); full <= compact {
		t.Errorf("full strip %d px, compact %d px: the compact one should be the narrower", full, compact)
	}
}

// TestCompactScreensLayoutWithoutPanic walks the compact screen through the
// states a game passes — pre-start (the ready bar), playing, over — as a
// player and as a spectator, in every mode, with either panel open, in both
// orientations and with the NATS message panel showing.
func TestCompactScreensLayoutWithoutPanic(t *testing.T) {
	players := []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}
	for _, sz := range []image.Point{{X: 390, Y: 844}, {X: 844, Y: 390}, {X: 820, Y: 1180}} {
		for _, st := range []config.GameStatus{config.GameStatusCreated, config.GameStatusInProgress, config.GameStatusFinished} {
			for _, gmode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive, config.ModeTeams} {
				for _, m := range []engine.Mode{engine.ModePlayer, engine.ModeSpectator} {
					for _, panel := range []string{"", "hud", "chat"} {
						a := newTestApp()
						a.touchUI = true
						a.deviceHint, a.deviceHinted = devicePhone, true
						a.eng = engine.New(nil, "g1", "alice", "bob", gmode, m, 0, 0, 0)
						a.gamePlayers, a.readyPlayers = players, players
						a.screen = screenGame
						a.gameStatus = string(st)
						a.gameOver = st == config.GameStatusFinished
						a.hudDrawer, a.chatDrawer = panel == "hud", panel == "chat"
						a.showMsgs.Value = true
						if d := a.layout(testCtx(sz.X, sz.Y)); d.Size.X == 0 || d.Size.Y == 0 {
							t.Fatalf("%v %s %v %v panel=%q: zero-size screen", sz, st, gmode, m, panel)
						}
					}
				}
			}
		}
	}
}

// TestCompactChatUnreadMark: messages that arrive while the chat panel is
// shut are what the bar's dot is for — opening the panel marks them seen.
func TestCompactChatUnreadMark(t *testing.T) {
	a := newTestApp()
	a.touchUI = true
	a.deviceHint, a.deviceHinted = devicePhone, true
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.chatLog = []lobby.ChatMessage{{GameID: "g1", Name: "bob", Text: "gl hf"}}
	a.layout(testCtx(390, 844))
	if a.chatSeen != 0 {
		t.Fatalf("chatSeen = %d with the panel shut, want 0 — the dot would never show", a.chatSeen)
	}
	a.chatDrawer = true
	a.layout(testCtx(390, 844))
	if a.chatSeen != 1 {
		t.Fatalf("chatSeen = %d after showing the panel, want the 1 message it showed", a.chatSeen)
	}
}

// TestGameChatLogFolds: the panel's list is this game's messages plus the
// lobby's, and nothing from another game.
func TestGameChatLogFolds(t *testing.T) {
	a := newTestApp()
	a.chatLog = []lobby.ChatMessage{
		{GameID: "g1", Name: "alice", Text: "mine"},
		{GameID: "", Name: "bob", Text: "lobby"},
		{GameID: "g2", Name: "carol", Text: "another game"},
	}
	got := a.gameChatLog("g1")
	want := []string{"mine", "lobby"}
	texts := make([]string, len(got))
	for i, m := range got {
		texts[i] = m.Text
	}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("game chat = %v, want %v", texts, want)
	}
}

// TestPanelsResetOnANewGame: a panel left open when the player walks out of
// a game must not be over the board of the next one, and the unread mark
// starts over — the chat there is a different conversation.
func TestPanelsResetOnANewGame(t *testing.T) {
	a := newTestApp()
	a.touchUI = true
	a.deviceHint, a.deviceHinted = devicePhone, true
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.chatLog = []lobby.ChatMessage{{GameID: "g1", Name: "bob", Text: "gl hf"}}
	a.layout(testCtx(390, 844)) // the game screen's first frame
	a.chatDrawer = true
	a.layout(testCtx(390, 844))
	if !a.chatDrawer || a.chatSeen != 1 {
		t.Fatalf("first game: chat panel open=%v seen=%d, want open with its one message read", a.chatDrawer, a.chatSeen)
	}
	// A second game: a new engine, so a new screen.
	a.eng = engine.New(nil, "g2", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.layout(testCtx(390, 844))
	if a.drawerOpen() {
		t.Error("the panel from the last game is over the new one's board")
	}
	if a.chatSeen != 0 {
		t.Errorf("chatSeen = %d on the new game, want 0", a.chatSeen)
	}
	// The pad switch is the player's own and survives the move.
	a.padPref = 1
	a.eng = engine.New(nil, "g3", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.layout(testCtx(390, 844))
	if !a.padVisible() {
		t.Error("the player's pad choice did not carry into the next game")
	}
}
