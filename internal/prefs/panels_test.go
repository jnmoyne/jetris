//go:build !js

// The filesystem store (panels_fs.go); the browser build keeps the switches
// in localStorage and has no file to exercise.

package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh install (no file) shows every panel; a saved set reloads exactly as
// written — a panel hidden on purpose does not come back.
func TestPanelsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p, err := LoadPanels()
	if err != nil {
		t.Fatal(err)
	}
	if p != DefaultPanels() {
		t.Fatalf("fresh panels = %+v, want the defaults %+v", p, DefaultPanels())
	}
	if want := (Panels{true, true, true, true, true, true, true}); p != want {
		t.Fatalf("the defaults hide something: %+v", p)
	}

	want := DefaultPanels()
	want.Chat, want.Pad, want.LobbyPlayers = false, false, false
	if err := SavePanels(want); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPanels()
	if err != nil {
		t.Fatal(err)
	}
	if p != want {
		t.Fatalf("reloaded panels = %+v, want %+v", p, want)
	}

	// Every switch off is a real answer too, and must survive the round trip
	// rather than reading as "nothing saved".
	if err := SavePanels(Panels{}); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPanels()
	if err != nil {
		t.Fatal(err)
	}
	if p != (Panels{}) {
		t.Fatalf("everything-hidden panels = %+v, want them all off", p)
	}
}

// A set saved before a switch existed: the missing key shows, as a fresh
// install does, rather than reading as a panel the player put away.
func TestPanelsAbsentKeyShows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writePanelsFile(t, dir, `{"menu": false, "chat": false}`)

	p, err := LoadPanels()
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultPanels()
	want.Menu, want.Chat = false, false
	if p != want {
		t.Fatalf("panels from a partial file = %+v, want %+v", p, want)
	}
}

// A hand-edited file that will not parse falls back to the defaults with an
// error: the player still gets a whole screen.
func TestPanelsTolerantLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writePanelsFile(t, dir, "not json")

	p, err := LoadPanels()
	if err == nil {
		t.Fatal("unparsable file: want an error")
	}
	if p != DefaultPanels() {
		t.Fatalf("unparsable file: panels = %+v, want the defaults", p)
	}
}

func writePanelsFile(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, panelsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
