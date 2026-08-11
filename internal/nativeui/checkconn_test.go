package nativeui

// The login screen's connection check must really connect and really measure a
// core NATS ping — for the LAN-mode option too, where "checking" means bringing
// the embedded server up and dialing it, not just printing its address.

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"jetris/internal/config"
	"jetris/internal/testutil"
)

func TestCheckConnURL(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := newTestApp()

	msg, ok := a.checkConn(config.Config{NATSURL: url})
	if !ok {
		t.Fatalf("check failed: %s", msg)
	}
	if !strings.HasPrefix(msg, "✓ ") || !strings.Contains(msg, "Core NATS ping") {
		t.Fatalf("msg = %q, want a ✓ line reporting the core NATS ping", msg)
	}
	if strings.Contains(msg, "—") {
		t.Fatalf("msg = %q, want a measured ping, not the unset placeholder", msg)
	}
}

func TestCheckConnURLUnreachable(t *testing.T) {
	a := newTestApp()

	msg, ok := a.checkConn(config.Config{NATSURL: "nats://127.0.0.1:1"})
	if ok {
		t.Fatalf("unroutable URL reported as reachable: %s", msg)
	}
	if !strings.HasPrefix(msg, "✗ ") {
		t.Fatalf("msg = %q, want a ✗ error line", msg)
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
	msg, ok := a.checkConn(config.Config{RunEmbedded: true, EmbeddedPort: port})
	if !ok {
		t.Fatalf("embedded check failed: %s", msg)
	}
	if a.embSrv == nil {
		t.Fatal("embedded check did not start the server")
	}
	if !strings.Contains(msg, "Core NATS ping") || !strings.Contains(msg, a.embAddr) {
		t.Fatalf("msg = %q, want the ping and the served address", msg)
	}
	if strings.Contains(msg, "—") {
		t.Fatalf("msg = %q, want a measured ping, not the unset placeholder", msg)
	}

	// Checking again reuses the running server rather than failing on the
	// port it already holds.
	srv := a.embSrv
	if msg, ok := a.checkConn(config.Config{RunEmbedded: true, EmbeddedPort: port}); !ok {
		t.Fatalf("second embedded check failed: %s", msg)
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
	msg, ok := a.checkConn(cfg)
	if !ok {
		t.Fatalf("embedded check with an overridden IP failed: %s", msg)
	}
	want := "127.0.0.1:" + strconv.Itoa(port)
	if a.embAddr != want {
		t.Fatalf("shareable address = %q, want the overridden %q", a.embAddr, want)
	}
	if !strings.Contains(msg, want) {
		t.Fatalf("msg = %q, want the overridden address %q", msg, want)
	}

	// An address that does not reach this machine fails the check rather than
	// being reported as serving.
	if msg, ok := a.checkConn(config.Config{RunEmbedded: true, EmbeddedHost: "192.0.2.1", EmbeddedPort: port}); ok {
		t.Fatalf("unreachable overridden IP reported as serving: %s", msg)
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
