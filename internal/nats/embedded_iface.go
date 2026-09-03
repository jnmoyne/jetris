package nats

import "net"

// EmbeddedServer is what the app holds on to for the LAN party mode's
// in-process nats-server: the subset of *natsserver.Server it needs. Being
// an interface lets the browser (js/wasm) build, which cannot run a server,
// compile without linking nats-server at all.
type EmbeddedServer interface {
	Addr() net.Addr
	// WebsocketURL is the WebSocket listener's "ws://host:port" — the port
	// is what matters, the host being the bind address (port 0 when the
	// server was started without a listener).
	WebsocketURL() string
	ID() string
	Shutdown()
}
