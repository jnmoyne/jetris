//go:build js

package prefs

import "errors"

// The browser store: window.localStorage, beside the handling's key.

// keymapKey is the localStorage key holding the key bindings JSON.
const keymapKey = "jetris.keys"

func loadKeymapData() (data []byte, found bool, err error) {
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", keymapKey)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

func saveKeymapData(data []byte) (err error) {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", keymapKey, string(data))
	return err
}
