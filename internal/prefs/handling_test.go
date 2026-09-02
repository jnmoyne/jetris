//go:build !js

// The filesystem store (handling_fs.go); the browser build keeps the tuning
// in localStorage and has no file to exercise.

package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh install (no file) starts at the defaults; a saved tuning reloads;
// out-of-range values — saved or hand-edited — come back clamped.
func TestHandlingRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h, err := LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if h != DefaultHandling() {
		t.Fatalf("fresh handling = %+v, want the defaults %+v", h, DefaultHandling())
	}

	want := Handling{DASMs: 85, ARRMs: 35, SDF: 12, DropGuardMs: 45}
	if err := SaveHandling(want); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if h != want {
		t.Fatalf("reloaded handling = %+v, want %+v", h, want)
	}

	if err := SaveHandling(Handling{DASMs: 9000, ARRMs: -5, SDF: 9000, DropGuardMs: 9000}); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Handling{DASMs: MaxHandlingMs, ARRMs: 0, SDF: MaxSDF, DropGuardMs: MaxHandlingMs}); h != want {
		t.Fatalf("clamped handling = %+v, want %+v", h, want)
	}
}

// A tuning saved before the soft drop and the accidental-drop guard had knobs
// of their own: each missing key reads as its default — the default factor,
// not the slowest one; the default guard, not a guard switched off.
func TestHandlingSDFAbsentTakesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, handlingFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"das_ms": 90, "arr_ms": 15}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Handling{DASMs: 90, ARRMs: 15, SDF: DefaultSDF, DropGuardMs: DefaultDropGuardMs}); h != want {
		t.Fatalf("handling from a pre-SDF file = %+v, want %+v", h, want)
	}
}

// The guard's 0 is a value of its own — the knob switched off — and survives
// a save and a reload as such, where an absent key takes the default.
func TestHandlingDropGuardOffPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	off := DefaultHandling()
	off.DropGuardMs = 0
	if err := SaveHandling(off); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if h != off {
		t.Fatalf("reloaded handling = %+v, want the guard off %+v", h, off)
	}
}

// Hand-edited files: unparsable JSON falls back to the defaults with an
// error, and an out-of-range edit is clamped on load.
func TestHandlingTolerantLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, handlingFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHandling()
	if err == nil {
		t.Fatal("unparsable file: want an error")
	}
	if h != DefaultHandling() {
		t.Fatalf("unparsable file: handling = %+v, want the defaults", h)
	}

	if err := os.WriteFile(path, []byte(`{"das_ms": 500, "arr_ms": 20, "sdf": 99, "drop_guard_ms": 500}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Handling{DASMs: MaxHandlingMs, ARRMs: 20, SDF: MaxSDF, DropGuardMs: MaxHandlingMs}); h != want {
		t.Fatalf("hand-edited file: handling = %+v, want %+v", h, want)
	}
}
