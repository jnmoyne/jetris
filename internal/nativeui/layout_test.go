package nativeui

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
)

// newTestApp builds an App wired for headless layout: nil NATS handles (only
// used by background goroutines, never by the layout code) and a real theme.
func newTestApp() *App {
	a := New(nil, nil)
	a.th = newUITheme()
	return a
}

// renderOnce builds a manual frame context (zero input.Source, which Gio treats
// as disabled — safe) and lays out the current screen. A panic fails the test.
func renderOnce(t *testing.T, a *App) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
	}
	d := a.layout(gtx)
	if d.Size.X == 0 || d.Size.Y == 0 {
		// Screens fill the window; a zero size means nothing was laid out.
		t.Fatalf("layout produced zero dimensions: %+v", d.Size)
	}
}

// TestScreensLayoutWithoutPanic exercises every screen's layout code path
// (including the busy lobby and game screens, which can't be reached via the
// live launch smoke test) to catch nil derefs, bad indexing, or op misuse.
func TestScreensLayoutWithoutPanic(t *testing.T) {
	players := []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}

	t.Run("login", func(t *testing.T) {
		a := newTestApp()
		renderOnce(t, a)
	})

	t.Run("login-collision", func(t *testing.T) {
		a := newTestApp()
		a.loginCollision = true
		renderOnce(t, a)
	})

	t.Run("login-picker", func(t *testing.T) {
		// Without flags the first favorite starts selected, ahead of the
		// nats CLI's current context; that context is only the fallback of
		// a machine with no favorites at all.
		a := NewWithPicker(config.Config{}, []string{"alpha", "beta"}, "beta", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if a.connTab != connTabBrowser || a.connSel != urlKey(prefs.JetrisEUWS.URL) {
			t.Fatalf("default choice = %q/%q, want the browser tab with the first favorite", a.connTab, a.connSel)
		}
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NATSURL != prefs.JetrisEUWS.URL || cfg.NATSContext != "" || cfg.RunEmbedded {
			t.Fatalf("pickerConfig = %+v, want the first favorite's URL only", cfg)
		}
		if b := NewWithPicker(config.Config{}, []string{"alpha", "beta"}, "beta", nil); b.connSel != ctxKey("beta") {
			t.Fatalf("default choice with no favorites = %q, want the CLI's current context beta", b.connSel)
		}
		if b := NewWithPicker(config.Config{}, []string{"alpha", "beta"}, "", nil); b.connSel != ctxKey("alpha") {
			t.Fatalf("default choice with no favorites and no current context = %q, want the first context", b.connSel)
		}
		// The CLI's current context is marked as such — never "(selected)",
		// which would read as the browser's own selection.
		if e, ok := a.connEntry(ctxKey("beta")); !ok || e.label != "beta (nats CLI current)" {
			t.Fatalf("current-context row = %+v, want the nats CLI current marker", e)
		}
		renderOnce(t, a)
		// Every browser state lays out: a collapsed section, the add form,
		// a probe in flight and a finished one, then the LAN tab.
		a.connSecClosed[secContexts] = true
		a.connAddOpen = true
		a.connAddScroll = true // just opened: the list scrolls the form into view
		a.connProbing = map[string]bool{ctxKey("beta"): true}
		a.connProbes[urlKey(prefs.JetrisEUWS.URL)] = probeResult{ok: true, msg: "✓ ok", rtt: 12 * time.Millisecond, lobby: true, players: 2}
		renderOnce(t, a)
		if a.connAddScroll {
			t.Fatal("the add-form scroll request should be consumed by the frame that lays out the form")
		}
		a.connTab = connTabLAN
		renderOnce(t, a)
	})

	t.Run("login-picker-no-contexts", func(t *testing.T) {
		// Without contexts the first favorite (the demo server) starts
		// selected, and the CONTEXTS section shows its hint.
		a := NewWithPicker(config.Config{}, nil, "", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if a.connSel != urlKey(prefs.JetrisEUWS.URL) {
			t.Fatalf("default choice = %q, want the first favorite when no contexts exist", a.connSel)
		}
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NATSURL != prefs.JetrisEUWS.URL {
			t.Fatalf("pickerConfig URL = %q, want %q", cfg.NATSURL, prefs.JetrisEUWS.URL)
		}
		renderOnce(t, a)

		// Nothing to select at all (no contexts, every favorite deleted):
		// Play explains instead of dialing nowhere.
		a = NewWithPicker(config.Config{}, nil, "", nil)
		a.th = newTestApp().th
		if a.connSel != "" {
			t.Fatalf("default choice = %q, want none", a.connSel)
		}
		if _, err := a.pickerConfig(); err == nil {
			t.Fatal("pickerConfig with nothing selected should error")
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-server-flag", func(t *testing.T) {
		// --server selects that URL, beating the CLI's selected context; a
		// URL that isn't a favorite is listed under COMMAND LINE.
		a := NewWithPicker(config.Config{NATSURL: "nats://example:4222", NATSUser: "u", NATSPassword: "p"}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if a.connSel != urlKey("nats://example:4222") {
			t.Fatalf("default choice = %q, want the --server URL", a.connSel)
		}
		secs := a.connSections()
		if len(secs) != 3 || secs[2].title != secCLI || len(secs[2].entries) != 1 || secs[2].entries[0].url != "nats://example:4222" {
			t.Fatalf("sections = %+v, want a COMMAND LINE section holding the --server URL", secs)
		}
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NATSURL != "nats://example:4222" || cfg.NATSUser != "u" || cfg.NATSPassword != "p" {
			t.Fatalf("pickerConfig = %+v, want the --server URL with its credentials", cfg)
		}
		renderOnce(t, a)

		// A --server URL that IS a favorite selects the favorite instead.
		a = NewWithPicker(config.Config{NATSURL: prefs.JetrisEUWS.URL}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if secs := a.connSections(); len(secs) != 2 {
			t.Fatalf("sections = %+v, want no COMMAND LINE section for a favorite URL", secs)
		}
		if e, ok := a.connEntry(a.connSel); !ok || e.fav != 0 {
			t.Fatalf("selected entry = %+v, want the demo favorite", e)
		}
	})

	t.Run("login-picker-context-flag", func(t *testing.T) {
		// --context preselects that context, adding it to the list if the
		// lister didn't discover it.
		a := NewWithPicker(config.Config{NATSContext: "mine"}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if a.connSel != ctxKey("mine") {
			t.Fatalf("default choice = %q, want context mine when --context is given", a.connSel)
		}
		if !slices.Contains(a.connContexts, "mine") {
			t.Fatalf("contexts %v should include the --context value", a.connContexts)
		}
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NATSContext != "mine" {
			t.Fatalf("pickerConfig context = %q, want mine", cfg.NATSContext)
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-selected-line", func(t *testing.T) {
		// The SELECTED line words each kind of row: a favorite by name + URL
		// (URL only when the name IS the URL), a context as "context <name>"
		// without its nats-CLI marker, the --server row as its URL.
		a := NewWithPicker(config.Config{NATSURL: "nats://example:4222"}, []string{"beta"}, "beta",
			append(prefs.DefaultFavorites(), prefs.Favorite{Label: "nats://10.0.0.7:4222", URL: "nats://10.0.0.7:4222"}))
		a.th = newTestApp().th
		a.connCtxURLs["beta"] = "nats://beta:4222"
		for key, want := range map[string][2]string{
			urlKey(prefs.JetrisEUWS.URL):   {prefs.JetrisEUWS.Label, prefs.JetrisEUWS.URL},
			urlKey("nats://10.0.0.7:4222"): {"nats://10.0.0.7:4222", ""},
			ctxKey("beta"):                 {"context beta", "nats://beta:4222"},
			urlKey("nats://example:4222"):  {"nats://example:4222", "from --server"},
		} {
			e, ok := a.connEntry(key)
			if !ok {
				t.Fatalf("no entry %q", key)
			}
			if label, detail := selectionCaption(e); label != want[0] || detail != want[1] {
				t.Fatalf("selectionCaption(%q) = %q/%q, want %q/%q", key, label, detail, want[0], want[1])
			}
			a.connSel = key
			renderOnce(t, a)
		}
		// Nothing selected still lays out (the line says so).
		a.connSel = ""
		renderOnce(t, a)
	})

	t.Run("login-update-notice", func(t *testing.T) {
		// A newer release found by the startup check lays out on the login
		// screen and turns the plate's label into "… · <new> AVAILABLE".
		a := NewWithPicker(config.Config{}, nil, "", prefs.DefaultFavorites())
		a.th = newTestApp().th
		if got := versionLabel(""); got != "VER DEV" {
			t.Fatalf("versionLabel() = %q, want VER DEV", got)
		}
		a.NotifyUpdate("v9.9.9", "https://github.com/jnmoyne/jetris/releases/tag/v9.9.9")
		if tag, url := a.update(); tag != "v9.9.9" || url == "" {
			t.Fatalf("update() = %q/%q", tag, url)
		}
		if got := versionLabel("v9.9.9"); got != "VER DEV · 9.9.9 AVAILABLE" {
			t.Fatalf("versionLabel(v9.9.9) = %q", got)
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-favorites", func(t *testing.T) {
		// Adding a favorite bookmarks + selects + persists it (label
		// defaulting to the scheme-less URL); a duplicate just selects the
		// existing row; deleting the selected one moves the selection on.
		// Starts from a single bookmark so the counts below are exact.
		a := NewWithPicker(config.Config{}, nil, "", []prefs.Favorite{prefs.JetrisEUWS})
		a.th = newTestApp().th
		var saved [][]prefs.Favorite
		a.favSave = func(f []prefs.Favorite) error { saved = append(saved, f); return nil }

		a.connAddURLEd.SetText("nats://10.0.0.7:4222")
		a.addFavorite()
		if len(a.favorites) != 2 || a.favorites[1] != (prefs.Favorite{Label: "10.0.0.7:4222", URL: "nats://10.0.0.7:4222"}) {
			t.Fatalf("favorites after add = %+v", a.favorites)
		}
		if a.connSel != urlKey("nats://10.0.0.7:4222") || a.connAddOpen || a.connAddURLEd.Text() != "" {
			t.Fatalf("after add: sel=%q addOpen=%v url=%q; want the new row selected and the form closed", a.connSel, a.connAddOpen, a.connAddURLEd.Text())
		}
		if len(saved) != 1 || len(saved[0]) != 2 {
			t.Fatalf("saved = %+v, want one save of both favorites", saved)
		}

		a.connAddLabelEd.SetText("Office")
		a.connAddURLEd.SetText("nats://office:4222")
		a.addFavorite()
		if len(a.favorites) != 3 || a.favorites[2].Label != "Office" {
			t.Fatalf("favorites after labeled add = %+v", a.favorites)
		}

		a.connSel = ctxKey("none")
		a.connAddURLEd.SetText(prefs.JetrisEUWS.URL)
		a.addFavorite()
		if len(a.favorites) != 3 || a.connSel != urlKey(prefs.JetrisEUWS.URL) || len(saved) != 2 {
			t.Fatalf("duplicate add: favorites=%d sel=%q saves=%d; want no new row, the existing one selected, no save", len(a.favorites), a.connSel, len(saved))
		}

		a.connAddURLEd.SetText("nats://bad url:4222")
		a.addFavorite()
		if len(a.favorites) != 3 || a.loginErr == "" {
			t.Fatalf("a URL with spaces was accepted (favorites=%d err=%q)", len(a.favorites), a.loginErr)
		}

		a.deleteFavorite(0) // the selected demo row
		if len(a.favorites) != 2 || a.connSel != urlKey("nats://10.0.0.7:4222") || len(saved) != 3 {
			t.Fatalf("after delete: favorites=%+v sel=%q saves=%d", a.favorites, a.connSel, len(saved))
		}
		a.deleteFavorite(1)
		a.deleteFavorite(0)
		if len(a.favorites) != 0 || a.connSel != "" {
			t.Fatalf("after deleting all: favorites=%+v sel=%q, want none", a.favorites, a.connSel)
		}
		if saved[len(saved)-1] == nil || len(saved[len(saved)-1]) != 0 {
			t.Fatalf("last save = %#v, want an empty (non-nil) list so the demo server stays deleted", saved[len(saved)-1])
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-reset", func(t *testing.T) {
		// Reset favorites: the defaults replace the list and are persisted;
		// a selection the reset removed moves to the first default, one that
		// survives it (a context) stays; the open add form is dropped; the
		// confirmation modal renders over the login screen.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha", []prefs.Favorite{{Label: "home", URL: "nats://10.0.0.7:4222"}})
		a.th = newTestApp().th
		var saved [][]prefs.Favorite
		a.favSave = func(f []prefs.Favorite) error { saved = append(saved, f); return nil }
		a.connSel = urlKey("nats://10.0.0.7:4222")
		a.connAddOpen = true
		a.connAddURLEd.SetText("nats://half-typed")
		a.connResetOpen = true
		renderOnce(t, a)

		a.resetFavorites()
		if !slices.Equal(a.favorites, prefs.DefaultFavorites()) || a.connSel != urlKey(prefs.JetrisEUWS.URL) {
			t.Fatalf("after reset: favorites=%+v sel=%q; want the defaults with the first one selected", a.favorites, a.connSel)
		}
		if len(saved) != 1 || !slices.Equal(saved[0], prefs.DefaultFavorites()) {
			t.Fatalf("saved = %+v, want one save of the defaults", saved)
		}
		if a.connAddOpen || a.connAddURLEd.Text() != "" {
			t.Fatal("the add form should be closed and cleared by a reset")
		}

		a.connSel = ctxKey("alpha")
		a.resetFavorites()
		if a.connSel != ctxKey("alpha") {
			t.Fatalf("a selected context was moved by the reset: sel=%q", a.connSel)
		}
		a.connResetOpen = false
		renderOnce(t, a)
	})

	t.Run("login-picker-undialable", func(t *testing.T) {
		// Playing the browser build, which can only dial ws/wss: other URL
		// rows are listed greyed out and never selected — not at startup
		// (--server and the first favorite alike fall through to the first
		// dialable one), not after a delete or a reset — and the add form
		// refuses them.
		orig := dialable
		dialable = func(u string) bool { return strings.HasPrefix(u, "ws://") || strings.HasPrefix(u, "wss://") }
		defer func() { dialable = orig }()

		favs := []prefs.Favorite{prefs.JetrisEU, prefs.JetrisEUWS, prefs.JetrisAP}
		a := NewWithPicker(config.Config{NATSURL: prefs.JetrisAP.URL}, []string{"alpha"}, "alpha", favs)
		a.th = newTestApp().th
		var saved [][]prefs.Favorite
		a.favSave = func(f []prefs.Favorite) error { saved = append(saved, f); return nil }
		if a.connSel != urlKey(prefs.JetrisEUWS.URL) {
			t.Fatalf("default selection = %q, want the first dialable favorite (an undialable --server and favorite skipped)", a.connSel)
		}
		secs := a.connSections()
		if e := secs[0].entries; len(e) != 3 || e[0].dialable || !e[1].dialable || e[2].dialable {
			t.Fatalf("favorites dialability = %+v, want only the ws:// row dialable", e)
		}
		if !secs[1].entries[0].dialable {
			t.Fatal("a context row must always be dialable")
		}
		renderOnce(t, a) // greyed rows render

		a.connAddURLEd.SetText("nats://10.0.0.7:4222")
		a.addFavorite()
		if len(a.favorites) != 3 || a.loginErr == "" || len(saved) != 0 {
			t.Fatalf("an undialable URL was added (favorites=%d err=%q saves=%d)", len(a.favorites), a.loginErr, len(saved))
		}

		a.deleteFavorite(1) // the selected, only dialable favorite
		if a.connSel != ctxKey("alpha") {
			t.Fatalf("after deleting the last dialable favorite: sel=%q, want the context (the greyed rows skipped)", a.connSel)
		}
		a.connContexts = nil
		a.connSel = urlKey(prefs.JetrisEU.URL) // as if a greyed row had been picked
		a.resetFavorites()
		if a.connSel != urlKey(prefs.JetrisEUWS.URL) {
			t.Fatalf("after reset: sel=%q, want the first dialable default", a.connSel)
		}
		if _, err := a.pickerConfig(); err != nil {
			t.Fatalf("pickerConfig on a dialable selection: %v", err)
		}
		a.connSel = urlKey(prefs.JetrisEU.URL)
		if _, err := a.pickerConfig(); err == nil {
			t.Fatal("pickerConfig accepted an undialable selection")
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-embedded", func(t *testing.T) {
		// The LAN-mode tab resolves to the embedded mark with the detected IP
		// and the default port, no URL or context, and its rows (IP + port
		// entry, shareable-URL line) render.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		a.connTab = connTabLAN
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.RunEmbedded || cfg.NATSURL != "" || cfg.NATSContext != "" {
			t.Fatalf("embedded pickerConfig = %+v, want RunEmbedded only", cfg)
		}
		if cfg.EmbeddedPort != config.DefaultEmbeddedPort {
			t.Fatalf("EmbeddedPort = %d, want the %d default", cfg.EmbeddedPort, config.DefaultEmbeddedPort)
		}
		if cfg.EmbeddedHost != a.lanIP {
			t.Fatalf("EmbeddedHost = %q, want the pre-filled detected IP %q", cfg.EmbeddedHost, a.lanIP)
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-embedded-ip", func(t *testing.T) {
		// The IP field overrides the auto-detected address; clearing it goes
		// back to auto-detection ("" — resolved at connect time); a URL pasted
		// into it errors instead of producing a bogus address.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		a.connTab = connTabLAN
		a.connHostEd.SetText("192.168.7.9")
		a.connPortEd.SetText("14222")
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EmbeddedHost != "192.168.7.9" {
			t.Fatalf("EmbeddedHost = %q, want the entered 192.168.7.9", cfg.EmbeddedHost)
		}
		if got := a.pickerAddr(); got != "192.168.7.9:14222" {
			t.Fatalf("advertised address = %q, want 192.168.7.9:14222", got)
		}
		renderOnce(t, a)

		a.connHostEd.SetText("")
		cfg, err = a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EmbeddedHost != "" {
			t.Fatalf("EmbeddedHost = %q, want empty (auto-detect) for an empty field", cfg.EmbeddedHost)
		}
		if got := a.pickerAddr(); got != a.lanIP+":14222" {
			t.Fatalf("advertised address = %q, want the detected %s:14222", got, a.lanIP)
		}

		// An IPv6 literal is accepted and bracketed in the address.
		a.connHostEd.SetText("fd00::1")
		if got := a.pickerAddr(); got != "[fd00::1]:14222" {
			t.Fatalf("advertised address = %q, want the bracketed IPv6 literal", got)
		}

		a.connHostEd.SetText("nats://192.168.7.9:4222")
		if _, err := a.pickerConfig(); err == nil {
			t.Fatal("pickerConfig accepted a whole URL in the IP field")
		}
	})

	t.Run("login-picker-embedded-port", func(t *testing.T) {
		// A custom port carries through; garbage in the port field errors.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha", prefs.DefaultFavorites())
		a.th = newTestApp().th
		a.connTab = connTabLAN
		a.connPortEd.SetText("14222")
		cfg, err := a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EmbeddedPort != 14222 {
			t.Fatalf("EmbeddedPort = %d, want 14222", cfg.EmbeddedPort)
		}
		a.connPortEd.SetText("99999")
		if _, err := a.pickerConfig(); err == nil {
			t.Fatal("pickerConfig accepted out-of-range port 99999")
		}
		a.connPortEd.SetText("")
		cfg, err = a.pickerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EmbeddedPort != config.DefaultEmbeddedPort {
			t.Fatalf("empty port field gave %d, want the %d default", cfg.EmbeddedPort, config.DefaultEmbeddedPort)
		}
	})

	t.Run("lobby", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.chatLog = []lobby.ChatMessage{{Name: "alice", Text: "hi"}}
		a.connName, a.connURL = "context demo", "nats://demo.nats.io:4222"
		renderOnce(t, a)
	})

	t.Run("lobby-embedded-server", func(t *testing.T) {
		// Hosting the embedded server adds the shareable YOUR SERVER line.
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.usingEmbedded = true
		a.embAddr = "192.168.1.23:4222"
		renderOnce(t, a)
	})

	t.Run("lobby-game-row-abandoned", func(t *testing.T) {
		// An abandoned game's row carries the red "· abandoned" tag and the
		// Delete button; clicking Delete swaps the action buttons for the
		// inline "Are you sure…" confirmation (confirmDeleteID set).
		a := newTestApp()
		g := lobby.GameListing{
			GameID:      "abandoned-game-1234",
			Mode:        config.ModeCooperative,
			Status:      config.GameStatusCreated,
			PlayerCount: 2,
			CreatedAt:   time.Now().Add(-20 * time.Minute),
		}
		row := func() {
			var ops op.Ops
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(800, 60)),
			}
			a.gameRow(gtx, g, true)
		}
		row()
		a.confirmDeleteID = g.GameID
		row()
	})

	t.Run("lobby-game-row-with-agents", func(t *testing.T) {
		// A competitive row with an agent policy shows the "agents k/N" info and
		// tags agent players; the create row is the wizard-opening button.
		a := newTestApp()
		g := lobby.GameListing{
			GameID:      "agent-game-1234",
			Mode:        config.ModeCompetitive,
			Status:      config.GameStatusCreated,
			PlayerCount: 3,
			MaxAgents:   2,
			Players: []lobby.PlayerSummary{
				{PlayerID: "alice", Name: "alice", Ready: true},
				{PlayerID: "hal", Name: "hal", Agent: true},
			},
			CreatedAt: time.Now(),
		}
		render := func(w func(C) D) {
			var ops op.Ops
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(800, 60)),
			}
			w(gtx)
		}
		render(func(gtx C) D { return a.gameRow(gtx, g, false) })
		render(a.createRow)
	})

	t.Run("create-wizard", func(t *testing.T) {
		// Every wizard step lays out, including the branches: teams mode's
		// per-team seat label (step 1), the agents step with the max-agents
		// editor shown (Allow agents checked), and step 3 with invite-only
		// selected (which relabels Next and drops the step count to 3).
		render := func(w func(C) D) {
			var ops op.Ops
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(1200, 820)),
			}
			w(gtx)
		}
		a := newTestApp()
		a.modeEnum.Value = "teams"
		for step := wizStepMode; step <= wizStepAgents; step++ {
			a.createWizStep = step
			render(a.createWizardOverlay)
		}
		a.allowAgentsCb.Value = true
		a.createWizStep = wizStepAgents
		render(a.createWizardOverlay)
		a.createJoinEnum.Value = "invite"
		a.createWizStep = wizStepJoin
		render(a.createWizardOverlay)
		// Step 2 is the Guideline preset's read-only list or the custom
		// editors; both carry the garbage rules for the modes that raise
		// garbage (competitive, teams) and hide them for cooperative.
		a.createWizStep = wizStepNext
		for _, rules := range []string{"guideline", "custom"} {
			a.rulesEnum.Value = rules
			for _, mode := range []string{"competitive", "cooperative"} {
				a.modeEnum.Value = mode
				render(a.createWizardOverlay)
			}
		}

		// The wizard also renders as the lobby's modal overlay.
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.createWizStep = wizStepMode
		renderOnce(t, a)
	})

	t.Run("invite-overlays", func(t *testing.T) {
		render := func(w func(C) D) {
			var ops op.Ops
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(1200, 820)),
			}
			w(gtx)
		}
		// Competitive picker: plain include toggles + capacity line.
		a := newTestApp()
		a.invitePickerGameID = "g-comp"
		a.invitePickerMode = config.ModeCompetitive
		a.invitePickerPC = 3
		a.invitePicker = map[string]*inviteChoice{
			"alice": {playerID: "alice", name: "alice"},
			"hal":   {playerID: "hal", name: "hal", agent: true},
		}
		a.invitePicker["alice"].sel.Value = true
		render(a.invitePickerOverlay)

		// Teams picker: three-way team selectors + per-team capacity, with an
		// over-subscription error shown.
		a.invitePickerMode = config.ModeTeams
		a.invitePickerPC, a.invitePickerTS = 4, 2
		a.invitePicker["alice"].team.Value = "0"
		a.invitePickerErr = "Team A is full (joined + invited)."
		render(a.invitePickerOverlay)

		// Incoming pop-up, one per mode.
		for _, inv := range []lobby.Invitation{
			{FromName: "carol", Mode: config.ModeCompetitive},
			{FromName: "carol", Mode: config.ModeCooperative},
			{FromName: "carol", Mode: config.ModeTeams, Team: 1},
		} {
			inv := inv
			render(func(gtx C) D { return a.incomingInviteOverlay(gtx, &inv) })
		}
	})

	t.Run("game-coop-player", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		renderOnce(t, a)
	})

	t.Run("game-competitive-player", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		a.countdown = 3
		renderOnce(t, a)
	})

	t.Run("game-spectator-competitive", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "spec", "", config.ModeCompetitive, engine.ModeSpectator, 0, 0, 0)
		a.gamePlayers = players
		a.screen = screenGame
		renderOnce(t, a)
	})

	t.Run("game-over", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = players
		a.screen = screenGame
		a.gameOver = true
		a.won = true
		// A won game carries a fireworks show; renderOnce's zero gtx.Now equals
		// the zero start time, so the overlay's Stack path is exercised at t=0.
		a.fireworks = newFireworksShow(time.Time{})
		renderOnce(t, a)
	})

	t.Run("game-with-chat", func(t *testing.T) {
		// The in-game chat panel shows this game's messages plus lobby lines;
		// other games' messages are filtered out.
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		a.chatLog = []lobby.ChatMessage{
			{Name: "carol", Text: "hi from the lobby"},                     // GameID "" → shown as @lobby
			{Name: "bob", Text: "good luck", GameID: "g1"},                 // this game
			{Name: "dave", Text: "you can't see me", GameID: "other-game"}, // filtered out
			{Name: "eve", Text: "watching", GameID: "g1", Spectator: true}, // (spec) marker
		}
		renderOnce(t, a)
	})
}

// TestFireworksOverlay pins the victory-fireworks building blocks: the logo
// sampling yields a real particle set from the embedded icon, the show loops
// (active forever once started, drawing wraps modulo the cycle), and drawing
// panics at no point of the show (rise, logo pop-in, hold, scatter, fade-out,
// and a wrapped second cycle all covered by the sampled times).
func TestFireworksOverlay(t *testing.T) {
	pts := fwLogoPoints()
	if len(pts) < 50 {
		t.Fatalf("logo sampling yielded %d particles, want >= 50", len(pts))
	}
	colors := map[colorN]bool{}
	for _, p := range pts {
		colors[p.col] = true
	}
	if len(colors) < 3 {
		t.Fatalf("logo particles use %d colors, want >= 3 (quadrants + white N)", len(colors))
	}
	syn := fwSynadiaPoints()
	if len(syn) < 50 {
		t.Fatalf("synadia sampling yielded %d particles, want >= 50", len(syn))
	}
	synColors := map[colorN]bool{}
	for _, p := range syn {
		synColors[p.col] = true
	}
	if len(synColors) < 2 {
		t.Fatalf("synadia particles use %d colors, want >= 2 (emerald square + white S)", len(synColors))
	}

	// Every show must roll at least one Synadia rocket — the choreography
	// loops until dropped, so a show without one would never display it.
	for i := 0; i < 25; i++ {
		show := newFireworksShow(time.Unix(1_000_000, int64(i)))
		n := 0
		for _, r := range show.rockets {
			if r.synadia {
				n++
			}
		}
		if n == 0 {
			t.Fatal("show rolled zero Synadia rockets; want at least one per show")
		}
	}

	start := time.Unix(1_000_000, 0)
	fw := newFireworksShow(start)
	// Force one rocket of each logo kind so both burst draw paths are
	// exercised regardless of what the show's RNG rolled.
	fw.rockets[0].synadia = false
	fw.rockets[1].synadia = true
	if !fw.active(start) {
		t.Fatal("show should be active at its start")
	}
	if !fw.active(start.Add(fw.cycle + time.Hour)) {
		t.Fatal("show should loop forever until the App drops it")
	}
	if fw.active(start.Add(-time.Second)) {
		t.Fatal("show should not be active before its start")
	}

	for _, dt := range []time.Duration{
		0,
		300 * time.Millisecond, // rockets rising
		1500 * time.Millisecond,
		3 * time.Second, // logo bursts in flight
		5 * time.Second,
		fw.cycle - time.Millisecond,        // tail end of the first cycle
		fw.cycle + 1500*time.Millisecond,   // wrapped into the second cycle
		10*fw.cycle + 300*time.Millisecond, // deep into the loop
	} {
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
			Now:         start.Add(dt),
		}
		if d := fireworksOverlay(gtx, fw); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("overlay at +%v produced zero dimensions", dt)
		}
	}
}

// TestChatLine pins the in-game chat formatting: lobby messages get the @lobby
// prefix and their own color; spectators are marked.
func TestChatLine(t *testing.T) {
	if txt, col := chatLine(lobby.ChatMessage{Name: "carol", Text: "hey"}); txt != "@lobby carol: hey" || col != colLobby {
		t.Fatalf("lobby line = %q (col %v)", txt, col)
	}
	if txt, col := chatLine(lobby.ChatMessage{Name: "bob", Text: "gl", GameID: "g1"}); txt != "bob: gl" || col != colFg {
		t.Fatalf("game line = %q (col %v)", txt, col)
	}
	if txt, _ := chatLine(lobby.ChatMessage{Name: "eve", Text: "hi", GameID: "g1", Spectator: true}); txt != "eve (spec): hi" {
		t.Fatalf("spectator line = %q", txt)
	}
}
