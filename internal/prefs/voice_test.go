//go:build !js

// The filesystem store (voice_fs.go); the browser build keeps the settings
// in localStorage and has no file to exercise.

package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh install starts at the defaults; a saved set reloads, an open gate
// (0) and a silenced playback (false) included; an out-of-range gate comes
// back clamped.
func TestVoiceRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	v, err := LoadVoice()
	if err != nil {
		t.Fatal(err)
	}
	if v != DefaultVoice() {
		t.Fatalf("fresh voice = %+v, want the defaults %+v", v, DefaultVoice())
	}

	want := Voice{GateDb: 0, Listen: false}
	if err := SaveVoice(want); err != nil {
		t.Fatal(err)
	}
	if v, err = LoadVoice(); err != nil {
		t.Fatal(err)
	}
	if v != want {
		t.Fatalf("reloaded voice = %+v, want %+v", v, want)
	}

	if err := SaveVoice(Voice{GateDb: 900, Listen: true}); err != nil {
		t.Fatal(err)
	}
	if v, err = LoadVoice(); err != nil {
		t.Fatal(err)
	}
	if want := (Voice{GateDb: MaxGateDb, Listen: true}); v != want {
		t.Fatalf("clamped voice = %+v, want %+v", v, want)
	}
}

// A key the file does not carry keeps its default; unparsable JSON falls
// back to the defaults with an error.
func TestVoiceTolerantLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, voiceFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"gate_db": 20}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVoice()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Voice{GateDb: 20, Listen: true}); v != want {
		t.Fatalf("voice from a gate-only file = %+v, want %+v", v, want)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err = LoadVoice(); err == nil {
		t.Fatal("unparsable file: want an error")
	}
	if v != DefaultVoice() {
		t.Fatalf("unparsable file: voice = %+v, want the defaults", v)
	}
}
