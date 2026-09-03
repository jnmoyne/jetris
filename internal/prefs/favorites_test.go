//go:build !js

// The filesystem store (favorites_fs.go); the browser build keeps favorites
// in localStorage and has no file to exercise.

package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A fresh install (no file) starts with the default servers; after the player
// deletes every favorite, the saved empty list stays empty on reload.
func TestFavoritesRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	favs, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(favs, DefaultFavorites()) {
		t.Fatalf("fresh favorites = %+v, want the defaults %+v", favs, DefaultFavorites())
	}

	want := []Favorite{JetrisEUWS, {Label: "home", URL: "nats://192.168.1.5:4222"}}
	if err := SaveFavorites(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("reloaded favorites = %+v, want %+v", got, want)
	}

	if err := SaveFavorites(nil); err != nil {
		t.Fatal(err)
	}
	got, err = LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("favorites after deleting all = %+v, want none (the defaults must not come back)", got)
	}
}

// A favorites list saved before US west joined the defaults (no seeded
// marker) gets it once, in its default place; deleting it afterwards sticks,
// and defaults the player had already deleted do not come back.
func TestFavoritesSeedNewDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, favoritesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	home := Favorite{Label: "home", URL: "nats://192.168.1.5:4222"}
	old, _ := json.Marshal([]Favorite{JetrisEUWS, JetrisAPWS, home, JetrisEU})
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	want := []Favorite{JetrisEUWS, JetrisUSWestWS, JetrisAPWS, home, JetrisEU, JetrisUSWest}
	if !slices.Equal(got, want) {
		t.Fatalf("seeded favorites = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, seededFile)); err != nil {
		t.Fatalf("seeded marker not written: %v", err)
	}

	// Reloading is stable, and the deleted demo server stays deleted.
	again, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again, want) {
		t.Fatalf("reloaded favorites = %+v, want %+v", again, want)
	}

	// The player deletes US west: it is not offered a second time.
	if err := SaveFavorites([]Favorite{JetrisEUWS, JetrisAPWS}); err != nil {
		t.Fatal(err)
	}
	got, err = LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []Favorite{JetrisEUWS, JetrisAPWS}) {
		t.Fatalf("favorites after deleting US west = %+v, want it gone for good", got)
	}

	// A list that lost every original default still gets the newcomer, at
	// the front.
	if err := os.WriteFile(path, []byte(`[{"label":"home","url":"nats://192.168.1.5:4222"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, seededFile)); err != nil {
		t.Fatal(err)
	}
	got, err = LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []Favorite{JetrisUSWestWS, JetrisUSWest, home}) {
		t.Fatalf("seeding a list without the originals = %+v, want US west first", got)
	}
}

// Hand-edited files: entries without a URL are dropped, a missing label falls
// back to the URL, and unparsable JSON falls back to the defaults with an error.
func TestFavoritesTolerantLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, favoritesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"label":"","url":" nats://a:4222 "},{"label":"nourl"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	// The file predates the seeded marker, so US west is offered too; the
	// hand-edited entries come through as one, labeled by its URL.
	if len(got) != 3 || got[2].URL != "nats://a:4222" || got[2].Label != "nats://a:4222" {
		t.Fatalf("tolerant load = %+v, want US west then one entry labeled by its URL", got)
	}

	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = LoadFavorites()
	if err == nil {
		t.Fatal("corrupt favorites file loaded without error")
	}
	if !slices.Equal(got, DefaultFavorites()) {
		t.Fatalf("corrupt file fallback = %+v, want the defaults", got)
	}
}
