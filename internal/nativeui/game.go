package nativeui

import (
	"fmt"
	"image"
	"sort"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	"jetris/internal/render"
)

// gameView is the per-frame snapshot of game scalars, taken under a.mu so the
// layout never reads fields the pump goroutine is writing.
type gameView struct {
	score, level         int
	teamScores           [config.TeamCount]int
	teamLevels           [config.TeamCount]int
	rtt                  time.Duration
	status               string
	countdown            int
	countdownAt          time.Time
	gameOver, won        bool
	myReady              bool
	players, readyPlayer []lobby.PlayerSummary
	flash                map[[2]int]time.Time
	specFlash            map[int]map[[2]int]time.Time // spectator: per-board (playerIdx or team) flashes
	flashActive          bool
	rowStrobes           map[int]rowStrobe         // own board: arcade row strobes (clears + landed garbage)
	specRowStrobes       map[int]map[int]rowStrobe // spectator: per-board row strobes (landed garbage)
	shakeStart           time.Time                 // own board: garbage impact-shake epoch (zero = idle)
	fireworks            *fireworksShow            // nil unless this player/team won (competitive/teams)
	outcome              liveOutcome               // the decided game's reveal — a spectator's, or a winning player's (zero while undecided, and on a beaten player's screen)
	// Keyboard owner while the keys drive the piece (handleGameFocus): the
	// white focus outline goes on whichever of the two holds them.
	boardFocused, chatFocused bool
}

func (a *App) snapshotGame(now time.Time) gameView {
	a.mu.Lock()
	defer a.mu.Unlock()
	fc := make(map[[2]int]time.Time)
	for k, v := range a.flash {
		if now.Sub(v) < flashDur {
			fc[k] = v
		} else {
			delete(a.flash, k)
		}
	}
	// Spectator per-board flashes: prune expired cells (and empty boards).
	sf := make(map[int]map[[2]int]time.Time)
	specActive := false
	for board, m := range a.specFlash {
		for k, v := range m {
			if now.Sub(v) < flashDur {
				if sf[board] == nil {
					sf[board] = make(map[[2]int]time.Time)
				}
				sf[board][k] = v
				specActive = true
			} else {
				delete(m, k)
			}
		}
		if len(m) == 0 {
			delete(a.specFlash, board)
		}
	}
	// Own-board row strobes (clears + landed garbage): prune expired rows.
	rs := make(map[int]rowStrobe)
	for r, s := range a.rowStrobes {
		if now.Sub(s.start) < rowStrobeDur {
			rs[r] = s
		} else {
			delete(a.rowStrobes, r)
		}
	}
	// Spectator per-board row strobes: prune expired rows (and empty boards).
	srs := make(map[int]map[int]rowStrobe)
	for board, m := range a.specRowStrobes {
		for r, s := range m {
			if now.Sub(s.start) < rowStrobeDur {
				if srs[board] == nil {
					srs[board] = make(map[int]rowStrobe)
				}
				srs[board][r] = s
			} else {
				delete(m, r)
			}
		}
		if len(m) == 0 {
			delete(a.specRowStrobes, board)
		}
	}
	return gameView{
		score:          a.score,
		level:          a.level,
		teamScores:     a.teamScores,
		teamLevels:     a.teamLevels,
		rtt:            a.rtt,
		status:         a.gameStatus,
		countdown:      a.countdown,
		countdownAt:    a.countdownAt,
		gameOver:       a.gameOver,
		won:            a.won,
		myReady:        a.myReady,
		players:        append([]lobby.PlayerSummary(nil), a.gamePlayers...),
		readyPlayer:    append([]lobby.PlayerSummary(nil), a.readyPlayers...),
		flash:          fc,
		specFlash:      sf,
		flashActive:    len(fc) > 0 || specActive,
		rowStrobes:     rs,
		specRowStrobes: srs,
		shakeStart:     a.shakeStart,
		fireworks:      a.fireworks,
	}
}

func (a *App) layoutGame(gtx C) D {
	eng := a.getEngine()
	if eng == nil {
		return D{}
	}
	mode := eng.Mode()
	gmode := eng.GameMode()

	view := a.snapshotGame(gtx.Now)
	// The decided game — a spectator's, or a winning player's own: the reveal
	// the boards, the legend and the spectator's result box all draw from
	// (spectator_reveal.go).
	view.outcome = a.resolveOutcome(eng, view, gmode, gtx.Now)
	started := view.status == string(config.GameStatusInProgress)
	// playing: the keyboard drives the piece (a seated player, game in
	// progress, not eliminated). Only then do the board and the chat compete
	// for the keys — the player chats by clicking into the chat panel and
	// plays again by clicking anywhere else (handleGameFocus); before the
	// start, and for spectators, the chat editor is the only key consumer.
	playing := mode == engine.ModePlayer && started && !view.gameOver
	a.handleGameFocus(gtx, eng, playing) // first thing in the frame — see its doc
	// Focus outline: the chat's while its editor (or its Send button, for the
	// one frame a press leaves it there) has the keys, the board's otherwise
	// — while playing they converge to one or the other within a frame.
	view.chatFocused = playing && (gtx.Source.Focused(&a.gameChatEd) || gtx.Source.Focused(&a.gameChatBtn))
	view.boardFocused = playing && !view.chatFocused
	// Dispatch moves (the board's key filters are registered here every
	// frame; its key-input target, event.Op, is the root pointerArea below).
	if mode == engine.ModePlayer && started {
		a.handleKeys(gtx, eng)
	}
	// The on-screen pad mirrors the keyboard scheme; its clicks are drained
	// every frame and only dispatched while the game is actually playable.
	a.handlePadClicks(gtx, eng, playing)

	if a.readyBtn.Clicked(gtx) {
		go a.toggleReady()
	}
	if a.backBtn.Clicked(gtx) {
		// Walking out of a running game deserves an "are you sure?" — the seat
		// is kept and the lobby offers Rejoin, but the board plays on without
		// you. Any other state (pre-start, game over, spectating) leaves
		// directly; leaving pre-start also clears the ready mark.
		if mode == engine.ModePlayer && started && !view.gameOver {
			a.confirmLeave = true
		} else {
			go a.leaveCurrentGame()
		}
	}
	if a.leaveYesBtn.Clicked(gtx) {
		a.confirmLeave = false
		go a.leaveCurrentGame()
	}
	if a.leaveNoBtn.Clicked(gtx) {
		a.confirmLeave = false
	}
	a.handleGameChatSubmit(gtx, eng)
	if view.flashActive {
		animate(gtx) // keep animating the flash until it expires
	}
	if len(view.rowStrobes) > 0 || len(view.specRowStrobes) > 0 || gtx.Now.Sub(view.shakeStart) < shakeDur {
		animate(gtx) // keep the row strobes / garbage impact shake animating
	}
	if countdownVisible(view, mode) && gtx.Now.Sub(view.countdownAt) < countdownAnimDur {
		animate(gtx) // keep animating the countdown pop until it settles
	}
	if view.fireworks != nil && view.fireworks.active(gtx.Now) {
		animate(gtx) // keep the victory fireworks animating until the show ends
	}
	if view.outcome.decided {
		animate(gtx) // keep the winner show animating while the screen is up
	}

	// Mirror the checkbox into the locked flag that gates the consumer-side
	// message tap (recordStreamMsg runs on the engine's consumer goroutines).
	showMsgs := a.showMsgs.Value
	a.mu.Lock()
	a.msgShow = showMsgs
	a.mu.Unlock()

	content := func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				// HUD column: width-reactive (~19% of the window) within sane
				// bounds, so a wide window doesn't waste it all on the board.
				hudW := min(max(gtx.Constraints.Max.X*19/100, gtx.Dp(200)), gtx.Dp(300))
				gtx.Constraints.Max.X = hudW
				gtx.Constraints.Min.X = hudW
				return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
					return a.gameHUD(gtx, eng, view, mode, gmode)
				})
			}),
			layout.Flexed(1, func(gtx C) D {
				return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
					return a.gameBoardArea(gtx, eng, view, mode, gmode)
				})
			}),
			layout.Rigid(func(gtx C) D {
				if mode == engine.ModeSpectator || (gmode != config.ModeCompetitive && gmode != config.ModeTeams) {
					return D{}
				}
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
					return a.opponentColumn(gtx, eng)
				})
			}),
		)
	}
	children := []layout.FlexChild{
		layout.Flexed(1, content),
		layout.Rigid(func(gtx C) D { return a.gameChatPanel(gtx, eng, view) }),
	}
	if showMsgs {
		children = append(children, layout.Rigid(a.natsMsgSection))
	}
	root := func(gtx C) D { return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...) }
	base := root
	if view.fireworks != nil && view.fireworks.active(gtx.Now) {
		// Victory fireworks paint over the whole game screen; pure paint ops,
		// so clicks and typing still reach the widgets underneath.
		base = func(gtx C) D {
			return layout.Stack{}.Layout(gtx,
				layout.Stacked(root),
				layout.Expanded(func(gtx C) D { return fireworksOverlay(gtx, view.fireworks) }),
			)
		}
	}
	screen := base
	if a.confirmLeave {
		// Leave confirmation modal: scrim the game and swallow clicks behind it.
		screen = func(gtx C) D {
			return layout.Stack{}.Layout(gtx,
				layout.Expanded(base),
				layout.Expanded(func(gtx C) D {
					fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), withAlpha(colBg, 0xc0))
					return D{Size: gtx.Constraints.Max}
				}),
				layout.Stacked(func(gtx C) D {
					gtx.Constraints.Min = gtx.Constraints.Max
					return a.confirmLeaveOverlay(gtx)
				}),
			)
		}
	}
	// The whole screen is the board's input area: its key-input target, and
	// the pointer area whose presses (any not claimed by the chat panel) hand
	// the keys back to the board.
	return pointerArea(gtx, &a.boardTag, screen)
}

// confirmLeaveOverlay is the modal asking whether to leave an in-progress
// game. Leaving keeps the seat: the lobby lists the game as "playing" with a
// Rejoin button.
func (a *App) confirmLeaveOverlay(gtx C) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(420)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colErr, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "LEAVE GAME?", colErr).Layout),
							layout.Rigid(spacer(10)),
							layout.Rigid(a.body("Are you sure you want to leave? The game keeps going —", colFg)),
							layout.Rigid(a.body("you can rejoin it from the lobby.", colFg)),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &a.leaveYesBtn, "Yes, leave") }),
									layout.Rigid(hSpacer(10)),
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.leaveNoBtn, "No, keep playing") }),
								)
							}),
						)
					})
				})
			})
		})
	})
}

// handleGameChatSubmit dispatches the game screen's chat input: the Send
// button or Enter in the editor (which, mid-game, only has the keys once the
// player clicked into the chat — see handleGameFocus).
func (a *App) handleGameChatSubmit(gtx C, eng *engine.Engine) {
	send := a.gameChatBtn.Clicked(gtx)
	for {
		ev, ok := a.gameChatEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			send = true
		}
	}
	if !send {
		return
	}
	text := strings.TrimSpace(a.gameChatEd.Text())
	if text == "" {
		return
	}
	a.gameChatEd.SetText("")
	go a.sendGameChat(eng, text)
}

// gameChatPanel renders the chat strip at the bottom of the game screen: this
// game's messages plus the lobby chat folded in — lobby lines are prefixed
// "@lobby" and colored colLobby so they're obviously not from the game. Game
// messages are seen only by this game's players and spectators (per-game chat
// subject); a message typed here goes to the game chat, or to the lobby chat
// when it starts with "@lobby".
//
// Everyone types, players mid-game included: while the keys drive the piece
// a click into the panel hands them to the chat (handleGameFocus — the panel
// is the chat's pointer area, a.chatTag) and the panel wears the white focus
// ring; a click anywhere else, or Escape, hands them back to the board, and
// Shift-Tab switches either way. The editor's hint says which way the keys
// currently go.
func (a *App) gameChatPanel(gtx C, eng *engine.Engine, view gameView) D {
	gameID := eng.GameID()
	a.mu.Lock()
	msgs := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if m.GameID == gameID || m.GameID == "" {
			msgs = append(msgs, m)
		}
	}
	a.mu.Unlock()

	hint := "Message… (start with @lobby to message the lobby)"
	ring := colorN{} // transparent: the ring shows only while the chat holds the keys
	switch {
	case view.chatFocused:
		hint = "Message… (Esc, Shift-Tab or click the board to play again; @lobby messages the lobby)"
		ring = colFocus
	case view.boardFocused:
		hint = "Click here to chat — the keys are driving your piece; Shift-Tab to switch focus"
	}

	return pointerArea(gtx, &a.chatTag, func(gtx C) D {
		return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(a.header("CHAT")),
				layout.Rigid(func(gtx C) D {
					return focusRing(gtx, ring, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								return bordered(gtx, func(gtx C) D {
									// Height-reactive: at least 96 dp of chat, growing with the
									// window (12% of the available height) so a taller window
									// shows more of the conversation.
									if maxH := max(gtx.Dp(96), gtx.Constraints.Max.Y*12/100); gtx.Constraints.Max.Y > maxH {
										gtx.Constraints.Max.Y = maxH
									}
									return material.List(a.th, &a.gameChatList).Layout(gtx, len(msgs), func(gtx C, i int) D {
										txt, col := chatLine(msgs[i])
										return layout.Inset{Top: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, a.body(txt, col))
									})
								})
							}),
							layout.Rigid(spacer(6)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, func(gtx C) D {
										return a.editorBox(gtx, &a.gameChatEd, hint)
									}),
									layout.Rigid(func(gtx C) D {
										return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx)
									}),
									layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.gameChatBtn, "Send") }),
								)
							}),
						)
					})
				}),
			)
		})
	})
}

// focusRing frames w with a chunky 3 dp ring in col, padded so the ring never
// touches the content. The padding is always there (a stable layout whichever
// way the keys go); a transparent col draws no ring at all.
func focusRing(gtx C, col colorN, w layout.Widget) D {
	return widget.Border{Color: col, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(unit.Dp(6)).Layout(gtx, w)
	})
}

// chatLine formats one message for the in-game chat list: lobby messages get
// an "@lobby" prefix and their own color; game messages from spectators are
// marked "(spec)".
func chatLine(m lobby.ChatMessage) (string, colorN) {
	name := m.Name
	if m.Spectator {
		name += " (spec)"
	}
	if m.GameID == "" {
		return fmt.Sprintf("@lobby %s: %s", name, m.Text), colLobby
	}
	return fmt.Sprintf("%s: %s", name, m.Text), colFg
}

func (a *App) gameHUD(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	started := view.status == string(config.GameStatusInProgress)
	modeLabel := "Cooperative"
	switch gmode {
	case config.ModeCompetitive:
		modeLabel = "Competitive"
	case config.ModeTeams:
		modeLabel = "Teams"
		if mode != engine.ModeSpectator {
			modeLabel += " · TEAM " + teamName(eng.TeamIdx())
		}
	}
	if mode == engine.ModeSpectator {
		modeLabel = "Spectating · " + modeLabel
	}

	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return a.pixel(unit.Sp(11), modeLabel, colAccent).Layout(gtx)
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D { return a.legend(gtx, eng, view, gmode) }),
		layout.Rigid(spacer(14)),
	}

	if gmode == config.ModeTeams {
		// Teams: live per-team scoreboard (folded from every team's line-clear
		// events on every engine), shown to players and spectators alike. The
		// player's own team is highlighted; spectators have no own team and see
		// each team's level inline instead of the single LEVEL stat.
		for t := 0; t < config.TeamCount; t++ {
			valCol := colFg
			if mode != engine.ModeSpectator && t == eng.TeamIdx() {
				valCol = colAccent
			}
			val := fmt.Sprintf("%d", view.teamScores[t])
			if mode == engine.ModeSpectator {
				val = fmt.Sprintf("%d · lvl %d", view.teamScores[t], view.teamLevels[t])
			}
			children = append(children,
				layout.Rigid(a.hudStatColored("TEAM "+teamName(t), val, valCol)))
		}
	} else {
		children = append(children, layout.Rigid(a.hudStat("SCORE", view.score)))
	}
	if !(gmode == config.ModeTeams && mode == engine.ModeSpectator) {
		children = append(children, layout.Rigid(a.hudStat("LEVEL", view.level)))
	}

	if mode == engine.ModePlayer {
		children = append(children, layout.Rigid(a.hudStatColored("Batch RTT", formatRTT(view.rtt), rttColor(view.rtt))))
	}

	if mode == engine.ModePlayer && !started && !view.gameOver {
		children = append(children,
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D { return a.readyArea(gtx, view) }),
		)
	}

	children = append(children,
		layout.Rigid(spacer(14)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.showMsgs, "Show NATS messages")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(18)),
		layout.Rigid(func(gtx C) D {
			return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby")
		}),
		layout.Rigid(spacer(20)),
		layout.Rigid(a.natsTag(22, 10)),
	)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (a *App) legend(gtx C, eng *engine.Engine, view gameView, gmode config.GameMode) D {
	var children []layout.FlexChild

	// Once the game is decided (view.outcome — on a spectator's screen, or a
	// winning player's) the legend reveals it: the winners' names gold in
	// bold italic with a trophy, the beaten in their board colors — no more
	// "(out)", every beaten player is out.
	oc := view.outcome
	playerRow := func(i int, p lobby.PlayerSummary) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			elim := (gmode == config.ModeCompetitive || gmode == config.ModeTeams) && eng.IsEliminated(p.PlayerID)
			name := agentName(p.Name, p.Agent)
			textCol, won := colFg, false
			switch {
			case oc.wins(p.PlayerID):
				name, textCol, won = "🏆 "+name, colGold, true
			case oc.decided:
				textCol = render.PlayerColorRGBA(i)
			case elim:
				name += " (out)"
				textCol = colMuted
			}
			return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return swatch(gtx, render.PlayerColorRGBA(i), 12) }),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(a.boardLabel(name, textCol, won)),
				)
			})
		})
	}

	if gmode == config.ModeTeams {
		// Group players under TEAM A / TEAM B headers. Swatch colors stay
		// keyed by the GLOBAL roster index, matching Cell.PlayerIdx on boards.
		for t := 0; t < config.TeamCount; t++ {
			hdr := a.header("TEAM " + teamName(t))
			if oc.decided && oc.winTeam == t {
				// The winning team's header: gold, in the synthesized bold
				// italic (the pixel face has no such variants).
				hdr = func(gtx C) D {
					return layout.Inset{Bottom: unit.Dp(5)}.Layout(gtx, a.pixelEmph(unit.Sp(10), "TEAM "+teamName(t), colGold))
				}
			}
			children = append(children, layout.Rigid(hdr))
			for i, p := range view.players {
				if p.Team == t {
					children = append(children, playerRow(i, p))
				}
			}
			if t < config.TeamCount-1 {
				children = append(children, layout.Rigid(spacer(6)))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}

	children = append(children, layout.Rigid(a.header("PLAYERS")))
	for i, p := range view.players {
		children = append(children, playerRow(i, p))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (a *App) readyArea(gtx C, view gameView) D {
	label := "CLICK WHEN READY TO PLAY"
	if view.myReady {
		label = "CLICK IF NOT READY ANYMORE"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if view.myReady {
				// Standing down is not the action we're fishing for — no
				// attract chrome once the player has readied up.
				return a.primaryButton(gtx, &a.readyBtn, label)
			}
			return a.attractButton(gtx, &a.readyBtn, label)
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			var rows []layout.FlexChild
			for _, p := range view.readyPlayer {
				p := p
				rows = append(rows, layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, a.body(agentName(p.Name, p.Agent), colFg)),
							layout.Rigid(hSpacer(8)),
							layout.Rigid(a.readyBadge(p.Ready)),
						)
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		}),
	)
}

// readyBadge renders a filled square-cornered tag reading READY (green) or NOT
// READY (red) — the per-player status shown while waiting for everyone to
// ready up.
func (a *App) readyBadge(ready bool) layout.Widget {
	return func(gtx C) D {
		txt, col := "NOT READY", colErr
		if ready {
			txt, col = "READY", colGo
		}
		l := a.pixel(unit.Sp(8), txt, colBg)
		inset := layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}
		macro := op.Record(gtx.Ops)
		dims := inset.Layout(gtx, l.Layout)
		call := macro.Stop()
		fillRect(gtx.Ops, image.Rect(0, 0, dims.Size.X, dims.Size.Y), col)
		call.Add(gtx.Ops)
		return dims
	}
}

func (a *App) gameBoardArea(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	// Spectator views are still "content" below the shared overlays: the
	// pre-game countdown (and, in coop, the game-over box) must reach the
	// spectator's screen too.
	if mode == engine.ModeSpectator && (gmode == config.ModeCompetitive || gmode == config.ModeTeams) {
		inner := func(gtx C) D { return a.spectatorBoards(gtx, eng, view) }
		if gmode == config.ModeTeams {
			inner = func(gtx C) D { return a.spectatorTeamBoards(gtx, eng, view) }
		}
		// The boards must stay centered with or without the countdown Stack:
		// the direct call receives tight constraints from the enclosing Flexed
		// slot, which would otherwise pin the boards' Flex to the top-left the
		// moment the countdown overlay goes away.
		content := func(gtx C) D { return layout.Center.Layout(gtx, inner) }
		if view.outcome.decided {
			// Announce the verdict to the spectator in a result box beside
			// the boards (never over them — the final playfields stay
			// fully visible).
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, content),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
						return a.spectatorResultBox(gtx, view, view.outcome, gmode)
					})
				}),
			)
		}
		if countdownVisible(view, mode) {
			return layout.Stack{Alignment: layout.Center}.Layout(gtx,
				layout.Expanded(content),
				layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, view) }),
			)
		}
		return content(gtx)
	}

	snap := eng.Snapshot()
	localIdx := eng.PlayerIdx()
	if mode == engine.ModeSpectator {
		localIdx = -1
	}
	started := view.status == string(config.GameStatusInProgress)
	if mode == engine.ModePlayer && (gmode == config.ModeCompetitive || gmode == config.ModeTeams) {
		// Garbage that landed since the last frame strobes in the attacker's
		// color and judders the board (competitive modes' arcade impact).
		a.detectGarbage(gtx, snap)
	}
	// Hard-drop ghost: where the falling piece would land if dropped right
	// now, derived from the very snapshot being drawn — never published.
	// Agents already plan with HardDropDestination, so the ghost only levels
	// the field for humans. Whether it shows is the game's own rule, chosen
	// at creation (GameMeta.NoGhost, on by default) — one setting for every
	// player, like the piece preview.
	var ghost map[[2]int]game.PieceType
	if eng.ShowGhost() && mode == engine.ModePlayer && started && !view.gameOver {
		ghost = ghostCells(snap, localIdx, gmode)
	}
	// Players with a piece preview get the NEXT well beside the playfield —
	// per-seat queue, so spectators (no seat) never have one. Read the live
	// queue (not just NextCount) so the space is only reserved once the
	// engine's sequence is up and the well will actually render.
	nextPieces := eng.NextPieces()
	showNext := mode == engine.ModePlayer && len(nextPieces) > 0
	board := func(gtx C) D {
		// Cell size tracks the window: as much board as fits after reserving
		// room below for the player's move-buffer strip and (while the game is
		// still playable) the mouse control pad.
		reserved := 0
		if mode == engine.ModePlayer {
			reserved = gtx.Dp(90)
			if !view.gameOver {
				reserved += gtx.Dp(80)
			}
		}
		reservedX, extraCols := gtx.Dp(24), 0
		if showNext {
			// The NEXT well's tiles use the board cell size, so the well is
			// previewCols board cells wide: count it as extra board columns
			// plus a fixed slice for its frame and the gap, so the pair
			// always fits the window.
			extraCols = previewCols
			reservedX += gtx.Dp(18)
		}
		cell := fitCellPx(gtx, snap.Width+extraCols, snap.Height-snap.VisibleStart, 1, reservedX, reserved, 14, 56)
		boardCol := func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					fx := &boardFX{flash: view.flash, rows: view.rowStrobes, ghost: ghost}
					if view.boardFocused {
						fx.frame = colFocus // the well's frame lights up: the keys drive the piece
					}
					bw := a.boardWidget(snap, localIdx, cell, true, fx, gtx.Now)
					if view.outcome.decided {
						// The finished board wears the winner show: the crew's
						// co-op board (a spectator's or a player's — the run's
						// rank is the prize), or a winning player's own. Over
						// the victory fireworks the show is painted last, so
						// the crown floats above the rockets and bursts — but
						// never above the leave-game modal.
						crown := a.crownBoard
						if view.fireworks != nil && view.fireworks.active(gtx.Now) && !a.confirmLeave {
							crown = a.crownBoardOnTop
						}
						bw = crown(view.outcome.fx(), gtx.Now)(bw, cell)
					}
					if dx := boardShakeOffset(cell, gtx.Now.Sub(view.shakeStart)); dx != 0 {
						// Garbage impact: judder the whole well sideways for a
						// few decaying wobbles — pure paint offset, no layout.
						inner := bw
						bw = func(gtx C) D {
							defer op.Offset(image.Pt(dx, 0)).Push(gtx.Ops).Pop()
							return inner(gtx)
						}
					}
					if !countdownVisible(view, mode) {
						return bw(gtx)
					}
					// The pre-game countdown centers on the playfield itself
					// (not the whole board area, which would drift it toward
					// the NEXT well / surrounding whitespace).
					return layout.Stack{Alignment: layout.Center}.Layout(gtx,
						layout.Stacked(bw),
						layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, view) }),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if mode != engine.ModePlayer {
						return D{}
					}
					// Inputs queued behind the in-flight batch publish (very
					// visible on a high-RTT server); the strip drains as each
					// buffered move's own publish starts.
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
						return a.bufferedMovesStrip(gtx, eng.BufferedMoves())
					})
				}),
				layout.Rigid(func(gtx C) D {
					if mode != engine.ModePlayer || view.gameOver {
						return D{}
					}
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
						return a.controlPad(gtx, view.status == string(config.GameStatusInProgress))
					})
				}),
			)
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			if !showNext {
				return boardCol(gtx)
			}
			// NEXT well in its own sub-division hugging the playfield's
			// top-left, classic arcade style.
			return layout.Flex{Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return a.nextWell(gtx, nextPieces, cell) }),
				layout.Rigid(hSpacer(12)),
				layout.Rigid(boardCol),
			)
		})
	}
	switch {
	case view.gameOver:
		// The game-over box sits BESIDE the board, not stacked over it: the
		// final playfield must stay fully visible.
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, board),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
					return a.gameOverBox(gtx, gmode, view, eng.TeamIdx())
				})
			}),
		)
	default:
		// The pre-game countdown is stacked over the playfield inside
		// boardCol, so it needs no case of its own here.
		return board(gtx)
	}
}

// countdownVisible reports whether the centered pre-game countdown number
// should be drawn over the board — for players AND spectators alike (only a
// finished local player is excluded). The check is "the game has not started
// yet", NOT "the game is not in progress": at game end the status moves PAST
// in_progress to finished/archived, and with the last countdown value still 0
// an is-in-progress check would resurrect a giant GO! over the final boards.
func countdownVisible(view gameView, mode engine.Mode) bool {
	preStart := view.status == "" ||
		view.status == string(config.GameStatusCreated) ||
		view.status == string(config.GameStatusStarting)
	return view.countdown >= 0 && preStart && !view.gameOver && mode != engine.ModeGameOver
}

// countdownOverlay draws the big centered countdown number (or "GO!") with a
// pop-in scale + fade so each new number animates in (gold numbers, green GO!).
func (a *App) countdownOverlay(gtx C, view gameView) D {
	txt := fmt.Sprintf("%d", view.countdown)
	col := colGold
	if view.countdown == 0 {
		txt = "GO!"
		col = colGo
	}
	t := clampF(float64(gtx.Now.Sub(view.countdownAt))/float64(countdownAnimDur), 0, 1)
	scale := 0.4 + 0.6*easeOutBack(t)
	alpha := clampF(t/0.3, 0, 1)

	// The settled size tracks the window (≈1/8 of its short side) so the
	// countdown stays huge on a big screen and fits a small one.
	base := countdownBaseSp
	if pps := gtx.Metric.PxPerSp; pps > 0 {
		if m := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y); m > 0 {
			base = clampF(float64(m)/8/float64(pps), 56, 180)
		}
	}
	l := a.pixel(unit.Sp(float32(base*scale)), txt, withAlpha(col, alpha))
	return l.Layout(gtx)
}

func (a *App) spectatorBoards(gtx C, eng *engine.Engine, view gameView) D {
	opps := eng.OpponentSnapshots()
	// Reactive cells: fit every player's board side by side (16 dp gaps, name
	// row above each); below the minimum the strip scrolls instead.
	dims := eng.Snapshot()
	n := max(len(view.players), 1)
	cell := fitCellPx(gtx, dims.Width, dims.Height-dims.VisibleStart, n, n*gtx.Dp(16), gtx.Dp(30), 8, 30)

	// Elimination states drive the per-board overlays: an eliminated player's
	// board reads OUT while the game goes on, and once it is decided — all
	// but one out, view.outcome — the survivor's board wears the winner show
	// and every other board the OUT wash. A simultaneous-top-out draw washes
	// every board OUT and crowns nobody.
	oc := view.outcome
	var items []layout.Widget
	for i, p := range view.players {
		i, p := i, p
		snap, ok := opps[p.PlayerID]
		out := eng.IsEliminated(p.PlayerID)
		items = append(items, func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.boardLabel(p.Name, render.PlayerColorRGBA(i), oc.wins(p.PlayerID))),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						a.detectGarbageOn(gtx, i, snap) // landed garbage strobes on this board
						board := a.boardWidget(snap, i, cell, true, &boardFX{flash: view.specFlash[i], rows: view.specRowStrobes[i]}, gtx.Now)
						switch {
						case oc.wins(p.PlayerID):
							return a.crownBoard(oc.fx(), gtx.Now)(board, cell)(gtx)
						case oc.decided:
							return a.knockoutBoard(board, cell)(gtx)
						case out:
							return a.boardOverlay(board, "OUT", colErr)(gtx)
						}
						return board(gtx)
					}),
				)
			})
		})
	}
	return a.scrollableBoards(gtx, &a.specBoardsList, items)
}

// boardOverlay centers a compact label chip over a board — the spectator's
// OUT / WINNER(S) markers. Only the chip itself has a background; the board
// stays fully visible around it (a full-board scrim over the already-dark
// playfield made the board unreadable).
func (a *App) boardOverlay(board layout.Widget, txt string, col colorN) layout.Widget {
	return func(gtx C) D {
		return layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Stacked(board),
			layout.Stacked(func(gtx C) D {
				l := a.pixel(unit.Sp(12), txt, col)
				inset := layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}
				macro := op.Record(gtx.Ops)
				dims := inset.Layout(gtx, l.Layout)
				call := macro.Stop()
				fillRect(gtx.Ops, image.Rect(0, 0, dims.Size.X, dims.Size.Y), withAlpha(colBg, 0xd8))
				call.Add(gtx.Ops)
				return dims
			}),
		)
	}
}

// scrollableBoards lays a horizontal strip of board widgets. While the strip
// fits the available width it stays centered (the common case); once the boards
// together are wider than the window it becomes a horizontally scrollable list
// with a scrollbar, so an overflowing board can be scrolled to instead of
// spilling past the edge or overlapping its neighbour. Each item carries its
// own trailing gap.
func (a *App) scrollableBoards(gtx C, list *widget.List, items []layout.Widget) D {
	kids := make([]layout.FlexChild, len(items))
	for i, it := range items {
		kids[i] = layout.Rigid(it)
	}
	// Board widths are fixed by their cell size (independent of the constraints),
	// so laying the strip out unbounded on the main axis tells us its natural
	// width — and thus whether it overflows the window.
	m := gtx
	m.Constraints.Min = image.Point{}
	m.Constraints.Max.X = 1 << 20
	rec := op.Record(gtx.Ops)
	strip := layout.Flex{}.Layout(m, kids...)
	rec.Stop() // measure only — discard the recorded ops

	if strip.Size.X <= gtx.Constraints.Max.X {
		return layout.Center.Layout(gtx, func(gtx C) D {
			return layout.Flex{}.Layout(gtx, kids...)
		})
	}
	return material.List(a.th, list).Layout(gtx, len(items), func(gtx C, i int) D {
		return items[i](gtx)
	})
}

func (a *App) opponentColumn(gtx C, eng *engine.Engine) D {
	opps := eng.OpponentSnapshots()
	if len(opps) == 0 {
		return D{}
	}
	ids := make([]string, 0, len(opps))
	for id := range opps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Thumbnail cells scale with the window height: the whole stack of
	// opponent boards (plus ~34 dp of label/spacing each) should fit.
	first := opps[ids[0]]
	vis := first.Height - first.VisibleStart
	cell := fitCellPx(gtx, first.Width, vis*len(ids), 1, 0, len(ids)*gtx.Dp(34), 6, 13)
	var children []layout.FlexChild
	for _, id := range ids {
		snap := opps[id]
		label := id
		if eng.GameMode() == config.ModeTeams {
			label = "OPPOSING TEAM"
		}
		children = append(children,
			layout.Rigid(a.body(label, colMuted)),
			layout.Rigid(spacer(2)),
			layout.Rigid(a.boardWidget(snap, -1, cell, false, nil, gtx.Now)),
			layout.Rigid(spacer(12)),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// spectatorTeamBoards renders both teams' shared boards side by side for a
// teams-mode spectator. The spectator engine consumes team 0 as its "own"
// board and team 1 via the opponent consumer (see Engine.Start). Each board
// wears its team's color: the label, and a light tint on the empty squares
// and grid lines (boardFX.tint), so the two wells tell apart at a glance. A
// fully eliminated team is the decision itself (view.outcome): the other
// team's board wears the winner show and the beaten one the OUT wash; both
// out at once is a draw — both washed, nobody crowned.
func (a *App) spectatorTeamBoards(gtx C, eng *engine.Engine, view gameView) D {
	// Reactive cells: both team boards side by side, scrolling below the minimum.
	dims := eng.Snapshot()
	cell := fitCellPx(gtx, dims.Width, dims.Height-dims.VisibleStart, 2, 2*gtx.Dp(16), gtx.Dp(26), 10, 40)
	teamB, okB := eng.OpponentSnapshots()[engine.TeamBoardKey(1)]
	oc := view.outcome

	boards := []struct {
		label string
		snap  engine.BoardSnapshot
		ok    bool
		team  int
	}{
		{"TEAM A", eng.Snapshot(), true, 0},
		{"TEAM B", teamB, okB, 1},
	}
	var items []layout.Widget
	for _, b := range boards {
		b := b
		items = append(items, func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				teamCol := render.PlayerColorRGBA(b.team)
				won := oc.decided && oc.winTeam == b.team
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.boardLabel(b.label, teamCol, won)),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !b.ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						a.detectGarbageOn(gtx, b.team, b.snap) // landed garbage strobes on this board
						board := a.boardWidget(b.snap, -1, cell, true, &boardFX{flash: view.specFlash[b.team], rows: view.specRowStrobes[b.team], tint: teamCol}, gtx.Now)
						switch {
						case won:
							return a.crownBoard(oc.fx(), gtx.Now)(board, cell)(gtx)
						case oc.decided:
							return a.knockoutBoard(board, cell)(gtx)
						}
						return board(gtx)
					}),
				)
			})
		})
	}
	return a.scrollableBoards(gtx, &a.specTeamBoardsList, items)
}

// gameOverBox is the panel shown beside the board once the local player is out
// (or the game is over): title, win/loss message, the final score, and the
// Back to Lobby button. It is laid out next to the playfield — never over it,
// so the final board stays fully visible. myTeam is the local player's team
// index (teams mode only).
func (a *App) gameOverBox(gtx C, gmode config.GameMode, view gameView, myTeam int) D {
	won := view.won
	// Teams: a player can be out while their team plays on — show an interim
	// message (and no Back button pressure) until the game actually finishes.
	teamPlaysOn := gmode == config.ModeTeams && view.status == string(config.GameStatusInProgress) && !won
	title := "GAME OVER"
	if teamPlaysOn {
		title = "YOU'RE OUT"
	}
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, colBg, func(gtx C) D {
				return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx C) D {
					children := []layout.FlexChild{
						layout.Rigid(a.pixel(unit.Sp(18), title, colFg).Layout),
					}
					var msg string
					var c colorN
					switch {
					case teamPlaysOn:
						msg, c = "Your team plays on", colMuted
					case gmode == config.ModeTeams:
						msg, c = "YOUR TEAM LOST", colErr
						if won {
							msg, c = "YOUR TEAM WON!", colAccent
						}
					case gmode == config.ModeCompetitive:
						msg, c = "YOU LOST", colErr
						if won {
							msg, c = "YOU WON!", colAccent
						}
					}
					if msg != "" {
						children = append(children, layout.Rigid(spacer(10)), layout.Rigid(a.pixel(unit.Sp(12), msg, c).Layout))
					}
					// Final score: the shared total for cooperative, the player's own
					// score for competitive, both team totals (own team first) for
					// teams — while the team plays on these are the live totals.
					var scoreLine string
					switch gmode {
					case config.ModeCooperative:
						scoreLine = fmt.Sprintf("Score: %d (level %d)", view.score, view.level)
					case config.ModeCompetitive:
						scoreLine = fmt.Sprintf("Your score: %d (level %d)", view.score, view.level)
					case config.ModeTeams:
						if myTeam >= 0 && myTeam < config.TeamCount {
							other := 1 - myTeam
							scoreLine = fmt.Sprintf("TEAM %s %d (lvl %d) · TEAM %s %d (lvl %d)",
								teamName(myTeam), view.teamScores[myTeam], view.teamLevels[myTeam],
								teamName(other), view.teamScores[other], view.teamLevels[other])
						}
					}
					if scoreLine != "" {
						children = append(children, layout.Rigid(spacer(8)), layout.Rigid(func(gtx C) D {
							l := material.Body1(a.th, scoreLine)
							l.Color = colGold
							return l.Layout(gtx)
						}))
					}
					children = append(children, layout.Rigid(spacer(14)), layout.Rigid(func(gtx C) D {
						return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby")
					}))
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, children...)
				})
			})
		})
	})
}

func (a *App) hudStat(label string, val int) layout.Widget {
	return a.hudStatText(label, fmt.Sprintf("%d", val))
}

func (a *App) hudStatText(label, val string) layout.Widget {
	return a.hudStatColored(label, val, colFg)
}

func (a *App) hudStatColored(label, val string, valCol colorN) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(a.pixel(unit.Sp(9), label+"  ", colMuted).Layout),
				layout.Rigid(a.pixel(unit.Sp(13), val, valCol).Layout),
			)
		})
	}
}

// nextWell is the player's upcoming-piece preview in its own sub-division
// beside the playfield: a miniature arcade well (same colBorder frame idiom
// as the board, over the panel background so it reads as its own division)
// holding the NEXT label and one tile per revealed piece, stacked top-down in
// play order. Tiles use the SAME cell size as the playfield, so the preview
// reads exactly like the pieces on the board and tracks the window size along
// with it. pieces comes straight off the seekable sequence
// (Engine.NextPieces) every frame, so the well advances the moment a lock-in
// bumps pieceIdx — no queue state of its own.
func (a *App) nextWell(gtx C, pieces []game.PieceType, boardCellPx int) D {
	if len(pieces) == 0 {
		return D{}
	}
	cell := boardCellPx
	fw := max(cell/8, 2) // same frame proportion as the board's arcade well
	gap := max(cell/3, 6)

	inner := func(gtx C) D {
		kids := []layout.FlexChild{
			// The label scales with the tiles (≈0.55 cells tall).
			layout.Rigid(a.pixel(gtx.Metric.PxToSp(cell*11/20), "NEXT", colMuted).Layout),
		}
		for i, pt := range pieces {
			pt := pt
			top := gap
			if i == 0 {
				top = gap * 3 / 4
			}
			kids = append(kids, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: gtx.Metric.PxToDp(top)}.Layout(gtx, func(gtx C) D {
					h := drawMiniPiece(gtx.Ops, 0, 0, cell, pt)
					return D{Size: image.Pt(previewCols*cell, h)}
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, kids...)
	}

	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(gtx.Metric.PxToDp(fw+gap)).Layout(gtx, inner)
	call := macro.Stop()
	w, h := dims.Size.X, dims.Size.Y
	fillRect(gtx.Ops, image.Rect(0, 0, w, h), colBorder)
	fillRect(gtx.Ops, image.Rect(fw, fw, w-fw, h-fw), colPanel)
	call.Add(gtx.Ops)
	return dims
}

// Every piece's spawn orientation fits a 4-wide bounding box (the I is 4x1,
// the O 2x2, the rest 3x2), so each preview tile is previewCols wide; tile
// height follows the piece's own rows.
const previewCols = 4

// drawMiniPiece draws one preview tile at (x0,y0): the piece in its spawn
// orientation, centered in the fixed previewCols-wide box, styled like a
// locked cell of its type (render.CellStyle). Empty box cells stay unpainted
// so the tile sits directly on the well background. Returns the drawn height
// in px (the piece's own row count), so callers can pack tiles without the
// dead row a fixed-height box would leave under the 1-row I.
func drawMiniPiece(ops *op.Ops, x0, y0, cellPx int, pt game.PieceType) int {
	cells := game.Piece{Type: pt}.Cells() // orientation 0 offsets from a (0,0) anchor
	minR, maxR, minC, maxC := cells[0][0], cells[0][0], cells[0][1], cells[0][1]
	for _, rc := range cells[1:] {
		minR = min(minR, rc[0])
		maxR = max(maxR, rc[0])
		minC = min(minC, rc[1])
		maxC = max(maxC, rc[1])
	}
	shift := (previewCols-(maxC-minC+1))/2 - minC // whole-cell horizontal centering
	ap := render.CellStyle(game.Cell{Occupied: true, PieceType: pt}, -1, false)
	for _, rc := range cells {
		x := x0 + (rc[1]+shift)*cellPx
		y := y0 + (rc[0]-minR)*cellPx
		drawCell(ops, x, y, cellPx, ap.Fill, ap.Outline, ap.OutlineW, ap.Bevel)
	}
	return (maxR - minR + 1) * cellPx
}

// formatRTT renders the publish→echo round trip for the HUD: sub-10ms with a
// decimal, whole milliseconds above, an em dash before the first measurement.
func formatRTT(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	ms := float64(d) / float64(time.Millisecond)
	if ms < 10 {
		return fmt.Sprintf("%.1f ms", ms)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// rttColor maps the round trip to the HUD readout color: the normal text
// color up to 75 ms, then a blend that starts yellow and reaches orange at
// 150 ms, and red beyond that.
func rttColor(d time.Duration) colorN {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case d <= 0 || ms <= 75:
		return colFg
	case ms >= 150:
		return colErr
	default:
		return lerpColor(colWarn, colOrange, (ms-75)/75)
	}
}

// lerpColor blends linearly from a (t=0) to b (t=1).
func lerpColor(a, b colorN, t float64) colorN {
	lerp := func(x, y uint8) uint8 {
		return uint8(float64(x) + (float64(y)-float64(x))*t)
	}
	return colorN{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: lerp(a.A, b.A)}
}

// swatch draws a size×size dp filled square in c.
func swatch(gtx C, c colorN, size int) D {
	sz := gtx.Dp(unit.Dp(size))
	fillRect(gtx.Ops, image.Rect(0, 0, sz, sz), c)
	return D{Size: image.Pt(sz, sz)}
}

// background paints bg behind w, sized to w.
func background(gtx C, bg colorN, w layout.Widget) D {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	fillRect(gtx.Ops, image.Rect(0, 0, dims.Size.X, dims.Size.Y), bg)
	call.Add(gtx.Ops)
	return dims
}
