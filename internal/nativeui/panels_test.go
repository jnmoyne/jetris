package nativeui

import (
	"testing"

	"jetris/internal/prefs"
)

// TestPanelsStartOnEveryScreen: a fresh App shows every panel on both
// screens but the two ☰ menu columns, whatever the device and however the
// window is held. The form factor decides WHERE a panel goes (formfactor.go)
// and never whether it is there.
func TestPanelsStartOnEveryScreen(t *testing.T) {
	for _, f := range []screenForm{
		{device: deviceDesktop, w: 1280, h: 820},
		{device: deviceDesktop, w: 760, h: 720, compact: true},
		{device: deviceTablet, w: 1180, h: 740},
		{device: deviceTablet, w: 820, h: 1180, portrait: true, compact: true},
		{device: devicePhone, w: 390, h: 844, portrait: true, compact: true},
		{device: devicePhone, w: 844, h: 390, compact: true},
	} {
		a := newTestApp()
		a.form = f
		if a.hudVisible() || a.lobbyMenuVisible() {
			t.Errorf("%v %dx%d: a menu starts up: game=%v lobby=%v", f.device, f.w, f.h, a.hudVisible(), a.lobbyMenuVisible())
		}
		for _, c := range []struct {
			name string
			on   bool
		}{
			{"opponents", a.oppVisible()},
			{"pad", a.padVisible()},
			{"chat", a.chatVisible()},
			{"lobby players", a.lobbyPlayersVisible()},
			{"lobby chat", a.lobbyChatVisible()},
		} {
			if !c.on {
				t.Errorf("%v %dx%d: the %s panel starts hidden", f.device, f.w, f.h, c.name)
			}
		}
	}
}

// TestPanelsPersistAndReload: what the player puts away is written out, both
// screens' switches together, and comes back put away at the next launch; a
// menu they opened comes back open — while everything they left alone comes
// back as it started.
func TestPanelsPersistAndReload(t *testing.T) {
	a := newTestApp()
	var saved prefs.Panels
	a.panelsSave = func(p prefs.Panels) error { saved = p; return nil }

	a.chatShown, a.padShown, a.lobbyPlayersShown = false, false, false
	a.lobbyMenuShown = true
	a.persistPanels()
	want := prefs.DefaultPanels()
	want.Chat, want.Pad, want.LobbyPlayers = false, false, false
	want.LobbyMenu = true
	if saved != want {
		t.Fatalf("persisted %+v, want %+v", saved, want)
	}

	// The next launch (cmd/jetris/main.go: LoadPanels then SetPanels).
	b := newTestApp()
	b.SetPanels(saved)
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"menu", b.hudVisible(), false},
		{"opponents", b.oppVisible(), true},
		{"pad", b.padVisible(), false},
		{"chat", b.chatVisible(), false},
		{"lobby menu", b.lobbyMenuVisible(), true},
		{"lobby players", b.lobbyPlayersVisible(), false},
		{"lobby chat", b.lobbyChatVisible(), true},
	} {
		if c.got != c.want {
			t.Errorf("after reload: %s showing = %v, want %v", c.name, c.got, c.want)
		}
	}
	if b.panels() != saved {
		t.Errorf("reloaded set = %+v, want the saved %+v", b.panels(), saved)
	}
}

// A build with no store behind it (every test App, and the browser when
// localStorage is refused) flips its switches and says nothing.
func TestPanelsPersistWithoutAStore(t *testing.T) {
	a := newTestApp()
	a.chatShown = false
	a.persistPanels()
	if a.chatVisible() {
		t.Error("the chat came back on")
	}
}
