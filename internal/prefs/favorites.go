// Package prefs persists the player's local preferences — today, the NATS
// server browser's favorites. Files live under the same parent directory the
// NATS CLI uses for its contexts (<XDG_CONFIG_HOME|~/.config>), in a jetris/
// subdirectory, so a machine's NATS configuration and its Jetris preferences
// sit side by side.
package prefs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Favorite is one bookmarked NATS server in the login screen's server browser:
// a display label and the URL Jetris dials for it.
type Favorite struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// The bookmarks every fresh install starts with: the public nats.io demo
// server (US central) and the Jetris servers in EU central and AP south.
var (
	DemoFavorite   = Favorite{Label: "Demo.nats.io (US central)", URL: "nats://demo.nats.io:4222"}
	DemoFavoriteEU = Favorite{Label: "Jetris (EU central)", URL: "nats://172.239.19.14:4222"}
	DemoFavoriteAP = Favorite{Label: "Jetris (AP south)", URL: "nats://172.104.188.44:4222"}
)

// DefaultFavorites is the pre-populated favorites list of a fresh install:
// US, then EU, then AP.
func DefaultFavorites() []Favorite {
	return []Favorite{DemoFavorite, DemoFavoriteEU, DemoFavoriteAP}
}

// favoritesFile is the on-disk favorites list, relative to the config parent.
const favoritesFile = "jetris/favorites.json"

// configParent resolves the directory preferences live under: $XDG_CONFIG_HOME,
// else ~/.config — the same resolution the NATS CLI (and nats.ListContexts)
// use for contexts.
func configParent() (string, error) {
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// FavoritesPath is the absolute path of the favorites file.
func FavoritesPath() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, favoritesFile), nil
}

// LoadFavorites reads the saved favorites. A missing file is not an error: it
// yields DefaultFavorites (a fresh install starts with the US, EU and AP
// servers). A file that exists but lists nothing yields an empty list — the
// player deleted every bookmark on purpose, and the defaults must not come
// back.
// Entries without a URL are dropped; a missing label falls back to the URL.
func LoadFavorites() ([]Favorite, error) {
	path, err := FavoritesPath()
	if err != nil {
		return DefaultFavorites(), err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultFavorites(), nil
	}
	if err != nil {
		return DefaultFavorites(), err
	}
	var favs []Favorite
	if err := json.Unmarshal(data, &favs); err != nil {
		return DefaultFavorites(), err
	}
	out := make([]Favorite, 0, len(favs))
	for _, f := range favs {
		f.URL = strings.TrimSpace(f.URL)
		f.Label = strings.TrimSpace(f.Label)
		if f.URL == "" {
			continue
		}
		if f.Label == "" {
			f.Label = f.URL
		}
		out = append(out, f)
	}
	return out, nil
}

// SaveFavorites writes the favorites list, creating the jetris/ directory on
// first use. An empty (or nil) list is written as "[]" so that LoadFavorites
// keeps it empty instead of reviving the defaults.
func SaveFavorites(favs []Favorite) error {
	path, err := FavoritesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if favs == nil {
		favs = []Favorite{}
	}
	data, err := json.MarshalIndent(favs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
