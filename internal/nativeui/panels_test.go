package nativeui

import (
	"testing"

	"jetris/internal/prefs"
)

// TestPanelsStartOnEveryScreen: a fresh App shows every panel on both
// screens, whatever the device and however the window is held. The form
// factor decides WHERE a panel goes (formfactor.go) and never whether it is
// there, so a player who has set nothing gets the whole screen.
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
		for _, c := range []struct {
			name string
			on   bool
		}{
			{"menu", a.hudVisible()},
			{"opponents", a.oppVisible()},
			{"pad", a.padVisible()},
			{"chat", a.chatVisible()},
			{"lobby menu", a.lobbyMenuVisible()},
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
// screens' switches together, and comes back put away at the next launch —
// while everything they left alone comes back showing.
func TestPanelsPersistAndReload(t *testing.T) {
	a := newTestApp()
	var saved prefs.Panels
	a.panelsSave = func(p prefs.Panels) error { saved = p; return nil }

	a.chatShown, a.padShown, a.lobbyPlayersShown = false, false, false
	a.persistPanels()
	want := prefs.DefaultPanels()
	want.Chat, want.Pad, want.LobbyPlayers = false, false, false
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
		{"menu", b.hudVisible(), true},
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
	a.hudShown = false
	a.persistPanels()
	if a.hudVisible() {
		t.Error("the menu came back on")
	}
}
