package nativeui

import (
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
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
		a := NewWithPicker(config.Config{}, []string{"alpha", "beta"}, "beta")
		a.th = newTestApp().th
		if a.connEnum.Value != "context" || a.connCtx != "beta" {
			t.Fatalf("default choice = %q/%q, want context/beta (the selected context)", a.connEnum.Value, a.connCtx)
		}
		renderOnce(t, a)
		// The constructor's SetText on the URL editor queues a synthetic
		// ChangeEvent; the first frame must swallow it rather than let it
		// flip the choice to the URL option.
		if a.connEnum.Value != "context" {
			t.Fatalf("choice after first frame = %q, want context (SetText must not pick the URL option)", a.connEnum.Value)
		}
		// Render again with the context pull-down expanded.
		a.connDropOpen = true
		renderOnce(t, a)
	})

	t.Run("login-picker-no-contexts", func(t *testing.T) {
		a := NewWithPicker(config.Config{}, nil, "")
		a.th = newTestApp().th
		if a.connEnum.Value != "url" {
			t.Fatalf("default choice = %q, want url when no contexts exist", a.connEnum.Value)
		}
		if a.connURLEd.Text() != DefaultNATSURL {
			t.Fatalf("URL field = %q, want %q", a.connURLEd.Text(), DefaultNATSURL)
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-server-flag", func(t *testing.T) {
		// --server seeds the URL field and makes the URL option the default,
		// beating the CLI's selected context.
		a := NewWithPicker(config.Config{NATSURL: "nats://example:4222"}, []string{"alpha"}, "alpha")
		a.th = newTestApp().th
		if a.connEnum.Value != "url" {
			t.Fatalf("default choice = %q, want url when --server is given", a.connEnum.Value)
		}
		if a.connURLEd.Text() != "nats://example:4222" {
			t.Fatalf("URL field = %q, want the --server value", a.connURLEd.Text())
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-context-flag", func(t *testing.T) {
		// --context preselects that context, adding it to the list if the
		// lister didn't discover it.
		a := NewWithPicker(config.Config{NATSContext: "mine"}, []string{"alpha"}, "alpha")
		a.th = newTestApp().th
		if a.connEnum.Value != "context" || a.connCtx != "mine" {
			t.Fatalf("default choice = %q/%q, want context/mine when --context is given", a.connEnum.Value, a.connCtx)
		}
		found := false
		for _, c := range a.connContexts {
			if c == "mine" {
				found = true
			}
		}
		if !found {
			t.Fatalf("contexts %v should include the --context value", a.connContexts)
		}
		renderOnce(t, a)
	})

	t.Run("login-picker-embedded", func(t *testing.T) {
		// The "LAN mode (embedded NATS server)" option resolves to the
		// embedded mark with the default port, no URL or context, and its
		// rows (port entry + shareable-URL line) render.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha")
		a.th = newTestApp().th
		a.connEnum.Value = "embedded"
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
		renderOnce(t, a)
	})

	t.Run("login-picker-embedded-port", func(t *testing.T) {
		// A custom port carries through; garbage in the port field errors.
		a := NewWithPicker(config.Config{}, []string{"alpha"}, "alpha")
		a.th = newTestApp().th
		a.connEnum.Value = "embedded"
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
		// tags agent players; the create row with the Allow-agents checkbox on
		// renders the max-agents editor.
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
		a.modeEnum.Value = "competitive"
		a.allowAgentsCb.Value = true
		render(a.createRow)
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
