package nativeui

// The game screen — the same one on a phone, a tablet and a desktop.
//
// The rule is that nothing permanent stands between the player and the
// playfield. One slim bar runs across the top: the score, and a switch for
// each thing that costs the board room. Everything else is behind a tap —
// the HUD (players, stats, the controls legend, the lab switches, Back to
// Lobby) opens as a panel over the board, the chat as another, and each
// closes on the button that opened it, on its own ✕, or on a tap anywhere
// beside it. The playfield gets all the rest, with the HOLD box and the NEXT
// well flanking it as they always have, and the touch gestures (gesture.go)
// get its whole width — the surface a swipe lands on runs edge to edge, well
// past the board's own columns.
//
// The bar's switches are the on-screen pad and the opponents' boards. Each
// starts wherever the screen can afford it (padVisible, oppVisible) and
// stays wherever the player last put it: on a phone held portrait the pad
// stacks UNDER the playfield and takes about a third of its rows, so it
// starts off there, while on any screen with width to spare the opponents
// start on, as they always were on a desktop.

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"sort"

	"jetris/internal/config"
	"jetris/internal/engine"
)

const (
	// gameBarH is the top bar's height in dp — a comfortable touch target
	// for the buttons in it, and room for a two-cell preview tile.
	gameBarH = 54
	// drawerFrac is how much of the screen an open panel takes, in
	// percent of its long axis: the HUD panel's width, the chat panel's
	// height. The rest stays board, so the game is still visible behind the
	// panel and closing it is one tap on what you can see.
	drawerFrac = 78
	// drawerMaxH/drawerMaxW cap a bottom drawer on a big screen (see above).
	drawerMaxH = 420
	drawerMaxW = 820
	// oppColPct/oppColMaxW bound the opponents' column when it is shown: a
	// slice of the screen, never more than a thumbnail's worth. Every dp of
	// it comes off the playfield.
	oppColPct  = 24
	oppColMaxW = 150
)

// gameScreen is THE game screen, on every display: the bar, the board with
// everything left over, and whichever panel is open over them.
func (a *App) gameScreen(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode, showMsgs bool) D {
	started := view.status == string(config.GameStatusInProgress)
	var children []layout.FlexChild
	if mode == engine.ModePlayer && !started && !view.gameOver {
		// Pre-start there is nothing to play and everything to decide: the
		// ready-up action belongs on the screen, not behind the menu.
		children = append(children, layout.Rigid(func(gtx C) D { return a.readyBar(gtx, view) }))
	}
	children = append(children, layout.Flexed(1, func(gtx C) D {
		return layout.UniformInset(unit.Dp(4)).Layout(gtx, func(gtx C) D {
			// The board area never reports more than its slot. Its cell has a
			// floor (fitCellPx), so a column too short for a whole playfield
			// gets one that overflows — and a Flex places the next child after
			// whatever size this one CLAIMS, which would push the chat strip
			// clean off the bottom of the screen rather than merely crowd it.
			// Overflow is the board's own problem to draw; it is not the
			// chat's to be shoved out by.
			return clampH(gtx, func(gtx C) D {
				if !a.oppVisible() || !a.hasOpponents(eng, mode, gmode) {
					return a.gameBoardArea(gtx, eng, view, mode, gmode)
				}
				// The opponents stand beside the board, and the two are centred
				// TOGETHER: a playfield is portrait and a screen is not, so on a
				// wide one the board cannot use all the width whatever it does
				// (its cell is bound by the height) — and a board centred in
				// what is left over, with the opponents pinned out at the edge,
				// reads as two things that missed each other rather than as one
				// row. The column's width is taken off the board area BEFORE it
				// fits itself, so nothing is squeezed after the fact.
				oppW := min(gtx.Constraints.Max.X*oppColPct/100, gtx.Dp(oppColMaxW))
				return layout.Center.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min = image.Point{}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Max.X = max(0, gtx.Constraints.Max.X-oppW)
							return a.gameBoardArea(gtx, eng, view, mode, gmode)
						}),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.X, gtx.Constraints.Max.X = oppW, oppW
							return a.opponentBoards(gtx, eng)
						}),
					)
				})
			})
		})
	}))
	// The chat is a strip along the bottom, in the flow rather than over the
	// board: the bar's chat button shows and hides it, and while it is up the
	// board simply has that much less room. It is not something to dismiss —
	// a conversation you are half-watching while you play — so it has no
	// close button of its own and nothing about the board shuts it.
	if a.chatVisible() {
		children = append(children, layout.Rigid(func(gtx C) D {
			return a.gameChatPanel(gtx, eng, view)
		}))
	}
	if showMsgs {
		children = append(children, layout.Rigid(a.natsMsgSection))
	}
	body := func(gtx C) D { return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...) }

	// The HUD is the other kind of panel: a page of reference you open, read
	// and dismiss. It opens UNDER the bar, never over it, so the button that
	// opened it is always there to shut it again.
	if a.hudDrawer {
		inner := body
		body = func(gtx C) D {
			return a.drawer(gtx, inner, layout.W, func(gtx C) D {
				return a.gameHUD(gtx, eng, view, mode, gmode)
			})
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.gameBar(gtx, eng, view, mode, gmode) }),
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
		a.drawerEng, a.hudDrawer, a.chatSeen = eng, false, 0
	}
	for a.barHudBtn.Clicked(gtx) {
		a.hudDrawer = !a.hudDrawer
	}
	for a.barChatBtn.Clicked(gtx) {
		// Showing the chat and typing into it are separate: this only puts
		// the strip on screen. The keys move on a click or Tab (input.go).
		if a.chatVisible() {
			a.chatPref = -1
		} else {
			a.chatPref = 1
		}
	}
	for a.barOppBtn.Clicked(gtx) {
		if a.oppVisible() {
			a.oppPref = -1
		} else {
			a.oppPref = 1
		}
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
		a.hudDrawer = false
	}
	for a.drawerScrim.Clicked(gtx) {
		a.hudDrawer = false
	}
}

// drawer lays the screen out with panel over it, against the given edge, and
// a scrim over the rest — tinted, so the board still reads through it, and
// clickable, so a tap beside the panel closes it. The panel takes
// drawerFrac of the axis it slides in along.
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
					w := min(gtx.Constraints.Max.X*drawerFrac/100, gtx.Dp(340))
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
				default:
					// A share of the height, but never more than a panel's
					// worth: on a phone drawerFrac IS the panel, while on a
					// desktop the same fraction would hand a whole screen to
					// one line of chat. Capped, and no wider than a column of
					// text wants to be — layout.S centres what it is given, so
					// the narrower panel sits centred over the board.
					h := min(gtx.Constraints.Max.Y*drawerFrac/100, gtx.Dp(drawerMaxH))
					gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
					w := min(gtx.Constraints.Max.X, gtx.Dp(drawerMaxW))
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
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

// gameBar is the screen's one permanent row: the menu button, the score
// line, and the switches for the things that cost the playfield room — the
// opponents' boards, the on-screen pad — with the chat at the far end. The
// HOLD box and the NEXT well are NOT here: they flank the playfield, as they
// do on the full screen (gameBoardArea).
func (a *App) gameBar(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	barH := gtx.Dp(gameBarH)
	player := mode == engine.ModePlayer

	kids := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return a.barButton(gtx, &a.barHudBtn, glyphMenu, a.hudDrawer) }),
	}
	kids = append(kids, layout.Flexed(1, func(gtx C) D {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
			return a.barStats(gtx, view, mode, gmode)
		})
	}))
	if a.hasOpponents(eng, mode, gmode) {
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return a.barButton(gtx, &a.barOppBtn, glyphBoards, a.oppVisible())
			})
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
			d := a.barButton(gtx, &a.barChatBtn, glyphChat, a.chatVisible())
			// Unread mark: messages have arrived since the panel last showed
			// them (gameChatPanel records what it showed in chatSeen).
			if n := a.gameChatCount(eng.GameID()); n > a.chatSeen && !a.chatVisible() {
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
	sz := gtx.Dp(gameBarH - 14)
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

// clampH lays w out and reports its height capped at the room it was given,
// so an oversized child crowds its siblings rather than displacing them.
func clampH(gtx C, w layout.Widget) D {
	d := w(gtx)
	d.Size.Y = min(d.Size.Y, gtx.Constraints.Max.Y)
	return d
}

// hasOpponents reports whether this game has other playfields for the local
// player to watch: the competitive modes' own gate (a spectator already sees
// every board, and a co-op crew shares one). The bar's boards switch shows
// only when there is something behind it.
func (a *App) hasOpponents(eng *engine.Engine, mode engine.Mode, gmode config.GameMode) bool {
	if mode == engine.ModeSpectator || (gmode != config.ModeCompetitive && gmode != config.ModeTeams) {
		return false
	}
	return len(eng.OpponentSnapshots()) > 0
}

// opponentBoards is the opponents' playfields beside the board: thumbnails in
// a column down the side, the name over each. Every pixel of the column comes
// off the playfield — it is capped at oppColPct of the screen and its cells
// are clamped small — which is why it is a switch (oppVisible), on where
// there is width to spare and off where there is not.
//
// It is centred on the same axis the board area centres on, so the two read
// as one row rather than as a board with something bolted to its corner.
func (a *App) opponentBoards(gtx C, eng *engine.Engine) D {
	opps := eng.OpponentSnapshots()
	if len(opps) == 0 {
		return D{}
	}
	ids := make([]string, 0, len(opps))
	for id := range opps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	first := opps[ids[0]]
	vis := first.Height - first.VisibleStart
	cell := fitCellPx(gtx, first.Width, vis*len(ids), 1, gtx.Dp(8), len(ids)*gtx.Dp(16), 3, 14)
	teams := eng.GameMode() == config.ModeTeams
	return layout.Center.Layout(gtx, func(gtx C) D {
		var kids []layout.FlexChild
		for i, id := range ids {
			snap, label := opps[id], id
			if teams {
				label = "OPPONENTS"
			}
			if i > 0 {
				kids = append(kids, layout.Rigid(spacer(8)))
			}
			kids = append(kids,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = first.Width*cell + gtx.Dp(4)
					return a.pixelLabelFit(gtx, unit.Sp(7), label, colMuted)
				}),
				layout.Rigid(spacer(3)),
				layout.Rigid(a.boardWidget(snap, -1, cell, false, nil, gtx.Now)),
			)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// barStats is the bar's readout, in the room the previews and the buttons
// leave: the score (per team in teams mode), the level, and — while it is
// worth naming — the batch round trip, which is what this game is about. It
// elides rather than wraps, so the bar never grows a second line.
func (a *App) barStats(gtx C, view gameView, mode engine.Mode, gmode config.GameMode) D {
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

// readyBar is the pre-start row: the ready-up action, and how much of
// the table is waiting on the rest.
func (a *App) readyBar(gtx C, view gameView) D {
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
