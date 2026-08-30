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

	want := Handling{DASMs: 85, ARRMs: 35}
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

	if err := SaveHandling(Handling{DASMs: 9000, ARRMs: -5}); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if h != (Handling{DASMs: MaxHandlingMs, ARRMs: 0}) {
		t.Fatalf("clamped handling = %+v, want {%d 0}", h, MaxHandlingMs)
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

	if err := os.WriteFile(path, []byte(`{"das_ms": 500, "arr_ms": 20}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHandling()
	if err != nil {
		t.Fatal(err)
	}
	if h != (Handling{DASMs: MaxHandlingMs, ARRMs: 20}) {
		t.Fatalf("hand-edited file: handling = %+v, want {%d 20}", h, MaxHandlingMs)
	}
}
