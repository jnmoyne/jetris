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

// A favorites list saved before the seeded marker existed (the
// jetris.johnnyxmas.com names, the demo server deleted) gets every current
// default once, in its default place, and keeps what it had (the old names
// stay, outdated, until the player cleans up); deleting a newcomer
// afterwards sticks, and defaults the player had already deleted do not
// come back.
func TestFavoritesSeedNewDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, favoritesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	home := Favorite{Label: "home", URL: "nats://192.168.1.5:4222"}
	old, _ := json.Marshal([]Favorite{oldJetrisEUWS, oldJetrisAPWS, home, oldJetrisEU})
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	want := []Favorite{JetrisEUWS, JetrisUSWestWS, JetrisAPWS, JetrisEU, JetrisUSWest, JetrisAP, oldJetrisEUWS, oldJetrisAPWS, home, oldJetrisEU}
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
	if !slices.Equal(got, []Favorite{JetrisEUWS, JetrisUSWestWS, JetrisAPWS, JetrisEU, JetrisUSWest, JetrisAP, home}) {
		t.Fatalf("seeding a list without the originals = %+v, want the Jetris servers first, the deleted demo server still gone", got)
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
	// The file predates the seeded marker, so the six Jetris servers are
	// offered too; the hand-edited entries come through as one, labeled by
	// its URL.
	if len(got) != 7 || got[6].URL != "nats://a:4222" || got[6].Label != "nats://a:4222" {
		t.Fatalf("tolerant load = %+v, want the Jetris servers then one entry labeled by its URL", got)
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

// A list saved by an earlier release still carries that release's official
// servers — the Jetris servers by their old IPs: they load untouched (the
// player decides what goes), flagged Outdated, and RemoveOutdated drops
// exactly them, keeping the rest in order. The player's own bookmarks and
// the current official servers are never outdated.
func TestFavoritesOutdated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, favoritesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, r := range retiredDefaults {
		if Official(r.URL) {
			t.Fatalf("%s is retired and official at once: move it out of retiredDefaults", r.URL)
		}
		if !Outdated(r.URL) {
			t.Fatalf("Outdated(%s) = false for a retired default", r.URL)
		}
	}
	for _, d := range DefaultFavorites() {
		if !Official(d.URL) || Outdated(d.URL) {
			t.Fatalf("%s: Official=%v Outdated=%v, want a current default official and not outdated", d.URL, Official(d.URL), Outdated(d.URL))
		}
	}
	home := Favorite{Label: "home", URL: "nats://192.168.1.5:4222"}
	if Official(home.URL) || Outdated(home.URL) {
		t.Fatal("a bookmark of the player's own is neither official nor outdated")
	}

	oldEU := Favorite{Label: "Jetris EU central", URL: "ws://172.239.19.14:4223"}
	oldAP := oldJetrisAP
	old, _ := json.Marshal([]Favorite{oldEU, home, JetrisUSWS, oldAP})
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Favorite{oldEU, oldAP, home} {
		if !slices.Contains(got, f) {
			t.Fatalf("loaded favorites = %+v: %s went missing (loading must not remove anything)", got, f.URL)
		}
	}
	cleaned, n := RemoveOutdated(got)
	if n != 2 || slices.ContainsFunc(cleaned, func(f Favorite) bool { return Outdated(f.URL) }) {
		t.Fatalf("RemoveOutdated dropped %d of %+v, leaving %+v; want the two old IPs gone", n, got, cleaned)
	}
	if len(cleaned) != len(got)-2 || !slices.Contains(cleaned, home) || !slices.Contains(cleaned, JetrisUSWS) {
		t.Fatalf("RemoveOutdated left %+v, want everything but the old IPs, in order", cleaned)
	}
	if i, j := slices.Index(cleaned, home), slices.Index(cleaned, JetrisUSWS); i > j {
		t.Fatalf("RemoveOutdated reordered the list: %+v", cleaned)
	}
	if out, n := RemoveOutdated(nil); out == nil || n != 0 {
		t.Fatalf("RemoveOutdated(nil) = %#v, %d; want an empty non-nil list", out, n)
	}
}

// An official server renamed in a later release reads by its current label
// in a list saved under the old one, and the list is saved back that way;
// the player's own labels are left alone.
func TestFavoritesRefreshOfficialLabels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, favoritesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	home := Favorite{Label: "home", URL: "nats://192.168.1.5:4222"}
	if err := SaveFavorites([]Favorite{{Label: "Jetris US central (demo.nats.io)", URL: JetrisUSWS.URL}, home}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []Favorite{JetrisUSWS, home}) {
		t.Fatalf("loaded favorites = %+v, want the demo server under its current label and home untouched", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []Favorite
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved, got) {
		t.Fatalf("file after load = %+v, want the relabeled list saved back", saved)
	}
}
