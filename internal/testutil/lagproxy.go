package testutil

import (
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

// StartLagProxy fronts the NATS server at upstreamURL with a TCP proxy that
// delays every byte by oneWay in each direction — a far-away server on a
// LAN test box: a 95 ms one-way delay is the ~190 ms batch round trip a beta
// player saw on the wrong continent's server. Returns the URL to dial. The
// proxy stops with the test.
func StartLagProxy(t *testing.T, upstreamURL string, oneWay time.Duration) string {
	t.Helper()
	u, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", u.Host)
			if err != nil {
				c.Close()
				continue
			}
			go lagPipe(c, up, oneWay)
			go lagPipe(up, c, oneWay)
		}
	}()
	return "nats://" + ln.Addr().String()
}

// lagPipe copies src to dst, each chunk released oneWay after it was read,
// in order — the wire's latency, not its bandwidth.
func lagPipe(src, dst net.Conn, oneWay time.Duration) {
	defer dst.Close()
	type chunk struct {
		at time.Time
		b  []byte
	}
	ch := make(chan chunk, 1024)
	go func() {
		defer close(ch)
		for {
			buf := make([]byte, 64*1024)
			n, err := src.Read(buf)
			if n > 0 {
				ch <- chunk{time.Now().Add(oneWay), buf[:n]}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range ch {
		if d := time.Until(c.at); d > 0 {
			time.Sleep(d)
		}
		if _, err := dst.Write(c.b); err != nil {
			io.Copy(io.Discard, src)
			return
		}
	}
}
