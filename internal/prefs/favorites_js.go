//go:build js

package prefs

import (
	"errors"
	"syscall/js"
)

// The browser store: window.localStorage, private to the page's origin.

// DemoFavoriteWS is the browser's default bookmark: the public demo server's
// TLS WebSocket listener. The EU/AP Jetris servers only expose plain NATS
// ports, which a browser cannot dial, so they are not listed here.
var DemoFavoriteWS = Favorite{Label: "Demo.nats.io (US central)", URL: "wss://demo.nats.io:8443"}

func defaultFavorites() []Favorite {
	return []Favorite{DemoFavoriteWS}
}

// favoritesKey is the localStorage key holding the favorites JSON.
const favoritesKey = "jetris.favorites"

// localStorage returns window.localStorage, or an error when the browser
// refuses access (private mode, blocked site data — the accessor throws).
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
	store, err := localStorage()
	if err != nil {
		return nil, false, err
	}
	v := store.Call("getItem", favoritesKey)
	if v.IsNull() || v.IsUndefined() {
		return nil, false, nil
	}
	return []byte(v.String()), true, nil
}

func saveFavoritesData(data []byte) error {
	store, err := localStorage()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("localStorage refused the write (quota?)")
		}
	}()
	store.Call("setItem", favoritesKey, string(data))
	return err
}
