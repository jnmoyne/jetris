package webdist

import (
	"fmt"
	"net/url"
	"strings"
)

// JoinLink builds the join page's link: <page>/join.html?server=…[&name=…] —
// the URL a QR code carries (the lobby's Show QR code, scripts/gen-qr.go).
// page is where the browser build is served from, server the NATS server's
// WebSocket URL, name what the join page calls it (optional).
//
// It insists on a WebSocket URL — a browser can dial nothing else, so a
// nats:// URL in the QR would take every scanner to a page that cannot
// connect. (The one other combination that always fails, a plain ws:// server
// from an https page, is the caller's to warn about: mixed content.)
func JoinLink(page, server, name string) (string, error) {
	su, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("server URL %q: %w", server, err)
	}
	switch su.Scheme {
	case "ws", "wss":
	case "nats", "tls":
		return "", fmt.Errorf("%s is a %s:// URL — a browser can only dial ws:// or wss://, so give the server's WebSocket listener instead (nats-server's websocket{} block, often port 8080 or 443)", server, su.Scheme)
	default:
		return "", fmt.Errorf("server URL %q: need a ws:// or wss:// URL", server)
	}

	base, err := url.Parse(page)
	if err != nil {
		return "", fmt.Errorf("page URL %q: %w", page, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("page URL %q: need a full URL, e.g. https://jnmoyne.github.io/jetris/", page)
	}
	// A base that already names a page ("…/index.html") resolves to its
	// directory; one that names a directory needs the trailing slash or
	// ResolveReference would drop its last element.
	if !strings.HasSuffix(base.Path, "/") && !strings.Contains(base.Path[strings.LastIndex(base.Path, "/")+1:], ".") {
		base.Path += "/"
	}
	q := url.Values{"server": {server}}
	if name != "" {
		q.Set("name", name)
	}
	join, _ := url.Parse("join.html")
	join.RawQuery = q.Encode()
	return base.ResolveReference(join).String(), nil
}
