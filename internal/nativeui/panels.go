package nativeui

// The screens' switches — what the bar buttons stand for, and where they are
// remembered.
//
// Both screens carry the same idea: one slim bar, and on it a switch for
// every panel that costs the content room. The game screen's four
// (gamescreen.go) are the menu column, the opponents' boards, the on-screen
// pad and the chat strip; the lobby's three (lobby.go) are its own menu
// column, the players column and its own chat strip. They are the LOBBY's own
// and not the game's — the two screens stand different things beside their
// content, and a menu shut over a playfield says nothing about whether one is
// wanted beside a list of games.
//
// Every one of them starts ON. A player who has never touched a switch sees
// everything the screen has, on a desktop and on a phone alike: the game
// arrives whole rather than folded away behind buttons nobody has been told
// about. What a small screen changes is WHERE a panel goes — the menu drawn
// over the board instead of beside it (hudBeside), the pad under the
// playfield instead of next to it, the columns stacked under the panel
// instead of alongside (formfactor.go) — never whether it is there at all.
//
// And a switch, once flipped, stays flipped past this session: every flip
// writes the whole set (prefs.SavePanels — a file under ~/.config/jetris on
// the desktop, localStorage in the browser), and the next launch loads it back
// (SetPanels, from cmd/jetris/main.go). Put the chat away and it stays away.

import "jetris/internal/prefs"

// hudVisible reports whether the game's menu column (the stats, the controls
// legend, the lab switches, Back to Lobby) stands beside the board — or, where
// there is no room to stand beside it, over the board (hudBeside).
func (a *App) hudVisible() bool { return a.hudShown }

// padVisible reports whether the on-screen control pad is laid out at all.
// Note that it is the only one of the four that costs the playfield rows
// rather than columns on a phone held portrait, where it stacks UNDER the
// board — the touch gestures (gesture.go) play the game without it, which is
// what the switch is for.
func (a *App) padVisible() bool { return a.padShown }

// oppVisible reports whether the opponents' playfields show beside the board.
func (a *App) oppVisible() bool { return a.oppShown }

// chatVisible reports whether the chat strip shows under the board.
func (a *App) chatVisible() bool { return a.chatShown }

// lobbyMenuVisible reports whether the lobby's menu column — who we are, which
// server, the address to share while hosting one, Disconnect — stands beside
// the panel (or over it, where there is no room beside: lobbyMenuBeside).
func (a *App) lobbyMenuVisible() bool { return a.lobbyMenuShown }

// lobbyPlayersVisible reports whether everyone in the lobby shows in a column
// beside the panel — the counterpart of the game screen's opponents' boards.
func (a *App) lobbyPlayersVisible() bool { return a.lobbyPlayersShown }

// lobbyChatVisible reports whether the lobby chat strip shows under the panel.
func (a *App) lobbyChatVisible() bool { return a.lobbyChatShown }

// setDefaultPanels puts every switch on — the state a fresh install starts in,
// and the one every App is built with (New) until the saved set arrives.
func (a *App) setDefaultPanels() { a.SetPanels(prefs.DefaultPanels()) }

// SetPanels applies a saved set of switches: the loaded preferences' way in
// (cmd/jetris/main.go), before Run.
func (a *App) SetPanels(p prefs.Panels) {
	a.hudShown, a.oppShown, a.padShown, a.chatShown = p.Menu, p.Opponents, p.Pad, p.Chat
	a.lobbyMenuShown, a.lobbyPlayersShown, a.lobbyChatShown = p.LobbyMenu, p.LobbyPlayers, p.LobbyChat
}

// panels is the current set, as it would be saved.
func (a *App) panels() prefs.Panels {
	return prefs.Panels{
		Menu: a.hudShown, Opponents: a.oppShown, Pad: a.padShown, Chat: a.chatShown,
		LobbyMenu: a.lobbyMenuShown, LobbyPlayers: a.lobbyPlayersShown, LobbyChat: a.lobbyChatShown,
	}
}

// persistPanels saves the switches, both screens' at once. A failure is
// silent, as the handling knobs' is (persistHandling): neither screen has an
// error line, and the in-memory state still drives this session.
func (a *App) persistPanels() {
	if a.panelsSave == nil {
		return
	}
	_ = a.panelsSave(a.panels())
}
