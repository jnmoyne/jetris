//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file beside handling.json.

// keymapFile is the on-disk key bindings, relative to the config parent.
const keymapFile = "jetris/keys.json"

// KeymapPath is the absolute path of the key bindings file.
func KeymapPath() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, keymapFile), nil
}

// loadKeymapData reads the file; found is false when it does not exist.
func loadKeymapData() (data []byte, found bool, err error) {
	path, err := KeymapPath()
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

// saveKeymapData writes the file, creating the jetris/ directory on first
// use.
func saveKeymapData(data []byte) error {
	path, err := KeymapPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
