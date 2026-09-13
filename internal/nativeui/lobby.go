package nativeui

import (
	"fmt"
	"image"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/lobby"
	"jetris/internal/voice"
)

// The lobby screen — the game screen's shape, with a list of games where the
// playfield goes.
//
// It is built the same way and out of the same parts (gamescreen.go): one
// slim bar across the top, under it the line of buttons this screen's actions
// live on (lobbyActions), and under that the thing the screen is FOR, with a
// switch in the bar for each thing that costs it room. The menu column (who
// we are, which server, the address to share), the players in the lobby, the
// chat strip — every one of them shows or it does not, and none of
// them is a window: there is no scrim, nothing to dismiss, and the button that
// shows a column is the only thing that hides it. Each starts ON
// (lobbyMenuVisible, lobbyPlayersVisible, lobbyChatVisible — panels.go) and
// stays wherever the player last put it, past this session as well as through
// it, so a lobby opens with all three up on a desktop and on a phone alike;
// what the phone changes is where they go, its menu column standing over the
// panel rather than beside it and its players moving to a strip.
//
// What is left in the middle is one panel with the two lists a lobby has in
// it, on tabs: the games on offer now, and the games already played. They are
// tabs and not two stacked lists because either one can be long — twenty open
// games or two hundred finished ones — and a screen that gives half its
// height to each shows too little of both. The actions that belong to neither
// list stand over them all, on the button line under the bar, where nothing a
// switch does can take them away: Disconnect, Create a new game, How to play.
//
// The brand banner stays over the bar. This is the screen a player lands on
// and the one they leave from, and it is the only one that says what Jetris
// is.

func (a *App) layoutLobby(gtx C) D {
	lb := a.getLobby()
	if lb == nil {
		a.mu.Lock()
		a.screen = screenLogin
		a.mu.Unlock()
		return D{}
	}

	// --- event handling ---
	// Modal overlays: the create-game wizard, the invitee picker (after
	// creating an invite-only game), the incoming-invitation pop-up, and the
	// LAN party's QR code (lanqr.go). Their buttons are dispatched here so a
	// click can't fall through to the lobby underneath.
	wizOpen := a.handleCreateWizard(gtx)
	pickerOpen := a.handleInvitePicker(gtx)
	pendingInvite, inviteOpen := a.handleIncomingInvite(gtx)
	qrOpen := a.handleQRModal(gtx, wizOpen || pickerOpen || inviteOpen)
	// The key bindings dialog (keymap.go), opened from the menu's KEYS
	// legend.
	keysOpen := a.handleKeysModal(gtx, wizOpen || pickerOpen || inviteOpen || qrOpen)
	// An open game's share link (share.go), opened from its row's Share.
	shareOpen := a.handleShareModal(gtx)
	// The How to play tour (tutorial.go) is a modal of its own over the
	// whole screen: while it is up nothing under it takes a press.
	tour := a.tutorialUp()
	modal := wizOpen || pickerOpen || inviteOpen || qrOpen || keysOpen || shareOpen || tour
	// The bar's switches and the panel's tabs, drained before anything is
	// laid out so a column shown or hidden this frame is already in the
	// layout that measures it — and answered only while no modal is up, since
	// the scrim dims the bar without taking its presses.
	a.handleLobbyBarClicks(gtx, modal)
	// The way out, from the action line (lobbyActions): answered only while
	// no modal is up, as every other button on this screen is — the lobby's
	// scrim dims what is under it without taking its presses, and leaving the
	// server is not what a press meant for the wizard should do.
	if a.quitBtn.Clicked(gtx) && !modal {
		go a.quit()
	}
	// The Create button just opens the wizard; the wizard's last step does
	// the actual creating (finishCreateWizard). The previous run's choices
	// stick around as this run's defaults.
	if a.createBtn.Clicked(gtx) && !modal {
		a.createWizStep = wizStepType
		a.mu.Lock()
		a.lobbyErr = "" // a fresh attempt clears the previous failure strip
		a.mu.Unlock()
		wizOpen = true
	}
	// How to play opens the tour, from a lobby with nothing else up.
	if a.tutBtn.Clicked(gtx) && !modal {
		a.startTutorial()
	}
	a.handleChatSubmit(gtx)

	gamesByID, presence, archiveRecs, logEntries := lb.Games(), lb.Players(), lb.Archives(), lb.LogEntries()
	abandoned := lb.AbandonedGames()
	if tour {
		// The tour's own lobby in place of the server's: every row it
		// points at is there whatever the server holds.
		gamesByID, presence, archiveRecs, logEntries = a.tutorialLobby()
		abandoned = nil
	}
	games := sortedGames(gamesByID)
	players := sortedPlayers(presence)
	// The lobby's voice session (voice.go): the mic button, the menu's
	// VOICE section, who is heard beside the names.
	vs := a.voiceFrame(voice.ChannelAll)
	voiceRepaints(gtx, vs, a.lobbyMenuVisible())

	// The lobby screen shows only lobby-scoped messages; per-game messages
	// (GameID != "") appear on that game's screen instead.
	a.mu.Lock()
	chat := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if m.GameID == "" {
			chat = append(chat, m)
		}
	}
	connName, connURL := a.connName, a.connURL
	msg := a.lobbyErr
	a.mu.Unlock()

	// dispatch per-game buttons
	for _, g := range games {
		btns := a.gameButtons(g.GameID)
		if btns.join.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 0)
		}
		for t := 0; t < g.Teams(); t++ {
			if btns.joinTeam[t].Clicked(gtx) {
				id, team := g.GameID, t
				go a.joinGame(id, team)
			}
		}
		if btns.spectate.Clicked(gtx) {
			id := g.GameID
			go a.spectateGame(id)
		}
		if btns.share.Clicked(gtx) && !modal {
			a.openGameShare(g.GameID)
			shareOpen = true
		}
		if btns.reinvite.Clicked(gtx) {
			// Re-open the picker for this already-created invite-only game so
			// the creator can invite more players after joining and returning
			// to the lobby.
			a.reopenInvitePicker(g)
		}
		if btns.del.Clicked(gtx) {
			a.confirmDeleteID = g.GameID
		}
		if btns.delYes.Clicked(gtx) {
			id := g.GameID
			a.confirmDeleteID = ""
			go a.deleteGame(id)
		}
		if btns.delNo.Clicked(gtx) {
			a.confirmDeleteID = ""
		}
	}

	// --- render ---
	archives := a.archivesForDisplay(archiveRecs, func(id string) bool { return a.isPinned(lb, id) })
	// Under the bar: whichever columns are switched on, the panel with
	// everything they leave, and the chat strip along the bottom.
	body := func(gtx C) D {
		// Where each column stands, decided once for the whole body: the menu
		// takes its share off the room the panel row has, and the players
		// stand beside the panel only if what is then left is still a panel.
		menuBeside := a.lobbyMenuVisible() && a.lobbyMenuBeside(gtx)
		rowW := gtx.Constraints.Max.X
		if menuBeside {
			rowW -= a.lobbyMenuW(gtx)
		}
		playersBeside := a.lobbyPlayersVisible() && a.lobbyPlayersBeside(gtx, rowW)
		children := []layout.FlexChild{
			layout.Flexed(1, func(gtx C) D {
				// The panel with the players' column beside it when it is
				// switched on and there is room for it. The column's width
				// comes off the panel BEFORE it fits itself, so nothing is
				// squeezed after the fact.
				content := func(gtx C) D {
					panel := func(gtx C) D {
						return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
							return a.lobbyPanel(gtx, games, abandoned, archives, logEntries)
						})
					}
					var d D
					if !playersBeside {
						d = panel(gtx)
					} else {
						pw := a.lobbyPlayersColW(gtx, gtx.Constraints.Max.X)
						d = layout.Flex{}.Layout(gtx,
							layout.Flexed(1, panel),
							layout.Rigid(a.tutMarked(tutLobbyPlayers, func(gtx C) D {
								gtx.Constraints.Min.X, gtx.Constraints.Max.X = pw, pw
								gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
								return a.lobbyPlayersColumn(gtx, players, vs)
							})),
						)
					}
					if !a.lobbyPlayersVisible() {
						// The players put away, whoever is heard is named in
						// a strip over the panel's bottom-left corner
						// (voiceStrip), painted after it, as on the game
						// screen.
						macro := op.Record(gtx.Ops)
						sgtx := gtx
						sgtx.Constraints = layout.Constraints{Max: d.Size}
						sd := a.voiceStrip(sgtx, vs, lobbySpeakerName(players))
						call := macro.Stop()
						if sd.Size != (image.Point{}) {
							defer op.Offset(image.Pt(0, d.Size.Y-sd.Size.Y)).Push(gtx.Ops).Pop()
							call.Add(gtx.Ops)
						}
					}
					return d
				}
				menuW := a.lobbyMenuW(gtx)
				menu := a.tutMarked(tutLobbyMenu, func(gtx C) D {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = menuW, menuW
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
					return a.lobbyMenuColumn(gtx, lb.PlayerName(), connName, connURL, vs)
				})
				switch {
				case !a.lobbyMenuVisible():
					return content(gtx)
				case a.lobbyMenuBeside(gtx):
					// Room for both: the menu stands against the screen's edge
					// and the panel takes what is left.
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(menu),
						layout.Flexed(1, content),
					)
				default:
					// No room to stand beside it (a phone held portrait): the
					// menu is drawn OVER the panel rather than squeezing it
					// down to a column of ellipses. Its own pointer area keeps
					// its presses off the rows underneath, and — as on the
					// game screen — there is no scrim and nothing here closes
					// it but the button that opened it.
					return layout.Stack{}.Layout(gtx,
						layout.Expanded(func(gtx C) D {
							// The slot the panel has when no menu is up: a
							// Stack hands its expanded children a zero
							// minimum, and a panel laid out to its own size
							// would jump the moment the menu came over it.
							gtx.Constraints.Min = gtx.Constraints.Max
							return content(gtx)
						}),
						layout.Expanded(func(gtx C) D {
							return layout.W.Layout(gtx, func(gtx C) D {
								return pointerArea(gtx, &a.lobbyMenuTag, menu)
							})
						}),
					)
				}
			}),
		}
		// The players, where they could not stand beside the panel: a strip of
		// their own across the full width, just above the chat.
		if a.lobbyPlayersVisible() && !playersBeside {
			children = append(children, layout.Rigid(a.tutMarked(tutLobbyPlayers, func(gtx C) D {
				return a.lobbyPlayersStrip(gtx, players, vs)
			})))
		}
		// The chat is a strip along the bottom, the full width of the screen
		// and in the flow, exactly as it is in a game: while it is up the
		// panel simply has that many fewer rows.
		if a.lobbyChatVisible() {
			children = append(children, layout.Rigid(a.tutMarked(tutLobbyChat, func(gtx C) D {
				return a.lobbyChatStrip(gtx, chat)
			})))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
	base := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.lobbyBanner),
		layout.Rigid(func(gtx C) D {
			return a.lobbyBar(gtx, lb.PlayerName(), connName, len(chat) > a.lobbyChatSeen, vs)
		}),
		layout.Rigid(a.lobbyActions),
		layout.Rigid(func(gtx C) D {
			if msg == "" {
				return D{}
			}
			return layout.Center.Layout(gtx, func(gtx C) D {
				return layout.UniformInset(unit.Dp(6)).Layout(gtx, a.pixel(unit.Sp(9), msg, colErr).Layout)
			})
		}),
		layout.Flexed(1, body),
	)
	if !pickerOpen && !inviteOpen && !wizOpen && !qrOpen && !keysOpen && !shareOpen {
		return base
	}
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D { return base }),
		layout.Expanded(func(gtx C) D {
			// Scrim: dim the lobby and swallow clicks behind the modal.
			fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), withAlpha(colBg, 0xc0))
			return D{Size: gtx.Constraints.Max}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			switch {
			case inviteOpen:
				return a.incomingInviteOverlay(gtx, pendingInvite)
			case pickerOpen:
				return a.invitePickerOverlay(gtx)
			case qrOpen:
				return a.qrOverlay(gtx)
			case keysOpen:
				return a.keysOverlay(gtx)
			case shareOpen:
				return a.shareOverlay(gtx)
			default:
				return a.createWizardOverlay(gtx)
			}
		}),
	)
}

// logInvite logs an invitation-send failure without stopping the batch.
func logInvite(err error) { log.Printf("send invite: %v", err) }

// The lobby's three tabs: the games on offer, the games already played, and
// the server log.
const (
	lobbyTabGames   = "games"
	lobbyTabHistory = "history"
	lobbyTabLog     = "log"
)

const (
	// lobbyPanelMinW is the width the games panel will not be pushed under.
	// It is what the widest thing in it — a game row's info line beside its
	// Join and Spectate buttons — needs to stay one row instead of stacking
	// into a paragraph; take more than this off it and the menu is better
	// drawn OVER the panel (lobbyMenuBeside).
	lobbyPanelMinW = 420
	// lobbyPlayersPct/lobbyPlayersMaxW/lobbyPlayersMinW bound the players
	// column: a slice of the screen, never wider than a column of names and
	// states wants, never narrower than one can be read at, and never more
	// than a third of the row. Every dp of it comes off the panel, which is
	// why it is a switch, exactly as the opponents' boards are in a game.
	lobbyPlayersPct  = 26
	lobbyPlayersMaxW = 260
	lobbyPlayersMinW = 170
	// lobbyPlayersStripRows is how many packed lines the players strip shows
	// before it stops growing and scrolls (lobbyPlayersStrip). It is a strip:
	// a busy lobby must not push the games off the screen to list who is in
	// it.
	lobbyPlayersStripRows = 3
)

// lobbyPlayersColW bounds the players column in w dp of room (see
// lobbyPlayersPct).
func (a *App) lobbyPlayersColW(gtx C, w int) int {
	cw := min(w*lobbyPlayersPct/100, gtx.Dp(lobbyPlayersMaxW))
	return min(max(cw, gtx.Dp(lobbyPlayersMinW)), w/3)
}

// lobbyPlayersBeside reports whether the players can stand in a column beside
// the panel in w dp of room — what the panel row has once the menu column has
// taken its share. Only where what is left is still a panel worth reading
// (lobbyPanelMinW): every dp of that column comes off the games list and the
// history table, and at a phone's width the difference is a MODE column
// against a stack of one-letter lines. Where they cannot stand beside it they
// go where the chat goes instead — a strip across the full width, just above
// it — which costs the panel only its own height.

// handleLobbyBarClicks drains the lobby bar's switches and the panel's tabs.
// The presses are drained whether or not they are answered, so a click that
// landed under a modal is spent there rather than arriving the frame it
// closes; modal says to spend them and do nothing.
func (a *App) handleLobbyBarClicks(gtx C, modal bool) {
	// Each switch is just that: the button that shows a column is the button
	// that hides it, and nothing else does.
	// A flip is the player's standing answer, so it outlives the session
	// (panels.go).
	flipped := false
	flip := func(btn *widget.Clickable, shown *bool) {
		n := 0
		for btn.Clicked(gtx) {
			n++
		}
		if n == 0 || modal {
			return
		}
		*shown, flipped = !*shown, true
	}
	flip(&a.barLobbyMenuBtn, &a.lobbyMenuShown)
	flip(&a.barLobbyPlayersBtn, &a.lobbyPlayersShown)
	flip(&a.barLobbyChatBtn, &a.lobbyChatShown)
	if flipped {
		a.persistPanels()
	}
	// The mic button (voice.go): the session's switch, never written out —
	// it lasts the visit to the server. In the click's own frame, as on the
	// game screen — the browser opens the microphone only on the player's
	// gesture.
	mic := 0
	for a.barMicBtn.Clicked(gtx) {
		mic++
	}
	if mic > 0 && !modal {
		a.toggleMic()
	}
	for _, t := range []struct {
		btn *widget.Clickable
		tab string
	}{{&a.lobbyTabBtns[0], lobbyTabGames}, {&a.lobbyTabBtns[1], lobbyTabHistory}, {&a.lobbyTabBtns[2], lobbyTabLog}} {
		n := 0
		for t.btn.Clicked(gtx) {
			n++
		}
		if n > 0 && !modal {
			a.lobbyTab = t.tab
		}
	}
}

// lobbyBar is the lobby's one permanent row — the game bar's twin
// (gamescreen.go), at the same height and out of the same buttons, so the
// chrome does not move under the player between the two screens. The menu
// switch, who we are and which server this session is on, and at the far end
// the switches for the two things that cost the panel room: the players
// column and the chat strip.
func (a *App) lobbyBar(gtx C, playerName, connName string, unread bool, vs voice.Snapshot) D {
	barH := gtx.Dp(gameBarH)
	kids := []layout.FlexChild{
		layout.Rigid(a.tutMarked(tutLobbyMenuBtn, func(gtx C) D {
			return a.barButton(gtx, &a.barLobbyMenuBtn, glyphMenu, a.lobbyMenuVisible())
		})),
		// The voice switch, beside the menu button as on the game screen.
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutLobbyMic, func(gtx C) D {
				return a.micButton(gtx, vs)
			}))
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
				return a.lobbyBarLine(gtx, playerName, connName)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutLobbyPlayersBtn, func(gtx C) D {
				return a.barButton(gtx, &a.barLobbyPlayersBtn, glyphPlayers, a.lobbyPlayersVisible())
			}))
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutLobbyChatBtn, func(gtx C) D {
				d := a.barButton(gtx, &a.barLobbyChatBtn, glyphChat, a.lobbyChatVisible())
				// Unread mark: messages have arrived since the strip last
				// showed them (lobbyChatStrip records what it showed).
				if unread && !a.lobbyChatVisible() {
					dot := gtx.Dp(7)
					fillRect(gtx.Ops, image.Rect(d.Size.X-dot, 0, d.Size.X, dot), colGold)
				}
				return d
			}))
		}),
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = barH, barH
	return background(gtx, colPanel, func(gtx C) D {
		d := layout.Inset{Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
		// A hairline under the bar, so it reads as its own division over the
		// panel rather than as part of it.
		fillRect(gtx.Ops, image.Rect(0, barH-gtx.Dp(2), d.Size.X, barH), colBorder)
		return D{Size: image.Pt(d.Size.X, barH)}
	})
}

// lobbyBarLine is the bar's readout, in the room the buttons leave: who we
// are, then which server this session is on. Whole or not at all, like the
// game bar's (barStats) — a server name cut to "..." is noise, and the menu
// column one press away carries it in full along with its URL.
func (a *App) lobbyBarLine(gtx C, playerName, connName string) D {
	return layout.W.Layout(gtx, func(gtx C) D {
		// A text label handed a tall minimum height takes that height and
		// sits its glyphs at the top of it; zeroed, it is its own height and
		// W centers it on the bar.
		gtx.Constraints.Min.Y = 0
		if connName == "" {
			return a.pixelLabelFit(gtx, unit.Sp(10), playerName, colFg)
		}
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.pixelLabelFit(gtx, unit.Sp(10), playerName, colFg) }),
			layout.Flexed(1, func(gtx C) D {
				txt := "  @ " + connName
				if a.pixelWidth(gtx, unit.Sp(8), txt) > gtx.Constraints.Max.X {
					return D{}
				}
				return a.pixel(unit.Sp(8), txt, colMuted).Layout(gtx)
			}),
		)
	})
}

// lobbyMenuBeside reports whether the menu column can stand beside the panel
// in the area gtx measures: only where what is left of it still holds a games
// list worth reading (lobbyPanelMinW). Where it cannot, the menu is drawn
// over the panel instead, which costs the panel nothing.
func (a *App) lobbyMenuBeside(gtx C) bool {
	return gtx.Constraints.Max.X-a.hudColBesideW(gtx) >= gtx.Dp(lobbyPanelMinW)
}

// lobbyMenuW is the width the menu column is laid out at. It is the game
// menu's own width policy (hudColBesideW, hudOverPct) and not a second one:
// this is the same column on the other screen, and a menu that changed width
// between the lobby and a game would read as a different thing.
func (a *App) lobbyMenuW(gtx C) int {
	if a.lobbyMenuBeside(gtx) {
		return a.hudColBesideW(gtx)
	}
	return min(gtx.Constraints.Max.X*hudOverPct/100, gtx.Dp(hudColMaxW))
}

func (a *App) lobbyPlayersBeside(gtx C, w int) bool {
	return w-a.lobbyPlayersColW(gtx, w) >= gtx.Dp(lobbyPanelMinW)
}

// lobbyMenuColumn is the lobby's menu as a column beside the panel: its own
// panel ground and a hairline down the edge it meets the panel on, and
// nothing else — no scrim behind it, no close button in it. What it holds is
// everything about this SESSION rather than about any game: who we are, and
// the server we are on and the one we are hosting.
//
// The column is the slot's exact height and its content scrolls in it, as
// the game screen's does (hudColumn): the menu with the legend under it is
// taller than a short window, and a Flex handed less room than its rows want
// gives the last of them none. A list gives every row its own height and a
// scrollbar down the edge for the rest.
func (a *App) lobbyMenuColumn(gtx C, playerName, connName, connURL string, vs voice.Snapshot) D {
	return background(gtx, colPanel, func(gtx C) D {
		d := material.List(a.th, &a.lobbyMenuList).Layout(gtx, 1, func(gtx C, _ int) D {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx C) D {
				return a.lobbyMenu(gtx, playerName, connName, connURL, vs)
			})
		})
		fillRect(gtx.Ops, image.Rect(d.Size.X-gtx.Dp(2), 0, d.Size.X, d.Size.Y), colBorder)
		return d
	})
}

// lobbyMenu is that column's contents: who we are and where we are connected,
// the address to hand out while hosting a server, and — since
// this is the screen a player sits on before they play — how the game is
// played, the same legend the game's own menu carries (controlsSections).
// The way out is NOT in here: Disconnect stands on the button line over the
// column (lobbyActions), where a put-away menu cannot take it with it.
// Everything in it stacks rather than running along a line: the column is a
// third of a phone's width at its narrowest, and a server URL beside its
// label there would be two ellipses.
func (a *App) lobbyMenu(gtx C, playerName, connName, connURL string, vs voice.Snapshot) D {
	// The addresses other players use while this session is hosting the LAN
	// party: the page the phones open and the NATS address the desktop builds
	// and agents dial — the two things on this screen the host has to be able
	// to read out loud, so they get lines of their own in the NATS green,
	// and under them the button that puts the page on the screen as a QR
	// code (lanqr.go). The NATS one is also the URL we are connected to, so
	// it is said once, here, and the plain connection line above drops to
	// just the server's name.
	addr, _, _ := a.lanAddrs()
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.pixelLabelFit(gtx, unit.Sp(11), playerName, colAccent) }),
	}
	if connName != "" {
		children = append(children, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return a.pixelLabelFit(gtx, unit.Sp(9), "@ "+connName, colMuted)
			})
		}))
		if connURL != "" && addr == "" {
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
					return a.pixelLabelFit(gtx, unit.Sp(8), connURL, colMuted)
				})
			}))
		}
	}
	if addr != "" && browserBuildReady() {
		children = append(children,
			layout.Rigid(spacer(14)),
			layout.Rigid(a.header("YOUR SERVER'S URLS ARE")),
			layout.Rigid(func(gtx C) D {
				return a.pixelLabelFit(gtx, unit.Sp(10), strings.TrimSuffix(a.lanPageURL(), "/"), colNATSGreen)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
					return a.pixelLabelFit(gtx, unit.Sp(10), "nats://"+addr, colNATSGreen)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx,
					a.body("Browsers open the first; desktop builds and agents dial the second.", colMuted))
			}),
			layout.Rigid(spacer(8)),
			layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.qrShowBtn, "Show QR code") }),
		)
	} else if addr != "" {
		// A binary built without the browser build: no page to open, so no
		// QR code — the NATS address alone, and why.
		children = append(children,
			layout.Rigid(spacer(14)),
			layout.Rigid(a.header("YOUR SERVER'S URL IS")),
			layout.Rigid(func(gtx C) D {
				return a.pixelLabelFit(gtx, unit.Sp(10), "nats://"+addr, colNATSGreen)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx,
					a.body("Share this address so others can join you. No browser build in this binary: run scripts/build-wasm.sh before building to serve one.", colMuted))
			}),
		)
	}
	// The voice chat (voice.go): the lobby's room is everyone in it.
	children = append(children,
		layout.Rigid(spacer(14)),
		layout.Rigid(func(gtx C) D { return a.voiceSection(gtx, vs, "LOBBY") }),
	)
	// The controls, on the screen where a player is deciding whether to play
	// rather than in the middle of playing. Hold is left out of it: whether
	// there is a hold queue is the GAME's rule and no game has been chosen
	// yet (controlsSections). This is the legend whose KEYS lines open the
	// key bindings dialog (keymap.go) — here, where there is time for it.
	for _, sec := range a.controlsSections(false, true) {
		children = append(children,
			layout.Rigid(spacer(16)),
			layout.Rigid(a.tutMarked(tutControls, func(gtx C) D {
				// The legend's parts at their own sizes, not the column's: a
				// key label handed the column's width as its minimum reports
				// it, and controlsHint lines the moves up past the widest key.
				gtx.Constraints.Min = image.Point{}
				return a.controlsHint(gtx, sec)
			})),
		)
	}
	// The parts at their own heights — the button still spans the width —
	// and the whole at its own: the column scrolls it (lobbyMenuColumn).
	gtx.Constraints.Min.Y = 0
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// lobbyPlayersColumn is everyone in the lobby, in a column beside the panel:
// the counterpart of the opponents' boards on the game screen, switched on
// and off by the bar the same way. Each row is the name over what that player
// is doing — stacked, not side by side, because the column is narrow and a
// name and a state sharing a line would both be cut.
func (a *App) lobbyPlayersColumn(gtx C, players []lobby.PlayerPresence, vs voice.Snapshot) D {
	return background(gtx, colPanel, func(gtx C) D {
		d := layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(a.header(fmt.Sprintf("PLAYERS (%d)", len(players)))),
				layout.Flexed(1, func(gtx C) D {
					if len(players) == 0 {
						return a.body("Nobody else is here.", colMuted)(gtx)
					}
					return material.List(a.th, &a.playerList).Layout(gtx, len(players), func(gtx C, i int) D {
						p := players[i]
						return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(a.lobbyPlayerName(p, vs)),
								layout.Rigid(a.caption(statusText(p.Status), colMuted)),
							)
						})
					})
				}),
			)
		})
		fillRect(gtx.Ops, image.Rect(0, 0, gtx.Dp(2), d.Size.Y), colBorder)
		return d
	})
}

// lobbyPlayersStrip is the players list where it cannot stand beside the
// panel (lobbyPlayersBeside): the same names and states, laid ACROSS the full
// width just above the chat instead of down a column beside the games. It
// costs the panel its own height rather than a quarter of its width — and it
// takes as little of that as it can, packing the players along each line and
// starting a new one only when the next will not fit (lobbyPlayersFlow),
// scrolling only once there are more lines than lobbyPlayersStripRows.
func (a *App) lobbyPlayersStrip(gtx C, players []lobby.PlayerPresence, vs voice.Snapshot) D {
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.header(fmt.Sprintf("PLAYERS (%d)", len(players)))),
			layout.Rigid(func(gtx C) D {
				if len(players) == 0 {
					return a.body("Nobody else is here.", colMuted)(gtx)
				}
				return bordered(gtx, func(gtx C) D {
					rows := a.lobbyPlayersFlow(gtx, players, vs)
					// Three lines of them at most; past that the strip keeps
					// its height and scrolls, so a lobby with thirty agents in
					// it cannot push the games off the screen.
					m := gtx
					m.Constraints.Min = image.Point{}
					rec := op.Record(gtx.Ops)
					rowH := rows[0](m).Size.Y
					rec.Stop() // measure only — discard the recorded ops
					if maxH := rowH * lobbyPlayersStripRows; gtx.Constraints.Max.Y > maxH {
						gtx.Constraints.Max.Y = maxH
					}
					return material.List(a.th, &a.playerStripLst).Layout(gtx, len(rows), func(gtx C, i int) D {
						return rows[i](gtx)
					})
				})
			}),
		)
	})
}

// lobbyPlayersFlow packs the players onto as many lines as they need: each
// entry takes the width its name wants (up to a column's worth) and a line
// breaks where the next entry would not fit. Gio has no flow layout, so the
// entries are measured at their own sizes into a discarded macro and the
// lines assembled by hand — cheap, since a lobby holds a handful of players
// and every entry is two labels.
func (a *App) lobbyPlayersFlow(gtx C, players []lobby.PlayerPresence, vs voice.Snapshot) []layout.Widget {
	entry := func(p lobby.PlayerPresence) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.X = 0
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(lobbyPlayersMaxW))
			return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(a.lobbyPlayerName(p, vs)),
					layout.Rigid(a.caption(statusText(p.Status), colMuted)),
				)
			})
		}
	}
	items := make([]layout.Widget, 0, len(players))
	for _, p := range players {
		items = append(items, entry(p))
	}
	return flowRows(gtx, gtx.Dp(14), items)
}

// flowRows packs widgets along a line, gap px apart, and starts a new line
// where the next one will not fit: each is measured at its own width first,
// and a line is broken only between two of them.
func flowRows(gtx C, gap int, items []layout.Widget) []layout.Widget {
	avail := gtx.Constraints.Max.X
	var rows, line []layout.Widget
	x := 0
	for _, w := range items {
		m := gtx
		m.Constraints.Min = image.Point{}
		rec := op.Record(gtx.Ops)
		need := w(m).Size.X
		rec.Stop() // measure only — discard the recorded ops
		if len(line) > 0 && x+gap+need > avail {
			rows = append(rows, flowLine(line, gap))
			line, x = nil, 0
		}
		if len(line) > 0 {
			x += gap
		}
		line = append(line, w)
		x += need
	}
	if len(line) > 0 {
		rows = append(rows, flowLine(line, gap))
	}
	return rows
}

// buttonLine is a row of buttons packed along one line and wrapped onto
// further lines where the screen has not the width for them — what the
// screens' action lines are made of (lobbyActions, gameActions). A Flex
// handed more than it has squeezes its children instead, which on a phone
// turns three buttons into three stacks of syllables.
func buttonLine(gtx C, gap int, items ...layout.Widget) D {
	rows := flowRows(gtx, gap, items)
	kids := make([]layout.FlexChild, 0, 2*len(rows))
	for i, r := range rows {
		if i > 0 {
			kids = append(kids, layout.Rigid(spacer(8)))
		}
		kids = append(kids, layout.Rigid(r))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// lobbyPlayerName is a presence's name line: the name, and the speaker mark
// beside it while that player is heard in the lobby room (voice.go).
func (a *App) lobbyPlayerName(p lobby.PlayerPresence, vs voice.Snapshot) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.body(agentName(p.Name, p.Agent), colFg)),
			layout.Rigid(a.speakerMark(vs, p.PlayerID, unit.Dp(10))),
		)
	}
}

// flowLine is one packed line: its entries side by side, gap px apart, each
// at its own width and all of them topped off at the same line.
func flowLine(items []layout.Widget, gap int) layout.Widget {
	return func(gtx C) D {
		kids := make([]layout.FlexChild, 0, 2*len(items))
		for i, w := range items {
			if i > 0 {
				kids = append(kids, layout.Rigid(func(gtx C) D { return D{Size: image.Pt(gap, 0)} }))
			}
			kids = append(kids, layout.Rigid(w))
		}
		return layout.Flex{}.Layout(gtx, kids...)
	}
}

// lobbyChatStrip is the lobby's chat along the bottom of the screen, the same
// strip the game screen has (gameChatPanel) and built from the same two
// pieces, minus the two things a lobby has no need of: there is no piece here
// for the keys to go back to, so no focus ring and no pointer area of its
// own, and every message in it is already the lobby's, so none is prefixed
// @lobby.
func (a *App) lobbyChatStrip(gtx C, chat []lobby.ChatMessage) D {
	a.lobbyChatSeen = len(chat) // seen: the bar's unread dot goes out
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.header("CHAT")),
			layout.Rigid(func(gtx C) D {
				return a.chatLogBox(gtx, &a.chatList, len(chat), func(i int) (string, colorN) {
					if chat[i].System {
						return chat[i].Text, colMuted
					}
					return fmt.Sprintf("%s: %s", chat[i].Name, chat[i].Text), colFg
				})
			}),
			layout.Rigid(spacer(6)),
			layout.Rigid(func(gtx C) D {
				return a.chatComposer(gtx, &a.chatEd, &a.chatBtn, "Message…")
			}),
		)
	})
}

// lobbyPanel is the middle of the screen: the lists a lobby has in it, on
// tabs. The actions that belong to none of them are not in here — they stand
// on the button line above the whole body (lobbyActions), off every list and
// out of every column.
func (a *App) lobbyPanel(gtx C, games []lobby.GameListing, abandoned map[string]bool, archives []config.ArchiveRecord, logEntries []config.LogEntry) D {
	history := a.lobbyTab == lobbyTabHistory
	serverLog := a.lobbyTab == lobbyTabLog
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// The tabs sit on the panel's top border like file-folder tabs
			// (the connection page's, connPage), each carrying its own count
			// so the one that is not showing still says how much is in it.
			return layout.Flex{Alignment: layout.End}.Layout(gtx,
				layout.Rigid(a.tutMarked(tutLobbyTabGames, func(gtx C) D {
					return a.tabChip(gtx, &a.lobbyTabBtns[0], fmt.Sprintf("GAMES (%d)", len(games)), !history && !serverLog)
				})),
				layout.Rigid(hSpacer(4)),
				layout.Rigid(a.tutMarked(tutLobbyTabHistory, func(gtx C) D {
					return a.tabChip(gtx, &a.lobbyTabBtns[1], fmt.Sprintf("GAME HISTORY (%d)", len(archives)), history)
				})),
				layout.Rigid(hSpacer(4)),
				layout.Rigid(a.tutMarked(tutLobbyTabLog, func(gtx C) D {
					return a.tabChip(gtx, &a.lobbyTabBtns[2], fmt.Sprintf("SERVER LOG (%d)", len(logEntries)), serverLog)
				})),
			)
		}),
		layout.Flexed(1, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colPanel, func(gtx C) D {
					gtx.Constraints.Min = gtx.Constraints.Max
					return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
						if history {
							return a.lobbyHistoryTab(gtx, archives)
						}
						if serverLog {
							return a.lobbyLogTab(gtx, logEntries)
						}
						return a.lobbyGamesTab(gtx, games, abandoned)
					})
				})
			})
		}),
	)
}

// lobbyGamesTab is the games on offer now, newest first, ruled off from one
// another.
func (a *App) lobbyGamesTab(gtx C, games []lobby.GameListing, abandoned map[string]bool) D {
	if len(games) == 0 {
		return layout.Inset{Top: unit.Dp(4), Left: unit.Dp(6)}.Layout(gtx,
			a.body("No games yet — create one and the others will see it here.", colMuted))
	}
	return material.List(a.th, &a.gameList).Layout(gtx, len(games), func(gtx C, i int) D {
		g := games[i]
		row := func(gtx C) D { return a.gameRow(gtx, g, abandoned[g.GameID]) }
		if i == 0 {
			return row(gtx)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return hrule(gtx, colBorder, 1) }),
			layout.Rigid(row),
		)
	})
}

// lobbyHistoryTab is the games already played: the sort and crew controls,
// the all-time teams scoreboard when there is one, and the high-score table.
func (a *App) lobbyHistoryTab(gtx C, archives []config.ArchiveRecord) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.historyControls),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D { return a.teamStandingsLine(gtx, archives) }),
		layout.Flexed(1, func(gtx C) D {
			if len(archives) == 0 {
				return layout.Inset{Top: unit.Dp(6), Left: unit.Dp(6)}.Layout(gtx,
					a.body("No finished games yet.", colMuted))
			}
			// An arcade high-score table: a pixel-font column header over a
			// scrolling list of games, each row a fixed SCORE / TIME / MODE
			// column trio (the score largest, in gold) and a flexed
			// winner-first PLAYERS column, ruled off from the next game.
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(a.archiveHistoryHeader),
				layout.Rigid(func(gtx C) D { return hrule(gtx, colAccent, 1) }),
				layout.Flexed(1, func(gtx C) D {
					lb := a.getLobby()
					return material.List(a.th, &a.archiveLst).Layout(gtx, len(archives), func(gtx C, i int) D {
						for len(a.archiveBtns) <= i {
							a.archiveBtns = append(a.archiveBtns, widget.Clickable{})
							a.replayBtns = append(a.replayBtns, widget.Clickable{})
						}
						btn := &a.archiveBtns[i]
						if btn.Clicked(gtx) && !a.tutorialUp() {
							a.openArchive(archives[i])
						}
						// Games whose stream was archived to a replay
						// stream grow a Replay button; clicking it opens the
						// replay itself — there is nothing to ask first.
						var replayBtn *widget.Clickable
						if a.hasReplay(lb, archives[i].GameID) {
							replayBtn = &a.replayBtns[i]
							if replayBtn.Clicked(gtx) && !a.tutorialUp() {
								a.startReplay(archives[i])
							}
						}
						// Games in their bucket's all-time top 10 are marked
						// (the ranking counts every record, not just the rows
						// the filter shows — and only once the bucket has
						// more than ten games).
						top := a.isTopRanked(lb, archives[i].GameID)
						// A pinned replay (share.go) is tagged too: kept for
						// good, whatever its rank or age.
						pinned := a.isPinned(lb, archives[i].GameID)
						return a.archiveHistoryRow(gtx, archives[i], btn, replayBtn, top, pinned)
					})
				}),
			)
		}),
	)
}

// hasReplay and isTopRanked are the lobby's HasReplay and IsTopRanked over
// the history on screen — the server's, or the How to play tour's while it
// is up (tutorial.go), whose top game has both.
func (a *App) hasReplay(lb *lobby.Lobby, gameID string) bool {
	if a.tutorialUp() {
		return tutorialHasReplay(gameID)
	}
	return lb != nil && lb.HasReplay(gameID)
}

func (a *App) isTopRanked(lb *lobby.Lobby, gameID string) bool {
	if a.tutorialUp() {
		return tutorialTopRanked(gameID)
	}
	return lb != nil && lb.IsTopRanked(gameID)
}

// lobbyLogTab is the server log: the journal every client appends to as it
// connects, disconnects, or comes back, and as games are created and
// started (config.LogEntry) — one dated line each, newest first so the
// latest is always in view, game lines in the foreground color and
// connection lines muted so a game stands out from the coming and going
// around it.
func (a *App) lobbyLogTab(gtx C, entries []config.LogEntry) D {
	if len(entries) == 0 {
		return layout.Inset{Top: unit.Dp(6), Left: unit.Dp(6)}.Layout(gtx,
			a.body("Nothing in the server log yet.", colMuted))
	}
	return material.List(a.th, &a.logLst).Layout(gtx, len(entries), func(gtx C, i int) D {
		e := entries[len(entries)-1-i]
		col := colMuted
		switch e.Kind {
		case config.LogKindGameCreated, config.LogKindGameStarted:
			col = colFg
		}
		line := e.Time.Local().Format("Jan _2 15:04:05") + "  " + e.Text()
		return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, a.body(line, col))
	})
}

// historyControls is the history tab's own toolbar: how the table is ordered,
// and which crews are listed in it. On one line where it fits and two where it
// does not — and that is measured rather than guessed at from the form factor,
// because what it has to fit inside is the PANEL's width and not the screen's:
// a desktop lobby with both columns up leaves the panel far less than the
// window. The measuring pass lays the row out into a discarded macro, on
// throwaway widget state so the real controls' presses are not spent on it.
func (a *App) historyControls(gtx C) D {
	sort := func(sortEnum *widget.Enum) layout.Widget {
		return func(gtx C) D {
			rb := func(v, label string) layout.FlexChild {
				return layout.Rigid(func(gtx C) D {
					b := material.RadioButton(a.th, sortEnum, v, label)
					b.Color = colFg
					return b.Layout(gtx)
				})
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				rb("score", "By score"),
				layout.Rigid(hSpacer(6)),
				rb("date", "By date"),
			)
		}
	}
	// The filters: the three crew boxes, and Pinned only (share.go's pins)
	// after them.
	crew := func(humans, mixed, agents, pinned *widget.Bool) layout.Widget {
		return func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.histFilterBox(humans, "Players only")),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(a.histFilterBox(mixed, "Agents and players")),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(a.histFilterBox(agents, "Agents only")),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(a.histFilterBox(pinned, "Pinned only")),
			)
		}
	}
	// Stacked, for a panel that cannot hold the four of them side by side: a
	// checkbox handed less width than its label wants does not elide, it
	// WRAPS — "Agents only" came out as three lines of stacked syllables on a
	// phone — so below the width they need each one takes a line.
	crewStack := func(humans, mixed, agents, pinned *widget.Bool) layout.Widget {
		return func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(a.histFilterBox(humans, "Players only")),
				layout.Rigid(a.histFilterBox(mixed, "Agents and players")),
				layout.Rigid(a.histFilterBox(agents, "Agents only")),
				layout.Rigid(a.histFilterBox(pinned, "Pinned only")),
			)
		}
	}
	oneLine := func(s, c layout.Widget) layout.Widget {
		return func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(s),
				layout.Rigid(hSpacer(14)),
				layout.Rigid(c),
			)
		}
	}
	// What each arrangement wants, measured into a discarded macro on
	// throwaway widget state so the real controls' presses are not spent on
	// it. It is measured rather than guessed at from the form factor because
	// what these have to fit inside is the PANEL's width and not the
	// screen's: a desktop lobby with both columns up leaves the panel far
	// less than the window.
	var mEnum widget.Enum
	var mHumans, mMixed, mAgents, mPinned widget.Bool
	m := gtx
	m.Constraints.Min = image.Point{}
	m.Constraints.Max.X = 1 << 20
	rec := op.Record(gtx.Ops)
	bothW := oneLine(sort(&mEnum), crew(&mHumans, &mMixed, &mAgents, &mPinned))(m).Size.X
	crewW := crew(&mHumans, &mMixed, &mAgents, &mPinned)(m).Size.X
	rec.Stop() // measure only — discard the recorded ops
	row := crew(&a.histHumansCb, &a.histMixedCb, &a.histAgentsOnlyCb, &a.histPinnedCb)
	switch {
	case bothW <= gtx.Constraints.Max.X:
		return oneLine(sort(&a.histSortEnum), row)(gtx)
	case crewW > gtx.Constraints.Max.X:
		row = crewStack(&a.histHumansCb, &a.histMixedCb, &a.histAgentsOnlyCb, &a.histPinnedCb)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(sort(&a.histSortEnum)),
		layout.Rigid(spacer(4)),
		layout.Rigid(row),
	)
}

// histFilterBox is one of the GAME HISTORY filters: a checkbox that lists
// (checked) or hides the games of one agent composition — or, Pinned only,
// narrows the list to the pinned replays.
func (a *App) histFilterBox(b *widget.Bool, label string) layout.Widget {
	return func(gtx C) D {
		cb := material.CheckBox(a.th, b, label)
		cb.Color = colFg
		cb.IconColor = colAccent
		return cb.Layout(gtx)
	}
}

// archivesForDisplay applies the history controls: keep only the games whose
// crew composition (config.ArchiveRecord.AgentClass — all-human, mixed
// human/agent, or agents-only) has its filter box checked — only the pinned
// ones among them while Pinned only is checked (the lobby's pins, share.go;
// pinned tells which) — then order the survivors by the selected key. "By score" (the default) is the ranking
// table: agent composition first — agents-only games, then mixed human/agent
// games, then all-human games — and the shared RankBefore order within each
// group. "By date" is strictly chronological, the last game played at the
// top, whoever played it.
func (a *App) archivesForDisplay(recs []config.ArchiveRecord, pinned func(gameID string) bool) []config.ArchiveRecord {
	listed := map[int]bool{
		config.AgentClassHumansOnly: a.histHumansCb.Value,
		config.AgentClassMixed:      a.histMixedCb.Value,
		config.AgentClassAgentsOnly: a.histAgentsOnlyCb.Value,
	}
	kept := recs[:0:0]
	for _, r := range recs {
		if listed[r.AgentClass()] && (!a.histPinnedCb.Value || pinned(r.GameID)) {
			kept = append(kept, r)
		}
	}
	recs = kept
	if a.histSortEnum.Value == "date" {
		return sortedArchivesByDate(recs)
	}
	return sortedArchives(recs)
}

// teamStandings folds the teams-mode games among recs into overall per-team
// totals: wins (a draw counts for neither side) and points (sum of final team
// scores). games is how many teams games were counted.
func teamStandings(recs []config.ArchiveRecord) (wins, points []int, games int) {
	// The board is as wide as the widest game in the history: a three-way
	// game contributes a TEAM C column that two-team games simply never
	// score in.
	slots := 0
	for _, r := range recs {
		if r.Mode == config.ModeTeams {
			slots = max(slots, r.Teams())
		}
	}
	wins, points = make([]int, slots), make([]int, slots)
	for _, r := range recs {
		if r.Mode != config.ModeTeams {
			continue
		}
		games++
		if r.WinningTeam >= 0 && r.WinningTeam < slots {
			wins[r.WinningTeam]++
		}
		for t := 0; t < slots && t < len(r.TeamScores); t++ {
			points[t] += r.TeamScores[t]
		}
	}
	return wins, points, games
}

// teamStandingsLine renders the all-time TEAM A vs TEAM B (vs TEAM C…)
// scoreboard over the teams games currently listed in the history (so the
// agent filter applies): wins and total points per team, the leading team (by
// wins, points as the tie-break) in gold, and no highlight at all where the
// lead is shared. It is as wide as the widest game in the history — a column
// per team any of them was played between. Nothing is drawn while no teams
// game has finished.
func (a *App) teamStandingsLine(gtx C, archives []config.ArchiveRecord) D {
	wins, points, games := teamStandings(archives)
	if games == 0 || len(wins) == 0 {
		return D{}
	}
	lead, tied := 0, false
	for t := 1; t < len(wins); t++ {
		switch {
		case wins[t] > wins[lead] || (wins[t] == wins[lead] && points[t] > points[lead]):
			lead, tied = t, false
		case wins[t] == wins[lead] && points[t] == points[lead]:
			tied = true
		}
	}
	if tied {
		lead = -1 // dead even at the top, no highlight
	}
	seg := func(t int) layout.FlexChild {
		col := colFg
		if t == lead {
			col = colGold
		}
		txt := fmt.Sprintf("TEAM %s %dW · %d PTS", teamName(t), wins[t], points[t])
		return layout.Rigid(a.pixel(unit.Sp(8), txt, col).Layout)
	}
	label := fmt.Sprintf("TEAMS OVERALL (%d GAMES)", games)
	if games == 1 {
		label = "TEAMS OVERALL (1 GAME)"
	}
	oneLine := func(gtx C) D {
		kids := []layout.FlexChild{layout.Rigid(a.pixel(unit.Sp(8), label+"   ", colMuted).Layout)}
		for t := range wins {
			if t > 0 {
				kids = append(kids, layout.Rigid(a.pixel(unit.Sp(8), "  —  ", colMuted).Layout))
			}
			kids = append(kids, seg(t))
		}
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, kids...)
	}
	// What the one-line form would measure: the label plus a segment per team
	// at its widest, separated by the dashes.
	probe := label + "   "
	for t := range wins {
		if t > 0 {
			probe += "  —  "
		}
		probe += " TEAM " + teamName(t) + " 00W · 000000 PTS"
	}
	return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		// One line where it fits, a line per team where it does not. A Flex
		// hands a rigid child whatever is left and lets it be one glyph wide,
		// which on a phone printed TEAM B's totals vertically down the edge
		// of the table; measured, the line stacks instead.
		if a.pixelWidth(gtx, unit.Sp(8), probe) <= gtx.Constraints.Max.X {
			return oneLine(gtx)
		}
		kids := []layout.FlexChild{layout.Rigid(a.pixel(unit.Sp(8), label, colMuted).Layout)}
		for t := range wins {
			t := t
			kids = append(kids, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
					return layout.Flex{}.Layout(gtx, seg(t))
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// sortedArchivesByDate orders the history strictly by finish time, the most
// recent game first — no grouping by crew: "By date" answers "what was played
// last", and the crew filters are there to narrow who. Exact ties (records
// from before finish timestamps) fall back to the score ranking.
func sortedArchivesByDate(recs []config.ArchiveRecord) []config.ArchiveRecord {
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].FinishedAt.Equal(recs[j].FinishedAt) {
			return recs[i].FinishedAt.After(recs[j].FinishedAt)
		}
		return recs[i].RankBefore(recs[j])
	})
	return recs
}

// sortedArchives groups the history by agent composition (agents-only, then
// mixed, then all-human) and orders within each group by the shared "By score"
// ranking (config.ArchiveRecord.RankBefore) — the same ordering the replay
// archiver's top-N cut uses, so the replay-carrying games are exactly the
// top of each group when sorted by score.
func sortedArchives(recs []config.ArchiveRecord) []config.ArchiveRecord {
	sort.SliceStable(recs, func(i, j int) bool {
		if ci, cj := recs[i].AgentClass(), recs[j].AgentClass(); ci != cj {
			return ci < cj
		}
		return recs[i].RankBefore(recs[j])
	})
	return recs
}

// archiveWhen renders a record's start date/time (in the viewer's local
// timezone) and duration, e.g. "2026-07-06 14:03 PDT · 4m32s".
func archiveWhen(r config.ArchiveRecord) string {
	if r.StartedAt.IsZero() {
		return ""
	}
	s := r.StartedAt.Local().Format("2006-01-02 15:04 MST")
	if d := r.Duration(); d > 0 {
		s += " · " + d.Round(time.Second).String()
	}
	return s
}

// Column widths (dp) for the arcade-style GAME HISTORY table. SCORE is
// right-aligned (numbers line up), the rest left-aligned; PLAYERS takes the
// remaining width and wraps.
const (
	histScoreW = 92
	histTimeW  = 96
	histModeW  = 124
	// The same columns where the row has been folded onto two lines
	// (histStacked): narrower, because there all three of them share a line
	// with nothing beside them and the roster has the next line to itself.
	histScoreNarrowW = 76
	histTimeNarrowW  = 84
	// histRowW is the width the one-line row needs: the three columns and
	// their gaps, a roster column worth reading beside them, and the row's
	// actions. Under it — a phone, or a desktop panel with both lobby columns
	// up — the row folds, because a Flex hands its flexed child whatever the
	// rigid ones leave and that is a PLAYERS column one letter wide, printed
	// down the side of the table.
	histRowW = 620
)

// histStacked reports whether the history table's rows are folded onto two
// lines in the width gtx measures — the columns over the roster — instead of
// running as one.
func histStacked(gtx C) bool { return gtx.Constraints.Max.X < gtx.Dp(unit.Dp(histRowW)) }

// fixedCol lays wdg inside a fixed-width (dp) column, aligned by dir.
func fixedCol(gtx C, w int, dir layout.Direction, wdg layout.Widget) D {
	cw := gtx.Dp(unit.Dp(w))
	gtx.Constraints.Min.X = cw
	gtx.Constraints.Max.X = cw
	return dir.Layout(gtx, wdg)
}

// hrule draws a full-width horizontal rule h dp tall in color c — the visual
// delimiter between history rows (and the header underline).
func hrule(gtx C, c colorN, h int) D {
	height := gtx.Dp(unit.Dp(h))
	w := gtx.Constraints.Max.X
	fillRect(gtx.Ops, image.Rect(0, 0, w, height), c)
	return D{Size: image.Pt(w, height)}
}

// archiveHistoryHeader is the pixel-font column-label row above the history
// list, aligned to the same column widths as archiveHistoryRow.
func (a *App) archiveHistoryHeader(gtx C) D {
	col := func(txt string, w int, dir layout.Direction) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return fixedCol(gtx, w, dir, a.pixel(unit.Sp(8), txt, colAccent).Layout)
		})
	}
	stacked := histStacked(gtx)
	scoreW, timeW := histScoreW, histTimeW
	if stacked {
		scoreW, timeW = histScoreNarrowW, histTimeNarrowW
	}
	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(5), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		kids := []layout.FlexChild{
			col("SCORE", scoreW, layout.E),
			layout.Rigid(hSpacer(10)),
			col("TIME", timeW, layout.W),
			layout.Rigid(hSpacer(10)),
		}
		if stacked {
			// The roster is on the row's second line, under all three of
			// these, so there is no PLAYERS column here to label.
			kids = append(kids, layout.Flexed(1, a.pixel(unit.Sp(8), "MODE", colAccent).Layout))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		}
		kids = append(kids,
			col("MODE", histModeW, layout.W),
			layout.Rigid(hSpacer(10)),
			layout.Flexed(1, a.pixel(unit.Sp(8), "PLAYERS", colAccent).Layout),
		)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

// archiveHistoryRow renders one finished game as a table row: the headline
// SCORE (largest, gold), the game TIME (duration over date), the MODE, and a
// flexed winner-first PLAYERS column, closed by a rule separating it from the
// next game. replayBtn is non-nil for games with a replay archive and adds
// the Replay action beside View board. top marks a game in its bucket's
// all-time top 10 (config.ReplayTopRankedCut): a gold bar down the row's
// left edge and a TOP 10 tag under its score — a marker, deliberately not a
// row tint, which would read as a selection — so the showcase games stand
// out from the merely recent ones in either sort order. pinned marks a game
// whose replay is pinned (share.go): a PINNED tag in the same place.
func (a *App) archiveHistoryRow(gtx C, rec config.ArchiveRecord, btn, replayBtn *widget.Clickable, top, pinned bool) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			content := func(gtx C) D {
				return a.archiveHistoryCells(gtx, rec, btn, replayBtn, top, pinned)
			}
			if !top {
				return content(gtx)
			}
			// Lay the cells out first to learn the row's height, then paint
			// the edge bar underneath before replaying them.
			macro := op.Record(gtx.Ops)
			dims := content(gtx)
			call := macro.Stop()
			fillRect(gtx.Ops, image.Rect(0, 0, gtx.Dp(unit.Dp(4)), dims.Size.Y), colGold)
			call.Add(gtx.Ops)
			return dims
		}),
		layout.Rigid(func(gtx C) D { return hrule(gtx, colBorder, 1) }),
	)
}

// archiveHistoryActions is the row's action cluster: View board, and Replay
// for a game whose stream was archived to one. mark is the id the cluster
// carries for the How to play tour (tutorialHistoryMark), "" for none.
func (a *App) archiveHistoryActions(mark string, btn, replayBtn *widget.Clickable) []layout.FlexChild {
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.viewBoardButton(gtx, btn) }),
	}
	if replayBtn != nil {
		kids = append(kids,
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D {
				return a.smallActionButton(gtx, replayBtn, "Replay", colNATSGreen)
			}),
		)
	}
	if mark == "" {
		return kids
	}
	return []layout.FlexChild{layout.Rigid(a.tutMarked(mark, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}))}
}

// archiveHistoryStackedCells is the row folded onto two lines, for a panel
// too narrow to run it as one (histStacked): the SCORE, TIME and MODE columns
// on top — MODE taking whatever the two fixed ones leave rather than a fixed
// column of its own — and under them the roster with the row's actions beside
// it. Nothing is dropped and nothing is abbreviated; the row is simply two
// lines tall, which is what a phone has to spend.
func (a *App) archiveHistoryStackedCells(gtx C, rec config.ArchiveRecord, btn, replayBtn *widget.Clickable, top, pinned bool) D {
	return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return fixedCol(gtx, histScoreNarrowW, layout.E, a.archiveScoreCell(rec, top, pinned))
					}),
					layout.Rigid(hSpacer(10)),
					layout.Rigid(func(gtx C) D {
						return fixedCol(gtx, histTimeNarrowW, layout.W, a.archiveTimeCell(rec))
					}),
					layout.Rigid(hSpacer(10)),
					layout.Flexed(1, a.archiveModeCell(rec)),
				)
			}),
			layout.Rigid(spacer(6)),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					append([]layout.FlexChild{
						layout.Flexed(1, a.archivePlayersCell(rec)),
						layout.Rigid(hSpacer(8)),
					}, a.archiveHistoryActions(tutorialHistoryMark(rec), btn, replayBtn)...)...)
			}),
		)
	})
}

// archiveHistoryCells is the history row's inset column flex (see
// archiveHistoryRow), folded onto two lines where the panel is too narrow to
// run it as one.
func (a *App) archiveHistoryCells(gtx C, rec config.ArchiveRecord, btn, replayBtn *widget.Clickable, top, pinned bool) D {
	if histStacked(gtx) {
		return a.archiveHistoryStackedCells(gtx, rec, btn, replayBtn, top, pinned)
	}
	return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D { return fixedCol(gtx, histScoreW, layout.E, a.archiveScoreCell(rec, top, pinned)) }),
			layout.Rigid(hSpacer(10)),
			layout.Rigid(func(gtx C) D { return fixedCol(gtx, histTimeW, layout.W, a.archiveTimeCell(rec)) }),
			layout.Rigid(hSpacer(10)),
			layout.Rigid(func(gtx C) D { return fixedCol(gtx, histModeW, layout.W, a.archiveModeCell(rec)) }),
			layout.Rigid(hSpacer(10)),
			layout.Flexed(1, a.archivePlayersCell(rec)),
			layout.Rigid(hSpacer(8)),
		}
		children = append(children, a.archiveHistoryActions(tutorialHistoryMark(rec), btn, replayBtn)...)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

// archiveScoreCell is the headline SCORE (gold pixel numerals) over a small
// achieved-level line — the game's most important figure, so the largest —
// and, for a game in its bucket's top 10, a gold TOP 10 tag beneath; a game
// whose replay is pinned gets a green PINNED tag there too. A survival
// game's headline is the time it survived, its tier under it.
func (a *App) archiveScoreCell(r config.ArchiveRecord, top, pinned bool) layout.Widget {
	return func(gtx C) D {
		headline, sub := strconv.Itoa(r.HeadlineScore()), fmt.Sprintf("LVL %d", archiveHeadlineLevel(r))
		if r.IsSurvival() {
			headline, sub = formatSurvived(r.Duration()), strings.ToUpper(r.SurvivalTier().String())
		}
		children := []layout.FlexChild{
			layout.Rigid(a.pixel(unit.Sp(13), headline, colGold).Layout),
			layout.Rigid(a.caption(sub, colMuted)),
		}
		if top {
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, a.pixel(unit.Sp(7), "TOP 10", colGold).Layout)
			}))
		}
		if pinned {
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, a.pixel(unit.Sp(7), "PINNED", colNATSGreen).Layout)
			}))
		}
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx, children...)
	}
}

// archiveTimeCell is the game TIME: the duration (prominent, accent) over the
// start date (muted).
func (a *App) archiveTimeCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		dur := "—"
		if d := r.Duration(); d > 0 {
			dur = d.Round(time.Second).String()
		}
		var children []layout.FlexChild
		children = append(children, layout.Rigid(a.body(dur, colAccent)))
		if !r.StartedAt.IsZero() {
			children = append(children, layout.Rigid(a.caption(r.StartedAt.Local().Format("01-02 15:04"), colMuted)))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
}

// archiveModeCell names the MODE (pixel accent) over the player count / team
// shape (muted).
func (a *App) archiveModeCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		name, sub := "COOPERATIVE", fmt.Sprintf("%d PLAYERS", len(r.Players))
		if len(r.Players) == 1 {
			sub = "1 PLAYER" // a solo co-op game, played for the high score
		}
		switch r.Mode {
		case config.ModeCooperative:
			if r.IsSurvival() {
				name = "SURVIVAL · " + strings.ToUpper(r.SurvivalTier().String())
			}
		case config.ModeCompetitive:
			name = "COMPETITIVE"
		case config.ModeTeams:
			// "2v2", and "2v2v2" for a game played between more teams.
			shape := make([]string, r.Teams())
			for t := range shape {
				shape[t] = strconv.Itoa(r.TeamSize)
			}
			name, sub = "TEAMS", strings.Join(shape, "v")
		}
		// Crew line: whether the game was human-vs-human or had agent seats,
		// so the two kinds can be told apart at a glance.
		crew, crewCol := "HUMANS", colNATSGreen
		if r.HasAgents() {
			crew, crewCol = "WITH AGENTS", colOrange
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(8), name, colAccent).Layout),
			layout.Rigid(a.caption(sub, colMuted)),
			layout.Rigid(a.caption(crew, crewCol)),
		)
	}
}

// archivePlayersCell is the flexed PLAYERS column: the winner(s) on the first
// line (gold, trophy), everyone else below (muted) — see archiveRosterLines.
func (a *App) archivePlayersCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		lines := archiveRosterLines(r)
		children := make([]layout.FlexChild, 0, len(lines))
		for _, ln := range lines {
			children = append(children, layout.Rigid(a.markedBody(ln.text, ln.col, false)))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
}

// caption is a small muted-scale label (dates, sub-counts) in the given color.
func (a *App) caption(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Caption(a.th, txt)
		l.Color = c
		return l.Layout(gtx)
	}
}

// rosterLine is one colored line of a history row's PLAYERS column.
type rosterLine struct {
	text string
	col  colorN
}

// archiveHeadlineLevel is the level that goes with HeadlineScore: the shared
// final level (co-op), the winning team's level (teams; the best if a draw),
// or the top-scoring player's level (competitive).
func archiveHeadlineLevel(r config.ArchiveRecord) int {
	switch r.Mode {
	case config.ModeCooperative:
		return r.FinalLevel
	case config.ModeTeams:
		if r.WinningTeam >= 0 && r.WinningTeam < len(r.TeamLevels) {
			return r.TeamLevels[r.WinningTeam]
		}
		best := 0
		for _, l := range r.TeamLevels {
			if l > best {
				best = l
			}
		}
		return best
	}
	best, found := config.PlayerResult{}, false
	for _, p := range r.Players {
		if !found || p.Score > best.Score {
			best, found = p, true
		}
	}
	return best.Level
}

// archiveRosterLines builds the PLAYERS column's colored lines for a record,
// winner(s) first and highlighted in gold: competitive lists players by
// winner-then-score with each score/level, teams one line per team (winner
// first, its members and totals), cooperative just the shared roster.
func archiveRosterLines(r config.ArchiveRecord) []rosterLine {
	switch r.Mode {
	case config.ModeTeams:
		return teamRosterLines(r)
	case config.ModeCooperative:
		return coopRosterLines(r)
	default:
		return competitiveRosterLines(r)
	}
}

func competitiveRosterLines(r config.ArchiveRecord) []rosterLine {
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].Winner != players[j].Winner {
			return players[i].Winner // winners first
		}
		return players[i].Score > players[j].Score
	})
	var winners, rest []string
	for _, p := range players {
		s := fmt.Sprintf("%s %d (lvl %d)", agentName(p.PlayerID, p.Agent), p.Score, p.Level)
		if p.Winner {
			winners = append(winners, s)
		} else {
			rest = append(rest, s)
		}
	}
	var out []rosterLine
	if len(winners) > 0 {
		out = append(out, rosterLine{winnerMark + strings.Join(winners, " · "), colGold})
	}
	if len(rest) > 0 {
		out = append(out, rosterLine{strings.Join(rest, " · "), colMuted})
	}
	return out
}

func teamRosterLines(r config.ArchiveRecord) []rosterLine {
	// Winning team first, the rest in index order.
	order := make([]int, 0, r.Teams())
	if r.WinningTeam >= 0 && r.WinningTeam < r.Teams() {
		order = append(order, r.WinningTeam)
	}
	for t := 0; t < r.Teams(); t++ {
		if t != r.WinningTeam {
			order = append(order, t)
		}
	}
	out := make([]rosterLine, 0, len(order))
	for _, t := range order {
		var members []string
		for _, p := range r.Players {
			if p.Team == t {
				members = append(members, agentName(p.PlayerID, p.Agent))
			}
		}
		sort.Strings(members)
		stats := ""
		if t < len(r.TeamScores) {
			stats = fmt.Sprintf(" %d", r.TeamScores[t])
			if t < len(r.TeamLevels) {
				stats += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
			}
		}
		label := fmt.Sprintf("TEAM %s%s — %s", r.TeamName(t), stats, strings.Join(members, ", "))
		col := colMuted
		if r.WinningTeam == t {
			label, col = winnerMark+label, colGold
		}
		out = append(out, rosterLine{label, col})
	}
	return out
}

func coopRosterLines(r config.ArchiveRecord) []rosterLine {
	members := make([]string, 0, len(r.Players))
	for _, p := range r.Players {
		members = append(members, agentName(p.PlayerID, p.Agent))
	}
	sort.Strings(members)
	return []rosterLine{{strings.Join(members, ", "), colFg}}
}

// archiveLine summarizes a finished game for the history list.
func archiveLine(r config.ArchiveRecord) string {
	if when := archiveWhen(r); when != "" {
		return when + " · " + archiveModeLine(r)
	}
	return archiveModeLine(r)
}

// archiveModeLine is the mode-specific part of a history line (players,
// scores, levels, winners).
func archiveModeLine(r config.ArchiveRecord) string {
	if r.Mode == config.ModeTeams {
		// "teams · A 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave".
		// No winner mark: this line wraps (the replay dialog is 420 dp wide
		// and it runs to two lines there), and a DRAWN trophy cannot sit
		// inside wrapping text the way the old character did. The trophy is
		// on the roster lines beside this one instead, where each entry is a
		// row of its own — archiveRosterLines, which do carry it.
		parts := make([]string, 0, r.Teams())
		for t := 0; t < r.Teams(); t++ {
			var members []string
			for _, p := range r.Players {
				if p.Team == t {
					members = append(members, p.PlayerID)
				}
			}
			stats := ""
			if t < len(r.TeamScores) {
				// Older records predate per-team totals — skip stats for those.
				stats = fmt.Sprintf(" %d", r.TeamScores[t])
				if t < len(r.TeamLevels) {
					stats += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
				}
			}
			parts = append(parts, fmt.Sprintf("%s%s %s", r.TeamName(t), stats, strings.Join(members, ", ")))
		}
		return fmt.Sprintf("teams · %s", strings.Join(parts, " · "))
	}
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].Score > players[j].Score })
	parts := make([]string, 0, len(players))
	for _, p := range players {
		parts = append(parts, fmt.Sprintf("%s %d", p.PlayerID, p.Score))
	}
	if r.Mode == config.ModeCooperative {
		return fmt.Sprintf("co-op · total %d (lvl %d) · %s", r.TotalScore, r.FinalLevel, strings.Join(parts, ", "))
	}
	// Competitive: append each player's achieved level to their score.
	parts = parts[:0]
	for _, p := range players {
		parts = append(parts, fmt.Sprintf("%s %d (lvl %d)", p.PlayerID, p.Score, p.Level))
	}
	return fmt.Sprintf("competitive · %s", strings.Join(parts, ", "))
}

// lobbyActions is the lobby's line of buttons, across the whole screen just
// under the bar: the way out of the server, the single entry point to game
// creation — one button that opens the create-game wizard, where the game's
// attributes are chosen step by step instead of on an inline option row — and
// beside it, for a player who has never seen one, the How to play tour
// (tutorial.go).
//
// It is a line of its own and not part of anything the bar switches: the menu
// column, the panel and the players all begin UNDER it, so Disconnect is in
// the same place whether the menu is up or away, and none of the three can be
// scrolled off with a list. Where the screen has not the width for all of
// them the line wraps onto a second (buttonLine) rather than squeezing the
// labels into stacked syllables.
func (a *App) lobbyActions(gtx C) D {
	// A phone gets the line at tighter margins: the side inset and the gap a
	// desktop can afford are the difference between two lines and three on a
	// 375 dp screen, and a third line of buttons costs the games list more
	// than the margins are worth.
	pad, gap := unit.Dp(12), unit.Dp(12)
	if a.form.compact {
		pad, gap = unit.Dp(8), unit.Dp(8)
	}
	return layout.Inset{Left: pad, Right: pad, Top: unit.Dp(10), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return buttonLine(gtx, gtx.Dp(gap),
			a.tutMarked(tutLobbyQuit, func(gtx C) D {
				return a.secondaryButton(gtx, &a.quitBtn, "Disconnect")
			}),
			a.tutMarked(tutCreateBtn, func(gtx C) D {
				return a.attractButton(gtx, &a.createBtn, "Create a new game")
			}),
			a.tutMarked(tutHowToPlayBtn, func(gtx C) D {
				return a.secondaryButton(gtx, &a.tutBtn, "How to play")
			}),
		)
	})
}

// invitedTo reports whether this player holds a pending invitation to gameID.
func (a *App) invitedTo(gameID string) bool {
	lb := a.getLobby()
	if lb == nil {
		return false
	}
	return lb.InviteTo(gameID) != nil
}

// agentName appends the agent marker to a player's display name.
func agentName(name string, agent bool) string {
	if agent {
		return name + " [agent]"
	}
	return name
}

func (a *App) gameRow(gtx C, g lobby.GameListing, abandoned bool) D {
	btns := a.gameButtons(g.GameID)
	lb := a.getLobby()
	me := ""
	if lb != nil {
		me = lb.PlayerID()
	}
	joined := rosterHas(g, me)
	joinable, canJoin := joinGating(g)
	// Invite-only games are joined via the pop-up (or by the creator): don't
	// offer a Join button to random browsers. The creator, and anyone holding
	// a pending invitation to this game, keep theirs.
	if g.InviteOnly {
		if lb == nil || (me != g.CreatorID && !a.invitedTo(g.GameID)) {
			canJoin = false
		}
	}
	// A player who went "Back to Lobby" while holding a seat rejoins through
	// the same row — even into a full or already-running game.
	rejoin := joined && gameAlive(g.Status)
	canSpectate := !rejoin && (g.Status == config.GameStatusInProgress ||
		(joinable && len(g.Players) >= g.PlayerCount))
	// The creator of an invite-only game that still has open seats can re-open
	// the invitee picker to send more invitations — even after joining and
	// returning to the lobby (the picker only opens automatically at creation).
	canReinvite := g.InviteOnly && lb != nil && me == g.CreatorID &&
		joinable && len(g.Players) < g.PlayerCount
	canShare := canShareGame(g, abandoned)

	teams := g.Mode == config.ModeTeams
	// In an invite-only game every roster member was let in by name, so spell
	// their state out for the inviter: joined, and ready or not.
	nameOf := func(p lobby.PlayerSummary) string {
		n := agentName(p.Name, p.Agent)
		switch {
		case g.InviteOnly && p.Ready:
			n += " (joined · ready ✓)"
		case g.InviteOnly:
			n += " (joined)"
		case p.Ready:
			n += " ✓"
		}
		return n
	}
	var names []string
	if teams {
		// Group the roster by team: "A: alice, bob · B: carol"
		for t := 0; t < g.Teams(); t++ {
			var team []string
			for _, p := range g.Players {
				if p.Team != t {
					continue
				}
				team = append(team, nameOf(p))
			}
			names = append(names, fmt.Sprintf("%s: %s", g.TeamName(t), strings.Join(team, ", ")))
		}
	} else {
		for _, p := range g.Players {
			names = append(names, nameOf(p))
		}
	}
	// The status token reads from this player's point of view: a game you hold
	// a seat in shows as "joined" (waiting to start) or "playing" (started).
	statusTxt, statusCol := " · "+string(g.Status), colFg
	if joined {
		statusCol = colNATSGreen
		if g.Status == config.GameStatusInProgress {
			statusTxt = " · playing"
		} else if joinable {
			statusTxt = " · joined"
		}
	}
	info := fmt.Sprintf("%s · %s · %d/%d", shortID(g.GameID), gameShape(g), len(g.Players), g.PlayerCount)
	var extra string
	// The shape of a teams game: "2v2", and "2v2v2" past the usual two teams.
	if teams {
		shape := make([]string, g.Teams())
		for t := range shape {
			shape[t] = strconv.Itoa(g.TeamSize)
		}
		extra += " · " + strings.Join(shape, "v")
	}
	// The game's length in lines, when it has one (config.GameMeta.LineGoal),
	// or the rising floor and its tier (config.GameMeta.Survival).
	if g.LineGoal > 0 {
		extra += fmt.Sprintf(" · %d lines", g.LineGoal)
	} else if label := g.Survival.Label(); label != "" {
		extra += " · " + label
	}
	// Shared boards are as big as their seat count and the creator's
	// board-growth settings make them (config.SharedBoardWidth and
	// SharedBoardHeight) — the numbers that say what a joiner is walking
	// onto.
	if g.Mode != config.ModeCompetitive {
		extra += fmt.Sprintf(" · board %d×%d", g.BoardWidth(), g.BoardHeight()-config.HeadroomRows)
	}
	// The piece split: the seven types dealt out between the players of a
	// playfield, one ration each (config.GameMeta.SplitPieces).
	if g.SplitsPieces() {
		extra += " · split pieces"
	}
	if g.InviteOnly {
		extra += " · invite only"
	} else {
		extra += " · open"
	}
	// The agent policy: the seats agents hold against the seats they may
	// take — on each team of a teams game, in the whole game elsewhere —
	// and whether an agent left alone waits for company.
	if g.MaxAgents > 0 {
		if g.Mode == config.ModeTeams {
			extra += fmt.Sprintf(" · agents %d, max %d per team", g.AgentCount(), g.MaxAgents)
		} else {
			extra += fmt.Sprintf(" · agents %d/%d", g.AgentCount(), g.MaxAgents)
		}
		if g.AgentsPauseAlone {
			extra += " · agents pause when alone"
		}
	}
	// The play rules: one "modern" tag for the wizard's preset, else each
	// rule that differs from the classic game.
	if g.Rules().IsModern(g.Mode, g.Survival) {
		extra += " · modern"
	} else {
		if g.NextCount > 0 {
			extra += fmt.Sprintf(" · next %d", g.NextCount)
		}
		if g.Hold {
			extra += " · hold"
		}
		// The piece randomizer, when it is not the classic 7-bag: "double
		// bag" or "no bag" (config.Bag).
		if bag := g.Bag.Normalized(); bag != config.BagSingle {
			extra += " · " + bag.Label()
		}
		// The hidden rows drawn above the playfield, behind smoked glass
		// (config.GameMeta.ShowHeadroom).
		if g.ShowHeadroom {
			extra += " · hidden rows"
		}
		if g.GarbageHoles > 0 {
			if g.RandomGarbageHoles {
				extra += fmt.Sprintf(" · random holes %d", g.GarbageHoles)
			} else {
				extra += fmt.Sprintf(" · holes %d", g.GarbageHoles)
			}
		}
		if g.GuidelineGarbage {
			extra += " · modern attacks"
		}
	}

	// The creator of an invite-only game sees each outstanding invitation's
	// state under the roster line, with a retract/dismiss action per invitee.
	var invites []lobby.Invitation
	if g.InviteOnly && lb != nil && me == g.CreatorID {
		invites = lb.SentInvites(g.GameID)
	}

	sep := ", "
	if teams {
		sep = " · "
	}
	confirming := abandoned && a.confirmDeleteID == g.GameID
	// The game's line, in its dot-separated tokens: what it is, how it stands
	// for this player, how it is set up, and whether it was left behind. Each
	// is its own color, so each is its own label — packed along the line and
	// wrapped onto the next where the column runs out, never squeezed. A Flex
	// of the four turned them into four columns a letter wide on a phone.
	infoCol := func(gtx C) D {
		tokens := []layout.Widget{a.body(info, colFg), a.body(statusTxt, statusCol)}
		if extra != "" {
			tokens = append(tokens, a.body(extra, colFg))
		}
		if abandoned {
			tokens = append(tokens, a.body(" · abandoned", colErr))
		}
		// No gap between them: the tokens carry their own " · " separators.
		rows := flowRows(gtx, 0, tokens)
		kids := make([]layout.FlexChild, 0, len(rows)+2)
		for _, r := range rows {
			kids = append(kids, layout.Rigid(r))
		}
		kids = append(kids,
			layout.Rigid(a.body(strings.Join(names, sep), colMuted)),
			layout.Rigid(func(gtx C) D { return a.inviteStatusRows(gtx, g, invites) }),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
	// The row's actions, on whichever set of buttons is handed in: the real
	// ones to draw, a throwaway set to measure with (so a measuring pass does
	// not spend the real buttons' presses). They pack along one line and wrap
	// onto further ones where the row has not the width for them.
	actions := func(b *gameRowBtns) layout.Widget {
		var items []layout.Widget
		if canReinvite {
			items = append(items, func(gtx C) D { return a.secondaryButton(gtx, &b.reinvite, "Invite") })
		}
		switch {
		case rejoin:
			// Back into the seat we already hold (any mode — the roster
			// remembers our team).
			items = append(items, func(gtx C) D { return a.primaryButton(gtx, &b.join, "Rejoin") })
		case !canJoin:
		case teams:
			// One join button per team, each enabled while that team has room.
			for t := 0; t < g.Teams(); t++ {
				btn, team := &b.joinTeam[t], t
				items = append(items, func(gtx C) D { return a.teamJoinButton(gtx, btn, g, team) })
			}
		default:
			items = append(items, func(gtx C) D { return a.primaryButton(gtx, &b.join, "Join") })
		}
		if canSpectate {
			items = append(items, func(gtx C) D { return a.secondaryButton(gtx, &b.spectate, "Spectate") })
		}
		if canShare {
			items = append(items, func(gtx C) D { return a.secondaryButton(gtx, &b.share, "Share") })
		}
		if abandoned {
			items = append(items, func(gtx C) D { return a.dangerButton(gtx, &b.del, "Delete") })
		}
		return func(gtx C) D { return buttonLine(gtx, gtx.Dp(6), items...) }
	}
	return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		// The delete confirmation replaces the row's action buttons (so a stray
		// click can't join or delete while the question is up) and gets its own
		// line under the game info, so the question and buttons never squeeze
		// the info text.
		if confirming {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(infoCol),
				layout.Rigid(spacer(6)),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.body("Are you sure you want to delete this game?", colErr)),
						layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
						layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &btns.delYes, "Yes, delete") }),
						layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx) }),
						layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &btns.delNo, "Cancel") }),
					)
				}),
			)
		}
		// The buttons sit beside the game's line where the row is wide enough
		// for both — measured on a throwaway set, at the width they would
		// take unwrapped — and take a line of their own under it where they
		// are not. Rigid beside a flexed info column, a phone's buttons left
		// the column too little to lay a word out in.
		var mbtns gameRowBtns
		m := gtx
		m.Constraints.Min = image.Point{}
		m.Constraints.Max.X = 1 << 20
		rec := op.Record(gtx.Ops)
		btnW := actions(&mbtns)(m).Size.X
		rec.Stop() // measure only — discard the recorded ops
		if btnW > 0 && a.bodyWidth(gtx, info)+gtx.Dp(8)+btnW > gtx.Constraints.Max.X {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(infoCol),
				layout.Rigid(spacer(6)),
				layout.Rigid(actions(btns)),
			)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, infoCol),
			layout.Rigid(actions(btns)),
		)
	})
}

// inviteStatusRows renders the creator's per-invitation state lines on an
// invite-only game row: who is still invited (pending — retractable while
// unanswered) and who declined (dismissable). Accepted invitations don't
// appear here — accepting deletes the invitation and puts the player on the
// roster line above, marked "(joined)"/"(joined · ready ✓)".
func (a *App) inviteStatusRows(gtx C, g lobby.GameListing, invites []lobby.Invitation) D {
	if len(invites) == 0 {
		return D{}
	}
	teams := g.Mode == config.ModeTeams
	var rows []layout.FlexChild
	for _, inv := range invites {
		inv := inv
		btn := a.uninviteButton(g.GameID, inv.InviteeID)
		if btn.Clicked(gtx) {
			go a.uninvite(g.GameID, inv.InviteeID)
		}
		label, col, action := "", colGold, "Uninvite"
		if inv.Declined {
			label = fmt.Sprintf("✕ %s declined the invitation", inv.InviteeID)
			col, action = colErr, "Dismiss"
		} else {
			label = fmt.Sprintf("✉ %s invited — waiting…", inv.InviteeID)
			if teams {
				label = fmt.Sprintf("✉ %s invited to team %s — waiting…", inv.InviteeID, g.TeamName(inv.Team))
			}
		}
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body(label, col)),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(func(gtx C) D { return a.smallActionButton(gtx, btn, action, col) }),
				)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// uninviteButton returns the (lazily created) Uninvite/Dismiss button for one
// invitation, keyed by game and invitee.
func (a *App) uninviteButton(gameID, inviteeID string) *widget.Clickable {
	k := gameID + "|" + inviteeID
	b, ok := a.uninviteBtns[k]
	if !ok {
		b = &widget.Clickable{}
		a.uninviteBtns[k] = b
	}
	return b
}

// smallActionButton is the compact bordered list-row action (like
// viewBoardButton) in an arbitrary accent color.
func (a *App) smallActionButton(gtx C, btn *widget.Clickable, label string, col colorN) D {
	return widget.Border{Color: col, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		b := pixelize(material.Button(a.th, btn, label))
		b.Background = colPanel
		b.Color = col
		b.TextSize = unit.Sp(9)
		b.Inset = layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(10), Right: unit.Dp(10)}
		return b.Layout(gtx)
	})
}

// teamJoinButton renders the "Join A (1/2)"-style button for one team of a
// teams-mode listing, hidden once that team is full.
func (a *App) teamJoinButton(gtx C, btn *widget.Clickable, g lobby.GameListing, team int) D {
	n := g.TeamMemberCount(team)
	if n >= g.TeamSize {
		return D{}
	}
	label := fmt.Sprintf("Join %s (%d/%d)", g.TeamName(team), n, g.TeamSize)
	return a.primaryButton(gtx, btn, label)
}

// teamName renders a team index as its display letter (A, B, C, …) — the
// name of a team across MANY games (the history's overall tally), where
// each game's own names do not carry.
func teamName(team int) string { return config.TeamLetter(team) }

func (a *App) handleChatSubmit(gtx C) {
	send := a.chatBtn.Clicked(gtx)
	for {
		ev, ok := a.chatEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			send = true
		}
	}
	if send {
		text := strings.TrimSpace(a.chatEd.Text())
		if text != "" {
			a.chatEd.SetText("")
			go a.sendChat(text)
		}
	}
}

func (a *App) gameButtons(id string) *gameRowBtns {
	b, ok := a.gameBtns[id]
	if !ok {
		b = &gameRowBtns{}
		a.gameBtns[id] = b
	}
	return b
}

// --- small layout helpers ---

// pixelize restyles a material button into the 8-bit chrome: pixel face,
// square corners, and a smaller size (the pixel face runs large per point).
func pixelize(b material.ButtonStyle) material.ButtonStyle {
	b.CornerRadius = 0
	b.Font.Typeface = pixelTypeface
	b.TextSize = unit.Sp(11)
	return b
}

// primaryButton renders a filled-accent action (Join, Ready, Create Game,
// Send) in the 8-bit chrome: pixel face, square corners, hard offset shadow.
func (a *App) primaryButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return pixelize(material.Button(a.th, btn, label)).Layout(gtx)
	})
}

const (
	attractPeriod = 3 * time.Second        // time between glint sweeps
	attractSweep  = 600 * time.Millisecond // duration of one sweep
)

// attractButton renders a primaryButton in attract mode — the treatment for
// the one action a screen is waiting on (Create a new game, the initial
// ready-up click): a bas-relief bevel lit from the upper-left (same idiom as
// the board cells' block shading) and a chunky diagonal glint that sweeps
// across the face every few seconds, arcade attract-screen style.
func (a *App) attractButton(gtx C, btn *widget.Clickable, label string) D {
	return a.attractStyled(gtx, pixelize(material.Button(a.th, btn, label)))
}

// bigAttractButton is the attract treatment at marquee scale — the login
// screen's Play: a taller button with a larger pixel label, stretched to
// whatever width the caller gives it.
func (a *App) bigAttractButton(gtx C, btn *widget.Clickable, label string) D {
	b := pixelize(material.Button(a.th, btn, label))
	b.TextSize = unit.Sp(16)
	b.Inset = layout.Inset{Top: unit.Dp(14), Bottom: unit.Dp(14), Left: unit.Dp(24), Right: unit.Dp(24)}
	return a.attractStyled(gtx, b)
}

// attractStyled draws button style b with the attract-mode bevel and glint.
func (a *App) attractStyled(gtx C, b material.ButtonStyle) D {
	return hardShadow(gtx, func(gtx C) D {
		dims := b.Layout(gtx)
		w, h := dims.Size.X, dims.Size.Y
		bounds := image.Rect(0, 0, w, h)

		bv := gtx.Dp(3)
		hi := colorN{R: 0xff, G: 0xff, B: 0xff, A: 0x48}
		lo := colorN{A: 0x55}
		fillRect(gtx.Ops, image.Rect(0, 0, w-bv, bv), hi)
		fillRect(gtx.Ops, image.Rect(0, 0, bv, h-bv), hi)
		fillRect(gtx.Ops, image.Rect(bv, h-bv, w, h), lo)
		fillRect(gtx.Ops, image.Rect(w-bv, bv, w, h), lo)

		// Glint sweep, phase-locked to the wall clock so every attract button
		// on screen flashes in unison.
		ph := time.Duration(gtx.Now.UnixNano()) % attractPeriod
		if ph < 0 {
			ph += attractPeriod // zero/pre-epoch Now (headless tests)
		}
		if ph < attractSweep {
			t := float64(ph) / float64(attractSweep)
			band := gtx.Dp(16)
			step := max(gtx.Dp(4), 1)
			span := w + h/2 + 2*band
			pos := -band + int(t*float64(span))
			glint := colorN{R: 0xff, G: 0xff, B: 0xff, A: 0x5a}
			for y := 0; y < h; y += step {
				// Stepped 45° slant: each pixel-row of the band sits half a
				// step left of the one above, giving a chunky "/" streak.
				seg := image.Rect(pos-y/2, y, pos-y/2+band, y+step).Intersect(bounds)
				if !seg.Empty() {
					fillRect(gtx.Ops, seg, glint)
				}
			}
			animate(gtx) // keep the sweep animating
		} else {
			// Idle between sweeps: wake up exactly when the next one is due
			// instead of redrawing every frame.
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(attractPeriod - ph)})
		}
		return dims
	})
}

// secondaryButton renders a non-primary action (Spectate, Back to Lobby) so it
// reads as clearly clickable: accent-colored pixel label and a chunky accent
// border over the panel background, visually distinct from the filled-accent
// primary buttons without blending into the dark window background.
func (a *App) secondaryButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			b := pixelize(material.Button(a.th, btn, label))
			b.Background = colPanel
			b.Color = colAccent
			return b.Layout(gtx)
		})
	})
}

// dangerButton renders a destructive action (Delete, and its confirmation) in
// the secondary chrome but with the error red, so it cannot be mistaken for
// Join/Spectate.
func (a *App) dangerButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colErr, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			b := pixelize(material.Button(a.th, btn, label))
			b.Background = colPanel
			b.Color = colErr
			return b.Layout(gtx)
		})
	})
}

// viewBoardButton is the compact accent-bordered "View board" action on each
// GAME HISTORY row, making it obvious that a finished game can be opened to see
// its final playfield. It mirrors secondaryButton's styling but with tighter
// padding and a smaller label so it fits a list row.
func (a *App) viewBoardButton(gtx C, btn *widget.Clickable) D {
	return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		b := pixelize(material.Button(a.th, btn, "View board"))
		b.Background = colPanel
		b.Color = colAccent
		b.TextSize = unit.Sp(9)
		b.Inset = layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(10), Right: unit.Dp(10)}
		return b.Layout(gtx)
	})
}

// pixelLabelFit lays out a single-line pixel-face label, cutting the text
// to a trailing "..." when it would exceed the width. Gio's own MaxLines
// truncation is not used: with this face its truncator lands at the START of
// the line. The face (Press Start 2P) is monospace, so one unconstrained
// measurement of the full string gives the per-character width and the cut
// is a plain character count.
func (a *App) pixelLabelFit(gtx C, size unit.Sp, txt string, col colorN) D {
	l := a.pixel(size, txt, col)
	l.MaxLines = 1
	if full := a.pixelWidth(gtx, size, txt); full > gtx.Constraints.Max.X {
		runes := []rune(txt)
		per := float64(full) / float64(max(len(runes), 1))
		keep := int(float64(gtx.Constraints.Max.X)/per) - len("...")
		if keep < 1 {
			keep = 1
		}
		if keep < len(runes) {
			l.Text = string(runes[:keep]) + "..."
		}
	}
	return l.Layout(gtx)
}

// pixelWidth measures txt in the pixel face at size, unconstrained: what the
// label WOULD take on one line, which is how pixelLabelFit knows whether it
// has to cut and how callers choose between one line and two.
func (a *App) pixelWidth(gtx C, size unit.Sp, txt string) int {
	l := a.pixel(size, txt, colFg)
	l.MaxLines = 1
	m := gtx
	m.Constraints.Min = image.Point{}
	m.Constraints.Max.X = 1 << 20
	rec := op.Record(gtx.Ops)
	d := l.Layout(m)
	rec.Stop() // measure only — discard the recorded ops
	return d.Size.X
}

func (a *App) header(txt string) layout.Widget {
	return func(gtx C) D {
		l := a.pixel(unit.Sp(10), txt, colAccent)
		return layout.Inset{Bottom: unit.Dp(5)}.Layout(gtx, l.Layout)
	}
}

func (a *App) body(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = c
		return l.Layout(gtx)
	}
}

// winnerMark is where a line asks for the winner's trophy to be DRAWN. It
// travels inside the ordinary strings the roster and legend lines are built
// from — a control character, so it can never collide with anything a player
// could put in a name (config.ValidatePlayerName) — and markedText turns it
// into glyphTrophy at the one place that knows how. A typed trophy would be
// simpler, but it needs a font that has U+1F3C6 and since Gio v0.10 none in
// reach does: it shapes to .notdef and comes out a tofu box.
//
// In practice it always leads its line. That is not an accident: a drawn
// glyph is a widget, and a widget cannot sit INSIDE a paragraph that wraps,
// so the one line that wraps — the history summary (archiveModeLine) — marks
// no winner at all and leaves that to the roster lines beside it.
const winnerMark = "\x00"

// markedBody lays body text out with the trophy drawn wherever winnerMark
// appears — one plain label when it appears nowhere, which is nearly always,
// so an unmarked line behaves exactly as body does and still wraps.
// markedSpan is the same for a run inside spansLine, which packs runs across
// one row and so wants each held to a single line.
func (a *App) markedBody(txt string, col colorN, emph bool) layout.Widget {
	return a.markedText(txt, col, emph, 0)
}

func (a *App) markedSpan(txt string, col colorN, emph bool) layout.Widget {
	return a.markedText(txt, col, emph, 1)
}

// markedText is the two of them. The pieces are placed by hand on a shared
// baseline — a Flex would align them by its own rules, and its Baseline mode
// has no idea what to do with a bitmap — with the trophy sized to the text's
// ascent and standing on the baseline beside it. Each text run is given the
// width still left on the line, so a long marked line (the history summary in
// the replay dialog) wraps under itself as it did before there was a trophy
// in it, and the line reports the first run's baseline so it drops into
// spansLine and the legend rows exactly where a plain label would.
func (a *App) markedText(txt string, col colorN, emph bool, maxLines int) layout.Widget {
	run := func(s string) layout.Widget {
		return func(gtx C) D {
			l := material.Body2(a.th, s)
			l.Color, l.MaxLines = col, maxLines
			if emph {
				l.Font.Weight, l.Font.Style = font.Bold, font.Italic
			}
			return l.Layout(gtx)
		}
	}
	return func(gtx C) D {
		parts := strings.Split(txt, winnerMark)
		if len(parts) == 1 {
			return run(txt)(gtx)
		}
		// Each piece at its OWN size: these labels are often in a Flexed slot,
		// whose Min would otherwise make the first run report the whole slot's
		// width and the row come out that much wider than the line it holds.
		avail := gtx.Constraints.Max.X
		gtx.Constraints.Min = image.Point{}
		// The text's ascent (its top to its baseline), which sizes and seats
		// the trophy.
		ascent := 0
		for _, p := range parts {
			if p != "" {
				m := op.Record(gtx.Ops)
				d := run(p)(gtx)
				m.Stop()
				ascent = max(ascent, d.Size.Y-d.Baseline)
			}
		}
		gap := gtx.Dp(3)
		x, bottom, baseline := 0, 0, -1
		place := func(w layout.Widget, trophy bool) {
			cgtx := gtx
			cgtx.Constraints.Max.X = max(0, avail-x)
			m := op.Record(gtx.Ops)
			d := w(cgtx)
			call := m.Stop()
			y := ascent - d.Size.Y // the trophy stands ON the baseline
			if !trophy {
				y = ascent - (d.Size.Y - d.Baseline)
				if baseline < 0 {
					baseline = d.Baseline
				}
			}
			func() {
				defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
				call.Add(gtx.Ops)
			}()
			x += d.Size.X
			bottom = max(bottom, y+d.Size.Y)
			if trophy {
				x += gap
			}
		}
		for i, p := range parts {
			if i > 0 {
				place(glyphWidget(glyphTrophy, gtx.Metric.PxToDp(ascent), col), true)
			}
			if p != "" {
				place(run(p), false)
			}
		}
		return D{Size: image.Pt(x, bottom), Baseline: max(0, bottom-ascent)}
	}
}

// modalW is the width a modal dialog is laid out at: the width it was
// designed for, or everything the screen can give it less a margin, whichever
// is smaller. Setting Max.X to the design width alone does NOT fit it — a
// 480 dp wizard on a 390 dp phone is laid out 480 dp wide and hangs off both
// edges, taking its Next button over one of them, which is the one thing a
// wizard cannot afford to lose.
func modalW(gtx C, want unit.Dp) int {
	return min(gtx.Dp(want), max(gtx.Constraints.Max.X-gtx.Dp(12), gtx.Dp(1)))
}

func bordered(gtx C, w layout.Widget) D {
	return widget.Border{Color: colBorder, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(unit.Dp(4)).Layout(gtx, w)
	})
}

func statusText(s lobby.PresenceStatus) string {
	switch s {
	case lobby.StatusInGame:
		return "In Game"
	case lobby.StatusSpectating:
		return "Spectating"
	default:
		return "In Lobby"
	}
}

// shortID is how a game is named on screen: its NAME whole when its creator
// gave it one — a named game's ID is its name (config.GameName), and a name
// is what everyone calls the game — and a generated ID cut to its first
// group, which is as much of a UUID as anyone reads.
func shortID(id string) string {
	if config.IsGameName(id) {
		return id
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sortedGames(m map[string]lobby.GameListing) []lobby.GameListing {
	out := make([]lobby.GameListing, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func sortedPlayers(m map[string]lobby.PlayerPresence) []lobby.PlayerPresence {
	out := make([]lobby.PlayerPresence, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// gameShape names a listing's game type the way the wizard offers it:
// "co-op" for the crew's shared board, "competitive · one board" for a
// shared board scored per seat, "competitive" for a board each, "teams" for
// several shared boards.
func gameShape(g lobby.GameListing) string {
	switch {
	case g.Mode == config.ModeCooperative && g.IndividualScoring():
		return "competitive · one board"
	case g.Mode == config.ModeCooperative:
		return "co-op"
	default:
		return g.Mode.String()
	}
}

// canShareGame is the lobby row's word on its Share button: an open game is
// anyone's to pass on while it takes joiners (joinGating) — full or not, an
// open game's seats free up as players leave — and its link is the row's
// Share (share.go). An invite-only game is joined by invitation, not by
// link, and an abandoned one is for deleting.
func canShareGame(g lobby.GameListing, abandoned bool) bool {
	joinable, _ := joinGating(g)
	return !g.InviteOnly && joinable && !abandoned
}

// joinGating is the lobby row's word on a game's seats: joinable while the
// game takes joiners — before it starts, and, for an OPEN game, while it
// runs too (a free seat of a running open game is anyone's, played on the
// live board; the countdown is the one moment it is not) — and canJoin
// when a seat is free as well.
func joinGating(g lobby.GameListing) (joinable, canJoin bool) {
	joinable = g.Status == config.GameStatusCreated || g.Status == config.GameStatusStarting ||
		(g.Dynamic() && g.Status == config.GameStatusInProgress)
	_, _, free := g.FreeSeat(0)
	if g.Mode == config.ModeTeams {
		free = len(g.Players) < g.PlayerCount
	}
	return joinable, joinable && free
}
