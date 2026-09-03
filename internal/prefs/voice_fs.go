//go:build !js

package prefs

import (
	"errors"
	"os"
	"path/filepath"
)

// The desktop store: a JSON file beside handling.json.

// voiceFile is the on-disk voice settings, relative to the config parent.
const voiceFile = "jetris/voice.json"

// VoicePath is the absolute path of the voice file.
func VoicePath() (string, error) {
	parent, err := configParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, voiceFile), nil
}

// loadVoiceData reads the file; found is false when it does not exist.
func loadVoiceData() (data []byte, found bool, err error) {
	path, err := VoicePath()
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

// saveVoiceData writes the file, creating the jetris/ directory on first
// use.
func saveVoiceData(data []byte) error {
	path, err := VoicePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
