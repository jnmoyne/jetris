package prefs

import "encoding/json"

// The panel preferences: which of the two screens' switchable panels are
// showing — the game screen's menu column, opponents' boards, on-screen pad
// and chat strip, and the lobby's menu column, players column and chat strip
// (nativeui/panels.go). Flipped by the bar buttons and kept across launches,
// so a player who puts a panel away finds it away next time.

// Panels is the standing answer for each of the two screens' switches. Every
// one of them starts on: a fresh install shows everything the screen has, and
// a panel is away only because the player put it away.
type Panels struct {
	// The game screen's four (gamescreen.go).
	Menu      bool `json:"menu"`
	Opponents bool `json:"opponents"`
	Pad       bool `json:"pad"`
	Chat      bool `json:"chat"`
	// The lobby's three (lobby.go). They are the LOBBY's own and not the
	// game's: the two screens stand different things beside their content,
	// and a menu shut over a playfield says nothing about whether one is
	// wanted beside a list of games.
	LobbyMenu    bool `json:"lobby_menu"`
	LobbyPlayers bool `json:"lobby_players"`
	LobbyChat    bool `json:"lobby_chat"`
}

// DefaultPanels is a fresh install's screens: every panel showing.
func DefaultPanels() Panels {
	return Panels{
		Menu: true, Opponents: true, Pad: true, Chat: true,
		LobbyMenu: true, LobbyPlayers: true, LobbyChat: true,
	}
}

// LoadPanels reads the saved switches — from ~/.config/jetris on the desktop,
// from the browser's localStorage in the wasm build. A missing store is not an
// error: a fresh install starts at DefaultPanels. A store that exists is taken
// exactly as written, false included — a panel hidden on purpose must not come
// back — while a switch the file predates keeps its default and shows, because
// decoding starts from DefaultPanels rather than from a zero value.
func LoadPanels() (Panels, error) {
	data, found, err := loadPanelsData()
	if err != nil || !found {
		return DefaultPanels(), err
	}
	p := DefaultPanels()
	if err := json.Unmarshal(data, &p); err != nil {
		return DefaultPanels(), err
	}
	return p, nil
}

// SavePanels writes the switches — all of them, every time one is flipped.
func SavePanels(p Panels) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return savePanelsData(append(data, '\n'))
}
