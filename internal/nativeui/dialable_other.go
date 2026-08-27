//go:build !js

package nativeui

// The wording for URLs this build cannot dial (see dialable). On the desktop
// every scheme is dialable, so only addURLHint (the add-favorite form's URL
// placeholder) is ever shown; the other two exist so login.go reads the same
// in both builds, and so the tests that stand in a browser-like dialable see
// the browser build's text.
const (
	addURLHint     = "nats://host:4222"
	undialableHint = "needs ws:// or wss://"
	undialableErr  = "a browser can only dial ws:// or wss:// URLs"
)
