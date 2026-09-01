package nativeui

// A phone reaches every one of these screens now that the lobby fits one, and
// a Gio Flex does not complain when it runs out of room — it squeezes its
// flexed child to nothing, wraps a label into stacked syllables, or reports a
// baseline-aligned row as one line tall and draws three. These pin the places
// that were doing exactly that.

import (
	"image"
	"testing"
	"time"

	"gioui.org/layout"

	"jetris/internal/config"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
)

// TestModalWidthFitsTheScreen: a dialog asks for the width it was designed
// for and gets the screen's, less a margin, when the screen is smaller.
// Pinning Max.X to the design width alone laid a 480 dp wizard out 480 dp
// wide on a 390 dp phone, hanging its Next button off the edge.
func TestModalWidthFitsTheScreen(t *testing.T) {
	if got, want := modalW(looseCtx(1280, 820), 480), 480; got != want {
		t.Errorf("modalW on a desktop = %d, want the %d it asked for", got, want)
	}
	for _, w := range []int{390, 360, 320} {
		got := modalW(looseCtx(w, 844), 480)
		if got > w-12 {
			t.Errorf("modalW on a %d dp screen = %d, past the %d it has", w, got, w-12)
		}
		if got <= 0 {
			t.Errorf("modalW on a %d dp screen = %d", w, got)
		}
	}
}

// TestInviteNameCellStacks: a picker row's name and status run along one line
// where the column holds them and stack where it does not — and the stacked
// cell REPORTS the height it draws. The bug was a baseline-aligned Flex that
// wrapped the status into three lines, reported one, and let the row draw
// over the text beneath it.
func TestInviteNameCellStacks(t *testing.T) {
	a := newTestApp()
	const name, status = "You (tester)", "spectating when the game starts"
	cell := a.inviteNameCell(name, status, colMuted)
	line := a.body("x", colFg)(looseCtx(400, 200)).Size.Y
	if wide := cell(looseCtx(600, 200)); wide.Size.Y > line {
		t.Errorf("the cell took %d dp in a 600 dp column, past the one line (%d dp) it fits on", wide.Size.Y, line)
	}
	// In a phone's column the two stack, and the cell must report every dp it
	// draws: what the pair take on their own lines at this width. The
	// baseline-aligned Flex it replaced reported one line fewer than the
	// wrapped status actually filled, and the picker drew the difference over
	// the text beneath the row.
	const narrowW = 185
	narrow := looseCtx(narrowW, 200)
	want := a.body(name, colFg)(narrow).Size.Y + a.body(status, colMuted)(narrow).Size.Y
	got := cell(narrow)
	if got.Size.Y < want {
		t.Errorf("the cell reported %d dp in a %d dp column but its two lines draw %d: it overprints what is under it",
			got.Size.Y, narrowW, want)
	}
	if got.Size.X > narrowW {
		t.Errorf("the cell is %d dp wide in a %d dp column", got.Size.X, narrowW)
	}
}

// TestPhoneOverlaysLayout lays every screen and overlay a phone can reach
// out at a phone's viewport. A zero size means nothing was drawn.
func TestPhoneOverlaysLayout(t *testing.T) {
	sz := image.Pt(390, 844)
	form := screenForm{device: devicePhone, w: sz.X, h: sz.Y, portrait: true, compact: true}
	rec := config.ArchiveRecord{
		GameID: "g", Mode: config.ModeCompetitive,
		StartedAt: time.Now().Add(-time.Hour), FinishedAt: time.Now(),
		Players: []config.PlayerResult{{PlayerID: "alice", Score: 4200, Level: 5, Winner: true}, {PlayerID: "bob", Score: 900}},
		Boards:  []config.BoardPicture{{Label: "alice", Idx: 0, Width: 10, Height: 24}, {Label: "bob", Idx: 1, Width: 10, Height: 24}},
		Chat:    []config.ChatLine{{Name: "alice", Text: "gg", Timestamp: time.Now()}},
	}
	inv := lobby.Invitation{FromName: "carol", Mode: config.ModeTeams, Team: 1, GameID: "g-teams-1234"}

	newApp := func() *App {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.deviceHint, a.deviceHinted, a.touchUI = devicePhone, true, true
		a.form = form
		return a
	}
	cases := []struct {
		name string
		draw func(*App) layout.Widget
	}{
		{"wizard-mode", func(a *App) layout.Widget { a.createWizStep = wizStepMode; return a.createWizardOverlay }},
		{"wizard-rules", func(a *App) layout.Widget {
			a.createWizStep, a.rulesEnum.Value, a.modeEnum.Value = wizStepNext, "custom", "competitive"
			return a.createWizardOverlay
		}},
		{"wizard-join-invite", func(a *App) layout.Widget {
			a.createWizStep, a.createJoinEnum.Value = wizStepJoin, "invite"
			return a.createWizardOverlay
		}},
		{"wizard-agents", func(a *App) layout.Widget {
			a.createWizStep, a.allowAgentsCb.Value = wizStepAgents, true
			return a.createWizardOverlay
		}},
		{"invite-picker-teams", func(a *App) layout.Widget {
			a.invitePickerGameID, a.invitePickerMode = "g", config.ModeTeams
			a.invitePickerPC, a.invitePickerTS = 4, 2
			a.invitePicker = map[string]*inviteChoice{
				"alice":                {playerID: "alice", name: "alice"},
				"golang-mk1-3f7a-hard": {playerID: "golang-mk1-3f7a-hard", name: "golang-mk1-3f7a-hard", agent: true},
			}
			return a.invitePickerOverlay
		}},
		{"incoming-invite", func(a *App) layout.Widget {
			return func(gtx C) D { return a.incomingInviteOverlay(gtx, &inv) }
		}},
		{"replay-choice", func(a *App) layout.Widget {
			return func(gtx C) D { return a.replayChoiceOverlay(gtx, rec) }
		}},
		{"replay-loading", func(a *App) layout.Widget {
			a.replayView, a.screen = newReplayView(rec), screenReplay
			a.replayView.loaded, a.replayView.total = 900, 4000
			return a.layoutReplay
		}},
		{"replay-player", func(a *App) layout.Widget {
			// The transport splits its row on a phone: keys over speeds.
			a.replayView, a.screen = loadedReplay(rec), screenReplay
			return a.layoutReplay
		}},
		{"leave-game", func(a *App) layout.Widget { return a.confirmLeaveOverlay }},
		{"reset-favorites", func(a *App) layout.Widget { return a.confirmResetOverlay }},
		{"archive-viewer", func(a *App) layout.Widget {
			a.archiveSel, a.screen = &rec, screenArchive
			return a.layoutArchive
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newApp()
			w := c.draw(a)
			gtx := testCtx(sz.X, sz.Y)
			if d := w(gtx); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("%s drew nothing at %v", c.name, sz)
			}
		})
	}
}

// TestArchiveColumnsFit: the boards are what the archive viewer is FOR, and a
// Flex squeezes its flexed child first — between a 190 dp roster and a 320 dp
// chat panel they came out nothing wide on a phone. The three stand side by
// side only where what is left over is still a board area worth the name.
func TestArchiveColumnsFit(t *testing.T) {
	for _, c := range []struct {
		w    int
		want bool
	}{{1280, true}, {900, true}, {390, false}, {360, false}} {
		if got := archiveColumnsFit(looseCtx(c.w, 800)); got != c.want {
			t.Errorf("archiveColumnsFit at %d dp = %v, want %v (roster %d + chat %d + a %d dp board area)",
				c.w, got, c.want, archiveRosterW, archiveChatW, archiveBoardsMinW)
		}
	}
}

// TestBrowserRowKeepsItsName: a server row's probe readout is a pixel-face
// line 200 dp long, and as a rigid child beside the flexed name it took the
// row's width first — "Jetris EU central" came out one letter per line down
// the list. The readout gives way instead, under the name.
func TestBrowserRowKeepsItsName(t *testing.T) {
	a := NewWithPicker(config.Config{}, nil, "", prefs.DefaultFavorites())
	a.th = newTestApp().th
	fav := prefs.DefaultFavorites()[0]
	e := connEntry{key: urlKey(fav.URL), label: fav.Label, detail: fav.URL, dialable: true, fav: 0}
	probes := map[string]probeResult{e.key: {ok: true, msg: "ok",
		rtt: 21 * time.Millisecond, players: 2, agents: 3, lobby: true}}
	line := a.body("x", colFg)(looseCtx(400, 200)).Size.Y
	for _, sel := range []bool{false, true} {
		a.connSel = ""
		if sel {
			a.connSel = e.key
		}
		// A phone's list, and a desktop's for contrast.
		narrow := a.entryRow(e, probes, map[string]bool{})(looseCtx(300, 400))
		wide := a.entryRow(e, probes, map[string]bool{})(looseCtx(900, 400))
		if wide.Size.Y > 3*line {
			t.Errorf("selected=%v: the row took %d dp at 900 dp wide, past the name over its URL", sel, wide.Size.Y)
		}
		// Name, URL and the readout under them — three lines and their
		// padding, nothing like a name set vertically.
		if limit := 6 * line; narrow.Size.Y > limit {
			t.Errorf("selected=%v: the row took %d dp at 300 dp wide, past the %d dp of its three lines: the name is being set one letter at a time",
				sel, narrow.Size.Y, limit)
		}
	}
}

// TestLobbyPlayersMoveUnderThePanel: the players stand in a column beside the
// panel only while the panel keeps a width worth reading. Below that they go
// where the chat goes — a strip above it — because the column's quarter of a
// phone's width is the difference between the history table's MODE column and
// a stack of one-letter lines.
func TestLobbyPlayersMoveUnderThePanel(t *testing.T) {
	a := newTestApp()
	for _, c := range []struct {
		w    int
		want bool
	}{{1280 - 340, true}, {700, true}, {490, false}, {366, false}} {
		if got := a.lobbyPlayersBeside(looseCtx(c.w, 800), c.w); got != c.want {
			t.Errorf("lobbyPlayersBeside in %d dp = %v, want %v (a %d dp column over a %d dp panel)",
				c.w, got, c.want, a.lobbyPlayersColW(looseCtx(c.w, 800), c.w), lobbyPanelMinW)
		}
	}
}

// TestLoginTaglineWraps: the branding line breaks after "made with" rather
// than letting its last rigid child be squeezed and split "JetStream" across
// two lines mid-word.
func TestLoginTaglineWraps(t *testing.T) {
	a := newTestApp()
	wide := a.loginTagline(looseCtx(900, 200))
	narrow := a.loginTagline(looseCtx(340, 200))
	if wide.Size.Y == 0 || narrow.Size.Y == 0 {
		t.Fatal("the tagline drew nothing")
	}
	if narrow.Size.Y <= wide.Size.Y {
		t.Errorf("the tagline is %d dp tall at 340 dp and %d at 900: it did not break", narrow.Size.Y, wide.Size.Y)
	}
	// Two lines and the gap between them, and no more: squeezed onto one, its
	// last run wrapped mid-word and the line came out three deep.
	if limit := 2 * wide.Size.Y; narrow.Size.Y > limit {
		t.Errorf("the tagline is %d dp tall at 340 dp, past the %d dp of the two lines it breaks into: a word is being split",
			narrow.Size.Y, limit)
	}
	if narrow.Size.X > 340 {
		t.Errorf("the tagline is %d dp wide in a 340 dp card", narrow.Size.X)
	}
}
