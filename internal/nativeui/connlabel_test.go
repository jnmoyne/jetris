package nativeui

import (
	"context"
	"strings"
	"testing"

	"jetris/internal/config"
	"jetris/internal/lobby"
	"jetris/internal/testutil"
)

// TestConnectionLabel pins the lobby header's "which server am I on" text:
// the picker choice (context / URL / embedded) plus the URL actually reached,
// never with credentials.
func TestConnectionLabel(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.Config
		connected string
		favorite  string
		want      string
	}{
		{
			name:      "context shows its name and the server reached",
			cfg:       config.Config{NATSContext: "ngs"},
			connected: "nats://connect.ngs.global:4222",
			want:      "context ngs (nats://connect.ngs.global:4222)",
		},
		{
			name:      "plain URL shows the server reached",
			cfg:       config.Config{NATSURL: "nats://demo.nats.io:4222"},
			connected: "nats://demo.nats.io:4222",
			want:      "nats://demo.nats.io:4222",
		},
		{
			name:      "clustered URL reports the node actually connected to",
			cfg:       config.Config{NATSURL: "nats://n1:4222,nats://n2:4222"},
			connected: "nats://n2:4222",
			want:      "nats://n2:4222",
		},
		{
			name:      "LAN mode names the embedded server, its address in parentheses",
			cfg:       config.Config{RunEmbedded: true, NATSURL: "nats://192.168.1.23:4222"},
			connected: "nats://192.168.1.23:4222",
			want:      config.DefaultEmbeddedName + " (nats://192.168.1.23:4222)",
		},
		{
			name:      "credentials in the URL are dropped",
			cfg:       config.Config{NATSURL: "nats://alice:s3cret@host:4222"},
			connected: "nats://alice:s3cret@host:4222",
			want:      "nats://host:4222",
		},
		{
			name:      "no connected URL falls back to the configured one",
			cfg:       config.Config{NATSContext: "local", NATSURL: "nats://127.0.0.1:4222"},
			connected: "",
			want:      "context local (nats://127.0.0.1:4222)",
		},
		{
			name: "context with nothing else known is just the context",
			cfg:  config.Config{NATSContext: "local"},
			want: "context local",
		},
		{
			name:      "a favorite's name comes first, the server in parentheses",
			cfg:       config.Config{NATSURL: "nats://172.105.76.148:4222"},
			connected: "nats://172.105.76.148:4222",
			favorite:  "Jetris (EU central)",
			want:      "Jetris (EU central) (nats://172.105.76.148:4222)",
		},
		{
			name:     "a favorite with no URL known is just its name",
			favorite: "Jetris (EU central)",
			want:     "Jetris (EU central)",
		},
		{
			name:      "LAN mode ignores any browser favorite",
			cfg:       config.Config{RunEmbedded: true, NATSURL: "nats://192.168.1.23:4222"},
			connected: "nats://192.168.1.23:4222",
			favorite:  "demo",
			want:      config.DefaultEmbeddedName + " (nats://192.168.1.23:4222)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectionLabel(tc.cfg, tc.connected, tc.favorite); got != tc.want {
				t.Fatalf("connectionLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConnectAndLoginSetsLabel drives the real login path (connect, provision,
// lobby) against a plain URL and then against the embedded LAN-mode server,
// checking the lobby header's server label comes out of each — and is cleared
// by Quit, so a later login never shows the previous server.
func TestConnectAndLoginSetsLabel(t *testing.T) {
	t.Chdir(t.TempDir()) // the embedded server stores JetStream data under the cwd
	url, _ := testutil.StartServer(t)

	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx = ctx
	t.Cleanup(func() {
		a.quit()
		cancel()
		if a.embSrv != nil {
			a.embSrv.Shutdown()
		}
	})

	a.doConnectAndLogin("tester", config.Config{NATSURL: url}, "")
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("URL login: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	// A plain URL is its own name: there is nothing to put in parentheses
	// that the name does not already say.
	if a.connName != url || a.connURL != "" {
		t.Fatalf("URL login = name %q url %q, want name %q and no url", a.connName, a.connURL, url)
	}

	a.quit()
	if a.connName != "" || a.connURL != "" {
		t.Fatalf("after quit = name %q url %q, want both cleared", a.connName, a.connURL)
	}

	// Free ports for all three listeners: a Jetris running on this machine
	// holds the defaults.
	a.doConnectAndLogin("tester", config.Config{RunEmbedded: true, EmbeddedHost: "127.0.0.1", EmbeddedPort: freePort(t), EmbeddedWSPort: freePort(t), EmbeddedHTTPPort: freePort(t)}, "")
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("LAN login: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	if a.connName != config.DefaultEmbeddedName || !strings.HasPrefix(a.connURL, "nats://127.0.0.1:") || !a.usingEmbedded {
		t.Fatalf("LAN login = name %q url %q (embedded %v), want the LAN-mode parts", a.connName, a.connURL, a.usingEmbedded)
	}
}

// TestConnectionParts pins the split the screens rely on: the server's name
// and its URL come back apart, so the lobby header can put the name first and
// let the URL be the part a narrow window cuts, and the game HUD can show the
// name on its own.
func TestConnectionParts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       config.Config
		connected string
		favorite  string
		wantName  string
		wantURL   string
	}{
		{
			name:      "a favorite keeps its name, the server reached in the url",
			cfg:       config.Config{NATSURL: "nats://172.105.76.148:4222"},
			connected: "nats://172.105.76.148:4222",
			favorite:  "Jetris (EU central)",
			wantName:  "Jetris (EU central)",
			wantURL:   "nats://172.105.76.148:4222",
		},
		{
			name:      "a context is named by its context",
			cfg:       config.Config{NATSContext: "ngs"},
			connected: "nats://connect.ngs.global:4222",
			wantName:  "context ngs",
			wantURL:   "nats://connect.ngs.global:4222",
		},
		{
			name:      "LAN mode names the embedded server",
			cfg:       config.Config{RunEmbedded: true},
			connected: "nats://192.168.1.23:4222",
			wantName:  config.DefaultEmbeddedName,
			wantURL:   "nats://192.168.1.23:4222",
		},
		{
			name:      "a plain URL is its own name and adds no parentheses",
			cfg:       config.Config{NATSURL: "nats://demo.nats.io:4222"},
			connected: "nats://demo.nats.io:4222",
			wantName:  "nats://demo.nats.io:4222",
		},
		{
			name:      "credentials never reach either part",
			cfg:       config.Config{NATSURL: "nats://alice:s3cret@host:4222"},
			connected: "nats://alice:s3cret@host:4222",
			favorite:  "home",
			wantName:  "home",
			wantURL:   "nats://host:4222",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, u := connectionParts(tc.cfg, tc.connected, tc.favorite)
			if name != tc.wantName || u != tc.wantURL {
				t.Fatalf("connectionParts() = %q, %q, want %q, %q", name, u, tc.wantName, tc.wantURL)
			}
		})
	}
}

// TestSessionLine pins the game HUD's "you are here" line: nothing at all
// when there is no connection to name, one line where the name and the
// server fit the column, and the name over the server where they don't —
// never one truncated line, because half a server name identifies nothing.
func TestSessionLine(t *testing.T) {
	a := newTestApp()
	a.lobby = lobby.New(nil, nil, "tester", "tester")

	if d := a.sessionLine(looseCtx(240, 60)); d.Size.Y != 0 {
		t.Fatalf("no connection: line is %v, want nothing", d.Size)
	}

	a.connName, a.connURL = "Jetris EU central", "wss://eu-central.example.com:4223"
	one := a.sessionLine(looseCtx(400, 60))
	if one.Size.Y == 0 {
		t.Fatal("connected: no line at all")
	}
	// A column too narrow for "tester @ Jetris EU central" on one line.
	two := a.sessionLine(looseCtx(90, 60))
	if two.Size.Y <= one.Size.Y {
		t.Fatalf("narrow column: line is %v, want it taller than the one-line %v", two.Size, one.Size)
	}
	// The URL is the lobby header's business; the HUD names the server only,
	// so a very long URL never changes this line.
	a.connURL = "wss://a-very-long-hostname-that-would-never-fit.example.com:4223"
	if d := a.sessionLine(looseCtx(400, 60)); d.Size != one.Size {
		t.Fatalf("with a long URL the line is %v, want the %v it had without", d.Size, one.Size)
	}
}
