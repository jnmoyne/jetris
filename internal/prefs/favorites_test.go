package prefs

import (
	"os"
	"path/filepath"
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
	if len(favs) != 3 || favs[0] != DemoFavorite || favs[1] != DemoFavoriteEU || favs[2] != DemoFavoriteAP {
		t.Fatalf("fresh favorites = %+v, want the US demo server then the EU and AP Jetris servers", favs)
	}

	want := []Favorite{DemoFavorite, {Label: "home", URL: "nats://192.168.1.5:4222"}}
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
	if len(got) != 1 || got[0].URL != "nats://a:4222" || got[0].Label != "nats://a:4222" {
		t.Fatalf("tolerant load = %+v, want one entry labeled by its URL", got)
	}

	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = LoadFavorites()
	if err == nil {
		t.Fatal("corrupt favorites file loaded without error")
	}
	if len(got) != 3 || got[0] != DemoFavorite || got[1] != DemoFavoriteEU || got[2] != DemoFavoriteAP {
		t.Fatalf("corrupt file fallback = %+v, want the defaults", got)
	}
}
