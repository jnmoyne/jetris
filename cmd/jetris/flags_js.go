//go:build js

package main

import (
	"net/url"
	"strings"
	"syscall/js"

	"jetris/internal/config"
)

// applyPageParams reads the page's query string in the browser build, where
// there is no command line:
//
//	?server=wss://host:port[&user=..&password=..]
//
// preselects that server in the login screen's browser, just as --server
// does on the desktop; &name=.. names it for the browser row and the lobby
// header; &player=.. answers the login screen's one question, so the game
// connects and lands in the lobby without it; and &replay=<gameID> opens
// that game's replay on landing (a replay's share link, webdist.ReplayLink).
// The join page (web/join.html), which the QR codes of scripts/gen-qr.go
// and the replay screen's Share point at, is nothing but a form that
// collects that player name and comes back here with it.
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
	if v := q.Get("name"); v != "" {
		cfg.ServerLabel = v
	}
	if v := strings.TrimSpace(q.Get("player")); v != "" {
		cfg.PlayerName = v
	}
	if v := strings.TrimSpace(q.Get("replay")); v != "" {
		cfg.ReplayGameID = v
	}
}
