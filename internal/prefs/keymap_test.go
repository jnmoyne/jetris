//go:build !js

// The filesystem store (keymap_fs.go); the browser build keeps the bindings
// in localStorage and has no file to exercise.

package prefs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A fresh install (no file) starts at the defaults; a saved scheme reloads
// as saved.
func TestKeymapRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	k, err := LoadKeymap()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k, DefaultKeymap()) {
		t.Fatalf("fresh keymap = %v, want the defaults %v", k, DefaultKeymap())
	}

	want := DefaultKeymap()
	want[KeyRotateCW] = []string{"↑", "K", "X"}
	want[KeyHold] = []string{"H", "Shift"}
	if err := SaveKeymap(want); err != nil {
		t.Fatal(err)
	}
	k, err = LoadKeymap()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k, want) {
		t.Fatalf("reloaded keymap = %v, want %v", k, want)
	}
}

// Normalize mends a hand-edited file: a reserved key and a key named twice
// fall back to the slot's default, a missing action keeps its defaults, an
// extra slot is dropped, and a file that starves an action of every key is
// thrown out for the defaults.
func TestKeymapNormalize(t *testing.T) {
	k := Keymap{
		KeyMoveLeft:  {"Tab", "A"},            // Tab is reserved: the slot takes ←
		KeyMoveRight: {"→", "D", "E"},         // the extra slot goes
		KeySoftDrop:  {"A"},                   // A is move left's: ↓, and S comes back
		KeyRotateCCW: {"↑"},                   // ↑ is rotate CW's (absent, so at its defaults, and earlier): Z
		KeyHardDrop:  {""},                    // empty: Space
		KeyHold:      {"Space", "Shift", "Q"}, // Space is hard drop's: C
	}
	got := k.Normalize()
	want := Keymap{
		KeyMoveLeft:  {"←", "A"},
		KeyMoveRight: {"→", "D"},
		KeySoftDrop:  {"↓", "S"},
		KeyRotateCW:  {"↑", "W", "X"},
		KeyRotateCCW: {"Z", "Ctrl"},
		KeyHardDrop:  {"Space"},
		KeyHold:      {"C", "Shift"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize = %v, want %v", got, want)
	}

	// Every key of rotate CCW handed to move left, defaults included: no
	// scheme at all.
	k = Keymap{KeyMoveLeft: {"Z", "Ctrl"}, KeyRotateCCW: {"Z", "Ctrl"}}
	if got := k.Normalize(); !reflect.DeepEqual(got, DefaultKeymap()) {
		t.Fatalf("a starved action: Normalize = %v, want the defaults", got)
	}
}

// A file that is not JSON reads as the defaults, with the error.
func TestKeymapBadFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, keymapFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKeymap()
	if err == nil {
		t.Fatal("a broken file loaded without an error")
	}
	if !reflect.DeepEqual(k, DefaultKeymap()) {
		t.Fatalf("a broken file gave %v, want the defaults", k)
	}
}

func TestKeymapActionOf(t *testing.T) {
	k := DefaultKeymap()
	if got := k.ActionOf("A"); got != KeyMoveLeft {
		t.Errorf("ActionOf(A) = %q, want move_left", got)
	}
	if got := k.ActionOf("Shift"); got != KeyHold {
		t.Errorf("ActionOf(Shift) = %q, want hold", got)
	}
	if got := k.ActionOf("Q"); got != "" {
		t.Errorf("ActionOf(Q) = %q, want none", got)
	}
	if KeyBindable("Tab") || KeyBindable("") || !KeyBindable("Q") || !KeyBindable("←") {
		t.Error("KeyBindable: Tab and the empty name are reserved, Q and ← are not")
	}
}
