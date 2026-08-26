//go:build js

package nats

import "errors"

// ErrNoEmbeddedServer is returned by StartEmbeddedServer in the browser build:
// a wasm module has no sockets to listen on, so LAN party mode does not exist
// there (the login screen hides the tab).
var ErrNoEmbeddedServer = errors.New("the embedded NATS server is not available in the browser")

// StartEmbeddedServer always fails in the browser.
func StartEmbeddedServer(storeDir string, port int) (EmbeddedServer, error) {
	return nil, ErrNoEmbeddedServer
}

// LanIP has no meaning in the browser (there is no server to advertise).
func LanIP() string { return "" }
