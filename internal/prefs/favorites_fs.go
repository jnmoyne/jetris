//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file under the config directory.

// favoritesFile is the on-disk favorites list, relative to the config parent;
// seededFile beside it lists the default URLs the favorites list has been
// offered (LoadFavorites' seeded marker).
const (
	favoritesFile = "jetris/favorites.json"
	seededFile    = "jetris/favorites-seeded.json"
)

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

// ConfigDir is the directory every desktop store lives in — ~/.config/jetris,
// or $XDG_CONFIG_HOME/jetris — for whatever else wants a home there: the LAN
// party page's certificate (webdist). Not created here; a store creates it
// on its first write.
func ConfigDir() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, "jetris"), nil
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
	return readConfigFile(favoritesFile)
}

// saveFavoritesData writes the file, creating the jetris/ directory on first
// use.
func saveFavoritesData(data []byte) error {
	return writeConfigFile(favoritesFile, data)
}

// loadSeededData and saveSeededData are the seeded marker's file, beside the
// favorites.
func loadSeededData() (data []byte, found bool, err error) {
	return readConfigFile(seededFile)
}

func saveSeededData(data []byte) error {
	return writeConfigFile(seededFile, data)
}

// readConfigFile reads a file under the config parent; found is false when it
// does not exist.
func readConfigFile(rel string) (data []byte, found bool, err error) {
	parent, err := configParent()
	if err != nil {
		return nil, false, err
	}
	data, err = os.ReadFile(filepath.Join(parent, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// writeConfigFile writes a file under the config parent, creating its
// directory on first use.
func writeConfigFile(rel string, data []byte) error {
	parent, err := configParent()
	if err != nil {
		return err
	}
	path := filepath.Join(parent, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
