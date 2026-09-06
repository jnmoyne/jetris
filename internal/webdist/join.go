package webdist

import (
	"fmt"
	"net/url"
	"strings"
)

// DefaultPage is where the browser build is served from when no other page
// is known: the GitHub Pages copy, always the latest release. It is what a
// desktop build connected to a public server sends a replay's share link
// to, and scripts/gen-qr.go's default -page.
const DefaultPage = "https://jnmoyne.github.io/jetris/"

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
	return joinPageLink(page, server, name, "")
}

// ReplayLink builds the link to one game's replay on a server:
// <page>/join.html?server=…[&name=…]&replay=<gameID> — the replay screen's
// Share. It is a join link with the game named: the join page asks nothing
// and hands straight over to the game, which deals a Watcher_ name,
// connects, lands in the lobby and opens that replay
// (config.Config.ReplayGameID). The same rules as JoinLink's: the server
// must be a WebSocket URL.
func ReplayLink(page, server, name, gameID string) (string, error) {
	if gameID == "" {
		return "", fmt.Errorf("replay link: no game ID")
	}
	return joinPageLink(page, server, name, gameID)
}

// joinPageLink is JoinLink and ReplayLink's shared builder; replay is the
// game ID to name, or "" for a plain join link.
func joinPageLink(page, server, name, replay string) (string, error) {
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
	if replay != "" {
		q.Set("replay", replay)
	}
	join, _ := url.Parse("join.html")
	join.RawQuery = q.Encode()
	return base.ResolveReference(join).String(), nil
}
