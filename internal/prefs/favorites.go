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

// The official servers — the bookmarks every fresh install starts with, and
// the entries of a saved list this source owns (their labels included): the
// Jetris servers in EU central, US west and AP south and the public nats.io
// demo server (US central), each over WebSocket and over plain NATS.
var (
	JetrisEUWS     = Favorite{Label: "Jetris EU central", URL: "wss://eu-central.jetris.net:4223"}
	JetrisUSWestWS = Favorite{Label: "Jetris US west", URL: "wss://us-west.jetris.net:4223"}
	JetrisAPWS     = Favorite{Label: "Jetris AP south", URL: "wss://ap-south.jetris.net:4223"}
	JetrisUSWS     = Favorite{Label: "Demo.nats.io (US central)", URL: "wss://demo.nats.io:8443"}
	JetrisEU       = Favorite{Label: "Jetris EU central", URL: "nats://eu-central.jetris.net:4222"}
	JetrisUSWest   = Favorite{Label: "Jetris US west", URL: "nats://us-west.jetris.net:4222"}
	JetrisAP       = Favorite{Label: "Jetris AP south", URL: "nats://ap-south.jetris.net:4222"}
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

// The official servers of the releases before the jetris.net names, under
// the jetris.johnnyxmas.com ones: retired (retiredDefaults), and what the
// releases before the seeded-defaults marker offered (originalDefaults).
var (
	oldJetrisEUWS     = Favorite{Label: "Jetris EU central", URL: "wss://eu-central.jetris.johnnyxmas.com:4223"}
	oldJetrisUSWestWS = Favorite{Label: "Jetris US west", URL: "wss://us-west.jetris.johnnyxmas.com:4223"}
	oldJetrisAPWS     = Favorite{Label: "Jetris AP south", URL: "wss://ap-south.jetris.johnnyxmas.com:4223"}
	oldJetrisEU       = Favorite{Label: "Jetris EU central", URL: "nats://eu-central.jetris.johnnyxmas.com:4222"}
	oldJetrisUSWest   = Favorite{Label: "Jetris US west", URL: "nats://us-west.jetris.johnnyxmas.com:4222"}
	oldJetrisAP       = Favorite{Label: "Jetris AP south", URL: "nats://ap-south.jetris.johnnyxmas.com:4222"}
)

// originalDefaults are the defaults of the releases before the seeded-defaults
// marker existed (the jetris.johnnyxmas.com names, before US west joined):
// what a saved favorites list without a marker (loadSeededData finds
// nothing) is taken to have been offered already. Their absence from such a
// list means the player deleted them, and they must stay deleted. Only the
// demo server is still a default; the rest are retired, so listing them
// here just keeps the record straight.
var originalDefaults = []Favorite{oldJetrisEUWS, oldJetrisAPWS, JetrisUSWS, oldJetrisEU, oldJetrisAP, JetrisUS}

// retiredDefaults are the official servers of earlier releases that have
// since left DefaultFavorites — the Jetris servers under their
// jetris.johnnyxmas.com names, and by their bare IPs before they had names —
// as they were shipped. A favorites list saved by one of those releases (a
// binary's file, a browser's localStorage) may still carry them, and nothing
// else tells them from the player's own bookmarks: they are what Outdated
// flags, so the login screen can tag them and offer to remove them
// (RemoveOutdated). Never dropped on load — the player decides.
//
// Maintenance: when an official server changes URL or leaves the defaults,
// move its old entry here (the new URL, if any, reaches existing lists
// through seedNewDefaults). A URL both here and in DefaultFavorites counts
// as current.
var retiredDefaults = []Favorite{
	oldJetrisEUWS, oldJetrisUSWestWS, oldJetrisAPWS, oldJetrisEU, oldJetrisUSWest, oldJetrisAP,
	{Label: "Jetris (EU central)", URL: "nats://172.105.76.148:4222"},
	{Label: "Jetris EU central", URL: "nats://172.239.19.14:4222"},
	{Label: "Jetris EU central", URL: "ws://172.239.19.14:4223"},
	{Label: "Jetris (AP south)", URL: "nats://172.104.52.4:4222"},
	{Label: "Jetris AP south", URL: "nats://172.104.188.44:4222"},
	{Label: "Jetris AP south", URL: "ws://172.104.188.44:4223"},
}

// Official reports whether url is one of the current official servers
// (DefaultFavorites).
func Official(url string) bool {
	return slices.ContainsFunc(DefaultFavorites(), func(f Favorite) bool { return f.URL == url })
}

// Outdated reports whether url was an official server of an earlier release
// and is one no longer (retiredDefaults): a saved favorite worth cleaning up.
func Outdated(url string) bool {
	return !Official(url) && slices.ContainsFunc(retiredDefaults, func(f Favorite) bool { return f.URL == url })
}

// RemoveOutdated returns favs without its outdated entries (Outdated), the
// rest in their order, and how many it dropped. The result is never nil.
func RemoveOutdated(favs []Favorite) ([]Favorite, int) {
	out := make([]Favorite, 0, len(favs))
	for _, f := range favs {
		if !Outdated(f.URL) {
			out = append(out, f)
		}
	}
	return out, len(favs) - len(out)
}

// LoadFavorites reads the saved favorites — from ~/.config/jetris on the
// desktop, from the browser's localStorage in the wasm build. A missing store
// is not an error: it yields DefaultFavorites (a fresh install starts with
// the default servers). A store that exists but lists nothing yields an empty
// list — the player deleted every bookmark on purpose, and the defaults must
// not come back. Entries without a URL are dropped; a missing label falls
// back to the URL.
//
// The official list is the source's, and a saved list follows it: a default
// server added in a later release reaches existing installs too — the store
// remembers which default URLs it has offered (the seeded marker, written
// with every save), and a saved list is loaded with every default it was
// never offered inserted in its default position, once — so a player who
// then deletes it is not handed it again; and an official server the player
// still has is listed by its current label, whatever the release that saved
// it called it (refreshOfficialLabels). Either change is saved back. What
// is never done for the player is removing a server: one that left the
// official list stays in the saved list, flagged Outdated, until they clean
// it up.
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
	relabeled := refreshOfficialLabels(merged)
	if added || relabeled {
		if err := SaveFavorites(merged); err != nil {
			return merged, err
		}
	}
	return merged, nil
}

// refreshOfficialLabels gives every favorite that is a current official
// server (Official) the label the source ships it with, and reports whether
// it changed any: a server renamed in a later release reads by its new name
// in a list saved under the old one. An official server's label is not the
// player's to keep — there is no renaming in the login screen, only the
// source names these.
func refreshOfficialLabels(favs []Favorite) bool {
	changed := false
	for i := range favs {
		for _, d := range DefaultFavorites() {
			if favs[i].URL == d.URL && favs[i].Label != d.Label {
				favs[i].Label = d.Label
				changed = true
			}
		}
	}
	return changed
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
