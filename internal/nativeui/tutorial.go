package nativeui

// The How to play tour.
//
// A walkthrough the lobby's "How to play" button opens: one step at a time,
// each a few lines beside the part of the screen it is about, that part lit
// and everything else dimmed, with Next, Back and Close (and the keys: ← →,
// Esc). It starts in the lobby the player is in — the bar's buttons, the
// menu column's voice controls and legend, the panel's tabs — then walks the
// create-game wizard through a fictional game (a co-op crew of one, on
// Guideline rules, by invitation), the invitation picker over a made-up
// roster in which the player is Player 1, and ends on the game screen of
// that game: the playfield and what flanks it, the bar's buttons, and every
// section of the menu column down to Back to Lobby.
//
// Nothing the tour shows is real, and nothing it does reaches the server.
// The lobby screen is the real one, with the tour's OWN lists in place of
// the server's (tutorialLobby) so every row it points at is there whatever
// the server holds; the wizard is the real wizard, its widgets set to the
// tour's choices; the picker is the real picker's drawing over the tour's
// roster (drawInvitePicker), never its handler; and the game screen is the
// real game screen over a transport-less engine (engine.Offline) holding a
// board that never moves. A scrim over the whole window takes every press
// (the tour's own buttons are drawn over it), the game screen's input
// handlers are held off while the tour is up (tutorialUp), and the switches
// and choices the tour flips are put back the way they were when it closes
// (tutSaved).
//
// Where each part is on screen is read back off the frame: every part the
// tour can point at is drawn through tutMark, which labels the area it
// covers (a semantic label on an empty clip), and after the frame is laid
// out the tour runs Gio's input router over the same ops (tutorialObserve)
// and reads every labelled area's window rectangle out of the semantic tree
// — the same way the tests find the pad's buttons. The rectangles are a
// frame old, which a static walkthrough never notices, and a part inside a
// scrolling column reports its true position even off screen, so the tour
// can scroll the column until the part is in view (tutorialScroll).

import (
	"fmt"
	"image"
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
)

// The parts of the screens the tour can point at: what tutMark labels them
// with. Several parts may share one id — the KEYS and TOUCH sections of the
// legend, the two clusters of the pad — and the tour lights the union.
const (
	tutLobbyMenuBtn    = "lobby.bar.menu"
	tutLobbyMic        = "lobby.bar.mic"
	tutLobbyPlayersBtn = "lobby.bar.players"
	tutLobbyChatBtn    = "lobby.bar.chat"
	tutLobbyMenu       = "lobby.menu"    // the menu column (and the viewport its content scrolls in)
	tutLobbyPlayers    = "lobby.players" // the players column, or the strip it becomes
	tutLobbyChat       = "lobby.chat"    // the chat strip
	tutVoice           = "voice"         // the VOICE section, in the lobby's menu and the game's alike
	tutControls        = "controls"      // the KEYS and TOUCH legend, likewise
	tutLobbyTabGames   = "lobby.tab.games"
	tutLobbyTabHistory = "lobby.tab.history"
	tutLobbyTabLog     = "lobby.tab.log"
	tutHistoryActions  = "history.actions" // View board and Replay on the tour's own history row
	tutCreateBtn       = "lobby.create"
	tutHowToPlayBtn    = "lobby.howto"
	tutWizard          = "wizard"
	tutPicker          = "picker"
	tutPickerSelf      = "picker.self"
	tutPickerOthers    = "picker.others"
	tutGameReady       = "game.ready" // the TAP WHEN READY bar over the board, before the start
	tutGameBoard       = "game.board"
	tutGameHold        = "game.hold"
	tutGameNext        = "game.next"
	tutGameStrip       = "game.strip"
	tutGamePad         = "game.pad"      // the D-pad
	tutGamePadFace     = "game.pad.face" // the face buttons: its own part, so the two clusters light apart
	tutGameChat        = "game.chat"
	tutGameBarMenu     = "game.bar.menu"
	tutGameBarMic      = "game.bar.mic"
	tutGameBarPad      = "game.bar.pad"
	tutGameBarChat     = "game.bar.chat"
	tutHUDColumn       = "hud.column" // the menu column's viewport
	tutHUDStats        = "hud.stats"
	tutHUDMsgs         = "hud.msgs"
	tutNatsPanel       = "game.natspanel"
	tutHUDLab          = "hud.lab"
	tutHUDDAS          = "hud.das"
	tutHUDARR          = "hud.arr"
	tutHUDSDF          = "hud.sdf"
	tutHUDGuard        = "hud.guard"
	tutHUDBack         = "hud.back"
)

// tutLabelPrefix keeps the tour's labels apart from the screens' own
// (playfieldLabel, the pad arms') in the semantic tree.
const tutLabelPrefix = "tut:"

// The tour's fictional game and the history row it points at.
const (
	tutGameID    = "tour-coop"
	tutArchiveID = "tour-top"
	tutSelfName  = "Player 1"
)

// tutScene is what stands under the tour's callout: the lobby, the wizard
// over it, the picker over it, or the game screen.
type tutScene int

const (
	tutSceneLobby tutScene = iota
	tutSceneWizard
	tutScenePicker
	tutSceneGame
)

// tutStep is one stop of the tour.
type tutStep struct {
	scene   tutScene
	title   string
	body    string
	targets []string // the parts lit, as one rectangle; none lights nothing and centres the callout
	// prep puts the scene in the state the step shows — the tab, the columns,
	// the wizard's step and choices — every frame, so nothing else can move
	// it meanwhile.
	prep func(a *App)
	// scroll names the list the target scrolls in and the part that is its
	// viewport, for a target inside a column taller than the screen.
	scroll func(a *App) (*widget.List, string)
}

// tutorial is the tour's state: off while step is 0.
type tutorial struct {
	step  int
	steps []tutStep
	scene tutScene // the scene set up right now (tutorialSetScene)
	at    time.Time

	// rects are the marked parts' window rectangles, read off the frame
	// before (tutorialObserve); router is the reader.
	rects  map[string]image.Rectangle
	router input.Router
	// scrollWant/scrollStuck: the offset the last scroll nudge asked for,
	// and that the list would not take it — the target is as far in view
	// as the column allows, and the nudging stops.
	scrollWant  int
	scrollStuck bool

	tag                        int // the scrim's pointer area and the keys' focus
	nextBtn, backBtn, closeBtn widget.Clickable

	me       string                   // the player's ID: the fixtures seat it as Player 1
	eng      *engine.Engine           // the game scene's engine (engine.Offline): the game a while in
	engReady *engine.Engine           // the same game before its start: an empty board under the READY bar
	preStart bool                     // the step shows the game before its start (tutorialGameState)
	picker   map[string]*inviteChoice // the picker scene's roster
	saved    tutSaved
}

// tutSaved is what the tour changes and puts back when it closes.
type tutSaved struct {
	lobbyTab                           string
	lobbyMenu, lobbyPlayers, lobbyChat bool
	hud, pad, chat, opp                bool
	showMsgs                           bool
	lobbyMenuPos, hudPos               layout.Position
	mode, rules, join, count           string
}

// tutorialUp reports whether the tour is showing.
func (a *App) tutorialUp() bool { return a.tut.step != 0 }

// tutorialScene is the scene the tour has set up, tutSceneLobby while it is
// off.
func (a *App) tutorialScene() tutScene {
	if !a.tutorialUp() {
		return tutSceneLobby
	}
	return a.tut.scene
}

// tutorialStep is the current step (the tour is up).
func (a *App) tutorialStep() tutStep { return a.tut.steps[a.tut.step-1] }

// startTutorial opens the tour on its first step, from the lobby.
func (a *App) startTutorial() {
	lb := a.getLobby()
	if lb == nil || a.tutorialUp() {
		return
	}
	t := &a.tut
	if t.steps == nil {
		t.steps = tutorialSteps()
	}
	t.at = time.Now()
	t.me = lb.PlayerID()
	t.rects = nil
	t.eng, t.engReady, t.preStart = nil, nil, false
	t.picker = tutorialPicker()
	t.saved = tutSaved{
		lobbyTab:  a.lobbyTab,
		lobbyMenu: a.lobbyMenuShown, lobbyPlayers: a.lobbyPlayersShown, lobbyChat: a.lobbyChatShown,
		hud: a.hudShown, pad: a.padShown, chat: a.chatShown, opp: a.oppShown,
		showMsgs:     a.showMsgs.Value,
		lobbyMenuPos: a.lobbyMenuList.Position, hudPos: a.hudList.Position,
		mode: a.modeEnum.Value, rules: a.rulesEnum.Value, join: a.createJoinEnum.Value, count: a.countEd.Text(),
	}
	t.scene = tutSceneLobby
	t.step = 1
	a.tutorialSetScene(t.steps[0].scene)
	a.invalidate()
}

// endTutorial closes the tour and puts back what it changed.
func (a *App) endTutorial() {
	t := &a.tut
	if !a.tutorialUp() {
		return
	}
	a.tutorialSetScene(tutSceneLobby)
	s := t.saved
	a.lobbyTab = s.lobbyTab
	a.lobbyMenuShown, a.lobbyPlayersShown, a.lobbyChatShown = s.lobbyMenu, s.lobbyPlayers, s.lobbyChat
	a.hudShown, a.padShown, a.chatShown, a.oppShown = s.hud, s.pad, s.chat, s.opp
	a.showMsgs.Value = s.showMsgs
	a.lobbyMenuList.Position, a.hudList.Position = s.lobbyMenuPos, s.hudPos
	a.modeEnum.Value, a.rulesEnum.Value, a.createJoinEnum.Value = s.mode, s.rules, s.join
	if a.countEd.Text() != s.count {
		a.countEd.SetText(s.count)
	}
	t.step = 0
	t.eng, t.engReady, t.preStart = nil, nil, false
	t.picker = nil
	t.rects = nil
	a.invalidate()
}

// tutorialGo moves the tour to step n: past the last step it closes, before
// the first it stays. A change of scene tears the old one down and sets the
// new one up.
func (a *App) tutorialGo(n int) {
	t := &a.tut
	if n > len(t.steps) {
		a.endTutorial()
		return
	}
	n = max(n, 1)
	if n == t.step {
		return
	}
	t.step = n
	t.scrollWant, t.scrollStuck = 0, false
	a.tutorialSetScene(t.steps[n-1].scene)
}

// tutorialSetScene leaves the scene set up now and sets up s.
func (a *App) tutorialSetScene(s tutScene) {
	t := &a.tut
	if s == t.scene {
		return
	}
	switch t.scene {
	case tutSceneWizard:
		a.createWizStep = 0
	case tutScenePicker:
		a.inviteSelfSel.Value = false
	case tutSceneGame:
		a.tutorialLeaveGame()
	}
	t.scene = s
	switch s {
	case tutSceneWizard:
		a.createWizStep = wizStepMode
	case tutScenePicker:
		a.inviteSelfSel.Value = true
	case tutSceneGame:
		a.tutorialEnterGame()
	}
}

// tutorialEnterGame sets the game screen's scalars up for the tour's game —
// what startGameScreen sets for a real one, with a.eng left alone: the
// tour's engine is its own (tutorialScene picks the screen), so nothing that
// acts on a live game can mistake it for one.
func (a *App) tutorialEnterGame() {
	t := &a.tut
	if t.eng == nil {
		t.eng, t.engReady = tutorialEngine(t.me, true), tutorialEngine(t.me, false)
	}
	a.mu.Lock()
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: t.me, Name: tutSelfName, Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.score, a.level = 3400, 2
	a.teamScores, a.teamLevels = nil, nil
	a.rtt = 23 * time.Millisecond
	a.gameStatus = string(config.GameStatusInProgress)
	a.countdown = -1
	a.gameOver, a.won = false, false
	a.fireworks = nil
	a.decidedAt, a.liveRank, a.liveOf, a.liveRankFinal = time.Time{}, 0, 0, false
	a.confirmLeave = false
	a.myReady = true
	a.resetBoardFX()
	a.msgLog = tutorialStreamMsgs()
	a.resetMsgGroups()
	a.mu.Unlock()
}

// tutorialGameState puts the game scene before its start (preStart: the
// game created, nobody ready, the empty board under the READY bar) or a
// while in (in progress, the stack on the board) — the step's prep, every
// frame.
func (a *App) tutorialGameState(preStart bool) {
	t := &a.tut
	t.preStart = preStart
	status, ready := string(config.GameStatusInProgress), true
	if preStart {
		status, ready = string(config.GameStatusCreated), false
	}
	a.mu.Lock()
	if a.gameStatus != status {
		a.gameStatus = status
		a.myReady = ready
		a.readyPlayers = []lobby.PlayerSummary{{PlayerID: t.me, Name: tutSelfName, Ready: ready}}
		// Nothing scored and no round trip measured before the start.
		a.score, a.level, a.rtt = 3400, 2, 23*time.Millisecond
		if preStart {
			a.score, a.level, a.rtt = 0, 0, 0
		}
	}
	a.mu.Unlock()
}

// gameEngine is the engine the game scene stands on: the game before its
// start or a while in, as the step has it.
func (t *tutorial) gameEngine() *engine.Engine {
	if t.preStart {
		return t.engReady
	}
	return t.eng
}

// tutorialLeaveGame clears them again, as returnToLobby does.
func (a *App) tutorialLeaveGame() {
	a.mu.Lock()
	a.gamePlayers, a.readyPlayers = nil, nil
	a.score, a.level = 0, 0
	a.teamScores, a.teamLevels = nil, nil
	a.rtt = 0
	a.gameStatus = ""
	a.countdown = -1
	a.gameOver, a.won = false, false
	a.fireworks = nil
	a.decidedAt, a.liveRank, a.liveOf, a.liveRankFinal = time.Time{}, 0, 0, false
	a.confirmLeave = false
	a.myReady = false
	a.resetBoardFX()
	a.msgLog = nil
	a.msgShow = false
	a.resetMsgGroups()
	a.mu.Unlock()
}

// --- the frame ---

// tutorialUpdate runs at the top of the frame, before any screen is laid
// out: the tour's buttons and keys, its hold on the keyboard, the step's
// scene, and the scroll that brings the step's target into view — so the
// frame draws the step as it is after the click, not before.
func (a *App) tutorialUpdate(gtx C) {
	t := &a.tut
	if !a.tutorialUp() {
		return
	}
	if a.getScreen() != screenLobby || a.getLobby() == nil {
		a.endTutorial() // the lobby went away under the tour
		return
	}
	next, back, closeIt := false, false, false
	for t.nextBtn.Clicked(gtx) {
		next = true
	}
	for t.backBtn.Clicked(gtx) {
		back = true
	}
	for t.closeBtn.Clicked(gtx) {
		closeIt = true
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: &t.tag},
			key.Filter{Focus: &t.tag, Name: key.NameEscape},
			key.Filter{Focus: &t.tag, Name: key.NameRightArrow},
			key.Filter{Focus: &t.tag, Name: key.NameReturn},
			key.Filter{Focus: &t.tag, Name: key.NameEnter},
			key.Filter{Focus: &t.tag, Name: key.NameSpace},
			key.Filter{Focus: &t.tag, Name: key.NameLeftArrow},
			key.Filter{Focus: &t.tag, Name: key.NameDeleteBackward},
			pointer.Filter{Target: &t.tag, Kinds: pointer.Press | pointer.Release | pointer.Scroll},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch ke.Name {
		case key.NameEscape:
			closeIt = true
		case key.NameRightArrow, key.NameReturn, key.NameEnter, key.NameSpace:
			next = true
		case key.NameLeftArrow, key.NameDeleteBackward:
			back = true
		}
	}
	switch {
	case closeIt:
		a.endTutorial()
		return
	case next:
		a.tutorialGo(t.step + 1)
		if !a.tutorialUp() {
			return
		}
	case back:
		a.tutorialGo(t.step - 1)
	}
	// The keys are the tour's while it is up: the chat's editor, the board,
	// nothing else gets them. Asked for every frame until it holds — the
	// focus filter above is what makes the tag focusable, and a filter
	// counts from the frame after it is first registered.
	if !gtx.Focused(&t.tag) {
		gtx.Execute(key.FocusCmd{Tag: &t.tag})
	}
	st := a.tutorialStep()
	if st.prep != nil {
		st.prep(a)
	}
	a.tutorialScroll(gtx, st)
}

// tutorialScroll nudges the step's list until the target is inside its
// viewport, a part of the way each frame, and gives up where the list will
// not go further.
func (a *App) tutorialScroll(gtx C, st tutStep) {
	t := &a.tut
	if st.scroll == nil || t.scrollStuck {
		return
	}
	lst, viewport := st.scroll(a)
	if t.scrollWant != 0 && lst.Position.Offset != t.scrollWant {
		// The list clamped the last nudge: the target is as far in as the
		// column can bring it.
		t.scrollStuck = true
		return
	}
	vr, ok := t.rects[viewport]
	tr, has := a.tutorialTarget(st)
	if !ok || !has {
		return
	}
	margin := gtx.Dp(12)
	delta := 0
	switch {
	case tr.Dy() > vr.Dy()-2*margin:
		delta = tr.Min.Y - (vr.Min.Y + margin) // taller than the viewport: its top
	case tr.Max.Y > vr.Max.Y-margin:
		delta = tr.Max.Y - (vr.Max.Y - margin)
	case tr.Min.Y < vr.Min.Y+margin:
		delta = tr.Min.Y - (vr.Min.Y + margin)
	}
	if delta == 0 {
		t.scrollWant = 0
		return
	}
	step := max(gtx.Dp(28), abs(delta)/3)
	delta = min(max(delta, -step), step)
	lst.Position.BeforeEnd = true
	lst.Position.Offset += delta
	t.scrollWant = lst.Position.Offset
	animate(gtx)
}

// tutorialTargets is the step's parts on screen, as the frame before drew
// them: one rectangle per part found, parts that overlap merged into one.
func (a *App) tutorialTargets(st tutStep) []image.Rectangle {
	var rs []image.Rectangle
	for _, id := range st.targets {
		if r, ok := a.tut.rects[id]; ok && !r.Empty() {
			rs = append(rs, r)
		}
	}
	return mergeRects(rs)
}

// tutorialTarget is the step's target as one rectangle — the union of its
// parts — and whether any was found: what the scroll and the callout's
// placing go by.
func (a *App) tutorialTarget(st tutStep) (image.Rectangle, bool) {
	return unionRects(a.tutorialTargets(st))
}

// mergeRects folds every pair of overlapping rectangles into their union,
// until none overlap.
func mergeRects(rs []image.Rectangle) []image.Rectangle {
	for merged := true; merged; {
		merged = false
	outer:
		for i := range rs {
			for j := i + 1; j < len(rs); j++ {
				if rs[i].Overlaps(rs[j]) {
					rs[i] = rs[i].Union(rs[j])
					rs = append(rs[:j], rs[j+1:]...)
					merged = true
					break outer
				}
			}
		}
	}
	return rs
}

// unionRects is the rectangle covering rs, and whether there was one.
func unionRects(rs []image.Rectangle) (image.Rectangle, bool) {
	if len(rs) == 0 {
		return image.Rectangle{}, false
	}
	u := rs[0]
	for _, r := range rs[1:] {
		u = u.Union(r)
	}
	return u, true
}

// overlapsAny reports whether r covers any of rs.
func overlapsAny(r image.Rectangle, rs []image.Rectangle) bool {
	for _, o := range rs {
		if r.Overlaps(o) {
			return true
		}
	}
	return false
}

// tutorialObserve reads the marked parts' rectangles off the frame just
// laid out: the ops are run through the tour's own input router and the
// semantic tree walked for the tour's labels (tutMark). Called from the
// window loop after the layout, while the tour is up.
func (a *App) tutorialObserve(ops *op.Ops) {
	t := &a.tut
	if !a.tutorialUp() {
		return
	}
	t.router.Frame(ops)
	rects := map[string]image.Rectangle{}
	var walk func([]input.SemanticNode)
	walk = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			if id, ok := strings.CutPrefix(n.Desc.Label, tutLabelPrefix); ok && !n.Desc.Bounds.Empty() {
				if r, seen := rects[id]; seen {
					rects[id] = r.Union(n.Desc.Bounds)
				} else {
					rects[id] = n.Desc.Bounds
				}
			}
			walk(n.Children)
		}
	}
	walk(t.router.AppendSemantics(nil))
	t.rects = rects
}

// tutMark lays w out and labels the area it covers with id, for the tour to
// find on screen (tutorialObserve). The label rides on an empty clip the
// size of w, pushed and popped before w is drawn, so nothing about w — its
// shadow past its edge, its own input areas — is clipped or covered.
func (a *App) tutMark(gtx C, id string, w layout.Widget) D {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	cl := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	semantic.LabelOp(tutLabelPrefix + id).Add(gtx.Ops)
	cl.Pop()
	call.Add(gtx.Ops)
	return dims
}

// tutMarked is tutMark as a widget, for a Flex child.
func (a *App) tutMarked(id string, w layout.Widget) layout.Widget {
	return func(gtx C) D { return a.tutMark(gtx, id, w) }
}

// tutorialHistoryMark is the id the history row for rec carries on its
// actions: the tour's own row, and no other.
func tutorialHistoryMark(rec config.ArchiveRecord) string {
	if rec.GameID == tutArchiveID {
		return tutHistoryActions
	}
	return ""
}

// --- the overlay ---

// tutorialOverlay is drawn last, over the screen: the picker scene's modal
// where the step is in it, then the tour's own layer — the scrim with the
// target cut out of it, the ring around the target, and the callout beside
// it. The scrim is a pointer area, so every press under the callout is the
// tour's and nothing under the scrim gets one; the callout's buttons are
// drawn after it and take their own.
func (a *App) tutorialOverlay(gtx C) {
	t := &a.tut
	if !a.tutorialUp() {
		return
	}
	st := a.tutorialStep()
	gtx.Constraints.Min = gtx.Constraints.Max
	if st.scene == tutScenePicker {
		fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, withAlpha(colBg, 0.75))
		a.drawInvitePicker(gtx, a.tutorialPickerView())
	}
	pad := gtx.Dp(5)
	full := image.Rectangle{Max: gtx.Constraints.Max}
	var holes []image.Rectangle
	for _, r := range a.tutorialTargets(st) {
		if h := r.Inset(-pad).Intersect(full); !h.Empty() {
			holes = append(holes, h)
		}
	}
	holes = mergeRects(holes)
	pointerArea(gtx, &t.tag, func(gtx C) D {
		tutorialScrim(gtx.Ops, full, holes, withAlpha(colBg, 0.8))
		return D{Size: full.Max}
	})
	if len(holes) > 0 {
		// The ring around each lit part: pulsing between the gold and the
		// accent, a softer band outside it.
		phase := float64(gtx.Now.UnixNano()%int64(1200*time.Millisecond)) / float64(1200*time.Millisecond)
		mix := 0.5 + 0.5*math.Sin(2*math.Pi*phase)
		ring := lerpColor(colGold, colAccent, mix)
		for _, h := range holes {
			strokeRect(gtx.Ops, h, gtx.Dp(3), ring)
			strokeRect(gtx.Ops, h.Inset(-gtx.Dp(3)), gtx.Dp(2), withAlpha(ring, 0.4))
		}
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(40 * time.Millisecond)})
	} else if len(st.targets) > 0 {
		animate(gtx) // the target is not on the frame before: look again once it is
	}
	a.tutorialCallout(gtx, st, holes)
}

// tutorialScrim dims full but for the holes: one path, the window's outline
// wound one way and every hole's the other, filled by the non-zero winding
// rule — so the holes, which never overlap (mergeRects), are left out.
func tutorialScrim(ops *op.Ops, full image.Rectangle, holes []image.Rectangle, col colorN) {
	var p clip.Path
	p.Begin(ops)
	rect := func(r image.Rectangle, clockwise bool) {
		a := f32.Pt(float32(r.Min.X), float32(r.Min.Y))
		b := f32.Pt(float32(r.Max.X), float32(r.Min.Y))
		c := f32.Pt(float32(r.Max.X), float32(r.Max.Y))
		d := f32.Pt(float32(r.Min.X), float32(r.Max.Y))
		if !clockwise {
			b, d = d, b
		}
		p.MoveTo(a)
		p.LineTo(b)
		p.LineTo(c)
		p.LineTo(d)
		p.Close()
	}
	rect(full, true)
	for _, h := range holes {
		rect(h, false)
	}
	defer clip.Outline{Path: p.End()}.Op().Push(ops).Pop()
	paint.Fill(ops, col)
}

// tutorialCallout is the step's box: the tour's header and the step count,
// the title, the text, and the buttons — laid out at the width the room
// beside the lit parts allows and placed there (tutorialPlace). Where no
// box of that width fits beside them, a wider and so shorter one is tried
// before the box goes over them.
func (a *App) tutorialCallout(gtx C, st tutStep, holes []image.Rectangle) {
	t := &a.tut
	win := image.Rectangle{Max: gtx.Constraints.Max}
	gap := gtx.Dp(14)
	want, floor := modalW(gtx, 420), modalW(gtx, 300)
	w := want
	if target, has := unionRects(holes); has {
		// Narrower where a narrower box would fit beside the target and the
		// full one would not.
		if band := max(win.Max.X-target.Max.X, target.Min.X-win.Min.X) - 2*gap; band < want && band >= floor {
			w = band
		}
	}
	try := func(w int) (image.Point, op.CallOp, bool) {
		macro := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
		dims := a.tutorialBox(cgtx, st, t.step, len(t.steps))
		pos, clean := tutorialPlace(win, holes, dims.Size, gap)
		return pos, macro.Stop(), clean
	}
	pos, call, clean := try(w)
	if wide := win.Dx() - 2*gap; !clean && wide > w {
		if p, c, ok := try(wide); ok {
			pos, call = p, c
		}
	}
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
}

// tutorialPlace is where a callout of size goes: beside the lit parts — to
// their right, their left, under them, over them, the first that fits the
// window without covering any — and, where none does, along the bottom of
// the window, or the top where the parts are what is at the bottom; with
// nothing lit, the middle. clean reports a placing that covers none of
// them.
func tutorialPlace(win image.Rectangle, holes []image.Rectangle, size image.Point, gap int) (pos image.Point, clean bool) {
	inner := win.Inset(gap)
	clampX := func(x int) int { return min(max(x, inner.Min.X), max(inner.Min.X, inner.Max.X-size.X)) }
	clampY := func(y int) int { return min(max(y, inner.Min.Y), max(inner.Min.Y, inner.Max.Y-size.Y)) }
	target, has := unionRects(holes)
	if !has {
		return image.Pt(clampX((win.Dx()-size.X)/2), clampY((win.Dy()-size.Y)/2)), true
	}
	fits := func(p image.Point) bool {
		r := image.Rectangle{Min: p, Max: p.Add(size)}
		return r.In(inner) && !overlapsAny(r, holes)
	}
	beside := func(r image.Rectangle) []image.Point {
		return []image.Point{
			{r.Max.X + gap, clampY(r.Min.Y)},
			{r.Min.X - gap - size.X, clampY(r.Min.Y)},
			{clampX(r.Min.X), r.Max.Y + gap},
			{clampX(r.Min.X), r.Min.Y - gap - size.Y},
		}
	}
	// Beside all of them together first — never between two of them — then
	// beside any one of them.
	cands := beside(target)
	if len(holes) > 1 {
		for _, h := range holes {
			cands = append(cands, beside(h)...)
		}
	}
	for _, p := range cands {
		if fits(p) {
			return p, true
		}
	}
	p := image.Pt(clampX((win.Dx()-size.X)/2), inner.Max.Y-size.Y)
	if r := (image.Rectangle{Min: p, Max: p.Add(size)}); overlapsAny(r, holes) && target.Min.Y > win.Dy()/2 {
		p.Y = inner.Min.Y
	}
	p = image.Pt(clampX(p.X), clampY(p.Y))
	return p, !overlapsAny(image.Rectangle{Min: p, Max: p.Add(size)}, holes)
}

// tutorialBox is the callout itself, at its own size within the width it is
// given.
func (a *App) tutorialBox(gtx C, st tutStep, step, total int) D {
	t := &a.tut
	gtx.Constraints.Min = image.Point{}
	nextLabel := "Next"
	if step == total {
		nextLabel = "Finish"
	}
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colGold, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, colBg, func(gtx C) D {
				return layout.UniformInset(unit.Dp(16)).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
								layout.Flexed(1, a.pixel(unit.Sp(8), "HOW TO PLAY", colMuted).Layout),
								layout.Rigid(a.pixel(unit.Sp(8), fmt.Sprintf("%d / %d", step, total), colMuted).Layout),
							)
						}),
						layout.Rigid(spacer(8)),
						layout.Rigid(a.pixel(unit.Sp(11), st.title, colGold).Layout),
						layout.Rigid(spacer(8)),
						layout.Rigid(a.body(st.body, colFg)),
						layout.Rigid(spacer(14)),
						layout.Rigid(func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &t.closeBtn, "Close") }),
								layout.Flexed(1, func(gtx C) D {
									return layout.Center.Layout(gtx, func(gtx C) D {
										gtx.Constraints.Min = image.Point{}
										return a.pixelLabelFit(gtx, unit.Sp(7), "← →  ESC", colMuted)
									})
								}),
								layout.Rigid(func(gtx C) D {
									if step == 1 {
										return D{}
									}
									return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
										return a.secondaryButton(gtx, &t.backBtn, "Back")
									})
								}),
								layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &t.nextBtn, nextLabel) }),
							)
						}),
					)
				})
			})
		})
	})
}

// --- the fixtures ---

// tutorialLobby is the lobby the tour shows in place of the server's: three
// games on offer, a lobby full of players, a history with a top game in it,
// and a server log — every row the tour points at, whatever the server has.
// The presence lists the player as Player 1, under their own ID.
func (a *App) tutorialLobby() (games map[string]lobby.GameListing, players map[string]lobby.PlayerPresence, archives []config.ArchiveRecord, log []config.LogEntry) {
	t := &a.tut
	at := t.at
	me := t.me
	games = map[string]lobby.GameListing{
		"tour-open": {
			GameID: "tour-open", Mode: config.ModeCooperative, Status: config.GameStatusCreated, PlayerCount: 3,
			ExtraColumns: config.DefaultExtraColumns, NextCount: 6, Hold: true, CreatorID: "Player 2",
			Players:   []lobby.PlayerSummary{{PlayerID: "Player 2", Name: "Player 2"}, {PlayerID: "Player 3", Name: "Player 3", Ready: true}},
			CreatedAt: at.Add(-2 * time.Minute),
		},
		"tour-vs": {
			GameID: "tour-vs", Mode: config.ModeCompetitive, Status: config.GameStatusInProgress, PlayerCount: 2,
			NextCount: 6, Hold: true, GarbageHoles: 1, GuidelineGarbage: true, MaxAgents: 1, CreatorID: "Player 4",
			Players:   []lobby.PlayerSummary{{PlayerID: "Player 4", Name: "Player 4", Ready: true}, {PlayerID: "bot-mk1", Name: "bot-mk1", Ready: true, Agent: true}},
			CreatedAt: at.Add(-9 * time.Minute),
		},
		"tour-teams": {
			GameID: "tour-teams", Mode: config.ModeTeams, Status: config.GameStatusCreated, PlayerCount: 4, TeamCount: 2, TeamSize: 2,
			ExtraColumns: config.DefaultExtraColumns, NextCount: 6, Hold: true, GarbageHoles: 1, GuidelineGarbage: true,
			InviteOnly: true, CreatorID: "Player 5",
			Players:   []lobby.PlayerSummary{{PlayerID: "Player 5", Name: "Player 5", Team: 0}},
			CreatedAt: at.Add(-1 * time.Minute),
		},
	}
	players = map[string]lobby.PlayerPresence{
		me:         {PlayerID: me, Name: tutSelfName, Status: lobby.StatusInLobby},
		"Player 2": {PlayerID: "Player 2", Name: "Player 2", Status: lobby.StatusInLobby},
		"Player 3": {PlayerID: "Player 3", Name: "Player 3", Status: lobby.StatusInLobby},
		"Player 4": {PlayerID: "Player 4", Name: "Player 4", Status: lobby.StatusInGame, GameID: "tour-vs"},
		"Player 5": {PlayerID: "Player 5", Name: "Player 5", Status: lobby.StatusInLobby},
		"bot-mk1":  {PlayerID: "bot-mk1", Name: "bot-mk1", Status: lobby.StatusInGame, GameID: "tour-vs", Agent: true},
	}
	archives = []config.ArchiveRecord{
		{
			GameID: tutArchiveID, Mode: config.ModeCooperative, PlayerCount: 1,
			Players:   []config.PlayerResult{{PlayerID: tutSelfName, Score: 12400, Level: 5, Lines: 48}},
			StartedAt: at.Add(-40 * time.Minute), FinishedAt: at.Add(-34 * time.Minute),
			TotalScore: 12400, FinalLevel: 5, WinningTeam: -1,
		},
		{
			GameID: "tour-vs-done", Mode: config.ModeCompetitive, PlayerCount: 2,
			Players: []config.PlayerResult{
				{PlayerID: "Player 2", Score: 6100, Level: 3, Winner: true},
				{PlayerID: "Player 3", Score: 4300, Level: 2},
			},
			StartedAt: at.Add(-70 * time.Minute), FinishedAt: at.Add(-62 * time.Minute), WinningTeam: -1,
		},
		{
			GameID: "tour-teams-done", Mode: config.ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2,
			Players: []config.PlayerResult{
				{PlayerID: "Player 2", Score: 4100, Level: 3, Team: 0, Winner: true},
				{PlayerID: "Player 3", Score: 4100, Level: 3, Team: 0, Winner: true},
				{PlayerID: "Player 4", Score: 3200, Level: 2, Team: 1},
				{PlayerID: "bot-mk1", Score: 2700, Level: 2, Team: 1, Agent: true},
			},
			StartedAt: at.Add(-3 * time.Hour), FinishedAt: at.Add(-3*time.Hour + 11*time.Minute),
			WinningTeam: 0, TeamScores: []int{8200, 5900}, TeamLevels: []int{4, 3},
		},
	}
	log = []config.LogEntry{
		{Kind: config.LogKindConnected, PlayerID: "Player 2", Name: "Player 2", Time: at.Add(-75 * time.Minute)},
		{Kind: config.LogKindConnected, PlayerID: "Player 3", Name: "Player 3", Time: at.Add(-74 * time.Minute)},
		{Kind: config.LogKindGameStarted, PlayerID: "Player 2", Name: "Player 2", GameID: "tour-vs-done", Mode: config.ModeCompetitive, PlayerCount: 2, Time: at.Add(-70 * time.Minute)},
		{Kind: config.LogKindConnected, PlayerID: "bot-mk1", Name: "bot-mk1", Agent: true, Time: at.Add(-30 * time.Minute)},
		{Kind: config.LogKindConnected, PlayerID: "Player 4", Name: "Player 4", Time: at.Add(-12 * time.Minute)},
		{Kind: config.LogKindGameCreated, PlayerID: "Player 4", Name: "Player 4", GameID: "tour-vs", Mode: config.ModeCompetitive, PlayerCount: 2, Time: at.Add(-10 * time.Minute)},
		{Kind: config.LogKindGameStarted, PlayerID: "Player 4", Name: "Player 4", GameID: "tour-vs", Mode: config.ModeCompetitive, PlayerCount: 2, Time: at.Add(-9 * time.Minute)},
		{Kind: config.LogKindGameCreated, PlayerID: "Player 2", Name: "Player 2", GameID: "tour-open", Mode: config.ModeCooperative, PlayerCount: 3, Time: at.Add(-2 * time.Minute)},
		{Kind: config.LogKindConnected, PlayerID: me, Name: tutSelfName, Time: at.Add(-time.Minute)},
	}
	return games, players, archives, log
}

// tutorialHasReplay / tutorialTopRanked stand in for the lobby's HasReplay
// and IsTopRanked over the tour's history: its top game has both.
func tutorialHasReplay(id string) bool { return id == tutArchiveID }
func tutorialTopRanked(id string) bool { return id == tutArchiveID }

// tutorialPicker is the picker scene's roster: everyone else in the tour's
// lobby, none of them invited — the game has one seat, and the player is
// in it.
func tutorialPicker() map[string]*inviteChoice {
	picker := map[string]*inviteChoice{}
	for _, p := range []struct {
		id    string
		agent bool
	}{{"Player 2", false}, {"Player 3", false}, {"Player 5", false}, {"bot-mk1", true}} {
		picker[p.id] = &inviteChoice{playerID: p.id, name: p.id, agent: p.agent}
	}
	return picker
}

// tutorialPickerView is the picker as the tour draws it: the tour's game —
// one seat, the player in it — over the tour's roster.
func (a *App) tutorialPickerView() pickerView {
	t := &a.tut
	g := lobby.GameListing{
		GameID: tutGameID, Mode: config.ModeCooperative, Status: config.GameStatusCreated, PlayerCount: 1,
		NextCount: 6, Hold: true, InviteOnly: true, CreatorID: t.me,
		Players:   []lobby.PlayerSummary{{PlayerID: t.me, Name: tutSelfName}},
		CreatedAt: t.at,
	}
	return pickerView{
		gameID: tutGameID, picker: t.picker, mode: config.ModeCooperative,
		pc: 1, g: g, selfName: tutSelfName,
	}
}

// tutorialEngine is the game scene's engine: the tour's co-op game of one,
// on Guideline rules — before its start, an empty board and an empty hold
// slot; started, a while in: a stack with a well down one side, a T on its
// way down, an I set aside in the hold slot.
func tutorialEngine(me string, started bool) *engine.Engine {
	pf := game.NewPlayfieldWithHeight(config.StandardWidth, config.TotalRows)
	if !started {
		return engine.Offline(engine.OfflineGame{
			GameID: tutGameID, PlayerID: me, Mode: config.ModeCooperative,
			Board: pf, Rules: config.GuidelineRules(), Seed: 7,
		})
	}
	// The stack, bottom up, in piece letters; '.' is empty. Locked, all of
	// them the player's own (player 0).
	stack := []string{
		"..OO......",
		"..OOTTT...",
		"JJJ.TLSS..",
		"JSSLLSSII.",
		"ZZSLJJJII.",
		".ZZLJZZTL.",
		"IIIIZZTTL.",
		"TTTOOSSTL.",
		".TOOOOSSLL",
	}
	types := map[byte]game.PieceType{'I': game.PieceI, 'O': game.PieceO, 'T': game.PieceT, 'S': game.PieceS, 'Z': game.PieceZ, 'J': game.PieceJ, 'L': game.PieceL}
	top := config.TotalRows - len(stack)
	for i, line := range stack {
		for c := 0; c < len(line) && c < pf.Width; c++ {
			if pt, ok := types[line[c]]; ok {
				pf.Rows[top+i].Cells[c] = game.Cell{Occupied: true, PieceType: pt, PlayerIdx: 0}
			}
		}
	}
	pf.SetActivePieceForPlayer(game.Piece{Type: game.PieceT, Orientation: 0, Row: config.VisibleRowStart + 2, Col: 3}, 0)
	held := game.PieceI
	return engine.Offline(engine.OfflineGame{
		GameID: tutGameID, PlayerID: me, Mode: config.ModeCooperative,
		Board: pf, Rules: config.GuidelineRules(), Seed: 7, Held: &held,
	})
}

// tutorialStreamMsgs is the NATS messages panel's traffic for the tour's
// game: two moves, each one atomic batch of cells, with the meta between.
func tutorialStreamMsgs() []streamMsg {
	base := time.Now().Add(-2 * time.Second)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	cell := func(r, c int) string { return config.CoopCellSubject(tutGameID, r, c) }
	return []streamMsg{
		{ts: at(0), subject: cell(5, 4), payload: `{"a":true,"t":2,"r":0,"ar":5,"ac":3}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: cell(6, 3), payload: `{"a":true,"t":2,"r":0,"ar":5,"ac":3}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: cell(6, 4), payload: `{"a":true,"t":2,"r":0,"ar":5,"ac":3}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: cell(6, 5), payload: `{"a":true,"t":2,"r":0,"ar":5,"ac":3}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(2), subject: cell(4, 4), payload: `{}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(2), subject: cell(5, 3), payload: `{}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(2), subject: cell(5, 5), payload: `{}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(310), subject: config.MetaSubject(tutGameID), payload: `{"status":"in_progress","level":2,"score":3400}`, group: 2},
		{ts: at(520), subject: cell(6, 4), payload: `{"a":true,"t":2,"r":0,"ar":6,"ac":3}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(521), subject: cell(7, 3), payload: `{"a":true,"t":2,"r":0,"ar":6,"ac":3}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(521), subject: cell(7, 4), payload: `{"a":true,"t":2,"r":0,"ar":6,"ac":3}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(521), subject: cell(7, 5), payload: `{"a":true,"t":2,"r":0,"ar":6,"ac":3}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(522), subject: cell(5, 4), payload: `{}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(522), subject: cell(6, 3), payload: `{}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(522), subject: cell(6, 5), payload: `{}`, batch: "b17e05aa62", group: 3, batched: true},
	}
}

// --- the steps ---

// tutorialSteps is the tour, in order.
func tutorialSteps() []tutStep {
	// The lobby with every column up, on one tab.
	lobbyOn := func(tab string) func(*App) {
		return func(a *App) {
			a.lobbyTab = tab
			a.lobbyMenuShown, a.lobbyPlayersShown, a.lobbyChatShown = true, true, true
		}
	}
	// The wizard on one step, with the tour's choices made.
	wizard := func(step int) func(*App) {
		return func(a *App) {
			lobbyOn(lobbyTabGames)(a)
			a.createWizStep = step
			a.modeEnum.Value = "cooperative"
			if a.countEd.Text() != "1" {
				a.countEd.SetText("1")
			}
			a.rulesEnum.Value = "guideline"
			a.createJoinEnum.Value = "invite"
		}
	}
	picker := func(a *App) {
		lobbyOn(lobbyTabGames)(a)
		a.inviteSelfSel.Value = true
	}
	// The game screen with every panel up, the NATS messages panel where the
	// step is about it.
	gameOn := func(msgs bool) func(*App) {
		return func(a *App) {
			a.hudShown, a.padShown, a.chatShown, a.oppShown = true, true, true, true
			a.showMsgs.Value = msgs
			a.tutorialGameState(false)
		}
	}
	// The game screen before the start: the READY bar over an empty board.
	gameReady := func(a *App) {
		a.hudShown, a.padShown, a.chatShown, a.oppShown = true, true, true, true
		a.showMsgs.Value = false
		a.tutorialGameState(true)
	}
	menuScroll := func(a *App) (*widget.List, string) { return &a.lobbyMenuList, tutLobbyMenu }
	hudScroll := func(a *App) (*widget.List, string) { return &a.hudList, tutHUDColumn }
	lobbyStep := func(title, body string, targets ...string) tutStep {
		return tutStep{scene: tutSceneLobby, title: title, body: body, targets: targets, prep: lobbyOn(lobbyTabGames)}
	}
	gameStep := func(title, body string, targets ...string) tutStep {
		return tutStep{scene: tutSceneGame, title: title, body: body, targets: targets, prep: gameOn(false)}
	}
	// A step on the game screen before the start: the bar's buttons, then
	// the READY bar itself.
	readyStep := func(title, body string, targets ...string) tutStep {
		return tutStep{scene: tutSceneGame, title: title, body: body, targets: targets, prep: gameReady}
	}
	hudStep := func(title, body string, targets ...string) tutStep {
		s := gameStep(title, body, targets...)
		s.scroll = hudScroll
		return s
	}
	steps := []tutStep{
		lobbyStep("WELCOME TO JETRIS",
			"This tour walks through the lobby, creating a game, and the game screen. "+
				"Next and Back move through it (or the ← → keys); Close, or Esc, leaves it at any point."),
		lobbyStep("THE MENU BUTTON",
			"The ☰ button shows and hides the menu column: Disconnect, your name and the server you are on, the voice chat, and the controls legend. "+
				"It is a switch — the button that shows a column is the only thing that hides it — and it remembers your choice from one session to the next.",
			tutLobbyMenuBtn, tutLobbyMenu),
		lobbyStep("VOICE CHAT",
			"The mic button is the voice switch. Every session starts muted; tap it to open your microphone to everyone in the lobby — in a game, to your team or to the whole table. "+
				"Muted, open, or lit green while your voice is going out: the button shows which.",
			tutLobbyMic),
		{scene: tutSceneLobby, title: "GATE AND PLAY VOICE", targets: []string{tutVoice}, prep: lobbyOn(lobbyTabGames), scroll: menuScroll,
			body: "The VOICE section holds the rest. GATE is how much louder than the room you must be before anything is sent: OPEN at 0 sends whenever the mic is on, a higher setting keeps the keyboard's clatter to yourself, and the meter shows your level against it. " +
				"Play voice is whether you hear the others — off, you still see who is talking, by the speaker mark beside their name."},
		lobbyStep("PLAYERS",
			"Everyone on this server who is in the lobby, in a game, or spectating — the agents (the bots) marked as such. The button shows and hides the column; on a narrow screen the players become a strip above the chat instead.",
			tutLobbyPlayersBtn, tutLobbyPlayers),
		lobbyStep("CHAT",
			"The lobby's chat strip. Type a line and press Enter, or Send: everyone in the lobby reads it, and the players in a game see it prefixed @lobby. The dot on the button marks unread messages while the strip is put away.",
			tutLobbyChatBtn, tutLobbyChat),
		lobbyStep("GAMES",
			"The games on offer right now, newest first: each row names the mode, the seats filled, the board's width and the rules. "+
				"Join takes a seat (one button per team in a teams game), Spectate watches a running or full game, and an invite-only game offers Join only to the players invited to it.",
			tutLobbyTabGames),
		{scene: tutSceneLobby, title: "GAME HISTORY", targets: []string{tutLobbyTabHistory}, prep: lobbyOn(lobbyTabHistory),
			body: "Every game finished on this server: its score, time, mode and players, the winner first. Sort the table by score or by date, and filter it by crew — players only, agents and players, or agents only. TOP 10 marks a game in its bucket's all-time top ten."},
		{scene: tutSceneLobby, title: "VIEW BOARD AND REPLAY", targets: []string{tutHistoryActions}, prep: lobbyOn(lobbyTabHistory),
			body: "View board opens a finished game's final playfields and its chat. Replay plays the whole game back from its stream, with a tape deck: play and pause, the speed, and a bar to scrub through it. The top games keep their replay; the rest keep their final board."},
		{scene: tutSceneLobby, title: "SERVER LOG", targets: []string{tutLobbyTabLog}, prep: lobbyOn(lobbyTabLog),
			body: "The journal every client writes to: who connected, disconnected or came back, and which games were created and started — newest first."},
		{scene: tutSceneLobby, title: "KEYS AND TOUCH", targets: []string{tutControls}, prep: lobbyOn(lobbyTabGames), scroll: menuScroll,
			body: "How the piece is played. Keys: ← → (or A D) move it, ↓ (S) soft-drops, ↑ (W or X) rotates it clockwise, Z or Ctrl counter-clockwise, Space hard-drops, C or Shift holds, and Tab moves the keys between the board and the chat. " +
				"Touch: swipe left or right to move, tap the left or right half of the board to rotate, drag down to soft-drop, flick down to hard-drop, swipe up to hold."},
		lobbyStep("CREATE A NEW GAME",
			"This button opens the wizard that sets a game up, one choice at a time. Let's create one: a co-op game for a crew of one, on Guideline rules, by invitation.",
			tutCreateBtn),
		{scene: tutSceneWizard, title: "GAME TYPE AND PLAYERS", targets: []string{tutWizard}, prep: wizard(wizStepMode),
			body: "Co-op: everyone plays one shared board for one shared score. Competitive: a board each, the last player standing wins, and every line you clear sends garbage to the others. Teams: team against team, each team on a shared board of its own. " +
				"Players is the seat count — per team, in a teams game. One seat in co-op is a solo run for the high score, which is the game this tour creates; with more seats a slider sets how much wider the shared board grows per player."},
		{scene: tutSceneWizard, title: "GAME RULES", targets: []string{tutWizard}, prep: wizard(wizStepNext),
			body: "Guideline sets every rule to its Tetris Guideline setting: six pieces shown in the NEXT well, the ghost that marks where a hard drop lands, and the hold queue. " +
				"Custom lets you set each one yourself — and, in the modes that raise garbage, how many holes a garbage row has, whether the rows of one attack line them up, and whether attacks follow the Guideline table: a single sends nothing, a double one row, a triple two, a Tetris four."},
		{scene: tutSceneWizard, title: "WHO CAN JOIN", targets: []string{tutWizard}, prep: wizard(wizStepJoin),
			body: "Invite only: you choose the players, and only they can take a seat. Open: anyone in the lobby can join, and one more step decides whether agents may too, and how many. Choose players… creates the game and opens the invitation picker."},
		{scene: tutScenePicker, title: "INVITE PLAYERS", targets: []string{tutPickerSelf}, prep: picker,
			body: "The picker lists everyone in the lobby. The first row is you: Play takes a seat for yourself — ticked here, since you are playing this one; unticked, you host the game and spectate it. With one seat and you in it, the game is full."},
		{scene: tutScenePicker, title: "INVITING THE OTHERS", targets: []string{tutPickerOthers}, prep: picker,
			body: "Ticking a name sends the invitation on the spot, and unticking retracts it; each row says where it stands — invited and waiting, declined, or joined — and the tally at the top counts the seats. " +
				"The game starts on its own once every seat is filled and every player has tapped READY, so a one-seat game starts as soon as you do. Next: the game screen."},
		readyStep("THE MENU BUTTON",
			"Every seat filled, the game screen opens: an empty board, and a bar with the lobby's buttons. ☰ shows and hides the menu column: the players, the stats, and every setting the rest of this tour goes through. Nothing in it stops play — once the game is on, the piece keeps falling while the menu is open.",
			tutGameBarMenu),
		readyStep("VOICE",
			"The mic button is the same switch as in the lobby, on the game's own room: your team's in a teams game — the VOICE section can widen it to everyone — and the whole table otherwise. Muted at every game's start.",
			tutGameBarMic),
		readyStep("THE PAD SWITCH",
			"Shows and hides the on-screen pad. In a competitive or teams game a boards button stands beside it, for the opponents' playfields down the side of yours.",
			tutGameBarPad),
		readyStep("GAME CHAT",
			"Shows and hides the chat strip: this game's players and spectators, with the lobby's messages folded in and marked @lobby. Click into it, or press Tab, to type; click the board, or press Esc or Tab, to play on. A line that starts with @lobby answers the lobby.",
			tutGameBarChat, tutGameChat),
		readyStep("TAP WHEN READY",
			"Over the board, until the start, this button — and the menu column lists who has tapped it. Tap it when you are ready, and again to take it back. "+
				"Once the last player has, the game counts down 3, 2, 1, GO! over the board and the first piece falls. In a one-seat game that last player is you.",
			tutGameReady),
		gameStep("THE PLAYFIELD",
			"Your board, a while in. In co-op every teammate's piece is on it at once, each in its own color, and no two can ever overlap: players steer around each other. The white frame says the keys are driving the piece, and the faint copy under it is the ghost: where a hard drop would land it.",
			tutGameBoard),
		gameStep("HOLD AND NEXT",
			"NEXT shows the pieces to come, in order. HOLD sets the falling piece aside — C, Shift, a swipe up, or a tap on the box — and brings it back later, once per piece: the box dims until the next piece spawns.",
			tutGameHold, tutGameNext),
		gameStep("THE MOVE BUFFER",
			"Every move is a transaction on the game's stream, and the moves you make while one is in flight queue up here. On a far-away server the strip fills; each color is one batch that goes out together. The bar's readout shows the round trip.",
			tutGameStrip),
		gameStep("THE ON-SCREEN PAD",
			"The whole keyboard scheme as buttons: the D-pad moves (its ▲ rotates clockwise), the face buttons rotate either way, DROP hard-drops and HOLD holds. On a touch screen it grows to thumb size — and the swipes work on the playfield with or without it.",
			tutGamePad, tutGamePadFace),
		hudStep("PLAYERS AND STATS",
			"The menu column opens with the roster in its board colors, then SCORE and LEVEL — one shared pair in co-op, one per team in teams — and Batch RTT: the round trip from publishing a move to seeing it come back from the server, colored as it grows. "+
				"LINK LOST shows here if the connection drops; the moves you make meanwhile land once it is back.",
			tutHUDStats),
		hudStep("SHOW NATS MESSAGES",
			"Ticked, a panel opens along the bottom of the screen with the raw stream traffic: every cell written, as it happens. Let's turn it on.",
			tutHUDMsgs),
		{scene: tutSceneGame, title: "THE NATS MESSAGES", targets: []string{tutNatsPanel}, prep: gameOn(true),
			body: "Each line is one message on the game's stream: its time, its subject and its JSON payload. The rows of one color are one atomic batch — a move, published and committed as a whole. This is the blackboard everyone plays on. Drag the bar over the panel to resize it."},
		hudStep("MOVE PUBLISHING",
			"Two ways to play the round trip. Pessimistic sync ☹ shows a move only once the server has committed it and sent it back. Optimistic async ☺, the default, draws the piece where you steer it at once and pipelines the publishing; a lost race snaps it back and replays the moves behind it. Flip between them to feel what each costs.",
			tutHUDLab),
		hudStep("HANDLING: DAS",
			"DAS — Delayed Auto Shift: how long you hold ← or → before the piece starts sliding on its own, in milliseconds. Lower starts the slide sooner.",
			tutHUDDAS),
		hudStep("ARR",
			"ARR — Auto Repeat Rate: once the slide has started, the time between one step and the next. At 0 ms the piece goes straight to the wall.",
			tutHUDARR),
		hudStep("SDF",
			"SDF — Soft Drop Factor: how many times faster than gravity the piece falls while ↓ is held. MAX takes it to the floor at once, without locking it.",
			tutHUDSDF),
		hudStep("GUARD",
			"The accidental-drop guard: after a piece locks on its own, hard drop is refused for this long, so a Space meant for the last piece does not drop the next one. OFF at 0.",
			tutHUDGuard),
		hudStep("BACK TO LOBBY",
			"Leaves the game screen. In the middle of a game you are asked to confirm: the seat is kept, the board plays on, and the lobby's row offers Rejoin. Once a game is over, the lobby's history has its result.",
			tutHUDBack),
		gameStep("THAT'S THE TOUR",
			"Create a game, invite a friend or an agent, and play. The lobby's menu column keeps the controls legend, and every setting you saw is one tap away. Have fun!"),
	}
	return steps
}
