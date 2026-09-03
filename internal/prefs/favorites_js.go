//go:build js

package prefs

import (
	"errors"
	"syscall/js"
)

// The browser store: window.localStorage, private to the page's origin.

// favoritesKey is the localStorage key holding the favorites JSON; seededKey
// holds the default URLs the list has been offered (LoadFavorites' seeded
// marker).
const (
	favoritesKey = "jetris.favorites"
	seededKey    = "jetris.favorites.seeded"
)

// localStorage returns window.localStorage, or an error when the browser
// refuses access (private mode, blocked site data — the accessor throws).
// ConfigDir has no directory to name in the browser: the stores live in
// localStorage, and nothing else wants a home here.
func ConfigDir() (string, error) {
	return "", errors.New("no config directory in the browser")
}

func localStorage() (store js.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage is not available")
		}
	}()
	store = js.Global().Get("localStorage")
	if !store.Truthy() {
		return store, errors.New("localStorage is not available")
	}
	return store, nil
}

func loadFavoritesData() (data []byte, found bool, err error) {
	return readStorageKey(favoritesKey)
}

func saveFavoritesData(data []byte) error {
	return writeStorageKey(favoritesKey, data)
}

func loadSeededData() (data []byte, found bool, err error) {
	return readStorageKey(seededKey)
}

func saveSeededData(data []byte) error {
	return writeStorageKey(seededKey, data)
}

// readStorageKey reads one localStorage item; found is false when it is not
// set.
func readStorageKey(key string) (data []byte, found bool, err error) {
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", key)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

// writeStorageKey sets one localStorage item.
func writeStorageKey(key string, data []byte) (err error) {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", key, string(data))
	return err
}
