//go:build js

package nativeui

import natspkg "jetris/internal/nats"

// The browser build's wording for URLs it cannot dial (see dialable). It
// depends on how the page was served: over http a browser can open ws:// and
// wss:// sockets; over https (the GitHub Pages site) only wss://, as the
// browser refuses plain ws:// from a secure page (mixed content).
var (
	addURLHint     = "ws://host:4223"                               // the add-favorite form's URL placeholder
	undialableHint = "needs ws:// or wss://"                        // the greyed row's readout
	undialableErr  = "a browser can only dial ws:// or wss:// URLs" // why a selection or an added URL was refused
)

func init() {
	if natspkg.SecurePage() {
		addURLHint = "wss://host:443"
		undialableHint = "needs wss:// (this page is https)"
		undialableErr = "an https page can only dial wss:// URLs"
	}
}
