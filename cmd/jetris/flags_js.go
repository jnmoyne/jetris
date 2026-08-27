//go:build js

package main

import (
	"net/url"
	"strings"
	"syscall/js"

	"jetris/internal/config"
)

// applyPageParams reads the page's query string in the browser build, where
// there is no command line: ?server=wss://host:port[&user=..&password=..]
// preselects that server in the login screen's browser, just as --server
// does on the desktop.
func applyPageParams(cfg *config.Config) {
	loc := js.Global().Get("location")
	if !loc.Truthy() {
		return
	}
	// location.search keeps its leading "?", which ParseQuery would fold
	// into the first key.
	q, err := url.ParseQuery(strings.TrimPrefix(loc.Get("search").String(), "?"))
	if err != nil {
		return
	}
	if v := q.Get("server"); v != "" {
		cfg.NATSURL = v
	}
	if v := q.Get("user"); v != "" {
		cfg.NATSUser = v
	}
	if v := q.Get("password"); v != "" {
		cfg.NATSPassword = v
	}
}
