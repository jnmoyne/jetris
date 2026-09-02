//go:build js

package prefs

import "errors"

// The browser store: window.localStorage, beside the favorites' and the
// handling's keys.

// panelsKey is the localStorage key holding the panel JSON.
const panelsKey = "jetris.panels"

func loadPanelsData() (data []byte, found bool, err error) {
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", panelsKey)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

func savePanelsData(data []byte) (err error) {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", panelsKey, string(data))
	return err
}
