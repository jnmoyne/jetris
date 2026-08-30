//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file beside favorites.json.

// handlingFile is the on-disk handling tuning, relative to the config parent.
const handlingFile = "jetris/handling.json"

// HandlingPath is the absolute path of the handling file.
func HandlingPath() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, handlingFile), nil
}

// loadHandlingData reads the file; found is false when it does not exist.
func loadHandlingData() (data []byte, found bool, err error) {
	path, err := HandlingPath()
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

// saveHandlingData writes the file, creating the jetris/ directory on first
// use.
func saveHandlingData(data []byte) error {
	path, err := HandlingPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
