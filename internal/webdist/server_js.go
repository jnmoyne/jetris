//go:build js

package webdist

import "errors"

// A wasm module cannot listen for connections (nor carry a copy of itself):
// the browser build has no LAN party mode, and these stubs only let the
// package's callers compile.

// Server is the desktop build's HTTP server; the browser never has one.
type Server struct{}

// Serve always fails in the browser.
func Serve(port int, join func() string, opts Options) (*Server, error) {
	return nil, errors.New("the browser build cannot serve the LAN party page")
}

// Port has no server to report.
func (s *Server) Port() int { return 0 }

// Scheme has no page to serve.
func (s *Server) Scheme() string { return "http" }

// Close has nothing to stop.
func (s *Server) Close() {}

// Ready: there is no browser build inside the browser build.
func Ready() bool { return false }
