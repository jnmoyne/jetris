package nativeui

import (
	"context"
	"testing"

	"jetris/internal/config"
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
		want      string
	}{
		{
			name:      "context shows its name and the server reached",
			cfg:       config.Config{NATSContext: "ngs"},
			connected: "nats://connect.ngs.global:4222",
			want:      "context ngs · nats://connect.ngs.global:4222",
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
			// The lobby's YOUR SERVER'S URL line shows the address; the
			// header doesn't repeat it.
			name:      "LAN mode names the embedded server without its address",
			cfg:       config.Config{RunEmbedded: true, NATSURL: "nats://192.168.1.23:4222"},
			connected: "nats://192.168.1.23:4222",
			want:      "LAN mode (your embedded server)",
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
			want:      "context local · nats://127.0.0.1:4222",
		},
		{
			name: "context with nothing else known is just the context",
			cfg:  config.Config{NATSContext: "local"},
			want: "context local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectionLabel(tc.cfg, tc.connected); got != tc.want {
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

	a.doConnectAndLogin("tester", config.Config{NATSURL: url})
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("URL login: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	if a.connLabel != url {
		t.Fatalf("URL login label = %q, want %q", a.connLabel, url)
	}

	a.quit()
	if a.connLabel != "" {
		t.Fatalf("label after quit = %q, want cleared", a.connLabel)
	}

	a.doConnectAndLogin("tester", config.Config{RunEmbedded: true, EmbeddedHost: "127.0.0.1", EmbeddedPort: freePort(t)})
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("LAN login: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	if a.connLabel != "LAN mode (your embedded server)" || !a.usingEmbedded {
		t.Fatalf("LAN login label = %q (embedded %v), want the LAN-mode label", a.connLabel, a.usingEmbedded)
	}
}
