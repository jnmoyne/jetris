//go:build js

package prefs

import "errors"

// The browser store: window.localStorage, beside the handling key.

// voiceKey is the localStorage key holding the voice JSON.
const voiceKey = "jetris.voice"

func loadVoiceData() (data []byte, found bool, err error) {
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", voiceKey)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

func saveVoiceData(data []byte) (err error) {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", voiceKey, string(data))
	return err
}
