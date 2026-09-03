package nativeui

// The login screen's server probe (a browser row's ↻, LAN mode's Check
// embedded server) must really connect, really measure a core NATS ping, and
// count the lobby's players — for LAN mode too, where "checking" means
// bringing the embedded server up and dialing it, not just printing its
// address.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
	"jetris/internal/webdist"
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

// lanCleanup shuts the test app's LAN party servers down when the test ends.
func lanCleanup(t *testing.T, a *App) {
	t.Cleanup(func() {
		if a.embSrv != nil {
			a.embSrv.Shutdown()
		}
		if a.webSrv != nil {
			a.webSrv.Close()
		}
	})
}

// lanConfig is a LAN party on three free ports, advertised on loopback so
// the test never depends on the machine's network.
func lanConfig(t *testing.T) config.Config {
	t.Helper()
	// The page's certificate is kept with the preferences: a temporary home
	// for it, never the developer's own.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return config.Config{RunEmbedded: true, EmbeddedHost: "127.0.0.1", EmbeddedPort: freePort(t), EmbeddedWSPort: freePort(t), EmbeddedHTTPPort: freePort(t)}
}

// LAN mode: the check starts the embedded server and the page server,
// connects to the former over the same LAN address other players dial and
// pings it — and dials the WebSocket listener and the page as well, since
// the phones will.
func TestCheckConnEmbedded(t *testing.T) {
	t.Chdir(t.TempDir()) // the server stores JetStream data under the cwd
	a := newTestApp()
	lanCleanup(t, a)

	cfg := lanConfig(t)
	res := a.checkConn(cfg)
	if !res.ok {
		t.Fatalf("embedded check failed: %s", res.msg)
	}
	if a.embSrv == nil || a.webSrv == nil {
		t.Fatal("embedded check did not start both servers")
	}
	if !strings.Contains(res.msg, "Core NATS ping") || !strings.Contains(res.msg, a.embAddr) {
		t.Fatalf("msg = %q, want the ping and the served address", res.msg)
	}
	wantWS, wantHTTP := "127.0.0.1:"+strconv.Itoa(cfg.EmbeddedWSPort), "127.0.0.1:"+strconv.Itoa(cfg.EmbeddedHTTPPort)
	if a.embWSAddr != wantWS || a.embHTTPAddr != wantHTTP {
		t.Fatalf("addresses = ws %q http %q, want %q and %q", a.embWSAddr, a.embHTTPAddr, wantWS, wantHTTP)
	}
	if !strings.Contains(res.msg, "ws://"+wantWS) || !strings.Contains(res.msg, "https://"+wantHTTP) {
		t.Fatalf("msg = %q, want the WebSocket and the https page addresses", res.msg)
	}
	if a.embHTTPScheme != "https" {
		t.Fatalf("page scheme = %q, want https (the page has its certificate)", a.embHTTPScheme)
	}
	if strings.Contains(res.msg, "—") {
		t.Fatalf("msg = %q, want a measured ping, not the unset placeholder", res.msg)
	}

	// The WebSocket listener is this server's, and nats.go dials it as such.
	nc, err := nats.Connect("ws://" + wantWS)
	if err != nil {
		t.Fatalf("websocket connect: %v", err)
	}
	if nc.ConnectedServerId() != a.embSrv.ID() {
		t.Fatal("the websocket listener is not the embedded server's")
	}
	nc.Close()

	// The page answers: with the browser build embedded, / sends the phone to
	// the join page with this party's WebSocket address and the server's
	// name in the link (the default: the config named none); without the
	// build, a page saying what to build.
	// The page is https with a self-signed certificate; its WebSocket is
	// proxied on the page's own port, so the link's server is the page's
	// address as wss://.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Get("https://" + wantHTTP + "/")
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if webdist.Ready() {
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "https://"+wantHTTP+"/join.html?") || !strings.Contains(loc, "wss%3A%2F%2F"+strings.ReplaceAll(wantHTTP, ":", "%3A")) || !strings.Contains(loc, "name=Jetris+LAN+Party+nats+server") {
			t.Fatalf("GET / = %d %q, want a redirect to this party's join page over wss on the page's port", resp.StatusCode, loc)
		}
		// A phone that typed http:// is sent to the https page.
		plain, err := client.Get("http://" + wantHTTP + "/join.html?x=1")
		if err != nil {
			t.Fatalf("plain http on the https port: %v", err)
		}
		plain.Body.Close()
		if plain.StatusCode != http.StatusFound || plain.Header.Get("Location") != "https://"+wantHTTP+"/join.html?x=1" {
			t.Fatalf("plain http on the https port = %d %q, want a redirect to the https page", plain.StatusCode, plain.Header.Get("Location"))
		}
		// The browser build's socket: nats.go speaks wss:// natively, through
		// the page's proxy to the embedded server.
		wnc, err := nats.Connect("wss://"+wantHTTP, nats.Secure(&tls.Config{InsecureSkipVerify: true}))
		if err != nil {
			t.Fatalf("wss through the page: %v", err)
		}
		if wnc.ConnectedServerId() != a.embSrv.ID() {
			t.Fatal("the page's WebSocket proxy does not reach the embedded server")
		}
		wnc.Close()
		// A name the host gave the server is what the link carries.
		cfg.EmbeddedName = "Basement party"
		if res := a.checkConn(cfg); !res.ok {
			t.Fatalf("named check failed: %s", res.msg)
		}
		resp, err = client.Get("https://" + wantHTTP + "/")
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); !strings.Contains(loc, "name=Basement+party") {
			t.Fatalf("GET / after naming the server = %q, want the name in the link", loc)
		}
		cfg.EmbeddedName = ""
	} else if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "build-wasm.sh") {
		t.Fatalf("GET / without a browser build = %d %q", resp.StatusCode, body)
	}

	// Checking again reuses the running servers rather than failing on the
	// ports they already hold.
	srv, web := a.embSrv, a.webSrv
	if res := a.checkConn(cfg); !res.ok {
		t.Fatalf("second embedded check failed: %s", res.msg)
	}
	if a.embSrv != srv || a.webSrv != web {
		t.Fatal("second check restarted a server")
	}

	// A new HTTP port moves the page server and leaves the NATS server — and
	// whoever is connected to it — alone; new NATS ports move that one.
	cfg.EmbeddedHTTPPort = freePort(t)
	if res := a.checkConn(cfg); !res.ok {
		t.Fatalf("check on a new HTTP port failed: %s", res.msg)
	}
	if a.embSrv != srv || a.webSrv == web || a.webSrv.Port() != cfg.EmbeddedHTTPPort {
		t.Fatalf("a new HTTP port: nats server same %v, web server same %v, web port %d want %d", a.embSrv == srv, a.webSrv == web, a.webSrv.Port(), cfg.EmbeddedHTTPPort)
	}
	// A new WebSocket port moves the NATS server — and the page with it: the
	// page proxies the browser build's socket to that listener, so a new
	// page points at the new one, on the same HTTP port.
	web = a.webSrv
	cfg.EmbeddedWSPort = freePort(t)
	if res := a.checkConn(cfg); !res.ok {
		t.Fatalf("check on a new WebSocket port failed: %s", res.msg)
	}
	if a.embSrv == srv || a.webSrv == web || a.webSrv.Port() != cfg.EmbeddedHTTPPort || a.embWSAddr != "127.0.0.1:"+strconv.Itoa(cfg.EmbeddedWSPort) {
		t.Fatalf("a new WebSocket port: nats server same %v, web server same %v, web port %d, ws addr %q", a.embSrv == srv, a.webSrv == web, a.webSrv.Port(), a.embWSAddr)
	}
	wnc, err := nats.Connect("wss://"+a.embHTTPAddr, nats.Secure(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("wss through the new page: %v", err)
	}
	if wnc.ConnectedServerId() != a.embSrv.ID() {
		t.Fatal("the new page's proxy does not reach the moved server")
	}
	wnc.Close()
}

// A port something else holds fails the check at once, naming the port and
// the listener that wanted it — and the servers are not started at all.
func TestCheckConnEmbeddedPortInUse(t *testing.T) {
	t.Chdir(t.TempDir())
	a := newTestApp()
	lanCleanup(t, a)

	cfg := lanConfig(t)
	taken, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(cfg.EmbeddedWSPort))
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	res := a.checkConn(cfg)
	if res.ok || !strings.Contains(res.msg, "port "+strconv.Itoa(cfg.EmbeddedWSPort)+" (WebSocket) is already in use") {
		t.Fatalf("check with the WebSocket port taken = %+v, want ✗ naming the port", res)
	}
	if a.embSrv != nil || a.webSrv != nil {
		t.Fatal("a server was started despite the taken port")
	}

	// Two listeners on one port is refused before anything binds.
	cfg.EmbeddedWSPort = cfg.EmbeddedHTTPPort
	if res := a.checkConn(cfg); res.ok || !strings.Contains(res.msg, "must all differ") {
		t.Fatalf("check with two listeners on one port = %+v, want ✗ must all differ", res)
	}
	if a.embSrv != nil || a.webSrv != nil {
		t.Fatal("a server was started despite the clashing ports")
	}
}

// An overridden IP is what the check advertises and dials — the server listens
// on every interface, so the same running server answers on it.
func TestCheckConnEmbeddedHostOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	a := newTestApp()
	lanCleanup(t, a)

	cfg := lanConfig(t)
	res := a.checkConn(cfg)
	if !res.ok {
		t.Fatalf("embedded check with an overridden IP failed: %s", res.msg)
	}
	want := "127.0.0.1:" + strconv.Itoa(cfg.EmbeddedPort)
	if a.embAddr != want {
		t.Fatalf("shareable address = %q, want the overridden %q", a.embAddr, want)
	}
	if !strings.Contains(res.msg, want) {
		t.Fatalf("msg = %q, want the overridden address %q", res.msg, want)
	}

	// An address that does not reach this machine fails the check rather than
	// being reported as serving.
	cfg.EmbeddedHost = "192.0.2.1"
	if res := a.checkConn(cfg); res.ok {
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
