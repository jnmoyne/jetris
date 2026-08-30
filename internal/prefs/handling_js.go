//go:build js

package prefs

import "errors"

// The browser store: window.localStorage, beside the favorites' key.

// handlingKey is the localStorage key holding the handling JSON.
const handlingKey = "jetris.handling"

func loadHandlingData() (data []byte, found bool, err error) {
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", handlingKey)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

func saveHandlingData(data []byte) (err error) {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", handlingKey, string(data))
	return err
}
