//go:build js

package nats

import (
	"net"
	neturl "net/url"
	"strings"
	"syscall/js"
	"time"

	"github.com/nats-io/nats.go"
)

// transportOptions adapts a server URL to the browser, where the only
// transport is the WebSocket API: nats.go is handed a plain nats://host:port
// URL (so it speaks the raw NATS protocol, without doing its own WebSocket
// framing) plus a CustomDialer that opens a browser WebSocket to that
// host:port and presents it as a net.Conn. The browser frames the bytes and
// the server's websocket listener unframes them — exactly how nats.ws works.
//
// Scheme mapping: ws:// and wss:// are used as given; nats:// becomes ws://
// and tls:// becomes wss://. Note a page served over https may only open
// wss:// sockets (SecurePage; Dialable greys the rest out before it gets
// here), and the server needs a websocket {} listener.
func transportOptions(rawURL string) (string, []nats.Option) {
	scheme := "ws"
	host := rawURL
	if u, err := neturl.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
		switch strings.ToLower(u.Scheme) {
		case "wss", "tls":
			scheme = "wss"
		}
	} else if i := strings.Index(rawURL, "://"); i >= 0 {
		host = rawURL[i+3:]
	}
	d := &wsDialer{scheme: scheme, timeout: 10 * time.Second}
	return "nats://" + host, []nats.Option{
		nats.SetCustomDialer(d),
		nats.SkipHostLookup(),          // no DNS in the browser; the WebSocket resolves the name
		nats.IgnoreDiscoveredServers(), // advertised cluster URLs are TCP ports, unreachable here
	}
}

// Dialable reports whether this build can dial url. A browser has only the
// WebSocket API: ws:// and wss:// URLs, nothing else — and from an https page
// wss:// alone (browserDialable). The login screen's server browser greys out
// the rest.
func Dialable(rawURL string) bool {
	return browserDialable(SecurePage(), rawURL)
}

// SecurePage reports whether the page was served over https, in which case
// the browser refuses plain ws:// sockets (mixed content) and only wss:// can
// be dialed. The GitHub Pages site (https://jnmoyne.github.io/jetris/) is one
// such page; a local http://localhost server is not.
func SecurePage() bool {
	loc := js.Global().Get("location")
	return loc.Truthy() && loc.Get("protocol").String() == "https:"
}

// wsDialer is the nats.CustomDialer for the browser: it turns the
// "host:port" nats.go asks for into a <scheme>://host:port WebSocket.
type wsDialer struct {
	scheme  string
	timeout time.Duration
}

func (d *wsDialer) Dial(network, address string) (net.Conn, error) {
	return dialWebSocket(d.scheme+"://"+address, d.timeout)
}

// SkipTLSHandshake tells nats.go not to wrap the conn in crypto/tls even when
// the server's INFO says TLS is required: with wss:// the browser already did
// the TLS handshake.
func (d *wsDialer) SkipTLSHandshake() bool { return true }
