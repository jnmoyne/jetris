//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file beside favorites.json and handling.json.

// panelsFile is the on-disk panel state, relative to the config parent.
const panelsFile = "jetris/panels.json"

// PanelsPath is the absolute path of the panels file.
func PanelsPath() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, panelsFile), nil
}

// loadPanelsData reads the file; found is false when it does not exist.
func loadPanelsData() (data []byte, found bool, err error) {
	path, err := PanelsPath()
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

// savePanelsData writes the file, creating the jetris/ directory on first use.
func savePanelsData(data []byte) error {
	path, err := PanelsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
