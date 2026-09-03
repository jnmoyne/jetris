//go:build !js

package webdist

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server serves the browser build on every interface, for the LAN party
// mode. Start it with Serve, stop it with Close.
type Server struct {
	ln     net.Listener
	srv    *http.Server // the page: https when there is a certificate, http otherwise
	plain  *http.Server // with a certificate: the http→https redirect on the same port
	scheme string
}

// sniffTimeout bounds the first byte a new connection must show before it
// is sent to the https or the http server: an idle probe holds nothing up.
const sniffTimeout = 3 * time.Second

// Serve listens on every interface at port (0 picks a free one) and serves
// the browser build there (Handler). join is where a bare GET / is sent —
// the join link for this party — asked per request, so it can carry whatever
// the host's name and address are at the time; nil or "" serves the landing
// page instead. opts (Options) makes the page https and proxies the browser
// build's WebSocket to the embedded server; a plain http on an https port
// is redirected to https, so an address typed as http:// still lands.
func Serve(port int, join func() string, opts Options) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("http server: %w", err)
	}
	h := Handler(FS(), join)
	if opts.WSBackend != "" {
		h = proxyUpgrades(h, opts.WSBackend)
	}
	s := &Server{ln: ln, scheme: "http", srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}}
	if opts.CertDir == "" {
		go serve(s.srv, ln)
		return s, nil
	}
	cert, err := LoadOrCreateCert(opts.CertDir, opts.Hosts)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("LAN party certificate: %w", err)
	}
	s.scheme = "https"
	// HTTP/1.1 only: an h2 connection cannot be hijacked for the WebSocket
	// upgrade the proxy makes.
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	tlsLn, plainLn := splitListener(ln)
	s.plain = &http.Server{Handler: http.HandlerFunc(redirectHTTPS), ReadHeaderTimeout: 10 * time.Second}
	go serve(s.srv, tls.NewListener(tlsLn, tlsCfg))
	go serve(s.plain, plainLn)
	return s, nil
}

func serve(srv *http.Server, ln net.Listener) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		// The listener was taken from under it; nothing to do but note it.
		fmt.Println("http server:", err)
	}
}

// Port is the port the server listens on.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Scheme is "https" when the page has a certificate, "http" otherwise —
// what the join link and the address on the screen are built with.
func (s *Server) Scheme() string { return s.scheme }

// Close stops the server at once, dropping any transfer in flight.
func (s *Server) Close() {
	s.srv.Close()
	if s.plain != nil {
		s.plain.Close()
	}
	s.ln.Close()
}

// Handler serves the browser build in fsys the way the LAN party wants it:
//
//   - GET / with no query string is redirected to the join link (join()),
//     so the address the host reads out loud lands a phone on the "type your
//     name" page rather than the landing page — the one thing anyone on that
//     network wants from this server. With a query string (the join page
//     hands the game page ?server=…&player=…) it is the game page itself.
//   - /index.html is the landing page, served as is: the generic file server
//     would bounce it to /, and from there to the join page.
//   - Everything else is the build's files.
//
// A tree with no build in it (ready) serves a page saying so, on every path,
// rather than a bare 404 — the phone that scanned a QR code should learn what
// happened.
func Handler(fsys fs.FS, join func() string) http.Handler {
	if !ready(fsys) {
		return http.HandlerFunc(missingBuild)
	}
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if r.URL.RawQuery == "" && join != nil {
				if link := join(); link != "" {
					http.Redirect(w, r, link, http.StatusFound)
					return
				}
			}
			serveIndex(w, r, fsys)
		case "/index.html":
			serveIndex(w, r, fsys)
		default:
			files.ServeHTTP(w, r)
		}
	})
}

// proxyUpgrades sends every WebSocket upgrade — on any path: the browser
// build dials the origin's root — to backend, the embedded server's plain
// WebSocket listener, and everything else to next. Go's reverse proxy
// hijacks the connection on the backend's 101 and pipes both ways; the
// browser's Origin and Sec-WebSocket-* headers pass through untouched
// (nats-server accepts any origin unless told otherwise).
func proxyUpgrades(next http.Handler, backend string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: backend})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			proxy.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// redirectHTTPS is the plain-http server on the https port: whatever was
// asked for, at the same host and path, over https.
func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusFound)
}

// serveIndex writes the landing page without the /index.html → / redirect
// http.FileServer and http.ServeFileFS both insist on.
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}

// missingBuild is every page of a binary built without the browser build.
func missingBuild(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Jetris — no browser build</title>
<style>body{margin:0;padding:40px 20px;background:#000;color:#e8f4f2;font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}main{max-width:560px;margin:0 auto}h1{font:700 40px/1 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;letter-spacing:.1em;color:#8fd;text-align:center}code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#5cc8ff}</style></head>
<body><main><h1>JETRIS</h1>
<p>This Jetris build carries no browser version, so there is nothing to play on this page.</p>
<p>The host's copy was built without the WebAssembly build: run <code>scripts/build-wasm.sh</code> and rebuild <code>jetris</code>, or download a release build. Desktop players and agents can still join through the server's <code>nats://</code> address.</p>
</main></body></html>`)
}

// The one port, two servers: a connection's first byte says whether it is a
// TLS handshake (0x16, the record type) or plain http, and it goes to the
// server that speaks that — so https://host:8080 is the page and
// http://host:8080 a redirect to it, on the port the host read out.

// splitListener is ln's connections sorted by their first byte into the two
// listeners returned. Closing either closes ln and both.
func splitListener(ln net.Listener) (tlsLn, plainLn net.Listener) {
	stop := make(chan struct{})
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			close(stop)
			ln.Close()
		})
	}
	t := &chanListener{ch: make(chan net.Conn), addr: ln.Addr(), stop: stop, closeAll: closeAll}
	p := &chanListener{ch: make(chan net.Conn), addr: ln.Addr(), stop: stop, closeAll: closeAll}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				closeAll()
				return
			}
			go sort(c, t, p, stop)
		}
	}()
	return t, p
}

// sort peeks c's first byte and hands c to the listener for it; a
// connection that shows nothing within sniffTimeout is dropped.
func sort(c net.Conn, tlsLn, plainLn *chanListener, stop <-chan struct{}) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	dst := plainLn
	if first[0] == 0x16 {
		dst = tlsLn
	}
	select {
	case dst.ch <- &sniffedConn{Conn: c, r: br}:
	case <-stop:
		c.Close()
	}
}

// sniffedConn is a connection whose first bytes were peeked: reads go
// through the buffer that holds them.
type sniffedConn struct {
	net.Conn
	r io.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener is one half of a split listener.
type chanListener struct {
	ch       chan net.Conn
	addr     net.Addr
	stop     chan struct{}
	closeAll func()
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.stop:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.closeAll()
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
