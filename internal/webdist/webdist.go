//go:build !js

// Package webdist carries the browser build inside the desktop binary and
// serves it over plain HTTP — the LAN party mode's "join from a browser":
// the phones on the network open http://<host>:<port>/, scan the QR code the
// lobby shows, and play the WebAssembly build straight off the game that is
// hosting them, with the embedded server's WebSocket listener one hop away.
//
// The build is whatever scripts/build-wasm.sh last put under dist/ (the
// same files it publishes to GitHub Pages: index.html, join.html, jetris.wasm,
// wasm_exec.js and the icons). A binary built without running it carries an
// empty directory — Ready reports so, and the server says so on every page
// rather than 404ing — so `go build` never depends on a 25 MB artifact
// having been made first.
package webdist

import (
	"embed"
	"io/fs"
)

// files is the build directory; dist/.gitkeep is committed so the pattern
// always matches, wasm built or not.
//
//go:embed all:dist
var files embed.FS

// FS is the browser build, rooted at its own directory.
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // the directory is embedded above
	}
	return sub
}

// Ready reports whether this binary carries a whole browser build: the two
// pages, the module and Go's JS shim. Without all four there is nothing to
// serve, and the lobby offers no browser join.
func Ready() bool { return ready(FS()) }

// ready is Ready over any tree (the tests hand it their own).
func ready(fsys fs.FS) bool {
	for _, name := range []string{"index.html", "join.html", "jetris.wasm", "wasm_exec.js"} {
		if _, err := fs.Stat(fsys, name); err != nil {
			return false
		}
	}
	return true
}
