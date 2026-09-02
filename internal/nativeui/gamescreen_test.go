package nativeui

import (
	"context"
	"encoding/json"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
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

// bare puts the game screen's four panels away and lays the screen out
// again. Everything starts ON (panels.go), so this is the state of a player
// who has switched the lot off — and the one the geometry and gesture tests
// below want: a board column with nothing beside it and nothing under it.
func (g *screenRig) bare() *screenRig {
	g.a.hudShown, g.a.oppShown, g.a.padShown, g.a.chatShown = false, false, false, false
	g.frame()
	return g
}

// tap queues a touch press and release at a window position and runs the
// frames that dispatch it.
func (g *screenRig) tap(x, y float32) {
	touch(g.r, pointer.Press, 1, x, y, 0)
	touch(g.r, pointer.Release, 1, x, y, 60*time.Millisecond)
	g.frame()
	g.frame()
}

// press queues a key press and release and runs the frames that dispatch it —
// the keyboard's half of driving the screen, against whichever widget the
// screen has given the keys to.
func (g *screenRig) press(name key.Name) {
	g.r.Queue(key.Event{Name: name, State: key.Press}, key.Event{Name: name, State: key.Release})
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
func barCenterY() float32    { return gameBarH / 2 }
func barMenuX() float32      { return 6 + (gameBarH-14)/2 }
func barChatX(w int) float32 { return float32(w) - barMenuX() }
func barPadX(w int) float32  { return float32(w) - barMenuX() - (gameBarH - 14) - 6 }

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
	// This is about the board's own width, so the panels go away first: on a
	// phone every one of them takes some of it, the pad stacking UNDER the
	// board and the menu covering it.
	g.bare()
	if g.a.padVisible() {
		t.Fatal("the pad button did not put the pad away")
	}
	board, field := g.boardPx(), g.field(t)
	if board.X*2 <= w {
		t.Errorf("playfield %d px wide on a %d px phone: under half the screen", board.X, w)
	}
	if field.Dx() <= board.X {
		t.Errorf("swipe surface %d px, playfield %d px: no room gained", field.Dx(), board.X)
	}
	// It reaches the edges — across the ground the HOLD and NEXT wells stand
	// on, which is dead space below them: the board column's own inset and
	// the pixel the surface leaves itself are all that is missing.
	if slack := w - field.Dx(); slack > 16 {
		t.Errorf("swipe surface %d px on a %d px phone: %d px of it unreachable", field.Dx(), w, slack)
	}
	if g.a.gest.fieldW != field.Dx() {
		t.Errorf("recognizer width %d, surface on screen %v", g.a.gest.fieldW, field)
	}
}

// TestCompactWellsFlankTheBoard: the HOLD box and the NEXT well sit beside
// the playfield on a phone exactly as they do on the desktop — one either
// side, hanging from its top edge — at a reduced cell, so they cost the board
// far less than a full-size pair would. And the HOLD box still holds when
// tapped, though the gesture surface now runs underneath it.
func TestCompactWellsFlankTheBoard(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-wells", config.ModeCompetitive))
	if !g.a.form.compact {
		t.Fatalf("a %dx%d phone did not get the compact screen", w, h)
	}
	g.bare() // the wells against the board alone, with no panel over either
	cell := g.a.gest.cell
	if wc := g.a.wellCell(cell); wc >= cell || wc < narrowWellMinCell {
		t.Fatalf("well cell %d px against a %d px board cell: want it smaller but legible", wc, cell)
	}
	// Both wells are on screen, one either side of the playfield, level with
	// its top edge.
	board := g.boardPx()
	field := g.field(t)
	wellW := g.a.wellWidth(cell)
	// The playfield is centred in the surface, so the columns either side of
	// it are what is left — and each is at least a well wide.
	bx0 := field.Min.X + (field.Dx()-board.X)/2
	if margin := bx0 - field.Min.X; margin < wellW {
		t.Fatalf("only %d px left of the playfield for a %d px well", margin, wellW)
	}
	// A tap on the HOLD box holds — it hangs from the playfield's top edge,
	// centred in that left column, over the gesture surface, and the box wins
	// the press by being drawn after it. The engine really holds, so the
	// check is on the held piece rather than on the move queue, which this
	// live engine drains as it publishes.
	if _, has := g.a.eng.HeldPiece(); has {
		t.Fatal("a piece is held before anything was tapped")
	}
	g.tap(float32(bx0-wellW/2), float32(field.Min.Y+cell))
	held := false
	for range 40 {
		if _, has := g.a.eng.HeldPiece(); has {
			held = true
			break
		}
		time.Sleep(25 * time.Millisecond)
		g.frame()
	}
	if !held {
		t.Fatal("tapping the HOLD box held nothing — the press did not reach it")
	}
}

// TestCompactOpponentsToggle: the bar's boards switch brings the opposing
// boards in beside the playfield and takes them away again, and on a phone
// that column costs the board width — the reason they are a switch at all.
func TestCompactOpponentsToggle(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-opps", config.ModeCompetitive))
	g.bare()
	if g.a.oppVisible() {
		t.Fatal("the boards switch did not put the opponents away")
	}
	if !g.a.hasOpponents(g.a.eng, engine.ModePlayer, config.ModeCompetitive) {
		t.Skip("no opponent snapshot arrived yet")
	}
	without := g.a.gest.cell
	g.a.oppShown = true
	g.frame()
	if g.a.gest.cell >= without {
		t.Fatalf("with the opponents shown the cell is %d px, not under the %d px it had without", g.a.gest.cell, without)
	}
	g.a.oppShown = false
	g.frame()
	if g.a.gest.cell != without {
		t.Fatalf("cell back to %d px, want the %d px it had before", g.a.gest.cell, without)
	}
	// A co-op game has no opposing board, so it never offers the switch.
	if g.a.hasOpponents(g.a.eng, engine.ModePlayer, config.ModeCooperative) {
		t.Error("co-op offers an opponents switch")
	}
	if g.a.hasOpponents(g.a.eng, engine.ModeSpectator, config.ModeCompetitive) {
		t.Error("a spectator, who already sees every board, is offered the switch")
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
	probe := newScreenRig(t, image.Pt(390, 844), devicePhone, newEng()).bare()
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
			g := newScreenRig(t, image.Pt(390, 844), devicePhone, newEng()).bare()
			g.tap(c.x, cy)
			if got := g.a.eng.BufferedMoves(); !reflect.DeepEqual(got, []engine.MoveType{c.want}) {
				t.Fatalf("tap %s: moves %v, want exactly [%v]", c.name, got, c.want)
			}
		})
	}
}

// TestBarSwitchesShowAndHideTheColumns drives the bar's buttons for real: the
// menu button shows the menu column and hides it again — nothing else does —
// and the chat button the same for the chat strip. Neither is a window over
// the board: no tap on the playfield puts one away.
func TestBarSwitchesShowAndHideTheColumns(t *testing.T) {
	const w, h = 390, 844
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-panels", config.ModeCompetitive))
	// Everything starts up, a phone included; the button is what puts it away
	// and the same button is what brings it back.
	if !g.a.hudVisible() {
		t.Fatal("the menu column is not up to begin with")
	}
	g.tap(barMenuX(), barCenterY())
	if g.a.hudVisible() {
		t.Fatal("the menu button did not hide the menu column")
	}
	g.tap(barMenuX(), barCenterY())
	if !g.a.hudVisible() {
		t.Fatal("the menu button did not show the column it hid")
	}
	// A tap on the playfield leaves the menu exactly where the player put it:
	// there is no scrim to tap through and nothing about the board shuts it.
	field := g.field(t)
	g.tap(float32(field.Max.X-6), float32(field.Min.Y+field.Dy()/2))
	if !g.a.hudVisible() {
		t.Fatal("a tap on the board closed the menu column")
	}
	g.tap(barMenuX(), barCenterY())
	if g.a.hudVisible() {
		t.Fatal("the menu button did not hide the column it showed")
	}

	// The chat button hides and shows the strip. Both can be up at once, and
	// neither blocks play.
	if !g.a.chatVisible() {
		t.Fatal("the chat strip is not up to begin with")
	}
	g.tap(barChatX(w), barCenterY())
	if g.a.chatVisible() {
		t.Fatal("the chat button did not hide the strip")
	}
	g.tap(barChatX(w), barCenterY())
	if !g.a.chatVisible() {
		t.Fatal("the chat button did not show the strip again")
	}
	g.tap(barMenuX(), barCenterY())
	if !g.a.chatVisible() || !g.a.hudVisible() {
		t.Fatalf("menu and chat cannot be up together: chat=%v menu=%v", g.a.chatVisible(), g.a.hudVisible())
	}
}

// TestMenuColumnLeavesTheGamePlayable is the whole point of the menu being a
// switch and not a window: with it open the piece still takes moves, from a
// tap on the playfield and from the keys alike — a player flips a lab switch
// mid-game without handing the game over to a panel.
func TestMenuColumnLeavesTheGamePlayable(t *testing.T) {
	const w, h = 1280, 820
	// A transport-less engine: it never drains its queue, so BufferedMoves is
	// exactly what the screen fed it. And no on-screen pad: its buttons are
	// laid out over the gesture surface, and a tap meant for the playfield
	// would be one of theirs.
	eng := engine.New(nil, "menu-playable", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	g := newScreenRig(t, image.Pt(w, h), deviceDesktop, eng)
	g.a.padShown = false
	g.frame()
	if !g.a.hudVisible() {
		t.Fatal("a wide window does not start with the menu column up")
	}
	// A wide window has room for the menu beside the board, so the board area
	// — and the surface a swipe lands on — starts clear of the column.
	field := g.field(t)
	if !g.a.hudBeside(boardAreaCtx(w, h)) {
		t.Fatalf("a %dx%d window has no room for the menu beside the board", w, h)
	}
	if want := g.a.hudColW(boardAreaCtx(w, h)); field.Min.X < want {
		t.Errorf("the swipe surface starts at x=%d, inside the %d px menu column", field.Min.X, want)
	}
	before := len(eng.BufferedMoves())
	g.tap(float32(field.Min.X+field.Dx()/4), float32(field.Min.Y+field.Dy()/2))
	got := eng.BufferedMoves()
	if len(got) == before {
		t.Fatal("a tap on the playfield beside the open menu queued no move")
	}
	if got[len(got)-1] != engine.RotateCCW {
		t.Fatalf("tap left of the well's middle queued %v, want a counter-clockwise rotation", got[len(got)-1])
	}
	// And the keys are still the board's: nothing in the menu claimed them.
	before = len(eng.BufferedMoves())
	g.press(key.NameLeftArrow)
	if got := eng.BufferedMoves(); len(got) == before || got[len(got)-1] != engine.MoveLeft {
		t.Fatalf("the arrow key queued %v with the menu open, want a left move", got[before:])
	}
}

// TestLabSwitchFlipsWithoutTakingTheKeys is the scenario the menu became a
// switch for: mid-game, the player flips MOVE PUBLISHING from Optimistic
// async to Pessimistic sync in the open menu and plays straight on. The radio
// is a Clickable and takes the keys for the frame of its press (Gio keyboard
// navigation); the board area — the whole screen — must hand them back on the
// next, or the arrows would stop driving the piece the moment a switch is
// touched.
func TestLabSwitchFlipsWithoutTakingTheKeys(t *testing.T) {
	eng := engine.New(nil, "lab-switch", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	g := newScreenRig(t, image.Pt(1280, 820), deviceDesktop, eng)
	if !g.a.hudVisible() {
		t.Fatal("a wide window does not start with the menu column up")
	}
	if g.a.labEnum.Value == labSync {
		t.Fatal("the lab switch starts on Pessimistic sync; this test flips onto it")
	}
	r, ok := semanticClassBounds(g.r, semantic.RadioButton)
	if !ok {
		t.Fatal("no radio button in the open menu — the lab switches are not on screen")
	}
	g.tap(float32(r.Min.X+r.Dx()/2), float32(r.Min.Y+r.Dy()/2))
	if g.a.labEnum.Value != labSync {
		t.Fatalf("the lab switch reads %q after a click on its first radio, want %q", g.a.labEnum.Value, labSync)
	}
	if !g.a.hudVisible() {
		t.Fatal("flipping a switch inside the menu closed it")
	}
	before := len(eng.BufferedMoves())
	g.press(key.NameLeftArrow)
	if got := eng.BufferedMoves(); len(got) == before || got[len(got)-1] != engine.MoveLeft {
		t.Fatalf("after flipping a lab switch the arrow key queued %v, want a left move — the menu kept the keys", got[before:])
	}
}

// semanticClassBounds is the window-px rectangle of the first widget of a
// semantic class in the router's tree of the last frame (the labelled lookup
// is semanticBounds; a radio button carries its label as a child).
func semanticClassBounds(r *input.Router, class semantic.ClassOp) (image.Rectangle, bool) {
	var find func([]input.SemanticNode) (image.Rectangle, bool)
	find = func(nodes []input.SemanticNode) (image.Rectangle, bool) {
		for _, n := range nodes {
			if n.Desc.Class == class {
				return n.Desc.Bounds, true
			}
			if b, ok := find(n.Children); ok {
				return b, true
			}
		}
		return image.Rectangle{}, false
	}
	return find(r.AppendSemantics(nil))
}

// boardAreaCtx is the context the board area is laid out in on a window of
// this size: the screen less the bar and the area's own inset, which is what
// the menu column's own arithmetic (hudBeside, hudColW) measures.
func boardAreaCtx(w, h int) C { return testCtx(w-8, h-gameBarH-8) }

// TestMenuColumnNeverPushesTheBoardOffScreen: on a screen with room the menu
// takes its width off the board, and on one without (a phone held portrait,
// where what was left would be narrower than the move-buffer strip the board
// is centred on) it is drawn OVER the board instead — the playfield keeping
// the size, the cell and the place it had. Either way nothing lands outside
// the window.
func TestMenuColumnNeverPushesTheBoardOffScreen(t *testing.T) {
	for _, c := range []struct {
		name   string
		sz     image.Point
		dev    deviceKind
		beside bool
	}{
		{"phone portrait", image.Pt(390, 844), devicePhone, false},
		{"phone landscape", image.Pt(844, 390), devicePhone, true},
		{"tablet portrait", image.Pt(820, 1180), deviceTablet, true},
		{"desktop", image.Pt(1280, 820), deviceDesktop, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			eng := engine.New(nil, "menu-fit", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
			g := newScreenRig(t, c.sz, c.dev, eng)
			// From the board as it is with no menu — which is never how it
			// starts (panels.go), so switch it away first.
			g.a.hudShown = false
			g.frame()
			was, wasCell := g.field(t), g.a.gest.cell
			gtx := boardAreaCtx(c.sz.X, c.sz.Y)
			if got := g.a.hudBeside(gtx); got != c.beside {
				t.Fatalf("hudBeside = %v, want %v (column %d px of %d, strip %d px)",
					got, c.beside, g.a.hudColW(gtx), gtx.Constraints.Max.X, g.a.stripWidth(gtx))
			}
			g.tap(barMenuX(), barCenterY())
			now := g.field(t)
			// Across the window, not down it: a board column too short for a
			// whole playfield has always overflowed downwards (fitCellPx's
			// floor), and that is the board's own business. Off the SIDE of
			// the screen is the menu's doing and is not allowed.
			if now.Min.X < 0 || now.Max.X > c.sz.X {
				t.Errorf("with the menu open the playfield surface spans x %d..%d, outside the %d px window", now.Min.X, now.Max.X, c.sz.X)
			}
			switch {
			case c.beside:
				if now.Min.X <= was.Min.X || now.Dx() >= was.Dx() {
					t.Errorf("the menu stands beside the board and took nothing off it: surface %v, was %v", now, was)
				}
			default:
				if now != was || g.a.gest.cell != wasCell {
					t.Errorf("the menu drawn over the board moved it: surface %v (cell %d), was %v (cell %d)",
						now, g.a.gest.cell, was, wasCell)
				}
			}
		})
	}
}

// TestCompactPadButtonTogglesThePad: the bar's pad button is the player's
// standing answer on the on-screen controls. The pad starts on, as everything
// does; on a small phone held portrait it stacks UNDER the board and takes
// cell size off it, so switching it away is what buys that back — and
// switching it on again spends it. (A tall phone has height to spare for the
// 20-row board, whose cell the width settles instead, and there the pad costs
// it nothing.)
func TestCompactPadButtonTogglesThePad(t *testing.T) {
	const w, h = 390, 700
	g := newScreenRig(t, image.Pt(w, h), devicePhone, liveEngine(t, "compact-pad", config.ModeCompetitive))
	if !g.a.padVisible() {
		t.Fatal("the pad does not start on")
	}
	with := g.a.gest.cell
	g.tap(barPadX(w), barCenterY())
	if g.a.padVisible() {
		t.Fatal("the pad button did not switch the pad off")
	}
	if g.a.gest.cell <= with {
		t.Fatalf("without the pad the cell is %d px, not over the %d px it had with it", g.a.gest.cell, with)
	}
	g.tap(barPadX(w), barCenterY())
	if !g.a.padVisible() {
		t.Fatal("the pad button did not switch the pad back on")
	}
	if g.a.gest.cell != with {
		t.Fatalf("cell back to %d px, want the %d px it had before the pad went and came back", g.a.gest.cell, with)
	}
}

// TestOneScreenEverywhere: a desktop window gets the same screen a phone
// does — the bar on top, every column and strip on one of its switches. What
// differs is only what the screen can afford: a wide one is not "compact", so
// its menu column and its opponents show by default and its wells draw at the
// board's own cell.
func TestOneScreenEverywhere(t *testing.T) {
	eng := liveEngine(t, "one-screen", config.ModeCompetitive)
	wide := newScreenRig(t, image.Pt(1280, 820), deviceDesktop, eng)
	if wide.a.form.compact {
		t.Fatal("a 1280x820 window reports itself as compact")
	}
	// The menu column stands beside the board from the first frame here — a
	// wide window can afford it — and the bar's button takes it away and
	// brings it back, exactly as it does on a phone.
	if !wide.a.hudVisible() {
		t.Fatal("a wide window does not start with the menu column up")
	}
	wide.tap(barMenuX(), barCenterY())
	if wide.a.hudVisible() {
		t.Fatal("the desktop window has no working menu button")
	}
	wide.tap(barMenuX(), barCenterY())
	if !wide.a.hudVisible() {
		t.Fatal("the menu button did not bring back the column it hid")
	}
	// Room to spare: the opponents show without being asked, and the wells
	// draw at the board's own cell rather than the narrow screen's fraction.
	if !wide.a.oppVisible() {
		t.Error("a wide window hides the opponents by default")
	}
	if wide.a.narrowWells() {
		t.Error("a wide window shrinks its wells")
	}
	if c := wide.a.gest.cell; wide.a.wellCell(c) != c {
		t.Errorf("well cell %d px against a %d px board cell on a wide window: want them equal", wide.a.wellCell(c), c)
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
		if d := a.bufferedMovesStrip(testCtx(w, 780), batches, 1); d.Size.X != a.stripWidth(gtx) {
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
// player and as a spectator, in every mode, with the menu column or the chat
// strip up, in both orientations and with the NATS message panel showing.
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
						// The menu shuts itself on a screen's first frame,
						// so this one has to look like a screen already up.
						a.screenEng = a.eng
						a.hudShown = false
						if panel == "hud" {
							a.hudShown = true
						}
						if panel == "chat" {
							a.chatShown = true
						}
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
	a.chatShown = false // the strip put away: the state the dot exists for
	a.layout(testCtx(390, 844))
	if a.chatSeen != 0 {
		t.Fatalf("chatSeen = %d with the strip hidden, want 0 — the dot would never show", a.chatSeen)
	}
	a.chatShown = true
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

// TestPanelsResetOnANewGame: entering a new game starts the unread mark over
// — the chat there is a different conversation — while the bar's switches,
// the menu column among them, are the player's own and carry across.
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
	// The player puts the menu away and leaves the chat strip up.
	a.hudShown = false
	a.layout(testCtx(390, 844))
	if a.hudVisible() || a.chatSeen != 1 {
		t.Fatalf("first game: menu up=%v seen=%d, want it away with the one message read", a.hudVisible(), a.chatSeen)
	}
	// A second game: a new engine, so a new screen.
	a.eng = engine.New(nil, "g2", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.layout(testCtx(390, 844))
	if a.chatSeen != 0 {
		t.Errorf("chatSeen = %d on the new game, want 0", a.chatSeen)
	}
	if a.hudVisible() {
		t.Error("the player's menu choice did not carry into the next game")
	}
	// The bar's switches are the player's own and survive the move.
	if !a.chatVisible() {
		t.Error("the player's chat choice did not carry into the next game")
	}
	a.padShown = false
	a.eng = engine.New(nil, "g3", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.layout(testCtx(390, 844))
	if a.padVisible() {
		t.Error("the player's pad choice did not carry into the next game")
	}
}
