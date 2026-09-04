package nativeui

// The game screen — the same one on a phone, a tablet and a desktop.
//
// The rule is that nothing permanent stands between the player and the
// playfield, and that nothing on this screen ever takes the game away from
// the player. One slim bar runs across the top: the score, and a switch for
// each thing that costs the board room. Every one of them is a switch and
// not a window — the menu column (players, stats, the controls legend, the
// lab switches, Back to Lobby), the opponents' boards, the chat strip, the
// on-screen pad. They show or they do not; there is no scrim over the board,
// nothing to dismiss, and no panel that swallows the keys, so the piece keeps
// falling and keeps taking moves while the menu is open — which is the point
// of having the lab switches in there at all. The playfield gets all the rest,
// with the HOLD box and the NEXT well flanking it as they always have, and the
// touch gestures (gesture.go) get its whole width — the surface a swipe lands
// on runs edge to edge of the board area, well past the board's own columns.
//
// The bar's switches are the menu, the opponents' boards, the on-screen pad
// and the chat. Every one of them starts ON — the screen arrives whole rather
// than folded away behind buttons nobody has been told about — and stays
// wherever the player last put it, past this session as well as through it
// (panels.go). What a small screen changes is where a panel goes and not
// whether it is there: on a phone held portrait the pad stacks UNDER the
// playfield and the menu column is drawn OVER the board, both being room the
// screen has not got beside it.

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"sort"

	"jetris/internal/config"
	"jetris/internal/engine"
)

const (
	// gameBarH is the top bar's height in dp — a comfortable touch target
	// for the buttons in it, and room for a two-cell preview tile.
	gameBarH = 54
	// hudColPct/hudColMaxW/hudColMinW bound the menu column where it stands
	// beside the board: a slice of the screen, never wider than a column of
	// text and switches wants, never narrower than one can be read at, and
	// never more than half of what there is. Every dp of it comes off the
	// playfield, as the opponents' column does. hudOverPct is its share where
	// the screen is too narrow for that (hudBeside) and it is drawn over the
	// board instead, costing the playfield nothing.
	hudColPct  = 40
	hudColMaxW = 340
	hudColMinW = 240
	hudOverPct = 78
	// oppColPct/oppColMaxW bound the opponents' column when it is shown: a
	// slice of the screen, never more than a thumbnail's worth. Every dp of
	// it comes off the playfield.
	oppColPct  = 24
	oppColMaxW = 150
)

// gameScreen is THE game screen, on every display: the bar, whichever columns
// and strips are switched on beside and under the board, and the board with
// everything they leave.
func (a *App) gameScreen(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode, showMsgs bool) D {
	started := view.status == string(config.GameStatusInProgress)
	var children []layout.FlexChild
	if mode == engine.ModePlayer && !started && !view.gameOver {
		// Pre-start there is nothing to play and everything to decide: the
		// ready-up action belongs on the screen, not behind the menu.
		children = append(children, layout.Rigid(a.tutMarked(tutGameReady, func(gtx C) D { return a.readyBar(gtx, view) })))
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
				opps := a.oppVisible() && a.hasOpponents(eng, mode, gmode)
				// The board, with the opponents' column beside it when it is
				// switched on. The two are centred TOGETHER: a playfield is
				// portrait and a screen is not, so on a wide one the board
				// cannot use all the width whatever it does (its cell is bound
				// by the height) — and a board centred in what is left over,
				// with the opponents pinned out at the edge, reads as two
				// things that missed each other rather than as one row. The
				// column's width is taken off the board BEFORE it fits itself,
				// so nothing is squeezed after the fact.
				board := func(gtx C) D {
					if !opps {
						return a.gameBoardArea(gtx, eng, view, mode, gmode)
					}
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
								return a.opponentBoards(gtx, eng, view)
							}),
						)
					})
				}
				hudW := a.hudColW(gtx)
				menu := func(gtx C) D {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = hudW, hudW
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
					return a.hudColumn(gtx, eng, view, mode, gmode)
				}
				switch {
				case !a.hudVisible():
					// The menu away, whoever is heard is named in a strip
					// over the board area's bottom-left corner (voiceStrip):
					// recorded after the board and painted over it, so the
					// board's own layout is exactly what it was.
					d := board(gtx)
					macro := op.Record(gtx.Ops)
					sgtx := gtx
					sgtx.Constraints = layout.Constraints{Max: d.Size}
					sd := a.voiceStrip(sgtx, view.voice, gameSpeakerName(view))
					call := macro.Stop()
					if sd.Size != (image.Point{}) {
						defer op.Offset(image.Pt(0, d.Size.Y-sd.Size.Y)).Push(gtx.Ops).Pop()
						call.Add(gtx.Ops)
					}
					return d
				case a.hudBeside(gtx):
					// Room for both: the menu stands against the screen's edge
					// and the board takes what is left, the way the opponents'
					// column has always worked. The board is capped at the row's
					// height the way the whole area is (clampH above): a short
					// window's board overflows its slot, and a row that took the
					// board's word for its height would centre the menu on it —
					// half the overflow down, its foot under the chat's header
					// and its head off the bar.
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(menu),
						layout.Flexed(1, func(gtx C) D { return clampH(gtx, board) }),
					)
				default:
					// No room to stand beside it (a phone held portrait): the
					// menu is drawn OVER the board rather than squeezing it off
					// the screen — the playfield keeps its size and its cell,
					// the part of it the menu does not cover still takes a
					// swipe, and the keys still play. Its own pointer area
					// keeps its presses off the gesture surface underneath;
					// there is no scrim, and nothing here closes it.
					return layout.Stack{}.Layout(gtx,
						layout.Expanded(func(gtx C) D {
							// The slot the board area has when no menu is up:
							// a Stack hands its expanded children a zero
							// minimum, and the board laid out to its own size
							// instead of the screen's would jump the moment
							// the menu came over it.
							gtx.Constraints.Min = gtx.Constraints.Max
							return board(gtx)
						}),
						layout.Expanded(func(gtx C) D {
							return layout.W.Layout(gtx, func(gtx C) D {
								return pointerArea(gtx, &a.hudTag, menu)
							})
						}),
					)
				}
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
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.gameBar(gtx, eng, view, mode, gmode) }),
		layout.Flexed(1, body),
	)
}

// handleFormClicks drains the bar's switches. Called at the top of the game
// frame, before the pad and the gestures, so a column shown or hidden this
// frame is already in the layout they measure.
func (a *App) handleFormClicks(gtx C, eng *engine.Engine) {
	if a.screenEng != eng {
		// A fresh game screen (join, rejoin, spectate): its chat is a
		// different conversation, so the unread mark starts from nothing
		// rather than from what was read there. The switches are the
		// player's own and do carry over.
		a.screenEng, a.chatSeen = eng, 0
		// The voice switch too (voice.go): a teams game starts in the team's
		// room, whatever the last game's switch was left on.
		a.voiceChanEnum.Value = voiceChanTeam
	}
	// Each switch is just that: the button that shows a panel is the button
	// that hides it, and nothing else does — no scrim to tap through, no ✕ in
	// a corner, and no press on the board that puts the menu away while the
	// player meant to play. Every flip is the player's standing answer, so it
	// is written out (panels.go) and comes back at the next launch.
	flipped := false
	flip := func(btn *widget.Clickable, shown *bool) {
		for btn.Clicked(gtx) {
			*shown, flipped = !*shown, true
		}
	}
	flip(&a.barHudBtn, &a.hudShown)
	// Showing the chat and typing into it are separate: this only puts the
	// strip on screen. The keys move on a click or Tab (input.go).
	flip(&a.barChatBtn, &a.chatShown)
	flip(&a.barOppBtn, &a.oppShown)
	flip(&a.barPadBtn, &a.padShown)
	if flipped {
		a.persistPanels()
	}
	// The mic button is a switch of another kind (voice.go): the session's,
	// not a panel's, and never written out — every game starts muted. The
	// unmute runs here, in the click's own frame, because the browser opens
	// the microphone only on the player's gesture.
	for a.barMicBtn.Clicked(gtx) {
		if v := a.getVoice(); v != nil {
			v.SetMuted(!v.Muted())
		}
	}
}

// hudColBesideW is the menu column's width where it stands beside the board:
// a slice of the board area, capped at the width the menu wants and floored at
// a readable column, but never past half of what there is.
func (a *App) hudColBesideW(gtx C) int {
	w := min(gtx.Constraints.Max.X*hudColPct/100, gtx.Dp(hudColMaxW))
	return min(max(w, gtx.Dp(hudColMinW)), gtx.Constraints.Max.X/2)
}

// hudBeside reports whether the menu column can stand beside the board in the
// board area gtx measures: only where what is left of it still holds the
// playfield's own floor, which is the move-buffer strip the board is centred
// on (stripWidth) — squeeze the board under that and it hangs off the side of
// the screen. Where it cannot, the menu is drawn over the board instead, which
// costs the playfield nothing.
func (a *App) hudBeside(gtx C) bool {
	return gtx.Constraints.Max.X-a.hudColBesideW(gtx) >= a.stripWidth(gtx)
}

// hudColW is the width the menu column is actually laid out at: its share of
// the board area beside the board, and a wider one over it — over the board
// the width costs the playfield nothing, so the menu takes what it reads best
// at, which is the width it had as a panel.
func (a *App) hudColW(gtx C) int {
	if a.hudBeside(gtx) {
		return a.hudColBesideW(gtx)
	}
	return min(gtx.Constraints.Max.X*hudOverPct/100, gtx.Dp(hudColMaxW))
}

// hudColumn is the menu (gameHUD) as a column beside the board: its own panel
// ground, a hairline down the edge it meets the board on, and nothing else —
// no scrim behind it, no close button in it. It is laid out in the flow, so
// its presses are its own and the board's area (the whole screen, game.go)
// still hands the keys back to the piece the frame after one: a lab switch is
// flipped and the game plays on, which is the reason the menu is a switch.
//
// The column is the slot's exact height and its content scrolls in it: the
// menu is taller than a short window — a tablet's, a browser's with its
// toolbars, a phone's — and a Flex handed less room than its rows want gives
// the last of them none, drawing the knobs over one another and the button
// under them nowhere. A list gives every row its own height and a scrollbar
// down the edge for the rest.
func (a *App) hudColumn(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	return background(gtx, colPanel, func(gtx C) D {
		const pad = 10
		slotY := gtx.Constraints.Max.Y - 2*gtx.Dp(pad)
		// The list is the viewport the tour scrolls the column's parts into
		// (tutHUDColumn).
		d := a.tutMark(gtx, tutHUDColumn, func(gtx C) D {
			return material.List(a.th, &a.hudList).Layout(gtx, 1, func(gtx C, _ int) D {
				return layout.UniformInset(unit.Dp(pad)).Layout(gtx, func(gtx C) D {
					return a.gameHUD(gtx, eng, view, mode, gmode, slotY)
				})
			})
		})
		fillRect(gtx.Ops, image.Rect(d.Size.X-gtx.Dp(2), 0, d.Size.X, d.Size.Y), colBorder)
		return d
	})
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
		layout.Rigid(a.tutMarked(tutGameBarMenu, func(gtx C) D { return a.barButton(gtx, &a.barHudBtn, glyphMenu, a.hudVisible()) })),
		// The voice switch, beside the menu button (voice.go).
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutGameBarMic, func(gtx C) D {
				return a.micButton(gtx, view.voice)
			}))
		}),
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
			return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutGameBarPad, func(gtx C) D {
				return a.barButton(gtx, &a.barPadBtn, glyphPad, a.padVisible())
			}))
		}))
	}
	kids = append(kids, layout.Rigid(func(gtx C) D {
		return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, a.tutMarked(tutGameBarChat, func(gtx C) D {
			d := a.barButton(gtx, &a.barChatBtn, glyphChat, a.chatVisible())
			// Unread mark: messages have arrived since the panel last showed
			// them (gameChatPanel records what it showed in chatSeen).
			if n := a.gameChatCount(eng.GameID()); n > a.chatSeen && !a.chatVisible() {
				dot := gtx.Dp(7)
				fillRect(gtx.Ops, image.Rect(d.Size.X-dot, 0, d.Size.X, dot), colGold)
			}
			return d
		}))
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
	bg, fg := colPanel, colAccent
	if on {
		bg, fg = colAccent, colBg
	}
	return a.barButtonColors(gtx, btn, bm, bg, fg)
}

// barButtonColors is barButton in any colours: the mic button's third look
// (voice.go) is neither off nor on.
func (a *App) barButtonColors(gtx C, btn *widget.Clickable, bm []string, bg, fg colorN) D {
	sz := gtx.Dp(gameBarH - 14)
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
func (a *App) opponentBoards(gtx C, eng *engine.Engine, view gameView) D {
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
			// The speaker mark beside the label while that opponent is
			// heard (voice.go) — in a teams game, while any of them is: the
			// column is theirs together.
			speaking, mark := view.voice.Speaking(id)
			if teams {
				label = "OPPONENTS"
				for _, sp := range view.voice.Speakers {
					for _, p := range view.players {
						if p.PlayerID == sp.ID && p.Team != eng.TeamIdx() {
							speaking, mark = sp, true
						}
					}
				}
			}
			if i > 0 {
				kids = append(kids, layout.Rigid(spacer(8)))
			}
			kids = append(kids,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = first.Width*cell + gtx.Dp(4)
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return a.pixelLabelFit(gtx, unit.Sp(7), label, colMuted)
						}),
						layout.Rigid(func(gtx C) D {
							if !mark {
								return D{}
							}
							return layout.Inset{Left: unit.Dp(4)}.Layout(gtx, glyphWidget(glyphSpeaker, unit.Dp(8), speakerColor(speaking)))
						}),
					)
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
		// "A 1200 · B 940 · C 310  LV3" — every team, in index order, in the
		// room the bar has.
		parts := make([]string, 0, len(view.teamScores))
		for t := range view.teamScores {
			parts = append(parts, fmt.Sprintf("%s %d", teamName(t), view.teamScore(t)))
		}
		line = fmt.Sprintf("%s  LV%d", strings.Join(parts, " · "), view.level)
	}
	col := colFg
	if view.linkDown > 0 {
		line, col = "LINK LOST "+formatLinkDown(view.linkDown), colOrange
	} else if mode == engine.ModePlayer && view.rtt > 0 {
		line += "  " + formatRTT(view.rtt)
		col = rttColor(view.rtt)
	}
	// And, last so it is the first thing a narrow bar gives up, who we are
	// and which server this session is on — "tester @ Jetris EU central", the
	// lobby bar's own line (lobbyBarLine) and the HUD column's (sessionLine)
	// carried onto the board, because every move in this game is a round trip
	// to that server and a player reading an RTT off this very bar should not
	// have to remember which one it is. The HUD panel, one tap away, always
	// has it.
	a.mu.Lock()
	server := a.connName
	a.mu.Unlock()
	who := ""
	if lb := a.getLobby(); lb != nil {
		who = lb.PlayerName()
	}
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
				// Whole or not at all: a name cut to "..." is noise where a
				// phone's bar has no room, and the HUD panel one tap away
				// carries it in full either way. The player's name is the
				// half this line can spare — the server is what it exists to
				// say — so a bar too narrow for both drops it and keeps the
				// "@ server" alone.
				fits := func(s string) bool {
					return a.pixelWidth(gtx, unit.Sp(8), s) <= gtx.Constraints.Max.X
				}
				txt := "  @ " + server
				switch {
				case who != "" && fits("  "+who+" @ "+server):
					txt = "  " + who + " @ " + server
				case !fits(txt):
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
