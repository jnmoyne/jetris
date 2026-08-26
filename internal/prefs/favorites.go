// Package prefs persists the player's local preferences — today, the NATS
// server browser's favorites. Files live under the same parent directory the
// NATS CLI uses for its contexts (<XDG_CONFIG_HOME|~/.config>), in a jetris/
// subdirectory, so a machine's NATS configuration and its Jetris preferences
// sit side by side.
package prefs

import (
	"encoding/json"
	"strings"
)

// Favorite is one bookmarked NATS server in the login screen's server browser:
// a display label and the URL Jetris dials for it.
type Favorite struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// The bookmarks every fresh install starts with: the public nats.io demo
// server (US central) and the Jetris servers in EU central and AP south.
var (
	DemoFavorite   = Favorite{Label: "Demo.nats.io (US central)", URL: "nats://demo.nats.io:4222"}
	DemoFavoriteEU = Favorite{Label: "Jetris (EU central)", URL: "nats://172.239.19.14:4222"}
	DemoFavoriteAP = Favorite{Label: "Jetris (AP south)", URL: "nats://172.104.188.44:4222"}
)

// DefaultFavorites is the pre-populated favorites list of a fresh install:
// US, then EU, then AP on the desktop; the browser build lists only the
// servers reachable over WebSocket (see favorites_js.go).
func DefaultFavorites() []Favorite {
	return defaultFavorites()
}

// LoadFavorites reads the saved favorites — from ~/.config/jetris on the
// desktop, from the browser's localStorage in the wasm build. A missing store
// is not an error: it yields DefaultFavorites (a fresh install starts with
// the default servers). A store that exists but lists nothing yields an empty
// list — the player deleted every bookmark on purpose, and the defaults must
// not come back. Entries without a URL are dropped; a missing label falls
// back to the URL.
func LoadFavorites() ([]Favorite, error) {
	data, found, err := loadFavoritesData()
	if err != nil {
		return DefaultFavorites(), err
	}
	if !found {
		return DefaultFavorites(), nil
	}
	return decodeFavorites(data)
}

// SaveFavorites writes the favorites list. An empty (or nil) list is written
// as "[]" so that LoadFavorites keeps it empty instead of reviving the
// defaults.
func SaveFavorites(favs []Favorite) error {
	if favs == nil {
		favs = []Favorite{}
	}
	data, err := json.MarshalIndent(favs, "", "  ")
	if err != nil {
		return err
	}
	return saveFavoritesData(append(data, '\n'))
}

func decodeFavorites(data []byte) ([]Favorite, error) {
	var favs []Favorite
	if err := json.Unmarshal(data, &favs); err != nil {
		return DefaultFavorites(), err
	}
	out := make([]Favorite, 0, len(favs))
	for _, f := range favs {
		f.URL = strings.TrimSpace(f.URL)
		f.Label = strings.TrimSpace(f.Label)
		if f.URL == "" {
			continue
		}
		if f.Label == "" {
			f.Label = f.URL
		}
		out = append(out, f)
	}
	return out, nil
}
