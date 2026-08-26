//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file under the config directory.

// defaultFavorites: US, then EU, then AP.
func defaultFavorites() []Favorite {
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

// loadFavoritesData reads the file; found is false when it does not exist.
func loadFavoritesData() (data []byte, found bool, err error) {
	path, err := FavoritesPath()
	if err != nil {
		return nil, false, err
	}
	data, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// saveFavoritesData writes the file, creating the jetris/ directory on first
// use.
func saveFavoritesData(data []byte) error {
	path, err := FavoritesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
