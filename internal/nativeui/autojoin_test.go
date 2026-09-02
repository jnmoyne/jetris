package nativeui

// A join link (web/join.html, the QR codes of scripts/gen-qr.go) and the
// desktop's --name both arrive as config.Config.PlayerName: the login screen
// has nothing left to ask, so it plays its own Play button and the player
// lands in the lobby. These pin that it happens, that it happens once, and
// that everything it can get wrong is still shown on the screen it skipped.

import (
	"context"
	"strings"
	"testing"

	"jetris/internal/config"
	"jetris/internal/prefs"
	"jetris/internal/testutil"
)

// linkApp is an App built the way main.go builds it — the combined login
// screen, seeded from a link — with a live context and no favorites (the
// defaults would probe the real Jetris servers from a unit test).
func linkApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	a := NewWithPicker(cfg, nil, "", nil)
	a.th = newUITheme()
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx = ctx
	t.Cleanup(func() {
		a.quit()
		cancel()
	})
	return a
}

// The whole flow: a link naming a server and a player reaches the lobby off
// one frame of the login screen, and the name the link gave the server heads
// the lobby's connection line.
func TestAutoLoginFromLink(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url, ServerLabel: "LAN party", PlayerName: "alice"})

	if got := a.loginEd.Text(); got != "alice" {
		t.Fatalf("name field = %q, want the link's name filled in", got)
	}
	renderOnce(t, a)
	waitFor(t, "the lobby", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenLobby || a.loginErr != ""
	})
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("auto-join: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	if a.connName != "LAN party" || a.connURL != url {
		t.Fatalf("connection line = %q (%q), want the link's name and its URL", a.connName, a.connURL)
	}
	if lb := a.getLobby(); lb == nil {
		t.Fatal("no lobby joined")
	}
}

// Without a name from the link nothing is submitted: the screen waits, as it
// always has.
func TestNoAutoLoginWithoutAName(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url, ServerLabel: "LAN party"})
	if a.autoLogin {
		t.Fatal("auto-login armed with no player name")
	}
	renderOnce(t, a)
	renderOnce(t, a)
	if a.screen != screenLogin || a.loggingIn {
		t.Fatalf("screen %v (logging in %v), want to be left on the login screen", a.screen, a.loggingIn)
	}
}

// One shot. Quitting the lobby comes back to the login screen, and the frames
// drawn there must not rejoin behind the player's back — leaving is leaving.
func TestAutoLoginIsOneShot(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url, PlayerName: "alice"})
	renderOnce(t, a)
	waitFor(t, "the lobby", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenLobby || a.loginErr != ""
	})
	if a.screen != screenLobby {
		t.Fatalf("auto-join: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}

	a.quit()
	if a.screen != screenLogin {
		t.Fatalf("after quit: screen %v, want the login screen", a.screen)
	}
	for range 3 {
		renderOnce(t, a)
	}
	if a.screen != screenLogin || a.loggingIn {
		t.Fatalf("screen %v (logging in %v), want to stay on the login screen", a.screen, a.loggingIn)
	}
	// The name is still in the field, so playing again is one click.
	if got := a.loginEd.Text(); got != "alice" {
		t.Fatalf("name field = %q, want it still filled in", got)
	}
}

// A name a link cannot use is refused by the screen it skipped, not swallowed:
// the player sees why, with the name still there to fix.
func TestAutoLoginSurfacesBadName(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url, PlayerName: "bad name"})
	renderOnce(t, a)
	if a.screen != screenLogin || a.loggingIn {
		t.Fatalf("screen %v (logging in %v), want to be held on the login screen", a.screen, a.loggingIn)
	}
	if !strings.Contains(a.loginErr, "cannot contain") {
		t.Fatalf("loginErr = %q, want the name validation error", a.loginErr)
	}
}

// A URL this build cannot dial is never selected (NewWithPicker), so the
// auto-submit finds nothing selected — and must say which URL and why, rather
// than sending the player to a list to work it out.
func TestAutoLoginUndialableURLExplainsItself(t *testing.T) {
	origDialable := dialable
	dialable = func(url string) bool { return strings.HasPrefix(url, "ws") } // play the browser
	t.Cleanup(func() { dialable = origDialable })

	a := linkApp(t, config.Config{NATSURL: "nats://192.168.1.20:4222", PlayerName: "alice"})
	renderOnce(t, a)
	if a.screen != screenLogin {
		t.Fatalf("screen %v, want to be held on the login screen", a.screen)
	}
	if !strings.Contains(a.loginErr, "192.168.1.20") || !strings.Contains(a.loginErr, undialableErr) {
		t.Fatalf("loginErr = %q, want the URL and why it cannot be dialed", a.loginErr)
	}
}

// The browser row for a link's server: named by the link when it named one
// (and named rows are the ones whose label heads the lobby), by where it came
// from otherwise.
func TestLinkNamedServerRow(t *testing.T) {
	const url = "wss://lan.example:443"
	for _, tc := range []struct {
		label     string
		wantLabel string
		wantNamed bool
	}{
		{"", "--server", false},
		{"LAN party", "LAN party", true},
	} {
		a := NewWithPicker(config.Config{NATSURL: url, ServerLabel: tc.label}, nil, "", nil)
		e, ok := a.connEntry(urlKey(url))
		if !ok {
			t.Fatalf("label %q: the link's server is not in the browser", tc.label)
		}
		if e.label != tc.wantLabel || e.named != tc.wantNamed || e.detail != url {
			t.Errorf("label %q: row = %q (named %v, detail %q), want %q (named %v, detail %q)",
				tc.label, e.label, e.named, e.detail, tc.wantLabel, tc.wantNamed, url)
		}
		// And the SELECTED line under the browser, which is what tells the
		// player what Play will dial, says the same.
		wantCaption := [2]string{url, "from --server"}
		if tc.wantNamed {
			wantCaption = [2]string{tc.label, url}
		}
		if l, d := selectionCaption(e); l != wantCaption[0] || d != wantCaption[1] {
			t.Errorf("label %q: SELECTED line = %q · %q, want %q · %q", tc.label, l, d, wantCaption[0], wantCaption[1])
		}
	}

	// A favorite is named too: that is what has always put its label in the
	// lobby header.
	a := NewWithPicker(config.Config{}, nil, "", []prefs.Favorite{{Label: "home lab", URL: url}})
	if e, ok := a.connEntry(urlKey(url)); !ok || !e.named || e.label != "home lab" {
		t.Errorf("favorite row = %+v (found %v), want a named 'home lab' row", e, ok)
	}
}
