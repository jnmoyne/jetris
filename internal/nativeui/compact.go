package nativeui

// The compact game screen — a phone, a tablet held portrait, any window too
// small for the full three-column one (formfactor.go decides which).
//
// The rule here is that nothing permanent stands between the player and the
// playfield. What the full screen spends whole columns on, this one either
// shrinks into one slim bar across the top — the HOLD box, the score, the
// NEXT pieces, all at their own small cell size instead of the board's — or
// puts behind a tap: the HUD (players, stats, the controls legend, the lab
// switches, Back to Lobby) opens as a panel over the board, the chat as
// another, and each closes on the button that opened it, on its own ✕, or on
// a tap anywhere beside it. The playfield gets everything else, and the touch
// gestures (gesture.go) get the whole width of it — the surface a swipe lands
// on runs edge to edge, well past the board's own columns.
//
// The bar also carries the on-screen pad's switch. On a phone held portrait
// the pad sits UNDER the playfield and takes about a third of its rows, so it
// starts off there (padVisible) and one tap brings it back; held landscape,
// or on a tablet, it flanks the board in room the playfield could not have
// used and starts on.

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

const (
	// compactBarH is the top bar's height in dp — a comfortable touch target
	// for the buttons in it, and room for a two-cell preview tile.
	compactBarH = 54
	// compactTileCell is the bar's preview cell in dp: a tile is
	// previewCols × that wide and two of them tall. Small enough that the
	// HOLD box and three NEXT pieces leave the score its room on a phone,
	// big enough to read a piece by its shape and color at arm's length.
	compactTileCell = 10
	// compactNextMax is the most NEXT pieces the bar shows; a narrow one
	// shows fewer (compactBar fits them to the room left over).
	compactNextMax = 3
	// compactDrawerFrac is how much of the screen an open panel takes, in
	// percent of its long axis: the HUD panel's width, the chat panel's
	// height. The rest stays board, so the game is still visible behind the
	// panel and closing it is one tap on what you can see.
	compactDrawerFrac = 78
)

// compactGameScreen is the game screen for a small display: the bar, the
// board with everything left, and whichever panel is open over them.
func (a *App) compactGameScreen(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode, showMsgs bool) D {
	started := view.status == string(config.GameStatusInProgress)
	var children []layout.FlexChild
	if mode == engine.ModePlayer && !started && !view.gameOver {
		// Pre-start there is nothing to play and everything to decide: the
		// ready-up action belongs on the screen, not behind the menu.
		children = append(children, layout.Rigid(func(gtx C) D { return a.compactReadyBar(gtx, view) }))
	}
	children = append(children, layout.Flexed(1, func(gtx C) D {
		return layout.UniformInset(unit.Dp(4)).Layout(gtx, func(gtx C) D {
			return a.gameBoardArea(gtx, eng, view, mode, gmode)
		})
	}))
	if showMsgs {
		children = append(children, layout.Rigid(a.natsMsgSection))
	}
	body := func(gtx C) D { return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...) }

	// A panel opens UNDER the bar, never over it: the bar is this screen's
	// navigation, so the button that opened a panel is always there to shut
	// it again, and tapping the other one swaps panels in one go.
	switch {
	case a.hudDrawer:
		inner := body
		body = func(gtx C) D {
			return a.drawer(gtx, inner, layout.W, func(gtx C) D {
				return a.gameHUD(gtx, eng, view, mode, gmode)
			})
		}
	case a.chatDrawer:
		inner := body
		body = func(gtx C) D {
			return a.drawer(gtx, inner, layout.S, func(gtx C) D {
				return a.gameChatPanel(gtx, eng, view, true)
			})
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.compactBar(gtx, eng, view, mode, gmode) }),
		layout.Flexed(1, body),
	)
}

// handleFormClicks drains the compact screen's chrome and the full screen's
// fold handles. Called at the top of the game frame, before the pad and the
// gestures, so a panel opened or closed this frame already counts when they
// decide whether the board is reachable (drawerOpen).
func (a *App) handleFormClicks(gtx C, eng *engine.Engine) {
	if a.drawerEng != eng {
		// A fresh game screen (join, rejoin, spectate): no panel carries over
		// from the last one, and its chat is a different conversation, so the
		// unread mark starts from nothing rather than from what was read
		// there. The pad switch is the player's own and does carry over.
		a.drawerEng, a.hudDrawer, a.chatDrawer, a.chatSeen = eng, false, false, 0
	}
	for a.barHudBtn.Clicked(gtx) {
		a.hudDrawer, a.chatDrawer = !a.hudDrawer, false
	}
	for a.barChatBtn.Clicked(gtx) {
		a.chatDrawer, a.hudDrawer = !a.chatDrawer, false
	}
	for a.barPadBtn.Clicked(gtx) {
		// The player's standing answer on the on-screen pad, from whatever
		// the device's default happened to be showing.
		if a.padVisible() {
			a.padPref = -1
		} else {
			a.padPref = 1
		}
	}
	for a.drawerCloseBtn.Clicked(gtx) {
		a.hudDrawer, a.chatDrawer = false, false
	}
	for a.drawerScrim.Clicked(gtx) {
		a.hudDrawer, a.chatDrawer = false, false
	}
	for a.hudFoldBtn.Clicked(gtx) {
		a.hudFold = !a.hudFold
	}
	for a.chatFoldBtn.Clicked(gtx) {
		a.chatFold = !a.chatFold
	}
}

// drawer lays the screen out with panel over it, against the given edge, and
// a scrim over the rest — tinted, so the board still reads through it, and
// clickable, so a tap beside the panel closes it. The panel takes
// compactDrawerFrac of the axis it slides in along.
func (a *App) drawer(gtx C, base layout.Widget, edge layout.Direction, panel layout.Widget) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(base),
		layout.Expanded(func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			return a.drawerScrim.Layout(gtx, func(gtx C) D {
				// Dimmed, not blacked out: the board stays visible behind the
				// panel, so what closing it goes back to is never in doubt.
				fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), withAlpha(colBg, 0.62))
				return D{Size: gtx.Constraints.Max}
			})
		}),
		layout.Expanded(func(gtx C) D {
			return edge.Layout(gtx, func(gtx C) D {
				switch edge {
				case layout.W, layout.E:
					w := min(gtx.Constraints.Max.X*compactDrawerFrac/100, gtx.Dp(340))
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
				default:
					h := gtx.Constraints.Max.Y * compactDrawerFrac / 100
					gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
				}
				// The panel's own pointer area, over the scrim's: a press
				// inside the panel — on a widget or on its empty space
				// alike — is the panel's, so nothing about it closes it by
				// accident. Only a press BESIDE it reaches the scrim.
				return pointerArea(gtx, &a.drawerTag, func(gtx C) D {
					return background(gtx, colPanel, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								// The panel's own way out, at its top right.
								return layout.Inset{Top: unit.Dp(6), Right: unit.Dp(6), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
									return layout.E.Layout(gtx, func(gtx C) D {
										return a.barButton(gtx, &a.drawerCloseBtn, glyphClose, false)
									})
								})
							}),
							layout.Flexed(1, func(gtx C) D {
								return layout.UniformInset(unit.Dp(10)).Layout(gtx, panel)
							}),
						)
					})
				})
			})
		}),
	)
}

// compactBar is the screen's one permanent row: the menu button, the HOLD
// box, the score line, the NEXT pieces, the pad switch and the chat button.
// Everything in it is sized so the row holds together on the narrowest phone
// — the NEXT previews drop one at a time as the width runs out, and the
// score line takes whatever is left (it elides rather than wraps).
func (a *App) compactBar(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	barH := gtx.Dp(compactBarH)
	cell := gtx.Dp(compactTileCell)
	tile := previewCols * cell
	player := mode == engine.ModePlayer
	hold := player && eng.HoldEnabled()

	var pieces []game.PieceType
	if player {
		pieces = eng.NextPieces()
	}
	// The NEXT previews take what the buttons, the HOLD box and a legible
	// score line leave; a narrow bar simply shows fewer of them.
	btnW := gtx.Dp(compactBarH-14) + gtx.Dp(8)
	room := gtx.Constraints.Max.X - 3*btnW - gtx.Dp(96)
	if hold {
		room -= tile + gtx.Dp(8)
	}
	nNext := min(len(pieces), compactNextMax, max(0, room/(tile+gtx.Dp(4))))

	kids := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.barButton(gtx, &a.barHudBtn, glyphMenu, a.hudDrawer) }),
	}
	if hold {
		held, has := eng.HeldPiece()
		used := eng.HoldUsed()
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				// Tapping it holds, exactly as the full screen's HOLD box does.
				return a.holdBoxBtn.Layout(gtx, func(gtx C) D {
					return a.compactHoldTile(gtx, held, has, used, cell)
				})
			})
		}))
	}
	kids = append(kids, layout.Flexed(1, func(gtx C) D {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
			return a.compactStats(gtx, view, mode, gmode)
		})
	}))
	if nNext > 0 {
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return a.compactNextRow(gtx, pieces[:nNext], cell)
		}))
	}
	if player && !view.gameOver {
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return a.barButton(gtx, &a.barPadBtn, glyphPad, a.padVisible())
			})
		}))
	}
	kids = append(kids, layout.Rigid(func(gtx C) D {
		return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			d := a.barButton(gtx, &a.barChatBtn, glyphChat, a.chatDrawer)
			// Unread mark: messages have arrived since the panel last showed
			// them (gameChatPanel records what it showed in chatSeen).
			if n := a.gameChatCount(eng.GameID()); n > a.chatSeen && !a.chatDrawer {
				dot := gtx.Dp(7)
				fillRect(gtx.Ops, image.Rect(d.Size.X-dot, 0, d.Size.X, dot), colGold)
			}
			return d
		})
	}))

	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = barH, barH
	return background(gtx, colPanel, func(gtx C) D {
		d := layout.Inset{Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
		// A hairline under the bar, so it reads as its own division over the
		// board rather than as part of it.
		fillRect(gtx.Ops, image.Rect(0, barH-gtx.Dp(2), d.Size.X, barH), colBorder)
		return D{Size: image.Pt(d.Size.X, barH)}
	})
}

// barButton is one of the bar's square icon buttons — the same arcade chrome
// as the control pad's, at a thumb-friendly size. on lights it up: the panel
// it opens is open, or the pad it switches is showing.
func (a *App) barButton(gtx C, btn *widget.Clickable, bm []string, on bool) D {
	sz := gtx.Dp(compactBarH - 14)
	bg, fg := colPanel, colAccent
	if on {
		bg, fg = colAccent, colBg
	}
	gtx.Constraints = layout.Exact(image.Pt(sz, sz))
	return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return btn.Layout(gtx, func(gtx C) D {
			return background(gtx, bg, func(gtx C) D {
				return layout.Center.Layout(gtx, glyphWidget(bm, gtx.Metric.PxToDp(sz/2), fg))
			})
		})
	})
}

// compactHoldTile is the bar's HOLD box: the well's frame idiom at the bar's
// own small cell, holding the set-aside piece — dimmed while it is spent for
// this piece, empty until the first hold.
func (a *App) compactHoldTile(gtx C, held game.PieceType, has, used bool, cell int) D {
	w, h := previewCols*cell, 2*cell
	fw := max(cell/8, 2)
	fillRect(gtx.Ops, image.Rect(0, 0, w+2*fw, h+2*fw), colBorder)
	fillRect(gtx.Ops, image.Rect(fw, fw, w+fw, h+fw), colBg)
	if has {
		rows := 1
		for _, rc := range (game.Piece{Type: held}).Cells() {
			rows = max(rows, rc[0]+1)
		}
		drawMiniPiece(gtx.Ops, fw, fw+(h-rows*cell)/2, cell, held)
		if used {
			fillRect(gtx.Ops, image.Rect(fw, fw, w+fw, h+fw), withAlpha(colBg, 0.62))
		}
	}
	return D{Size: image.Pt(w+2*fw, h+2*fw)}
}

// compactNextRow is the bar's piece preview: the upcoming pieces left to
// right in play order, at the bar's cell, with no well around them (the row
// reads as a queue running toward the board).
func (a *App) compactNextRow(gtx C, pieces []game.PieceType, cell int) D {
	gap := gtx.Dp(4)
	x, h := 0, 2*cell
	for _, pt := range pieces {
		rows := 1
		for _, rc := range (game.Piece{Type: pt}).Cells() {
			rows = max(rows, rc[0]+1)
		}
		drawMiniPiece(gtx.Ops, x, (h-rows*cell)/2, cell, pt)
		x += previewCols*cell + gap
	}
	return D{Size: image.Pt(max(0, x-gap), h)}
}

// compactStats is the bar's readout, in the room the previews and the buttons
// leave: the score (per team in teams mode), the level, and — while it is
// worth naming — the batch round trip, which is what this game is about. It
// elides rather than wraps, so the bar never grows a second line.
func (a *App) compactStats(gtx C, view gameView, mode engine.Mode, gmode config.GameMode) D {
	line := fmt.Sprintf("%d  LV%d", view.score, view.level)
	if gmode == config.ModeTeams {
		line = fmt.Sprintf("%s %d · %s %d  LV%d",
			teamName(0), view.teamScores[0], teamName(1), view.teamScores[1], view.level)
	}
	col := colFg
	if view.linkDown > 0 {
		line, col = "LINK LOST "+formatLinkDown(view.linkDown), colOrange
	} else if mode == engine.ModePlayer && view.rtt > 0 {
		line += "  " + formatRTT(view.rtt)
		col = rttColor(view.rtt)
	}
	// And, last so it is the first thing a narrow bar gives up, which server
	// this session is on — the same reminder the full screen's HUD column
	// carries (sessionLine), for the bars wide enough to hold it. The HUD
	// panel, one tap away, always has it.
	a.mu.Lock()
	server := a.connName
	a.mu.Unlock()
	return layout.W.Layout(gtx, func(gtx C) D {
		// A text label handed a tall minimum height takes that height and
		// sits its glyphs at the top of it; zeroed, it is its own height and
		// W centers it on the bar.
		gtx.Constraints.Min.Y = 0
		if server == "" {
			return a.pixelLabelFit(gtx, unit.Sp(10), line, col)
		}
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(10), line, col).Layout),
			layout.Flexed(1, func(gtx C) D {
				// Whole or not at all: a server name cut to "..." is noise
				// where a phone's bar has no room, and the HUD panel one tap
				// away carries it in full either way.
				txt := "  @ " + server
				if a.pixelWidth(gtx, unit.Sp(8), txt) > gtx.Constraints.Max.X {
					return D{}
				}
				return a.pixel(unit.Sp(8), txt, colMuted).Layout(gtx)
			}),
		)
	})
}

// compactReadyBar is the pre-start row: the ready-up action, and how much of
// the table is waiting on the rest.
func (a *App) compactReadyBar(gtx C, view gameView) D {
	ready := 0
	for _, p := range view.readyPlayer {
		if p.Ready {
			ready++
		}
	}
	label := "TAP WHEN READY"
	if view.myReady {
		label = "NOT READY ANYMORE"
	}
	return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(6), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if view.myReady {
					return a.primaryButton(gtx, &a.readyBtn, label)
				}
				return a.attractButton(gtx, &a.readyBtn, label)
			}),
			layout.Rigid(hSpacer(10)),
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min.Y = 0
				return a.pixelLabelFit(gtx, unit.Sp(10),
					fmt.Sprintf("%d/%d READY", ready, len(view.readyPlayer)), colMuted)
			}),
		)
	})
}
