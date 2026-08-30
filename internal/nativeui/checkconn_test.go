package nativeui

// The login screen's server probe (a browser row's ↻, LAN mode's Check
// embedded server) must really connect, really measure a core NATS ping, and
// count the lobby's players — for LAN mode too, where "checking" means
// bringing the embedded server up and dialing it, not just printing its
// address.

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestCheckConnURL(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := newTestApp()

	res := a.checkConn(config.Config{NATSURL: url})
	if !res.ok {
		t.Fatalf("check failed: %s", res.msg)
	}
	if !strings.HasPrefix(res.msg, "✓ ") || !strings.Contains(res.msg, "Core NATS ping") {
		t.Fatalf("msg = %q, want a ✓ line reporting the core NATS ping", res.msg)
	}
	if strings.Contains(res.msg, "—") || res.rtt <= 0 {
		t.Fatalf("msg = %q (rtt %v), want a measured ping, not the unset placeholder", res.msg, res.rtt)
	}
	// A server nobody has played on yet: no lobby bucket, said so.
	if res.lobby || res.players != 0 || !strings.Contains(res.msg, "no lobby yet") {
		t.Fatalf("fresh server probe = %+v, want no lobby yet", res)
	}

	// With a lobby bucket holding two presence entries the probe counts them
	// — and the inline row summary shows ping + head count.
	ctx := context.Background()
	nc, _, kv, err := natspkg.Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	// A human, an agent (its presence says so) and a game listing: the probe
	// counts the players and the agents apart.
	for key, val := range map[string]string{"players.alice": "{}", "players.hal": `{"agent":true}`, "games.g1": "{}"} {
		if _, err := kv.Put(ctx, key, []byte(val)); err != nil {
			t.Fatal(err)
		}
	}
	res = a.checkConn(config.Config{NATSURL: url})
	if !res.ok || !res.lobby || res.players != 1 || res.agents != 1 || !strings.Contains(res.msg, "1 player · 1 agent online") {
		t.Fatalf("probe with a player and an agent = %+v, want 1 player · 1 agent online", res)
	}
	if txt, col := probeSummary(res, false); !strings.HasSuffix(txt, " · 1 player · 1 agent") || col != colGo {
		t.Fatalf("row summary = %q (%v), want '<ping> · 1 player · 1 agent' in green", txt, col)
	}
}

func TestCheckConnURLUnreachable(t *testing.T) {
	a := newTestApp()

	res := a.checkConn(config.Config{NATSURL: "nats://127.0.0.1:1"})
	if res.ok {
		t.Fatalf("unroutable URL reported as reachable: %s", res.msg)
	}
	if !strings.HasPrefix(res.msg, "✗ ") {
		t.Fatalf("msg = %q, want a ✗ error line", res.msg)
	}
	if txt, col := probeSummary(res, false); txt != "OFFLINE" || col != colErr {
		t.Fatalf("row summary = %q (%v), want a red OFFLINE", txt, col)
	}
	if txt, _ := probeSummary(probeResult{}, true); txt != "refreshing…" {
		t.Fatalf("row summary while probing = %q, want refreshing…", txt)
	}
	if txt, _ := probeSummary(probeResult{}, false); txt != "" {
		t.Fatalf("row summary before any probe = %q, want empty", txt)
	}
}

// LAN mode: the check starts the embedded server, connects to it over the same
// LAN address other players dial, and pings it.
func TestCheckConnEmbedded(t *testing.T) {
	t.Chdir(t.TempDir()) // the server stores JetStream data under the cwd
	a := newTestApp()
	t.Cleanup(func() {
		if a.embSrv != nil {
			a.embSrv.Shutdown()
		}
	})

	port := freePort(t)
	res := a.checkConn(config.Config{RunEmbedded: true, EmbeddedPort: port})
	if !res.ok {
		t.Fatalf("embedded check failed: %s", res.msg)
	}
	if a.embSrv == nil {
		t.Fatal("embedded check did not start the server")
	}
	if !strings.Contains(res.msg, "Core NATS ping") || !strings.Contains(res.msg, a.embAddr) {
		t.Fatalf("msg = %q, want the ping and the served address", res.msg)
	}
	if strings.Contains(res.msg, "—") {
		t.Fatalf("msg = %q, want a measured ping, not the unset placeholder", res.msg)
	}

	// Checking again reuses the running server rather than failing on the
	// port it already holds.
	srv := a.embSrv
	if res := a.checkConn(config.Config{RunEmbedded: true, EmbeddedPort: port}); !res.ok {
		t.Fatalf("second embedded check failed: %s", res.msg)
	}
	if a.embSrv != srv {
		t.Fatal("second check restarted the embedded server")
	}
}

// An overridden IP is what the check advertises and dials — the server listens
// on every interface, so the same running server answers on it.
func TestCheckConnEmbeddedHostOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	a := newTestApp()
	t.Cleanup(func() {
		if a.embSrv != nil {
			a.embSrv.Shutdown()
		}
	})

	port := freePort(t)
	cfg := config.Config{RunEmbedded: true, EmbeddedHost: "127.0.0.1", EmbeddedPort: port}
	res := a.checkConn(cfg)
	if !res.ok {
		t.Fatalf("embedded check with an overridden IP failed: %s", res.msg)
	}
	want := "127.0.0.1:" + strconv.Itoa(port)
	if a.embAddr != want {
		t.Fatalf("shareable address = %q, want the overridden %q", a.embAddr, want)
	}
	if !strings.Contains(res.msg, want) {
		t.Fatalf("msg = %q, want the overridden address %q", res.msg, want)
	}

	// An address that does not reach this machine fails the check rather than
	// being reported as serving.
	if res := a.checkConn(config.Config{RunEmbedded: true, EmbeddedHost: "192.0.2.1", EmbeddedPort: port}); res.ok {
		t.Fatalf("unreachable overridden IP reported as serving: %s", res.msg)
	}
}

// freePort returns a TCP port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
