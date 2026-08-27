package nats

import "strings"

// browserDialable reports whether the browser build can dial rawURL. A
// browser has only the WebSocket API, so ws:// and wss:// are the only
// schemes it can speak; and a page served over https (securePage) is barred
// by the browser from opening plain ws:// sockets (mixed content), which
// leaves wss:// alone. The GitHub Pages site is such a page. Kept free of
// syscall/js so it can be unit-tested on the desktop.
func browserDialable(securePage bool, rawURL string) bool {
	s := strings.ToLower(strings.TrimSpace(rawURL))
	if strings.HasPrefix(s, "wss://") {
		return true
	}
	return !securePage && strings.HasPrefix(s, "ws://")
}
