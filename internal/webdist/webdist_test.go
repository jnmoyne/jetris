//go:build !js

package webdist

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nats-io/nats.go"

	natspkg "jetris/internal/nats"
)

// A build tree with the four files in it.
func fakeBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":   {Data: []byte("<title>landing</title>")},
		"join.html":    {Data: []byte("<title>join</title>")},
		"jetris.wasm":  {Data: []byte{0, 'a', 's', 'm'}},
		"wasm_exec.js": {Data: []byte("// shim")},
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Result()
}

// A bare GET / goes to the join link; with a query string it is the game page;
// /index.html is the landing page itself, never bounced; the module is served
// as wasm.
func TestHandler(t *testing.T) {
	h := Handler(fakeBuild(), func() string { return "http://192.168.1.20:8080/join.html?server=ws%3A%2F%2F192.168.1.20%3A4223" })

	res := get(t, h, "/")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), "http://192.168.1.20:8080/join.html?") {
		t.Fatalf("GET / = %d %q, want a redirect to the join link", res.StatusCode, res.Header.Get("Location"))
	}
	for _, path := range []string{"/?server=ws%3A%2F%2F192.168.1.20%3A4223&player=alice", "/index.html"} {
		res = get(t, h, path)
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "landing") {
			t.Fatalf("GET %s = %d %q, want the landing page", path, res.StatusCode, body)
		}
	}
	res = get(t, h, "/join.html?server=ws%3A%2F%2F192.168.1.20%3A4223")
	if body, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || !strings.Contains(string(body), "join") {
		t.Fatalf("GET /join.html = %d %q, want the join page", res.StatusCode, body)
	}
	res = get(t, h, "/jetris.wasm")
	if ct := res.Header.Get("Content-Type"); res.StatusCode != http.StatusOK || ct != "application/wasm" {
		t.Fatalf("GET /jetris.wasm = %d %q, want 200 application/wasm", res.StatusCode, ct)
	}
	if res = get(t, h, "/nothing.here"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nothing.here = %d, want 404", res.StatusCode)
	}

	// No join link to send to: / is the landing page.
	h = Handler(fakeBuild(), nil)
	res = get(t, h, "/")
	if body, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || !strings.Contains(string(body), "landing") {
		t.Fatalf("GET / without a join link = %d %q, want the landing page", res.StatusCode, body)
	}
}

// A binary built without the browser build says so on every path.
func TestHandlerMissingBuild(t *testing.T) {
	empty := fstest.MapFS{".gitkeep": {}}
	if ready(empty) {
		t.Fatal("an empty tree reported ready")
	}
	if !ready(fakeBuild()) {
		t.Fatal("a whole tree reported not ready")
	}
	partial := fakeBuild()
	delete(partial, "jetris.wasm")
	if ready(partial) {
		t.Fatal("a tree without the module reported ready")
	}
	h := Handler(empty, func() string { return "http://x/join.html" })
	for _, path := range []string{"/", "/join.html?server=ws://x", "/jetris.wasm"} {
		res := get(t, h, path)
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "build-wasm.sh") {
			t.Fatalf("GET %s without a build = %d %q, want the explanation", path, res.StatusCode, body)
		}
	}
}

// Serve really listens: a free port is picked at 0, the port is reported, and
// the handler answers there until Close.
func TestServe(t *testing.T) {
	s, err := Serve(0, func() string { return "http://x/join.html" }, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Port() == 0 {
		t.Fatal("no port reported")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get("http://127.0.0.1:" + itoa(s.Port()) + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// With the build embedded / redirects to the join link; without it the
	// explanation page is served. Either way the server answers.
	if Ready() && res.StatusCode != http.StatusFound || !Ready() && res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d (build embedded: %v)", res.StatusCode, Ready())
	}
	s.Close()
	if _, err := client.Get("http://127.0.0.1:" + itoa(s.Port()) + "/"); err == nil {
		t.Fatal("the server still answers after Close")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestJoinLink(t *testing.T) {
	link, err := JoinLink("http://192.168.1.20:8080/", "ws://192.168.1.20:4223", "LAN party")
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://192.168.1.20:8080/join.html?name=LAN+party&server=ws%3A%2F%2F192.168.1.20%3A4223"; link != want {
		t.Fatalf("link = %q, want %q", link, want)
	}
	// A page URL naming a directory without its slash, and one naming index.html.
	for _, page := range []string{"https://jnmoyne.github.io/jetris", "https://jnmoyne.github.io/jetris/index.html"} {
		link, err := JoinLink(page, "wss://demo.nats.io:8443", "")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://jnmoyne.github.io/jetris/join.html?server=wss%3A%2F%2Fdemo.nats.io%3A8443"; link != want {
			t.Fatalf("link for %s = %q, want %q", page, link, want)
		}
	}
	for _, server := range []string{"nats://host:4222", "tls://host:4222", "host:4223", "http://host"} {
		if _, err := JoinLink("http://host:8080/", server, ""); err == nil {
			t.Fatalf("%s accepted as a join link's server", server)
		}
	}
	if _, err := JoinLink("dist/web", "ws://host:4223", ""); err == nil {
		t.Fatal("a relative page path accepted")
	}
}

// A replay's share link is the join link with the game named, under the
// same rules: a WebSocket server, a full page URL — and a game ID.
func TestReplayLink(t *testing.T) {
	link, err := ReplayLink(DefaultPage, "wss://eu-central.jetris.net:4223", "Jetris EU central", "1a2b-3c4d")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://jnmoyne.github.io/jetris/join.html?name=Jetris+EU+central&replay=1a2b-3c4d&server=wss%3A%2F%2Feu-central.jetris.net%3A4223"; link != want {
		t.Fatalf("link = %q, want %q", link, want)
	}
	if _, err := ReplayLink(DefaultPage, "wss://host:443", "", ""); err == nil {
		t.Fatal("a replay link without a game ID accepted")
	}
	if _, err := ReplayLink(DefaultPage, "nats://host:4222", "", "g"); err == nil {
		t.Fatal("a nats:// server accepted in a replay link")
	}
}

// tlsClient trusts nothing — the page's certificate is its own.
func tlsClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// With a certificate directory the page is https on its port; a plain
// http request on the same port is redirected to the https page; Scheme
// says so; and the certificate lands in the directory.
func TestServeTLS(t *testing.T) {
	dir := t.TempDir()
	s, err := Serve(0, nil, Options{CertDir: dir, Hosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Scheme() != "https" {
		t.Fatalf("Scheme() = %q, want https", s.Scheme())
	}
	if _, err := os.Stat(filepath.Join(dir, certFile)); err != nil {
		t.Fatalf("no certificate written: %v", err)
	}
	addr := "127.0.0.1:" + itoa(s.Port())
	client := tlsClient()
	res, err := client.Get("https://" + addr + "/index.html")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET https:///index.html = %d, want 200", res.StatusCode)
	}
	res, err = client.Get("http://" + addr + "/join.html?server=x")
	if err != nil {
		t.Fatalf("plain http on the https port: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://"+addr+"/join.html?server=x" {
		t.Fatalf("plain http on the https port = %d %q, want a redirect to the same page over https", res.StatusCode, res.Header.Get("Location"))
	}
}

// A WebSocket upgrade on the https port — on any path — is proxied to the
// backend, whose 101 comes back and whose bytes then flow both ways: a
// hand-made handshake against an echoing backend.
func TestServeProxiesWebSocket(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWebSocketUpgrade(r) {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
		buf.Flush()
		io.Copy(conn, buf.Reader) // echo whatever follows the handshake
	}))
	defer backend.Close()

	s, err := Serve(0, nil, Options{CertDir: t.TempDir(), Hosts: []string{"127.0.0.1"}, WSBackend: strings.TrimPrefix(backend.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	addr := "127.0.0.1:" + itoa(s.Port())
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /anything HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", addr)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the upgrade response: %v", err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade = %s, want 101 from the backend", res.Status)
	}
	if _, err := conn.Write([]byte("ping through the proxy")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping through the proxy"))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(got) != "ping through the proxy" {
		t.Fatalf("echo = %q", got)
	}

	// The same port still serves the page.
	res2, err := tlsClient().Get("https://" + addr + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("GET /index.html beside the proxy = %d", res2.StatusCode)
	}
}

// The browser build's whole path: nats.go, which speaks wss:// natively,
// through the page's proxy to an embedded nats-server's plain WebSocket
// listener — publish, and receive.
func TestServeProxiesNATS(t *testing.T) {
	srv, err := natspkg.StartEmbeddedServer(t.TempDir(), -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	wsURL, err := url.Parse(srv.WebsocketURL())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Serve(0, nil, Options{CertDir: t.TempDir(), Hosts: []string{"127.0.0.1"}, WSBackend: wsURL.Host})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nc, err := nats.Connect("wss://127.0.0.1:"+itoa(s.Port()), nats.Secure(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("wss through the page: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("jetris.voice.test.all.alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("jetris.voice.test.all.alice", []byte("frame")); err != nil {
		t.Fatal(err)
	}
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("no message through the proxy: %v", err)
	}
	if string(msg.Data) != "frame" {
		t.Fatalf("message = %q", msg.Data)
	}
}
