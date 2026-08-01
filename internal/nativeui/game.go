package nativeui

import (
	"fmt"
	"image"
	"sort"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/engine"
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
	fireworks            *fireworksShow // nil unless this player/team won (competitive/teams)
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
	return gameView{
		score:       a.score,
		level:       a.level,
		teamScores:  a.teamScores,
		teamLevels:  a.teamLevels,
		rtt:         a.rtt,
		status:      a.gameStatus,
		countdown:   a.countdown,
		countdownAt: a.countdownAt,
		gameOver:    a.gameOver,
		won:         a.won,
		myReady:     a.myReady,
		players:     append([]lobby.PlayerSummary(nil), a.gamePlayers...),
		readyPlayer: append([]lobby.PlayerSummary(nil), a.readyPlayers...),
		flash:       fc,
		specFlash:   sf,
		flashActive: len(fc) > 0 || specActive,
		fireworks:   a.fireworks,
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
	started := view.status == string(config.GameStatusInProgress)
	// Chat typing rule: players may chat until the game starts; once it is in
	// progress their keyboard drives the piece, so only spectators (and
	// eliminated players, whose keys no longer play) can type.
	canType := !started || mode != engine.ModePlayer

	// Register the whole window as a key-input target, then dispatch moves.
	// Focus is grabbed for the board only while actually playing — before the
	// game starts the chat editor must be able to hold keyboard focus.
	st := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.boardTag)
	st.Pop()
	if mode == engine.ModePlayer && started {
		a.handleKeys(gtx, eng)
	}
	// The on-screen pad mirrors the keyboard scheme; its clicks are drained
	// every frame and only dispatched while the game is actually playable.
	a.handlePadClicks(gtx, eng, mode == engine.ModePlayer && started && !view.gameOver)

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
	a.handleGameChatSubmit(gtx, eng, canType)
	if view.flashActive {
		a.invalidate() // keep animating the flash until it expires
	}
	if countdownVisible(view, mode) && gtx.Now.Sub(view.countdownAt) < countdownAnimDur {
		a.invalidate() // keep animating the countdown pop until it settles
	}
	if view.fireworks != nil && view.fireworks.active(gtx.Now) {
		a.invalidate() // keep the victory fireworks animating until the show ends
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
		layout.Rigid(func(gtx C) D { return a.gameChatPanel(gtx, eng, canType) }),
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
	if !a.confirmLeave {
		return base(gtx)
	}
	// Leave confirmation modal: scrim the game and swallow clicks behind it.
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

// handleGameChatSubmit dispatches the game screen's chat input. canType gates
// sending (see layoutGame); the editor is hidden when typing is disabled, so a
// stale click can't send either.
func (a *App) handleGameChatSubmit(gtx C, eng *engine.Engine, canType bool) {
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
	if !send || !canType {
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
func (a *App) gameChatPanel(gtx C, eng *engine.Engine, canType bool) D {
	gameID := eng.GameID()
	a.mu.Lock()
	msgs := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if m.GameID == gameID || m.GameID == "" {
			msgs = append(msgs, m)
		}
	}
	a.mu.Unlock()

	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.header("CHAT")),
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
				if !canType {
					return a.body("Chat is read-only while playing — spectators can still type.", colMuted)(gtx)
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return a.editorBox(gtx, &a.gameChatEd, "Message… (start with @lobby to message the lobby)")
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.gameChatBtn, "Send") }),
				)
			}),
		)
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

	playerRow := func(i int, p lobby.PlayerSummary) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			elim := (gmode == config.ModeCompetitive || gmode == config.ModeTeams) && eng.IsEliminated(p.PlayerID)
			name := agentName(p.Name, p.Agent)
			textCol := colFg
			if elim {
				name += " (out)"
				textCol = colMuted
			}
			return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return swatch(gtx, render.PlayerColorRGBA(i), 12) }),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(a.body(name, textCol)),
				)
			})
		})
	}

	if gmode == config.ModeTeams {
		// Group players under TEAM A / TEAM B headers. Swatch colors stay
		// keyed by the GLOBAL roster index, matching Cell.PlayerIdx on boards.
		for t := 0; t < config.TeamCount; t++ {
			children = append(children, layout.Rigid(a.header("TEAM "+teamName(t))))
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
	label := "READY TO PLAY"
	if view.myReady {
		label = "NOT READY TO PLAY"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.readyBtn, label) }),
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
		if gmode == config.ModeTeams {
			if decided, winner := teamsOutcome(eng, view.players); decided {
				// Announce the verdict to the spectator in a result box beside
				// the boards (never over them — the final playfields stay
				// fully visible).
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, content),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
							return a.spectatorTeamResultBox(gtx, view, winner)
						})
					}),
				)
			}
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
		cell := fitCellPx(gtx, snap.Width, snap.Height-snap.VisibleStart, 1, gtx.Dp(24), reserved, 14, 56)
		return layout.Center.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.boardWidget(snap, localIdx, cell, true, view.flash, gtx.Now)),
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
	case countdownVisible(view, mode):
		return layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Expanded(board),
			layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, view) }),
		)
	default:
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
	// board reads OUT, and once the game is decided (all but one out) the
	// survivor's board reads WINNER. A simultaneous-top-out draw shows every
	// board OUT and no winner.
	elimCount := 0
	for _, p := range view.players {
		if eng.IsEliminated(p.PlayerID) {
			elimCount++
		}
	}
	decided := len(view.players) > 1 && elimCount >= len(view.players)-1

	var items []layout.Widget
	for i, p := range view.players {
		i, p := i, p
		snap, ok := opps[p.PlayerID]
		out := eng.IsEliminated(p.PlayerID)
		items = append(items, func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := material.Body2(a.th, p.Name)
						l.Color = render.PlayerColorRGBA(i)
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						board := a.boardWidget(snap, i, cell, true, view.specFlash[i], gtx.Now)
						switch {
						case out:
							return a.boardOverlay(board, "OUT", colErr)(gtx)
						case decided:
							return a.boardOverlay(board, "WINNER", colGo)(gtx)
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
// board and team 1 via the opponent consumer (see Engine.Start). A fully
// eliminated team's board reads OUT; once either team is out, the other reads
// WINNERS.
func (a *App) spectatorTeamBoards(gtx C, eng *engine.Engine, view gameView) D {
	// Reactive cells: both team boards side by side, scrolling below the minimum.
	dims := eng.Snapshot()
	cell := fitCellPx(gtx, dims.Width, dims.Height-dims.VisibleStart, 2, 2*gtx.Dp(16), gtx.Dp(26), 10, 40)
	teamB, okB := eng.OpponentSnapshots()[engine.TeamBoardKey(1)]

	members := [config.TeamCount]int{}
	alive := [config.TeamCount]int{}
	for _, p := range view.players {
		if p.Team < 0 || p.Team >= config.TeamCount {
			continue
		}
		members[p.Team]++
		if !eng.IsEliminated(p.PlayerID) {
			alive[p.Team]++
		}
	}
	teamOut := func(t int) bool { return members[t] > 0 && alive[t] == 0 }
	over := teamOut(0) || teamOut(1)

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
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body(b.label, colMuted)),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !b.ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						board := a.boardWidget(b.snap, -1, cell, true, view.specFlash[b.team], gtx.Now)
						switch {
						case teamOut(b.team):
							return a.boardOverlay(board, "OUT", colErr)(gtx)
						case over:
							return a.boardOverlay(board, "WINNERS", colGo)(gtx)
						}
						return board(gtx)
					}),
				)
			})
		})
	}
	return a.scrollableBoards(gtx, &a.specTeamBoardsList, items)
}

// teamsOutcome reports whether a teams game has been decided — one team fully
// eliminated — and which team won (-1 for a simultaneous-top-out draw), from
// the roster and the engine's elimination records. This is how a pure
// spectator learns the verdict: spectator engines never receive the players'
// UpdateGameOver (they were never in the game), so the outcome is derived
// from the same elimination events that drive the OUT/WINNERS board chips.
func teamsOutcome(eng *engine.Engine, players []lobby.PlayerSummary) (decided bool, winner int) {
	members := [config.TeamCount]int{}
	alive := [config.TeamCount]int{}
	for _, p := range players {
		if p.Team < 0 || p.Team >= config.TeamCount {
			continue
		}
		members[p.Team]++
		if !eng.IsEliminated(p.PlayerID) {
			alive[p.Team]++
		}
	}
	out := func(t int) bool { return members[t] > 0 && alive[t] == 0 }
	switch {
	case out(0) && out(1):
		return true, -1
	case out(0):
		return true, 1
	case out(1):
		return true, 0
	}
	return false, 0
}

// spectatorTeamResultBox announces a decided teams game to a spectator: the
// winning team, both teams' final scores, and the Back to Lobby button. Like
// the player's gameOverBox it sits beside the boards, never over them.
func (a *App) spectatorTeamResultBox(gtx C, view gameView, winner int) D {
	msg, c := "DRAW", colMuted
	if winner >= 0 && winner < config.TeamCount {
		msg, c = "TEAM "+teamName(winner)+" WINS!", colAccent
	}
	score := fmt.Sprintf("TEAM %s %d (lvl %d) · TEAM %s %d (lvl %d)",
		teamName(0), view.teamScores[0], view.teamLevels[0],
		teamName(1), view.teamScores[1], view.teamLevels[1])
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, colBg, func(gtx C) D {
				return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.pixel(unit.Sp(18), "GAME OVER", colFg).Layout),
						layout.Rigid(spacer(10)),
						layout.Rigid(a.pixel(unit.Sp(12), msg, c).Layout),
						layout.Rigid(spacer(8)),
						layout.Rigid(func(gtx C) D {
							l := material.Body1(a.th, score)
							l.Color = colGold
							return l.Layout(gtx)
						}),
						layout.Rigid(spacer(14)),
						layout.Rigid(func(gtx C) D {
							return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby")
						}),
					)
				})
			})
		})
	})
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
