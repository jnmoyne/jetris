// Package prefs persists the player's local preferences — the NATS server
// browser's favorites and the keyboard's DAS/ARR handling tuning. Files live
// under the same parent directory the NATS CLI uses for its contexts
// (<XDG_CONFIG_HOME|~/.config>), in a jetris/ subdirectory, so a machine's
// NATS configuration and its Jetris preferences sit side by side.
package prefs

import (
	"encoding/json"
	"slices"
	"strings"
)

// Favorite is one bookmarked NATS server in the login screen's server browser:
// a display label and the URL Jetris dials for it.
type Favorite struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// The bookmarks every fresh install starts with: the Jetris servers in EU
// central, US west and AP south and the public nats.io demo server (US
// central), each over WebSocket and over plain NATS.
var (
	JetrisEUWS     = Favorite{Label: "Jetris EU central", URL: "wss://eu-central.jetris.johnnyxmas.com:4223"}
	JetrisUSWestWS = Favorite{Label: "Jetris US west", URL: "wss://us-west.jetris.johnnyxmas.com:4223"}
	JetrisAPWS     = Favorite{Label: "Jetris AP south", URL: "wss://ap-south.jetris.johnnyxmas.com:4223"}
	JetrisUSWS     = Favorite{Label: "Demo.nats.io (US central)", URL: "wss://demo.nats.io:8443"}
	JetrisEU       = Favorite{Label: "Jetris EU central", URL: "nats://eu-central.jetris.johnnyxmas.com:4222"}
	JetrisUSWest   = Favorite{Label: "Jetris US west", URL: "nats://us-west.jetris.johnnyxmas.com:4222"}
	JetrisAP       = Favorite{Label: "Jetris AP south", URL: "nats://ap-south.jetris.johnnyxmas.com:4222"}
	JetrisUS       = Favorite{Label: "Demo.nats.io (US central)", URL: "nats://demo.nats.io:4222"}
)

// DefaultFavorites is the pre-populated favorites list of a fresh install, in
// display order: the four WebSocket entries, then their nats:// counterparts
// — the same list on the desktop and in the browser. The desktop dials both
// kinds (nats.go speaks WebSocket natively); a browser can only reach the
// WebSocket rows, and lists the others greyed out (nats.Dialable), which is
// why those come first: the first favorite the build can dial is the login
// screen's default selection.
func DefaultFavorites() []Favorite {
	return []Favorite{JetrisEUWS, JetrisUSWestWS, JetrisAPWS, JetrisUSWS, JetrisEU, JetrisUSWest, JetrisAP, JetrisUS}
}

// originalDefaults are the defaults of the first release, before any server
// was added to the list: what a saved favorites list that predates the
// seeded-defaults marker (loadSeededData finds nothing) is taken to have
// been offered already. Their absence from such a list means the player
// deleted them, and they must stay deleted.
var originalDefaults = []Favorite{JetrisEUWS, JetrisAPWS, JetrisUSWS, JetrisEU, JetrisAP, JetrisUS}

// LoadFavorites reads the saved favorites — from ~/.config/jetris on the
// desktop, from the browser's localStorage in the wasm build. A missing store
// is not an error: it yields DefaultFavorites (a fresh install starts with
// the default servers). A store that exists but lists nothing yields an empty
// list — the player deleted every bookmark on purpose, and the defaults must
// not come back. Entries without a URL are dropped; a missing label falls
// back to the URL.
//
// A default server added in a later release reaches existing installs too:
// the store remembers which default URLs it has offered (the seeded marker,
// written with every save), and a saved list is loaded with every default
// it was never offered inserted in its default position, once — the list is
// saved back with the newcomer and the marker brought up to date, so a
// player who then deletes it is not handed it again.
func LoadFavorites() ([]Favorite, error) {
	data, found, err := loadFavoritesData()
	if err != nil {
		return DefaultFavorites(), err
	}
	if !found {
		return DefaultFavorites(), nil
	}
	favs, err := decodeFavorites(data)
	if err != nil {
		return favs, err
	}
	merged, added := seedNewDefaults(favs, loadSeeded())
	if added {
		if err := SaveFavorites(merged); err != nil {
			return merged, err
		}
	}
	return merged, nil
}

// seedNewDefaults inserts into favs every default whose URL is neither in
// seeded (offered before) nor already in the list, right after the nearest
// preceding default that is in the list (at the front when none is), and
// reports whether it added anything.
func seedNewDefaults(favs []Favorite, seeded []string) ([]Favorite, bool) {
	has := func(url string) bool {
		return slices.ContainsFunc(favs, func(f Favorite) bool { return f.URL == url })
	}
	added := false
	defaults := DefaultFavorites()
	for i, d := range defaults {
		if slices.Contains(seeded, d.URL) || has(d.URL) {
			continue
		}
		at := 0
		for j := i - 1; j >= 0; j-- {
			if k := slices.IndexFunc(favs, func(f Favorite) bool { return f.URL == defaults[j].URL }); k >= 0 {
				at = k + 1
				break
			}
		}
		favs = slices.Insert(favs, at, d)
		added = true
	}
	return favs, added
}

// SaveFavorites writes the favorites list. An empty (or nil) list is written
// as "[]" so that LoadFavorites keeps it empty instead of reviving the
// defaults. The seeded marker is written alongside: every current default
// has now been offered.
func SaveFavorites(favs []Favorite) error {
	if favs == nil {
		favs = []Favorite{}
	}
	data, err := json.MarshalIndent(favs, "", "  ")
	if err != nil {
		return err
	}
	if err := saveFavoritesData(append(data, '\n')); err != nil {
		return err
	}
	return saveSeeded()
}

// loadSeeded returns the default URLs the store has offered so far: the
// marker's list, or originalDefaults for a store without one (a list saved
// before the marker existed).
func loadSeeded() []string {
	data, found, err := loadSeededData()
	var urls []string
	if err == nil && found && json.Unmarshal(data, &urls) == nil {
		return urls
	}
	for _, f := range originalDefaults {
		urls = append(urls, f.URL)
	}
	return urls
}

// saveSeeded marks every current default as offered.
func saveSeeded() error {
	urls := make([]string, 0, len(DefaultFavorites()))
	for _, f := range DefaultFavorites() {
		urls = append(urls, f.URL)
	}
	data, err := json.Marshal(urls)
	if err != nil {
		return err
	}
	return saveSeededData(append(data, '\n'))
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
