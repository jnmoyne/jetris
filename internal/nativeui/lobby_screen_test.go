package nativeui

// The lobby screen's chrome (lobby.go): the bar's three switches, the panel's
// two tabs, and the shapes they can put the screen in.

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"jetris/internal/config"
	"jetris/internal/lobby"
)

// lobbyRig drives a whole lobby screen through a real Gio router, so the
// bar's buttons are hit-tested exactly where the screen draws them.
type lobbyRig struct {
	a  *App
	r  *input.Router
	sz image.Point
}

func newLobbyRig(t *testing.T, sz image.Point, dev deviceKind) *lobbyRig {
	t.Helper()
	a := newTestApp()
	a.touchUI = dev != deviceDesktop
	a.deviceHint, a.deviceHinted = dev, true
	a.lobby = lobby.New(nil, nil, "tester", "tester")
	a.screen = screenLobby
	a.connName, a.connURL = "Jetris EU central", "wss://eu-central.jetris.example:443"
	g := &lobbyRig{a: a, r: new(input.Router), sz: sz}
	g.frame()
	return g
}

// frame lays the lobby out through the router and commits it, the way the
// window loop does.
func (g *lobbyRig) frame() {
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
func (g *lobbyRig) tap(x, y float32) {
	touch(g.r, pointer.Press, 1, x, y, 0)
	touch(g.r, pointer.Release, 1, x, y, 60*time.Millisecond)
	g.frame()
	g.frame()
}

// barY is the middle of the lobby bar, which rides under the brand banner —
// measured rather than assumed, since the banner is one line on a phone and a
// sentence on a desktop. The x positions are the game bar's own (barMenuX and
// friends, gamescreen_test.go): the lobby's bar is built out of the same
// buttons at the same size, the menu leftmost and, right to left, the chat
// strip's switch and the players column's.
func (g *lobbyRig) barY() float32 {
	var ops op.Ops
	gtx := scaledContext(layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: g.sz},
	})
	return float32(g.a.lobbyBanner(gtx).Size.Y) + barCenterY()
}

func lobbyPlayersBtnX(w int) float32 { return barPadX(w) }

// TestLobbyBarSwitches: a desktop lobby opens with all three columns up, and
// each bar button hides exactly the one it shows — nothing else moves, and
// the button that showed a column is the only thing that puts it away.
func TestLobbyBarSwitches(t *testing.T) {
	const w, h = 1280, 820
	g := newLobbyRig(t, image.Pt(w, h), deviceDesktop)
	if g.a.form.compact {
		t.Fatalf("a %dx%d desktop window came out compact: %+v", w, h, g.a.form)
	}
	if !g.a.lobbyMenuVisible() || !g.a.lobbyPlayersVisible() || !g.a.lobbyChatVisible() {
		t.Fatalf("the lobby did not open with all three up: menu=%v players=%v chat=%v",
			g.a.lobbyMenuVisible(), g.a.lobbyPlayersVisible(), g.a.lobbyChatVisible())
	}
	y := g.barY()
	for _, c := range []struct {
		name string
		x    float32
		on   func() bool
		off  []func() bool
	}{
		{"menu", barMenuX(), g.a.lobbyMenuVisible, []func() bool{g.a.lobbyPlayersVisible, g.a.lobbyChatVisible}},
		{"players", lobbyPlayersBtnX(w), g.a.lobbyPlayersVisible, []func() bool{g.a.lobbyMenuVisible, g.a.lobbyChatVisible}},
		{"chat", barChatX(w), g.a.lobbyChatVisible, []func() bool{g.a.lobbyMenuVisible, g.a.lobbyPlayersVisible}},
	} {
		g.tap(c.x, y)
		if c.on() {
			t.Errorf("the %s button did not hide the %s", c.name, c.name)
		}
		for i, other := range c.off {
			if !other() {
				t.Errorf("hiding the %s put switch %d away with it", c.name, i)
			}
		}
		g.tap(c.x, y)
		if !c.on() {
			t.Errorf("the %s button did not bring the %s back", c.name, c.name)
		}
	}
}

// TestLobbyCompactOpensBare: a phone's lobby opens on the games and nothing
// else, and there its menu is drawn OVER the panel rather than beside it —
// what is left of a 390 dp screen after a menu column is no games list.
func TestLobbyCompactOpensBare(t *testing.T) {
	const w, h = 390, 844
	g := newLobbyRig(t, image.Pt(w, h), devicePhone)
	if !g.a.form.compact {
		t.Fatalf("a %dx%d phone did not get the compact form: %+v", w, h, g.a.form)
	}
	if g.a.lobbyMenuVisible() || g.a.lobbyPlayersVisible() || g.a.lobbyChatVisible() {
		t.Errorf("a phone's lobby opened with a column up: menu=%v players=%v chat=%v",
			g.a.lobbyMenuVisible(), g.a.lobbyPlayersVisible(), g.a.lobbyChatVisible())
	}
	if g.a.lobbyMenuBeside(looseCtx(w, h)) {
		t.Errorf("a %d dp screen claims room for the menu column beside the panel", w)
	}
	// And the switch is still the player's: pressing it puts the menu up, over
	// the panel, and pressing it again takes it away.
	y := g.barY()
	g.tap(barMenuX(), y)
	if !g.a.lobbyMenuVisible() {
		t.Error("the menu button did not open the menu on a phone")
	}
	g.tap(barMenuX(), y)
	if g.a.lobbyMenuVisible() {
		t.Error("the menu button did not close the menu on a phone")
	}
}

// TestLobbySwitchesUnderAModal: a press that lands on the bar while the
// create wizard is up is spent there and answered by nothing — neither that
// frame nor the one after the wizard closes.
func TestLobbySwitchesUnderAModal(t *testing.T) {
	const w, h = 1280, 820
	g := newLobbyRig(t, image.Pt(w, h), deviceDesktop)
	y := g.barY()
	g.a.createWizStep = wizStepMode
	g.frame()
	g.tap(barMenuX(), y)
	if !g.a.lobbyMenuVisible() {
		t.Error("a press on the bar under the wizard hid the menu column")
	}
	g.a.createWizStep = 0
	g.frame()
	g.frame()
	if !g.a.lobbyMenuVisible() {
		t.Error("the press spent under the wizard arrived again once it closed")
	}
}

// TestLobbyTabs: the panel opens on the games, and the chips move it between
// the two lists.
func TestLobbyTabs(t *testing.T) {
	g := newLobbyRig(t, image.Pt(1280, 820), deviceDesktop)
	if g.a.lobbyTab != lobbyTabGames {
		t.Fatalf("the lobby opened on tab %q, want %q", g.a.lobbyTab, lobbyTabGames)
	}
	g.a.lobbyTabBtns[1].Click()
	g.frame()
	if g.a.lobbyTab != lobbyTabHistory {
		t.Errorf("the history chip left the panel on %q", g.a.lobbyTab)
	}
	g.frame()
	g.a.lobbyTabBtns[0].Click()
	g.frame()
	if g.a.lobbyTab != lobbyTabGames {
		t.Errorf("the games chip left the panel on %q", g.a.lobbyTab)
	}
}

// TestLobbyPanelShapes lays the panel out on both tabs, with games and
// finished games in it and with neither, at the widths the columns leave it:
// a desktop window with both columns up, a window with none, and a phone's.
// A zero size means nothing was drawn.
func TestLobbyPanelShapes(t *testing.T) {
	a := newTestApp()
	a.lobby = lobby.New(nil, nil, "tester", "tester")
	games, archives := sampleLobbyGames(), sampleLobbyArchives()
	abandoned := map[string]bool{"gone-game-9999": true}
	for _, sz := range []image.Point{{X: 620, Y: 700}, {X: 1256, Y: 700}, {X: 366, Y: 760}} {
		for _, tab := range []string{lobbyTabGames, lobbyTabHistory} {
			for _, empty := range []bool{false, true} {
				a.lobbyTab = tab
				gs, ar := games, archives
				if empty {
					gs, ar = nil, nil
				}
				gtx := testCtx(sz.X, sz.Y)
				d := a.lobbyPanel(gtx, gs, abandoned, ar)
				if d.Size.X == 0 || d.Size.Y == 0 {
					t.Fatalf("panel %v tab %q empty=%v drew nothing", sz, tab, empty)
				}
			}
		}
	}
}

// TestLobbyScreenShapes renders the whole screen in each of the eight states
// its three switches can be in, at a desktop window and a phone's.
func TestLobbyScreenShapes(t *testing.T) {
	for _, sz := range []image.Point{{X: 1280, Y: 820}, {X: 390, Y: 844}} {
		for _, prefs := range [][3]int8{
			{-1, -1, -1}, {1, -1, -1}, {-1, 1, -1}, {-1, -1, 1},
			{1, 1, -1}, {1, -1, 1}, {-1, 1, 1}, {1, 1, 1},
		} {
			a := newTestApp()
			a.lobby = lobby.New(nil, nil, "tester", "tester")
			a.screen = screenLobby
			a.connName, a.connURL = "your embedded server", "nats://192.168.1.23:4222"
			a.usingEmbedded, a.embAddr = true, "192.168.1.23:4222"
			a.chatLog = []lobby.ChatMessage{{Name: "alice", Text: "ready when you are"}}
			a.lobbyMenuPref, a.lobbyPlayersPref, a.lobbyChatPref = prefs[0], prefs[1], prefs[2]
			var ops op.Ops
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(sz),
			}
			if d := a.layout(gtx); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("lobby %v switches %v drew nothing", sz, prefs)
			}
		}
	}
}

// sampleLobbyGames / sampleLobbyArchives are the lobby panel's stand-in
// contents, shared by the layout test above and the panel snapshots
// (screens_snapshot_test.go): an open game with an agent seat, a teams game
// already running, and an abandoned one; a competitive result and a teams
// one, so both roster shapes are drawn.
func sampleLobbyGames() []lobby.GameListing {
	now := time.Now()
	return []lobby.GameListing{
		{GameID: "open-game-abcdef", Mode: config.ModeCompetitive, Status: config.GameStatusCreated,
			PlayerCount: 3, MaxAgents: 1, NextCount: 1, Hold: true, CreatedAt: now,
			Players: []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "hal", Name: "hal", Agent: true}}},
		{GameID: "running-game-1234", Mode: config.ModeTeams, Status: config.GameStatusInProgress,
			PlayerCount: 4, TeamSize: 2, CreatedAt: now.Add(-time.Minute),
			Players: []lobby.PlayerSummary{{PlayerID: "bob", Name: "bob"}, {PlayerID: "carol", Name: "carol", Team: 1}}},
		{GameID: "gone-game-9999", Mode: config.ModeCooperative, Status: config.GameStatusCreated,
			PlayerCount: 2, CreatedAt: now.Add(-time.Hour)},
	}
}

func sampleLobbyArchives() []config.ArchiveRecord {
	now := time.Now()
	return []config.ArchiveRecord{
		{GameID: "done-1", Mode: config.ModeCompetitive, StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-50 * time.Minute),
			Players: []config.PlayerResult{{PlayerID: "alice", Score: 4200, Level: 5, Winner: true}, {PlayerID: "bob", Score: 900, Level: 2}}},
		{GameID: "done-2", Mode: config.ModeTeams, TeamSize: 1, WinningTeam: 0,
			StartedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-100 * time.Minute),
			TeamScores: []int{700, 300}, TeamLevels: []int{3, 2},
			Players: []config.PlayerResult{{PlayerID: "carol"}, {PlayerID: "bot", Agent: true, Team: 1}}},
	}
}

// The history tab has to survive a panel far narrower than a desktop's, now
// that a phone reaches it. Each of these pins one way a Gio Flex fails at
// that width rather than complaining: a rigid child handed one glyph of room
// prints its text DOWN the side, and a checkbox handed less than its label
// wants breaks the label into stacked syllables.

// TestHistoryTableFoldsOnANarrowPanel: under histRowW the rows fold onto two
// lines — the columns over the roster — and the header stops labelling a
// PLAYERS column that is no longer beside them.
func TestHistoryTableFoldsOnANarrowPanel(t *testing.T) {
	a := newTestApp()
	rec := sampleLobbyArchives()[0]
	var view, replay widget.Clickable
	wide, narrow := looseCtx(900, 500), looseCtx(340, 500)
	if histStacked(wide) {
		t.Error("a 900 dp panel folded the table")
	}
	if !histStacked(narrow) {
		t.Error("a 340 dp panel ran the table on one line")
	}
	oneLine := a.archiveHistoryCells(wide, rec, &view, &replay, false)
	folded := a.archiveHistoryCells(narrow, rec, &view, &replay, false)
	if folded.Size.X > 340 {
		t.Errorf("the folded row is %d dp wide in a 340 dp panel", folded.Size.X)
	}
	if folded.Size.Y <= oneLine.Size.Y {
		t.Errorf("the folded row is %d dp tall and the one-line row %d: it did not fold",
			folded.Size.Y, oneLine.Size.Y)
	}
	// The roster and the actions are on that second line and both got room:
	// the bug was a PLAYERS column squeezed to nothing by the fixed ones.
	if min := 3 * a.body("x", colFg)(looseCtx(340, 500)).Size.Y; folded.Size.Y < min {
		t.Errorf("the folded row is only %d dp tall, under the %d dp its two lines need", folded.Size.Y, min)
	}
	if a.archiveHistoryHeader(narrow).Size.X > 340 {
		t.Error("the folded header overflows the panel")
	}
}

// TestHistoryControlsStackWhenTheyMust: the sort selector and the crew filters
// run on one line where they fit, drop to two where they do not, and take a
// line each rather than let a checkbox wrap its own label. The two failures
// are told apart by height: three filters on lines of their own are SHORTER
// than three squeezed side by side with one of their labels broken into
// stacked syllables, which is what a phone was drawing.
func TestHistoryControlsStackWhenTheyMust(t *testing.T) {
	a := newTestApp()
	box := a.histFilterBox(&a.histHumansCb, "Players only")(looseCtx(400, 200)).Size.Y
	stacked := layout.Flex{Axis: layout.Vertical}.Layout(looseCtx(340, 400),
		layout.Rigid(a.histFilterBox(&a.histHumansCb, "Players only")),
		layout.Rigid(a.histFilterBox(&a.histMixedCb, "Agents and players")),
		layout.Rigid(a.histFilterBox(&a.histAgentsOnlyCb, "Agents only")),
	).Size.Y
	if wide := a.historyControls(looseCtx(1200, 200)); wide.Size.Y > box {
		t.Errorf("the controls took %d dp at 1200 dp wide, past the one line (%d dp) they fit on", wide.Size.Y, box)
	}
	narrow := a.historyControls(looseCtx(340, 400))
	if narrow.Size.Y < stacked {
		t.Errorf("the controls took %d dp at 340 dp wide, under the %d dp the stacked filters need: they are still on one line",
			narrow.Size.Y, stacked)
	}
	if limit := box + 8 + stacked; narrow.Size.Y > limit {
		t.Errorf("the controls took %d dp at 340 dp wide, past the %d dp of a sort line over stacked filters: a label wrapped",
			narrow.Size.Y, limit)
	}
	if narrow.Size.X > 340 {
		t.Errorf("the controls are %d dp wide in a 340 dp panel", narrow.Size.X)
	}
}

// TestTeamStandingsLineWraps: the all-time scoreboard stacks where it will not
// fit on one line — squeezed into a Flex it printed TEAM B's totals one letter
// wide down the edge of the table, 250 dp tall.
func TestTeamStandingsLineWraps(t *testing.T) {
	a := newTestApp()
	recs := []config.ArchiveRecord{}
	for i := 0; i < 8; i++ {
		recs = append(recs, config.ArchiveRecord{
			GameID: "t", Mode: config.ModeTeams, WinningTeam: i % 2,
			TeamScores: []int{2818, 1218}, TeamLevels: []int{4, 3},
		})
	}
	wide := a.teamStandingsLine(looseCtx(1200, 200), recs)
	narrow := a.teamStandingsLine(looseCtx(340, 400), recs)
	if wide.Size.Y == 0 || narrow.Size.Y == 0 {
		t.Fatal("the standings line drew nothing over eight teams games")
	}
	if narrow.Size.Y <= wide.Size.Y {
		t.Errorf("the standings line is %d dp tall at 340 dp and %d at 1200: it did not stack",
			narrow.Size.Y, wide.Size.Y)
	}
	// Three short lines, not one word printed downwards.
	if limit := 5 * wide.Size.Y; narrow.Size.Y > limit {
		t.Errorf("the standings line is %d dp tall at 340 dp, past the %d dp its three lines need — it is printing sideways",
			narrow.Size.Y, limit)
	}
	if narrow.Size.X > 340 {
		t.Errorf("the standings line is %d dp wide in a 340 dp panel", narrow.Size.X)
	}
}

// TestCompactVersionPlate: on a phone the corner plate shares its row with the
// brand banner, so it says the short thing — and once a newer release is
// known, only that.
func TestCompactVersionPlate(t *testing.T) {
	old := version
	version = "0.11.1-13-gf206014-dirty"
	t.Cleanup(func() { version = old })
	if got, want := versionLabelShort(""), "VER 0.11.1"; got != want {
		t.Errorf("versionLabelShort() = %q, want %q", got, want)
	}
	if got, want := versionLabelShort("v0.14.0"), "NEW 0.14.0"; got != want {
		t.Errorf("versionLabelShort(update) = %q, want %q", got, want)
	}
	if full := versionLabel("v0.14.0"); len(full) <= len(versionLabelShort("v0.14.0")) {
		t.Errorf("the full plate %q is no longer than the short one", full)
	}
}

// presence builds n lobby players, the first few short-named and the rest
// agents with the long generated names agents actually carry.
func presence(n int) []lobby.PlayerPresence {
	base := []lobby.PlayerPresence{
		{PlayerID: "JNM", Name: "JNM"},
		{PlayerID: "Joe", Name: "Joe"},
		{PlayerID: "Testing123", Name: "Testing123", Status: lobby.StatusInGame},
		{PlayerID: "golang-mk1-65f7-medium", Name: "golang-mk1-65f7-medium", Agent: true},
		{PlayerID: "golang-mk1-a4a3-easy", Name: "golang-mk1-a4a3-easy", Agent: true},
		{PlayerID: "golang-mk1-e959-hard", Name: "golang-mk1-e959-hard", Agent: true},
	}
	out := make([]lobby.PlayerPresence, 0, n)
	for i := 0; i < n; i++ {
		p := base[i%len(base)]
		if i >= len(base) {
			p.Name = fmt.Sprintf("%s-%d", p.Name, i)
			p.PlayerID = p.Name
		}
		out = append(out, p)
	}
	return out
}

// TestPlayersStripWrapsAndCaps: the strip packs the players along each line
// and breaks onto the next only when the one after will not fit — more of
// them per line the wider it is — and stops growing at
// lobbyPlayersStripRows, scrolling past that. A strip that grew with the
// lobby would push the games off a phone the moment a few agents logged in.
func TestPlayersStripWrapsAndCaps(t *testing.T) {
	a := newTestApp()
	wide := len(a.lobbyPlayersFlow(looseCtx(820, 300), presence(6)))
	narrow := len(a.lobbyPlayersFlow(looseCtx(340, 300), presence(6)))
	if wide >= 6 {
		t.Errorf("six players took %d lines in 820 dp: they are not being packed", wide)
	}
	if narrow <= wide {
		t.Errorf("six players took %d lines in 340 dp and %d in 820: the packing ignores the width", narrow, wide)
	}
	if wide < 1 || narrow < 1 {
		t.Fatalf("the flow produced no lines: %d wide, %d narrow", wide, narrow)
	}
	// Capped: two lobbies well past the cap are the same height, and that
	// height is three lines of it and not thirty.
	h12 := a.lobbyPlayersStrip(looseCtx(390, 600), presence(12)).Size.Y
	h30 := a.lobbyPlayersStrip(looseCtx(390, 600), presence(30)).Size.Y
	if h12 != h30 {
		t.Errorf("the strip is %d dp tall with 12 players and %d with 30: it grows with the lobby", h12, h30)
	}
	one := a.lobbyPlayersStrip(looseCtx(390, 600), presence(1)).Size.Y
	if h30 > lobbyPlayersStripRows*one {
		t.Errorf("the strip is %d dp tall with 30 players, past the %d dp of its %d lines",
			h30, lobbyPlayersStripRows*one, lobbyPlayersStripRows)
	}
}
