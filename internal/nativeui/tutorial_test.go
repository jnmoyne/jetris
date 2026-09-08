package nativeui

// The How to play tour (tutorial.go): every step finds the part it points
// at on screen, the keys and the buttons move through it, the scrim keeps
// every press off what is under it, and closing it puts the lobby back the
// way it was.

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/lobby"
)

// tutRig is a lobby through a real Gio router, with the tour's read of the
// frame after each one, the way the window loop runs it (App.Run).
type tutRig struct {
	a  *App
	r  *input.Router
	sz image.Point
}

func newTutRig(t *testing.T, sz image.Point) *tutRig {
	t.Helper()
	a := newTestApp()
	a.lobby = lobby.New(nil, nil, "tester", "tester")
	a.screen = screenLobby
	a.connName, a.connURL = "Jetris EU central", "wss://eu-central.jetris.example:443"
	g := &tutRig{a: a, r: new(input.Router), sz: sz}
	g.frame()
	return g
}

func (g *tutRig) frame() {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(g.sz),
		Source:      g.r.Source(),
		Now:         time.Now(),
	}
	g.a.layout(gtx)
	g.a.tutorialObserve(ops)
	g.r.Frame(ops)
}

// frames runs n frames.
func (g *tutRig) frames(n int) {
	for range n {
		g.frame()
	}
}

// press queues a key press and runs the frames that deliver it.
func (g *tutRig) press(name key.Name) {
	g.r.Queue(key.Event{Name: name, State: key.Press})
	g.frames(2)
}

// tap queues a click at a window position and runs the frames that
// dispatch it.
func (g *tutRig) tap(x, y float32) {
	touch(g.r, pointer.Press, 1, x, y, 0)
	touch(g.r, pointer.Release, 1, x, y, 60*time.Millisecond)
	g.frames(2)
}

// settle runs frames until the step's target is in view — its scroll done
// or given up — and at least enough for the frame-old rectangles to be the
// step's own.
func (g *tutRig) settle() {
	g.frames(3)
	for i := 0; i < 80 && !g.a.tut.scrollStuck; i++ {
		st := g.a.tutorialStep()
		if st.scroll == nil {
			return
		}
		_, viewport := st.scroll(g.a)
		vr, ok := g.a.tut.rects[viewport]
		tr, has := g.a.tutorialTarget(st)
		if ok && has && (tr.Dy() >= vr.Dy() || tr.In(vr)) {
			return
		}
		g.frame()
	}
}

// TestTutorialWalksEveryStep walks the whole tour with Next, at the default
// window and at the smallest one: every step's parts are found on screen
// (so every id the steps name is marked by a screen the step shows), a part
// inside a scrolling column ends up in its viewport, the game steps stand
// on the tour's engine and never a real one, and Finish closes the tour.
func TestTutorialWalksEveryStep(t *testing.T) {
	for _, sz := range []image.Point{{X: 1280, Y: 820}, {X: 760, Y: 720}} {
		g := newTutRig(t, sz)
		g.a.startTutorial()
		if !g.a.tutorialUp() {
			t.Fatalf("%v: the tour did not open", sz)
		}
		n := len(g.a.tut.steps)
		for i := 1; i <= n; i++ {
			if g.a.tut.step != i {
				t.Fatalf("%v: on step %d, want %d", sz, g.a.tut.step, i)
			}
			g.settle()
			st := g.a.tutorialStep()
			for _, id := range st.targets {
				r, ok := g.a.tut.rects[id]
				if !ok || r.Empty() {
					t.Errorf("%v step %d (%s): part %q not found on screen", sz, i, st.title, id)
				}
			}
			if st.scroll != nil {
				_, viewport := st.scroll(g.a)
				vr := g.a.tut.rects[viewport]
				tr, has := g.a.tutorialTarget(st)
				if has && tr.Dy() < vr.Dy() && !tr.In(vr) {
					t.Errorf("%v step %d (%s): target %v not scrolled into its viewport %v", sz, i, st.title, tr, vr)
				}
			}
			if st.scene == tutSceneGame {
				if g.a.tutorialScene() != tutSceneGame || g.a.tut.eng == nil {
					t.Errorf("%v step %d (%s): the game scene is not set up", sz, i, st.title)
				}
				if g.a.getEngine() != nil {
					t.Errorf("%v step %d: the tour installed a real engine", sz, i)
				}
			}
			g.a.tut.nextBtn.Click()
			g.frame()
		}
		if g.a.tutorialUp() {
			t.Errorf("%v: Finish on the last step left the tour up", sz)
		}
		if g.a.getScreen() != screenLobby || g.a.createWizStep != 0 || g.a.gameStatus != "" {
			t.Errorf("%v: the tour did not put the lobby back: screen=%v wizard=%d status=%q", sz, g.a.getScreen(), g.a.createWizStep, g.a.gameStatus)
		}
	}
}

// TestTutorialKeysAndRestore: the arrow keys move through the tour and Esc
// closes it, and what the tour changed — the tab, the columns, the game
// screen's switches, the wizard's choices — comes back as it was.
func TestTutorialKeysAndRestore(t *testing.T) {
	g := newTutRig(t, image.Pt(1280, 820))
	a := g.a
	a.lobbyTab = lobbyTabHistory
	a.lobbyChatShown = false
	a.padShown = false
	a.showMsgs.Value = true
	a.boardsEnum.Value = "multiple"
	a.lengthEnum.Value = "lines"
	a.lineGoalEd.SetText("20")
	a.createJoinEnum.Value = "open"
	a.countEd.SetText("4")
	g.frame()

	a.startTutorial()
	g.frames(3)
	if !gtxFocused(g) {
		t.Fatal("the tour did not take the keys")
	}
	g.press(key.NameRightArrow)
	if a.tut.step != 2 {
		t.Fatalf("→ left the tour on step %d, want 2", a.tut.step)
	}
	g.press(key.NameLeftArrow)
	if a.tut.step != 1 {
		t.Fatalf("← left the tour on step %d, want 1", a.tut.step)
	}
	g.press(key.NameLeftArrow)
	if a.tut.step != 1 {
		t.Fatalf("← before the first step moved to %d", a.tut.step)
	}
	// Deep into the game scene, then out with Esc.
	for i := 0; i < 30; i++ {
		g.press(key.NameRightArrow)
	}
	if a.tutorialScene() != tutSceneGame {
		t.Fatalf("thirty presses of → did not reach the game scene (step %d)", a.tut.step)
	}
	if a.lobbyTab != lobbyTabGames || !a.padShown || a.boardsEnum.Value != "single" || a.lengthEnum.Value != "topout" {
		t.Errorf("the tour did not set its scene up: tab=%q pad=%v boards=%q length=%q", a.lobbyTab, a.padShown, a.boardsEnum.Value, a.lengthEnum.Value)
	}
	g.press(key.NameEscape)
	if a.tutorialUp() {
		t.Fatal("Esc did not close the tour")
	}
	g.frame()
	switch {
	case a.lobbyTab != lobbyTabHistory:
		t.Errorf("tab put back as %q", a.lobbyTab)
	case a.lobbyChatShown:
		t.Error("the chat strip came back on")
	case a.padShown:
		t.Error("the pad switch came back on")
	case !a.showMsgs.Value:
		t.Error("Show NATS messages came back off")
	case a.boardsEnum.Value != "multiple" || a.createJoinEnum.Value != "open" || a.countEd.Text() != "4" || a.lengthEnum.Value != "lines" || a.lineGoalEd.Text() != "20":
		t.Errorf("the wizard's choices put back as %q/%q/%q/%q/%q", a.boardsEnum.Value, a.createJoinEnum.Value, a.countEd.Text(), a.lengthEnum.Value, a.lineGoalEd.Text())
	case a.createWizStep != 0:
		t.Errorf("the wizard left open on step %d", a.createWizStep)
	case a.gameStatus != "" || a.gamePlayers != nil || a.msgLog != nil:
		t.Error("the game scene's scalars were not cleared")
	}
}

// gtxFocused reports whether the tour's tag holds the rig router's focus,
// read the way the layout reads it.
func gtxFocused(g *tutRig) bool {
	return g.r.Source().Focused(&g.a.tut.tag)
}

// TestTutorialButtonAndScrim: the lobby's How to play button opens the
// tour, and while it is up a press on the bar's menu button — under the
// scrim — flips nothing; the tour's own Next does.
func TestTutorialButtonAndScrim(t *testing.T) {
	const w, h = 1280, 820
	g := newTutRig(t, image.Pt(w, h))
	r, ok := semanticBounds(g.r, tutLabelPrefix+tutHowToPlayBtn)
	if !ok {
		t.Fatal("the How to play button is not on the lobby screen")
	}
	c := r.Min.Add(r.Size().Div(2))
	g.tap(float32(c.X), float32(c.Y))
	if !g.a.tutorialUp() {
		t.Fatal("How to play did not open the tour")
	}
	g.frames(2)
	y := float32(g.a.lobbyBanner(testCtx(w, h)).Size.Y) + barCenterY()
	g.tap(barMenuX(), y)
	if !g.a.lobbyMenuVisible() {
		t.Error("a press on the bar under the tour's scrim hid the menu column")
	}
	if g.a.tut.step != 1 {
		t.Errorf("a press on the scrim moved the tour to step %d", g.a.tut.step)
	}
	nr, ok := semanticButton(g.r, "Next")
	if !ok {
		t.Fatal("the tour's Next button is not on screen")
	}
	nc := nr.Min.Add(nr.Size().Div(2))
	g.tap(float32(nc.X), float32(nc.Y))
	if g.a.tut.step != 2 {
		t.Errorf("Next left the tour on step %d, want 2", g.a.tut.step)
	}
}

// semanticButton is the window rectangle of the semantic button whose text
// reads label. A material button's label is a child of the button's node
// and reports the box the text was laid out in, not the button, so it is
// the button node's bounds that are wanted.
func semanticButton(r *input.Router, label string) (image.Rectangle, bool) {
	var labelled func([]input.SemanticNode) bool
	labelled = func(nodes []input.SemanticNode) bool {
		for _, n := range nodes {
			if n.Desc.Label == label || labelled(n.Children) {
				return true
			}
		}
		return false
	}
	var find func([]input.SemanticNode) (image.Rectangle, bool)
	find = func(nodes []input.SemanticNode) (image.Rectangle, bool) {
		for _, n := range nodes {
			if n.Desc.Class == semantic.Button && labelled(n.Children) {
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

// TestTutorialSnapshots renders every step of the tour to a PNG, at the
// default window and — the steps that scroll, and the modals — at the
// smallest one, for inspection. Skipped unless FW_SNAPSHOT_DIR is set
// (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestTutorialSnapshots
func TestTutorialSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render the tour's steps")
	}
	for _, sz := range []image.Point{{X: 1280, Y: 820}, {X: 760, Y: 720}} {
		w, err := headless.NewWindow(sz.X, sz.Y)
		if err != nil {
			t.Fatalf("headless window: %v", err)
		}
		g := newTutRig(t, sz)
		g.a.startTutorial()
		n := len(g.a.tut.steps)
		for i := 1; i <= n; i++ {
			g.settle()
			name := fmt.Sprintf("tour_%dx%d_%02d", sz.X, sz.Y, i)
			snapshotPNGSized(t, w, dir, name, sz, func(gtx C) {
				gtx.Source = g.r.Source()
				g.a.layout(gtx)
			})
			g.a.tut.nextBtn.Click()
			g.frame()
		}
		w.Release()
	}
}

// TestTutorialPlace pins where the callout goes: beside the lit parts where
// it fits, never over any of them, always inside the window; along the
// bottom where nothing beside fits; the middle with nothing lit.
func TestTutorialPlace(t *testing.T) {
	win := image.Rect(0, 0, 1280, 820)
	size, gap := image.Pt(420, 220), 14
	check := func(name string, holes ...image.Rectangle) image.Rectangle {
		t.Helper()
		p, clean := tutorialPlace(win, holes, size, gap)
		r := image.Rectangle{Min: p, Max: p.Add(size)}
		if !r.In(win) {
			t.Errorf("%s: callout %v runs off the window", name, r)
		}
		if clean != !overlapsAny(r, holes) {
			t.Errorf("%s: clean=%v but the callout %v overlaps %v", name, clean, r, holes)
		}
		return r
	}
	if r := check("top-left button", image.Rect(6, 60, 46, 100)); r.Min.X < 46 {
		t.Errorf("a top-left target got the callout on its left: %v", r)
	}
	if r := check("bottom-right button", image.Rect(1230, 770, 1274, 814)); r.Max.X > 1230 && r.Max.Y > 770 {
		t.Errorf("a bottom-right target got the callout over it: %v", r)
	}
	// A bar button top right and a strip along the bottom: the callout goes
	// under the button, over neither.
	if r := check("button and strip", image.Rect(1230, 50, 1274, 94), image.Rect(0, 660, 1280, 820)); r.Overlaps(image.Rect(0, 660, 1280, 820)) {
		t.Errorf("the callout covers the strip: %v", r)
	}
	// Two pad clusters flanking a board: the callout goes beside the pair,
	// never between them over the board.
	if r := check("pad clusters", image.Rect(540, 250, 660, 370), image.Rect(970, 250, 1060, 560)); r.Min.X > 540 && r.Max.X < 1060 && r.Max.Y > 250 && r.Min.Y < 560 {
		t.Errorf("the callout sits between the pad's clusters: %v", r)
	}
	check("full-width strip", image.Rect(0, 300, 1280, 400))
	if r := check("none"); r.Min.X < 300 || r.Min.Y < 200 {
		t.Errorf("no target: callout not centred: %v", r)
	}
	if rs := mergeRects([]image.Rectangle{image.Rect(0, 0, 10, 10), image.Rect(5, 5, 20, 20), image.Rect(30, 30, 40, 40)}); len(rs) != 2 || rs[0] != image.Rect(0, 0, 20, 20) {
		t.Errorf("mergeRects: %v", rs)
	}
}
