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
	"jetris/internal/voice"
)

// gameView is the per-frame snapshot of game scalars, taken under a.mu so the
// layout never reads fields the pump goroutine is writing.
type gameView struct {
	score, level int
	award        awardBanner // the last scored clear on this board (award.go), shown while it is fresh

	teamNames            []string // teams: what the teams are called (the engine's, nil = the letters) — read through teamName
	teamScores           []int    // teams: per-team scores in index order — read through teamScore, which answers 0 before the first stats update arrives
	teamLevels           []int    // teams: per-team levels in index order — read through teamLevel
	rtt                  time.Duration
	linkDown             time.Duration // how long the NATS link has been down (0 = up) — the HUD's LINK stat
	status               string
	countdown            int
	countdownAt          time.Time
	gameOver, won        bool
	finished             bool   // the game is over for everyone (finished or archived) — its replay is there to pin and share
	pinned               bool   // the lobby's word on whether that replay is pinned (gameOverActions)
	gameOverNote         string // under the game-over box's buttons: a refused pin
	myReady              bool
	readyNote            string // under the ready bar: what an open game still waits for (a playfield with nobody on it), or how many seats an invite game has to fill
	players, readyPlayer []lobby.PlayerSummary
	flash                map[[2]int]time.Time
	casWant              map[[2]int]time.Time         // own board: the outline blinking where a rejected step wanted the piece
	casKickAt            time.Time                    // own board: CAS-recoil epoch — the piece snaps back and vibrates where the rejection put it (zero = idle)
	casKickFrom          [2]float64                   // own board: the snap-back's start, where the lost step wanted the piece relative to where it stood (cells)
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
	// The voice chat as this frame finds it (voice.go): the mic button's
	// state, the meter, and who is heard.
	voice voice.Snapshot
}

// teamName is what team t is called on this screen (config.TeamName: the
// game's name for it, else its letter).
func (v gameView) teamName(t int) string { return config.TeamName(v.teamNames, t) }

// teamScore and teamLevel read one team's live total out of the view. The
// slices arrive with the engine's first UpdateTeamStats, so every screen that
// draws a scoreboard before then (and any index a stale roster could hand us)
// reads a plain 0 rather than running off the end.
func (v gameView) teamScore(team int) int {
	if team < 0 || team >= len(v.teamScores) {
		return 0
	}
	return v.teamScores[team]
}

func (v gameView) teamLevel(team int) int {
	if team < 0 || team >= len(v.teamLevels) {
		return 0
	}
	return v.teamLevels[team]
}

func (a *App) snapshotGame(now time.Time) gameView {
	var teamNames []string
	if a.eng != nil {
		teamNames = a.eng.TeamNames()
	}
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
	// The lost step's outline, on the own board: pruned the same way.
	cw := make(map[[2]int]time.Time)
	for k, v := range a.casWant {
		if now.Sub(v) < flashDur {
			cw[k] = v
		} else {
			delete(a.casWant, k)
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
		score:     a.score,
		level:     a.level,
		award:     a.award,
		teamNames: teamNames,

		teamScores:     a.teamScores,
		teamLevels:     a.teamLevels,
		rtt:            a.rtt,
		linkDown:       linkDownFor(a.linkDownAt, now),
		status:         a.gameStatus,
		countdown:      a.countdown,
		countdownAt:    a.countdownAt,
		gameOver:       a.gameOver,
		gameOverNote:   a.gameOverNote,
		finished:       a.gameStatus == string(config.GameStatusFinished) || a.gameStatus == string(config.GameStatusArchived),
		won:            a.won,
		myReady:        a.myReady,
		readyNote:      a.readyNote,
		players:        append([]lobby.PlayerSummary(nil), a.gamePlayers...),
		readyPlayer:    append([]lobby.PlayerSummary(nil), a.readyPlayers...),
		flash:          fc,
		casWant:        cw,
		casKickAt:      a.casKickAt,
		casKickFrom:    a.casKickFrom,
		specFlash:      sf,
		flashActive:    len(fc) > 0 || len(cw) > 0 || specActive,
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
	return a.layoutGameEngine(gtx, eng)
}

// layoutGameEngine is the game screen over eng: the live game's (layoutGame)
// or the How to play tour's transport-less one (tutorial.go), which the
// tour stands over — its input handlers held off while the tour is up,
// since every press is the tour's and the keys are too.
func (a *App) layoutGameEngine(gtx C, eng *engine.Engine) D {
	if eng == nil {
		return D{}
	}
	mode := eng.Mode()
	gmode := eng.GameMode()
	live := !a.tutorialUp()

	view := a.snapshotGame(gtx.Now)
	if view.finished && live {
		view.pinned = a.replayPinned(eng.GameID()) // the lobby's word, asked outside the lock
	}
	// The decided game — a spectator's, or a winning player's own: the reveal
	// the boards, the legend and the spectator's result box all draw from
	// (spectator_reveal.go).
	view.outcome = a.resolveOutcome(eng, view, gmode, gtx.Now)
	// The voice session's frame (voice.go): the menu's knobs go in as
	// atomics, the snapshot comes out under the session's own lock.
	view.voice = a.voiceFrame(a.voiceChannel())
	started := view.status == string(config.GameStatusInProgress)
	// playing: the keyboard drives the piece (a seated player, game in
	// progress, not eliminated). Only then do the board and the chat compete
	// for the keys — the player chats by clicking into the chat panel and
	// plays again by clicking anywhere else (handleGameFocus); before the
	// start, and for spectators, the chat editor is the only key consumer.
	playing := mode == engine.ModePlayer && started && !view.gameOver && live
	a.handleGameFocus(gtx, eng, playing) // first thing in the frame — see its doc
	// Focus outline: the chat's while its editor (or its Send button, for the
	// one frame a press leaves it there) has the keys, the board's otherwise
	// — while playing they converge to one or the other within a frame.
	view.chatFocused = playing && (gtx.Source.Focused(&a.gameChatEd) || gtx.Source.Focused(&a.gameChatBtn))
	view.boardFocused = playing && !view.chatFocused
	// The accidental-drop guard watches the pieces go by (dropguard.go):
	// first, so every hard drop the handlers below dispatch is judged against
	// the board as this frame finds it.
	a.observeDropGuard(gtx, eng, mode == engine.ModePlayer && started && live)
	// Dispatch moves (the board's key filters are registered here every
	// frame; its key-input target, event.Op, is the root pointerArea below).
	if mode == engine.ModePlayer && started && live {
		a.handleKeys(gtx, eng)
	}
	// The screen's own chrome first (gamescreen.go — the bar's switches), so
	// a column shown or hidden this frame is already in the layout the pad
	// and the gestures measure.
	a.handleFormClicks(gtx, eng)
	// Only the leave modal takes the game away: it scrims the screen and
	// swallows the frame's presses, so neither the pad nor the playfield may
	// act under it. The bar's columns — the menu among them — sit BESIDE the
	// board and never stop play.
	reachable := playing && !a.confirmLeave
	// The on-screen pad mirrors the keyboard scheme; its clicks are drained
	// every frame and only dispatched while the game is actually playable.
	// The ← → arms shift on the press instead and, held, auto-repeat
	// (handlePadShift feeds their edges to the DAS/ARR machine).
	a.handlePadClicks(gtx, eng, reachable)
	a.handlePadShift(gtx, eng, reachable)
	// The frame's DAS/ARR repeats (autoshift.go), fed by handleKeys and
	// handlePadShift above — the keyboard's gate, so a held key under the
	// leave modal repeats exactly as the OS repeat did, and any other state
	// resets the machine.
	a.handleAutoShift(gtx, eng, mode == engine.ModePlayer && started && live)
	// So are the touch gestures on the playfield (gesture.go) — after the
	// pad's Clickables have drained.
	a.handleGestures(gtx, eng, reachable)

	if a.readyBtn.Clicked(gtx) && live {
		go a.toggleReady()
	}
	// The game-over box's Pin and Share, and the share modal's buttons while
	// it is up (share.go); the modal scrims the screen, so Back is not
	// answered under it.
	shareUp := false
	if live {
		shareUp = a.handleGameOverActions(gtx, eng.GameID())
	}
	if a.backBtn.Clicked(gtx) && live && !shareUp {
		// Walking out of a running game deserves an "are you sure?" — the seat
		// is kept and the lobby offers Rejoin, but the board plays on without
		// you. Any other state (pre-start, game over, spectating) leaves
		// directly; leaving pre-start also clears the ready mark.
		if mode == engine.ModePlayer && started && !view.gameOver {
			a.confirmLeave = true
			a.leaveFreesSeat = a.currentGameOpen()
		} else {
			go a.leaveCurrentGame()
		}
	}
	if a.leaveYesBtn.Clicked(gtx) && live {
		a.confirmLeave = false
		go a.leaveCurrentGame()
	}
	if a.leaveNoBtn.Clicked(gtx) {
		a.confirmLeave = false
	}
	if live {
		a.handleGameChatSubmit(gtx, eng)
	}
	if view.flashActive {
		animate(gtx) // keep animating the flash until it expires
	}
	if len(view.rowStrobes) > 0 || len(view.specRowStrobes) > 0 || gtx.Now.Sub(view.shakeStart) < shakeDur {
		animate(gtx) // keep the row strobes / garbage impact shake animating
	}
	if countdownVisible(view, mode) && gtx.Now.Sub(view.countdownAt) < countdownAnimDur {
		animate(gtx) // keep animating the countdown pop until it settles
	}
	if view.award.visible(gtx.Now) {
		animate(gtx) // keep the award banner popping, rising and fading until it is gone
	}

	if view.fireworks != nil && view.fireworks.active(gtx.Now) {
		animate(gtx) // keep the victory fireworks animating until the show ends
	}
	if view.outcome.decided {
		animate(gtx) // keep the winner show animating while the screen is up
	}
	voiceRepaints(gtx, view.voice, a.hudVisible())

	// Mirror the checkbox into the locked flag that gates the consumer-side
	// message tap (recordStreamMsg runs on the engine's consumer goroutines).
	// The publish mode goes straight to the engine.
	showMsgs := a.showMsgs.Value
	a.mu.Lock()
	a.msgShow = showMsgs
	a.mu.Unlock()
	if mode == engine.ModePlayer {
		eng.SetPublishMode(publishModeOf(a.labAsync()))
		eng.SetInflightLimit(labInflight)
	}

	root := func(gtx C) D { return a.gameScreen(gtx, eng, view, mode, gmode, showMsgs) }
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
	if shareUp {
		screen = func(gtx C) D { return a.shareModalOver(gtx, base) }
	}
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
		gtx.Constraints.Max.X = modalW(gtx, 420)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colErr, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "LEAVE GAME?", colErr).Layout),
							layout.Rigid(spacer(10)),
							layout.Rigid(a.body("Are you sure you want to leave? The game keeps going —", colFg)),
							layout.Rigid(func(gtx C) D {
								if a.leaveFreesSeat {
									// An open game: the seat is freed for anyone,
									// and the lobby offers the game to join again.
									return a.body("your piece is taken off the board and your seat is free for anyone; you can join again from the lobby.", colFg)(gtx)
								}
								return a.body("you can rejoin it from the lobby.", colFg)(gtx)
							}),
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

// gameChatPanel renders the game's chat panel: this game's messages plus the
// lobby chat folded in — lobby lines are prefixed
// "@lobby" and colored colLobby so they're obviously not from the game. Game
// messages are seen only by this game's players and spectators (per-game chat
// subject); a message typed here goes to the game chat, or to the lobby chat
// when it starts with "@lobby".
//
// Everyone types, players mid-game included: while the keys drive the piece
// a click into the panel hands them to the chat (handleGameFocus — the panel
// is the chat's pointer area, a.chatTag) and the panel wears the white focus
// ring; a click anywhere else, or Escape, hands them back to the board, and
// Tab switches either way. The editor's hint says which way the keys
// currently go.
//
// It is a strip along the bottom of the screen, shown and hidden by the
// bar's chat button (gamescreen.go) and never over the board: while it is up
// the playfield simply has that many fewer rows.
func (a *App) gameChatPanel(gtx C, eng *engine.Engine, view gameView) D {
	msgs := a.gameChatLog(eng.GameID())
	a.chatSeen = len(msgs) // seen: the bar's unread dot goes out (gamescreen.go)

	// The hints spell the keyboard's board/chat switch out at full width and
	// abbreviate on the compact screen, where the editor is a phone's wide
	// and the switch is a tap on the panel that is open anyway.
	hint := "Message… (start with @lobby to message the lobby)"
	focusHint := "Message… (Esc, Tab or click the board to play again; @lobby messages the lobby)"
	boardHint := "Click here to chat — the keys are driving your piece; Tab to switch focus"
	if a.form.compact {
		hint, focusHint, boardHint = "Message…", "Message… (@lobby for the lobby)", "Tap to chat"
	}
	ring := colorN{} // transparent: the ring shows only while the chat holds the keys
	switch {
	case view.chatFocused:
		hint = focusHint
		ring = colFocus
	case view.boardFocused:
		hint = boardHint
	}

	log := func(gtx C) D {
		return a.chatLogBox(gtx, &a.gameChatList, len(msgs), func(i int) (string, colorN) {
			return chatLine(msgs[i])
		})
	}
	composer := func(gtx C) D {
		return a.chatComposer(gtx, &a.gameChatEd, &a.gameChatBtn, hint)
	}

	return a.tutMark(gtx, tutGameChat, func(gtx C) D {
		return pointerArea(gtx, &a.chatTag, func(gtx C) D {
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(a.header("CHAT")),
					layout.Rigid(func(gtx C) D {
						return focusRing(gtx, ring, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(log),
								layout.Rigid(spacer(6)),
								layout.Rigid(composer),
							)
						})
					}),
				)
			})
		})
	})
}

// chatLogBox is the conversation itself, the same in both chat strips (the
// game's above and the lobby's, lobbyChatStrip): the messages in a bordered
// scrolling list, height-reactive — at least 96 dp of talk, growing with the
// window (12% of the room the strip is given) so a taller screen shows more
// of it without eating into what is above it. line renders message i, so each
// strip keeps its own idea of what a line says.
func (a *App) chatLogBox(gtx C, lst *widget.List, n int, line func(i int) (string, colorN)) D {
	return bordered(gtx, func(gtx C) D {
		if maxH := max(gtx.Dp(96), gtx.Constraints.Max.Y*12/100); gtx.Constraints.Max.Y > maxH {
			gtx.Constraints.Max.Y = maxH
		}
		return material.List(a.th, lst).Layout(gtx, n, func(gtx C, i int) D {
			txt, col := line(i)
			return layout.Inset{Top: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, a.body(txt, col))
		})
	})
}

// chatComposer is the row under the log, in both strips: the editor and Send.
func (a *App) chatComposer(gtx C, ed *widget.Editor, send *widget.Clickable, hint string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D { return a.editorBox(gtx, ed, hint) }),
		layout.Rigid(hSpacer(6)),
		layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, send, "Send") }),
	)
}

// gameChatLog is this game's chat as the panel shows it: the game's own
// messages plus the lobby's (GameID ""), which the panel prefixes "@lobby".
// The lobby's join/leave notices stay in the lobby: who is coming and going
// out there is no business of a game in progress.
func (a *App) gameChatLog(gameID string) []lobby.ChatMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	msgs := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if inGameChat(m, gameID) {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

// inGameChat: does the game's chat panel show m? See gameChatLog.
func inGameChat(m lobby.ChatMessage, gameID string) bool {
	return !m.System && (m.GameID == gameID || m.GameID == "")
}

// gameChatCount is how many messages gameChatLog would return — what the
// compact bar's unread dot compares against every frame, without copying the
// conversation to find out.
func (a *App) gameChatCount(gameID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, m := range a.chatLog {
		if inGameChat(m, gameID) {
			n++
		}
	}
	return n
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

// sessionLine is the game HUD's "you are here": the player's name and the
// server this session is on, in the lobby header's own idiom — "tester @
// Jetris EU central". Every move in this game is a round trip to that server,
// and which one it is is exactly what a player comparing an RTT wants to be
// reminded of without leaving the board. The name alone, not the URL: the HUD
// column is 200-300 dp wide and the name is what identifies the server (the
// lobby header carries the URL, and so does the connection page).
func (a *App) sessionLine(gtx C) D {
	a.mu.Lock()
	server := a.connName
	a.mu.Unlock()
	if server == "" {
		return D{}
	}
	name := ""
	if lb := a.getLobby(); lb != nil {
		name = lb.PlayerName()
	}
	const size = unit.Sp(8)
	return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		// One line where it fits; otherwise the name over the server, never
		// one truncated line — the server is what this line exists to say,
		// and half a server name says nothing.
		if name == "" {
			return a.pixelLabelFit(gtx, size, "@ "+server, colMuted)
		}
		if a.pixelWidth(gtx, size, name+" @ "+server) <= gtx.Constraints.Max.X {
			return a.pixelLabelFit(gtx, size, name+" @ "+server, colMuted)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.pixelLabelFit(gtx, size, name, colMuted) }),
			layout.Rigid(func(gtx C) D { return a.pixelLabelFit(gtx, size, "@ "+server, colMuted) }),
		)
	})
}

// gameHUD is the menu column's content, top to bottom: the game's name and
// the session, the players, the stats, the ready list before the start, the
// switches and the knobs, Back to Lobby, the controls legend, and the NATS tag
// last. It is laid out at its own height — the column it is in scrolls it
// (hudColumn) — and slotY is the height that column shows at once, which is
// where the tag is pinned while the rest leaves it the room.
func (a *App) gameHUD(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode, slotY int) D {
	started := view.status == string(config.GameStatusInProgress)
	modeLabel := "Cooperative"
	switch gmode {
	case config.ModeCompetitive:
		modeLabel = "Competitive"
	case config.ModeTeams:
		modeLabel = "Teams"
		if mode != engine.ModeSpectator {
			modeLabel += " · TEAM " + eng.TeamName(eng.TeamIdx())
		}
	}
	if mode == engine.ModeSpectator {
		modeLabel = "Spectating · " + modeLabel
	}

	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			// The column is hidden by the same bar button that showed it; this
			// line just names the game.
			return a.pixelLabelFit(gtx, unit.Sp(11), modeLabel, colAccent)
		}),
		layout.Rigid(func(gtx C) D { return a.sessionLine(gtx) }),
		layout.Rigid(spacer(10)),
		// The roster and the stats under it are one part to the tour
		// (tutHUDStats: every one of them marked, the union lit).
		layout.Rigid(a.tutMarked(tutHUDStats, func(gtx C) D { return a.legend(gtx, eng, view, gmode) })),
		layout.Rigid(spacer(14)),
	}

	if gmode == config.ModeTeams {
		// Teams: live per-team scoreboard (folded from every team's line-clear
		// events on every engine), shown to players and spectators alike. The
		// player's own team is highlighted; spectators have no own team and see
		// each team's level inline instead of the single LEVEL stat.
		for t := 0; t < eng.TeamCount(); t++ {
			valCol := colFg
			if mode != engine.ModeSpectator && t == eng.TeamIdx() {
				valCol = colAccent
			}
			val := fmt.Sprintf("%d", view.teamScore(t))
			if mode == engine.ModeSpectator {
				val = fmt.Sprintf("%d · lvl %d", view.teamScore(t), view.teamLevel(t))
			}
			children = append(children,
				layout.Rigid(a.tutMarked(tutHUDStats, a.hudStatColored("TEAM "+eng.TeamName(t), val, valCol))))
		}
	} else {
		children = append(children, layout.Rigid(a.tutMarked(tutHUDStats, a.hudStat("SCORE", view.score))))
	}
	if !(gmode == config.ModeTeams && mode == engine.ModeSpectator) {
		children = append(children, layout.Rigid(a.tutMarked(tutHUDStats, a.hudStat("LEVEL", view.level))))
	}
	if _, goal := eng.GoalProgress(); goal > 0 {
		// The game's length in lines: this playfield's count against the
		// goal — every team's for a teams spectator, who has no playfield.
		if gmode == config.ModeTeams && mode == engine.ModeSpectator {
			parts := make([]string, 0, eng.TeamCount())
			for t := 0; t < eng.TeamCount(); t++ {
				lines, _ := eng.TeamGoalProgress(t)
				parts = append(parts, fmt.Sprintf("%s %d", eng.TeamName(t), lines))
			}
			children = append(children, layout.Rigid(a.tutMarked(tutHUDStats, a.hudStatText("LINES", strings.Join(parts, " · ")+fmt.Sprintf(" / %d", goal)))))
		} else {
			lines, _ := eng.GoalProgress()
			children = append(children, layout.Rigid(a.tutMarked(tutHUDStats, a.hudStatText("LINES", fmt.Sprintf("%d / %d", lines, goal)))))
		}
	}

	if mode == engine.ModePlayer {
		children = append(children, layout.Rigid(a.tutMarked(tutHUDStats, a.hudStatColored("Batch RTT", formatRTT(view.rtt), rttColor(view.rtt)))))
	}
	if view.linkDown > 0 {
		// The NATS link is down (link.go): every move is waiting on it. Name
		// the pause, and keep its clock ticking.
		children = append(children, layout.Rigid(func(gtx C) D {
			animate(gtx)
			return a.hudStatColored("LINK", "LOST "+formatLinkDown(view.linkDown), colOrange)(gtx)
		}))
	}

	if mode == engine.ModePlayer && !started && !view.gameOver {
		// Who is ready and who is not. The ready-up action itself is the
		// readyBar's, on the screen over the board (gamescreen.go) — not a
		// second button for it in here.
		children = append(children,
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D { return a.readyList(gtx, view) }),
		)
	}

	children = append(children,
		layout.Rigid(spacer(14)),
		layout.Rigid(a.tutMarked(tutHUDMsgs, func(gtx C) D {
			cb := material.CheckBox(a.th, &a.showMsgs, "Show NATS messages")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		})),
		// The voice chat (voice.go): a player's and a spectator's alike.
		layout.Rigid(spacer(12)),
		layout.Rigid(func(gtx C) D { return a.voiceSection(gtx, view.voice, a.voiceRoomName(eng, view.voice)) }),
	)
	if mode == engine.ModePlayer {
		// The lab toggles (lab.go): how the moves are published, what the
		// board paints while they round-trip. A player's, while playing.
		children = append(children,
			layout.Rigid(spacer(12)),
			layout.Rigid(a.tutMarked(tutHUDLab, a.labToggles)),
			// The handling knobs (autoshift.go): how a held ← → repeats,
			// and how fast a held ↓ falls.
			layout.Rigid(spacer(12)),
			layout.Rigid(a.handlingKnobs),
		)
	}
	children = append(children,
		layout.Rigid(spacer(18)),
		layout.Rigid(a.tutMarked(tutHUDBack, func(gtx C) D {
			return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby")
		})),
	)
	if mode == engine.ModePlayer {
		// Under it all, for a player, the controls legend: the keys and the
		// touch gestures, this screen's own scheme first, each section a
		// header over rows of a key (or gesture) beside the move it makes
		// (controlsSections). The whole of it, every time — the column
		// scrolls (hudColumn), so a short screen scrolls to it rather than
		// losing it.
		for _, s := range a.controlsSections(eng.HoldEnabled()) {
			children = append(children,
				layout.Rigid(spacer(16)),
				layout.Rigid(a.tutMarked(tutControls, func(gtx C) D {
					// The legend's parts at their own sizes, not the column's:
					// a key label handed the column's width as its minimum
					// reports it, and controlsHint lines the moves up past
					// the widest key.
					gtx.Constraints.Min = image.Point{}
					return a.controlsHint(gtx, s.header, s.rows)
				})),
			)
		}
	}

	// The column's parts at their own heights (the button and the checkbox
	// still span the width), and the NATS tag under them: at the column's
	// foot, on the move-buffer strip's line just over the chat, while the
	// parts leave it room there — slotY is the height the column shows at
	// once (hudColumn) — and under the last of them once they run past it,
	// where the scroll finds it.
	gtx.Constraints.Min.Y = 0
	macro := op.Record(gtx.Ops)
	topD := layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	topCall := macro.Stop()
	macro = op.Record(gtx.Ops)
	tagD := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(a.natsTag(22, 10)),
		layout.Flexed(1, func(gtx C) D {
			// The game screen has no corner for the version plate (the bar's
			// chat button is that corner), so it rides here.
			update, _ := a.update()
			col := colMuted
			if update != "" {
				col = colGold
			}
			return layout.E.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.Y = 0
				return a.pixelLabelFit(gtx, unit.Sp(8), versionLabel(update), col)
			})
		}),
	)
	tagCall := macro.Stop()
	tagY := max(topD.Size.Y+gtx.Dp(20), slotY-tagD.Size.Y)
	topCall.Add(gtx.Ops)
	func() {
		defer op.Offset(image.Pt(0, tagY)).Push(gtx.Ops).Pop()
		tagCall.Add(gtx.Ops)
	}()
	return D{Size: image.Pt(gtx.Constraints.Max.X, tagY+tagD.Size.Y)}
}

// controlsSection is one block of the legend: a heading and the mappings
// under it, each a key (or gesture) and the move it makes.
type controlsSection struct {
	header string
	rows   [][2]string
}

// controlsSections is what the legend says, wherever it is drawn — the game's
// menu column (gameHUD) and the lobby's (lobbyMenu). This screen's own
// scheme comes first: a touch player wants the gestures at the top and reads
// the keys as the footnote, and a player with a keyboard the other way round.
// hold adds the hold key and its gesture, and only a game whose rules carry
// the hold queue can promise it — the lobby, which has no game yet, says
// nothing about it rather than teaching a key that may do nothing.
func (a *App) controlsSections(hold bool) []controlsSection {
	// Every key that makes a move, on the row of the move it makes — the
	// arrows and their WASD twins (arrowForKey) together, and the
	// Guideline's modifier controls beside the letters they double.
	keys := [][2]string{
		{"← → A D", "move · hold slides"},
		{"↓ S", "soft drop · hold falls"},
		{"↑ W X", "rotate CW"},
		{"Z CTRL", "rotate CCW"},
		{"SPACE", "hard drop"},
	}
	touch := [][2]string{{"swipe ← →", "move"}, {"tap ◀", "rotate CCW"}, {"tap ▶", "rotate CW"}, {"drag ↓", "soft drop"}, {"flick ↓", "hard drop"}}
	if hold {
		keys = append(keys, [2]string{"C SHIFT", "hold"})
		touch = append(touch, [2]string{"swipe ↑", "hold"})
	}
	keys = append(keys, [2]string{"TAB", "chat / board"})
	sections := []controlsSection{{"KEYS", keys}, {"TOUCH", touch}}
	if a.touchUI {
		sections[0], sections[1] = sections[1], sections[0]
	}
	return sections
}

// controlsHint is one section of the controls legend: the header, then a
// row per mapping — the key or gesture in the foreground color, the move in
// the muted one — in the small pixel face, the moves lined up in a column
// past the widest key.
func (a *App) controlsHint(gtx C, header string, rows [][2]string) D {
	const size = unit.Sp(8)
	keyW := 0
	for _, r := range rows {
		macro := op.Record(gtx.Ops)
		keyW = max(keyW, a.pixel(size, r[0], colFg).Layout(gtx).Size.X)
		macro.Stop()
	}
	keyW += gtx.Sp(size) // a glyph's worth of air before the move
	kids := []layout.FlexChild{
		layout.Rigid(a.pixel(unit.Sp(9), header, colAccent).Layout),
		layout.Rigid(spacer(2)),
	}
	for _, r := range rows {
		key, move := r[0], r[1]
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = keyW
						return a.pixel(size, key, colFg).Layout(gtx)
					}),
					layout.Rigid(a.pixel(size, move, colMuted).Layout),
				)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (a *App) legend(gtx C, eng *engine.Engine, view gameView, gmode config.GameMode) D {
	var children []layout.FlexChild

	// Once the game is decided (view.outcome — on a spectator's screen, or a
	// winning player's) the legend reveals it: the winners' names gold in
	// bold italic with a trophy, the beaten in their board colors — no more
	// "(out)", every beaten player is out.
	oc := view.outcome
	// A board scored per seat lists every seat's own score beside its name,
	// best first — the live ranking the game is about.
	individual := eng.IndividualScoring()
	var scores map[string]int
	if individual {
		scores = eng.PlayerScores()
		scores[eng.PlayerID()] = view.score
	}
	playerRow := func(i int, p lobby.PlayerSummary) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			elim := (gmode == config.ModeCompetitive || gmode == config.ModeTeams) && eng.IsEliminated(p.PlayerID)
			name := agentName(p.Name, p.Agent)
			if individual {
				name = fmt.Sprintf("%s  %d", name, scores[p.PlayerID])
			}
			textCol, won := colFg, false
			switch {
			case oc.wins(p.PlayerID):
				name, textCol, won = winnerMark+name, colGold, true
			case oc.decided:
				textCol = render.PlayerColorRGBA(i)
			case elim:
				name += " (out)"
				textCol = colMuted
			}
			// In a split-pieces game the seat's ration goes under its name:
			// which of the seven types this player — seatmate or opponent —
			// is the one who can hand their board that shape.
			ration := eng.PieceSetForSlot(playfieldSlot(gmode, p))
			return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return swatch(gtx, render.PlayerColorRGBA(i), 12) }),
							layout.Rigid(hSpacer(6)),
							layout.Rigid(a.boardLabel(name, textCol, won)),
							layout.Rigid(a.speakerMark(view.voice, p.PlayerID, unit.Dp(12))),
						)
					}),
					layout.Rigid(func(gtx C) D {
						if len(ration) == 0 {
							return D{}
						}
						return layout.Inset{Left: unit.Dp(18), Top: unit.Dp(1)}.Layout(gtx, func(gtx C) D {
							return a.rationRow(gtx, ration)
						})
					}),
				)
			})
		})
	}

	if gmode == config.ModeTeams {
		// Group players under their TEAM A / TEAM B / … headers. Swatch colors
		// stay keyed by the GLOBAL roster index, matching Cell.PlayerIdx on
		// boards.
		teams := eng.TeamCount()
		for t := 0; t < teams; t++ {
			hdr := a.header("TEAM " + eng.TeamName(t))
			if oc.decided && oc.winTeam == t {
				// The winning team's header: gold, in the synthesized bold
				// italic (the pixel face has no such variants).
				hdr = func(gtx C) D {
					return layout.Inset{Bottom: unit.Dp(5)}.Layout(gtx, a.pixelEmph(unit.Sp(10), "TEAM "+eng.TeamName(t), colGold))
				}
			}
			children = append(children, layout.Rigid(hdr))
			for i, p := range view.players {
				if p.Team == t {
					children = append(children, playerRow(i, p))
				}
			}
			if t < teams-1 {
				children = append(children, layout.Rigid(spacer(6)))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}

	children = append(children, layout.Rigid(a.header("PLAYERS")))
	for _, r := range legendOrder(view.players, scores) {
		children = append(children, playerRow(r.idx, r.p))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// playfieldSlot is a player's slot on their playfield — the key of their
// piece ration (Engine.PieceSetForSlot): the seat itself on the crew's
// shared board, the slot within the team on a team's.
func playfieldSlot(gmode config.GameMode, p lobby.PlayerSummary) int {
	if gmode == config.ModeTeams {
		return p.TeamSlot
	}
	return p.Seat
}

// engineSeats is the roster as the engine tracks it (engine.SetRoster).
func engineSeats(players []lobby.PlayerSummary) []engine.Seat {
	seats := make([]engine.Seat, 0, len(players))
	for _, p := range players {
		seats = append(seats, engine.Seat{PlayerID: p.PlayerID, Seat: p.Seat, Team: p.Team, TeamSlot: p.TeamSlot})
	}
	return seats
}

// legendRow is a legend line: the player and their roster index (the swatch
// colour's key, which follows the seat, not the line's place in the list).
type legendRow struct {
	idx int
	p   lobby.PlayerSummary
}

// legendOrder is the legend's order: roster order, or — on a board scored
// per seat, when scores is set — best score first, the roster order breaking
// ties, so the ranking reads top to bottom.
func legendOrder(players []lobby.PlayerSummary, scores map[string]int) []legendRow {
	rows := make([]legendRow, 0, len(players))
	for i, p := range players {
		rows = append(rows, legendRow{i, p})
	}
	if scores != nil {
		sort.SliceStable(rows, func(i, j int) bool {
			return scores[rows[i].p.PlayerID] > scores[rows[j].p.PlayerID]
		})
	}
	return rows
}

// rankingLine writes every seat's score on a board scored per seat, best
// first — "alice 1200 · bob 940 · carol 310" — our own from the live view
// (the engine's own total), the others from the totals their events
// carried.
func rankingLine(players []lobby.PlayerSummary, scores map[string]int, me string, myScore int) string {
	scores[me] = myScore
	parts := make([]string, 0, len(players))
	for _, r := range legendOrder(players, scores) {
		parts = append(parts, fmt.Sprintf("%s %d", agentName(r.p.Name, r.p.Agent), scores[r.p.PlayerID]))
	}
	return joinParts(parts)
}

// rationRow writes one seat's piece ration — the types that seat's sequence
// draws from when the game splits the pieces between teammates — as its
// letters, each in the piece's own board color, so the hand reads at a glance
// (a cyan I, a purple T). Empty in every game that does not split.
func (a *App) rationRow(gtx C, set []game.PieceType) D {
	kids := make([]layout.FlexChild, 0, len(set)*2)
	for i, pt := range set {
		if i > 0 {
			kids = append(kids, layout.Rigid(hSpacer(3)))
		}
		kids = append(kids, layout.Rigid(a.pixel(unit.Sp(9), pt.String(), render.PieceLetterRGBA(pt)).Layout))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// readyList is the pre-start roll call in the menu column: a row per seat,
// the name beside a READY or NOT READY badge. The action itself — readying
// up, standing down — is the readyBar's, over the board.
func (a *App) readyList(gtx C, view gameView) D {
	var rows []layout.FlexChild
	for _, p := range view.readyPlayer {
		p := p
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, a.body(agentName(p.Name, p.Agent), colFg)),
					layout.Rigid(a.speakerMark(view.voice, p.PlayerID, unit.Dp(10))),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(a.readyBadge(p.Ready)),
				)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
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
			// Announce the verdict to the spectator in a result box next to
			// the boards (never over them — the final playfields stay
			// fully visible): beside them, or under them on the compact
			// screen, where the width is the boards' own.
			box := func(gtx C) D {
				return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
					return a.spectatorResultBox(gtx, view, view.outcome, gmode)
				})
			}
			if a.form.compact {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, content),
					layout.Rigid(box),
				)
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, content),
				layout.Rigid(box),
			)
		}
		if countdownVisible(view, mode) {
			return layout.Stack{Alignment: layout.Center}.Layout(gtx,
				layout.Expanded(content),
				layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, view.countdown, view.countdownAt) }),
			)
		}
		return content(gtx)
	}

	// Which board a player sees is the display position (lab.go): the
	// consumer's echo — positions 1 and 2 — or the engine's acked replica,
	// a delivery ahead of it (position 3). Everyone else sees the replica,
	// as always.
	display := a.displayMode()
	snap := eng.Snapshot()
	if mode == engine.ModePlayer && display != displayAck {
		snap = eng.EchoSnapshot()
	}
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
	// The pre-rendered move (lab.go): position 2 outlines where the piece is
	// headed over the consumer's board; position 3 draws the piece there at
	// once — colored, its ghost under it — and outlines where the acks have
	// it so far (the outline moves as the commits ack; a lost step snaps the
	// piece back onto it, and it flashes).
	var intent, acked map[[2]int]bool
	if mode == engine.ModePlayer && started && !view.gameOver {
		switch display {
		case displayOutline:
			intent = intentCells(eng, snap, localIdx)
		case displayAck:
			snap, intent, acked = optimisticBoard(eng, snap, localIdx)
		}
	}
	// Hard-drop ghost: where the falling piece would land if dropped right
	// now, derived from the very snapshot being drawn (after position 3 moved
	// the piece, so the ghost follows it) — never published. Agents already
	// plan with HardDropDestination, so the ghost only levels the field for
	// humans. Whether it shows is the game's own rule, chosen at creation
	// (GameMeta.NoGhost, on by default) — one setting for every player, like
	// the piece preview.
	var ghost map[[2]int]game.PieceType
	if eng.ShowGhost() && mode == engine.ModePlayer && started && !view.gameOver {
		ghost = ghostCells(snap, localIdx, gmode)
	}
	// The rejected write's recoil (effects.go): the piece flies back from
	// where the lost step wanted it and buzzes where the CAS failure put it,
	// its cells read off the very snapshot being drawn — so it follows the
	// piece through the repair's replay and the player steering on, in
	// every display position.
	var kick map[[2]int]bool
	if mode == engine.ModePlayer {
		kick = a.trackRecoil(gtx, snap, localIdx, view.casKickAt)
	}
	// Players with a piece preview get the NEXT well beside the playfield —
	// per-seat queue, so spectators (no seat) never have one. Read the live
	// queue (not just NextCount) so the space is only reserved once the
	// engine's sequence is up and the well will actually render. In a game
	// with the hold rule the HOLD box flanks the playfield's other side
	// (empty until the first hold; the slot is per seat too).
	// The wells flank the playfield on every screen, the compact one included
	// — that is what they are for, and where a player looks for them. What
	// the compact screen changes is their SIZE: they draw at a fraction of
	// the board's cell there (wellCell), because a phone cannot spare the
	// eight board columns a full-size pair would cost.
	nextPieces := eng.NextPieces()
	showNext := mode == engine.ModePlayer && len(nextPieces) > 0
	showHold := mode == engine.ModePlayer && eng.HoldEnabled()
	// The hidden rows above the playfield, behind smoked glass, when the
	// game shows them (GameMeta.ShowHeadroom) — one setting for every
	// board, the opponents' thumbnails included.
	headroom := eng.ShowHeadroom()
	board := func(gtx C) D {
		// Cell size tracks the window: as much board as fits after reserving
		// room for the wells beside it, the player's move-buffer strip under
		// it and (while the game is still playable) the control pad — beside
		// the playfield or under it, whichever leaves the bigger board
		// (fitBoardAndPad).
		player := mode == engine.ModePlayer
		showPad := player && !view.gameOver && a.padVisible()
		hold := eng.HoldEnabled()
		wells := sideWells{hold: showHold, next: showNext}
		// The wells' tiles use the board cell size; the plan measures a well
		// at a candidate cell by laying it out — at its content's size, not
		// the slot's — into a macro that is never played (the HOLD box
		// without its Clickable: laid out twice a frame, that would eat its
		// taps).
		loose := gtx
		loose.Constraints.Min = image.Point{}
		measure := func(w func(gtx C, cell int) D) func(int) int {
			return func(cell int) int {
				m := op.Record(loose.Ops)
				d := w(loose, cell)
				m.Stop()
				return d.Size.Y
			}
		}
		if showHold {
			wells.holdH = measure(func(gtx C, cell int) D {
				return a.holdWellBox(gtx, game.PieceI, false, false, a.wellCell(cell))
			})
		}
		if showNext {
			wells.nextH = measure(func(gtx C, cell int) D { return a.nextWell(gtx, nextPieces, a.wellCell(cell)) })
		}
		plan := a.fitBoardAndPad(gtx, snap.Width, boardRows(snap, headroom), wells, player, showPad, hold)
		cell := plan.cell
		padEnabled := view.status == string(config.GameStatusInProgress)
		// boardOnly is the playfield itself, with its effects and the
		// pre-game countdown over it.
		boardOnly := func(gtx C) D {
			fx := &boardFX{
				flash: view.flash, want: view.casWant, kick: kick, kickAt: view.casKickAt, kickFrom: view.casKickFrom,
				rows: view.rowStrobes, ghost: ghost, intent: intent, acked: acked,
			}
			if view.boardFocused {
				fx.frame = colFocus // the well's frame lights up: the keys drive the piece
			}
			bw := a.boardWidget(snap, localIdx, cell, true, fx, gtx.Now, headroom)
			if view.outcome.decided {
				// The finished board wears the winner show: the crew's
				// co-op board (a spectator's or a player's — the run's
				// rank is the prize), or a winning player's own. Over
				// the victory fireworks the show is painted last, so
				// the crown floats above the rockets and bursts — but
				// never above a modal: the leave-game one, or the share
				// modal the game-over box's Share puts up (its QR code
				// stands where the badge would float).
				crown := a.crownBoard
				if view.fireworks != nil && view.fireworks.active(gtx.Now) && !a.confirmLeave && !a.shareOpen {
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
			if !playfieldOverlayVisible(view, mode, gtx.Now) {
				return bw(gtx)
			}
			// The pre-game countdown — and, in play, the award banner
			// naming a scored clear — centers on the playfield itself
			// (not the whole board area, which would drift it toward
			// the NEXT well / surrounding whitespace) — and as an
			// Expanded child sized to the playfield, so a "GO!" wider
			// than a narrow board (it is, on a phone) neither widens the
			// board's slot nor shifts the columns beside it.
			return layout.Stack{Alignment: layout.Center}.Layout(gtx,
				layout.Stacked(bw),
				layout.Expanded(func(gtx C) D {
					sz := gtx.Constraints.Min
					gtx.Constraints.Min = image.Point{}
					m := op.Record(gtx.Ops)
					d := a.playfieldOverlay(gtx, view, mode)

					call := m.Stop()
					defer op.Offset(image.Pt((sz.X-d.Size.X)/2, (sz.Y-d.Size.Y)/2)).Push(gtx.Ops).Pop()
					call.Add(gtx.Ops)
					return D{Size: sz}
				}),
			)
		}
		// The side wells, in their own sub-divisions hanging from the
		// playfield's top edge: the HOLD box off its left, the NEXT well off
		// its right, classic arcade style.
		holdBox := a.tutMarked(tutGameHold, func(gtx C) D {
			held, has := eng.HeldPiece()
			return a.holdWell(gtx, held, has, eng.HoldUsed(), a.wellCell(cell))
		})
		nextBox := a.tutMarked(tutGameNext, func(gtx C) D { return a.nextWell(gtx, nextPieces, a.wellCell(cell)) })
		strip := a.tutMarked(tutGameStrip, func(gtx C) D {
			// Inputs queued behind the in-flight batch publish (very visible
			// on a high-RTT server); the strip drains as each buffered
			// move's own publish starts.
			return a.bufferedMovesStrip(gtx, eng.BufferedBatches(), eng.BatchesTaken())
		})
		// What layout.Center will stretch the column to (a Flexed slot's Min
		// is its Max on the main axis): the room left over on either side of
		// the centered content, which the gesture surface claims below.
		slot := gtx.Constraints.Min
		return layout.Center.Layout(gtx, func(gtx C) D {
			// The playfield's columns: the HOLD box off its left, the NEXT
			// well off its right, both hanging from its top edge — and, with
			// the pad flanking it like a handheld's controls, the D-pad
			// under the HOLD box and the face buttons under the NEXT well:
			// each column as wide as its wider member, well and pad centered
			// on each other, the pad centered on the playfield's height but
			// never over its well. The strip hangs under the playfield,
			// centered on it: wider than the playfield, it runs on under the
			// columns, and under whatever in them reaches past the
			// playfield's bottom. A pad that does not flank the playfield (a
			// tall, narrow column) follows under the strip, centered on the
			// playfield. flankGeom is the flanked layout's model; here every
			// part is recorded first, the positions following from the
			// sizes (a part not shown is a zero size and a no-op call).
			rec := func(show bool, w layout.Widget) (D, op.CallOp) {
				if !show {
					return D{}, op.CallOp{}
				}
				m := op.Record(gtx.Ops)
				d := w(gtx)
				return d, m.Stop()
			}
			flank := showPad && plan.beside
			boardD, boardCall := rec(true, a.tutMarked(tutGameBoard, boardOnly))
			holdD, holdCall := rec(showHold, holdBox)
			nextD, nextCall := rec(showNext, nextBox)
			dpadD, dpadCall := rec(flank, func(gtx C) D { return a.dpad(gtx, plan.padSizer, padEnabled) })
			faceD, faceCall := rec(flank, func(gtx C) D { return a.faceButtons(gtx, plan.padSizer, padEnabled, hold) })
			padD, padCall := rec(showPad && !flank, func(gtx C) D { return a.controlPad(gtx, plan.padSizer, padEnabled, hold) })
			stripD, stripCall := rec(player, strip)
			gap, vgap := gtx.Dp(wellGap), gtx.Dp(wellPadGap)
			if flank {
				gap = gtx.Dp(padSideGap)
			}
			leftW, rightW := max(holdD.Size.X, dpadD.Size.X), max(nextD.Size.X, faceD.Size.X)
			leftX := 0
			boardX := leftW
			if leftW > 0 {
				boardX += gap
			}
			rightX := boardX + boardD.Size.X
			if rightW > 0 {
				rightX += gap
			}
			// A handheld's controls belong at the screen's own edges, where
			// the hands holding it are — not huddled against a playfield
			// that, in a phone's landscape, is a fifth of its width. On the
			// compact screen the flanking columns take the slot's far edges
			// and the playfield sits dead centre between them; the room that
			// opens up on either side of it is all swipe surface.
			if flank && a.form.compact {
				if room := slot.X - gtx.Dp(4); room > rightX+rightW {
					if bx := (room - boardD.Size.X) / 2; bx-gap >= leftW && room-bx-boardD.Size.X-gap >= rightW {
						boardX, rightX = bx, room-rightW
					}
				}
			}
			low := a.padsAtBottom()
			dpadY := padTop(boardD.Size.Y, holdD.Size.Y, vgap, dpadD.Size.Y, low)
			faceY := padTop(boardD.Size.Y, nextD.Size.Y, vgap, faceD.Size.Y, low)
			leftBottom, rightBottom := holdD.Size.Y, nextD.Size.Y
			if flank {
				leftBottom, rightBottom = max(leftBottom, dpadY+dpadD.Size.Y), max(rightBottom, faceY+faceD.Size.Y)
			}
			stripX := boardX + boardD.Size.X/2 - stripD.Size.X/2
			stripBase := boardD.Size.Y
			if stripX < leftX+leftW {
				stripBase = max(stripBase, leftBottom)
			}
			if stripX+stripD.Size.X > rightX {
				stripBase = max(stripBase, rightBottom)
			}
			stripY := stripBase + gtx.Dp(10)
			bottom := max(boardD.Size.Y, leftBottom, rightBottom)
			if player {
				bottom = max(bottom, stripY+stripD.Size.Y)
			}
			padX, padY := boardX+boardD.Size.X/2-padD.Size.X/2, bottom+gtx.Dp(10)
			if padD.Size.Y > 0 {
				bottom = padY + padD.Size.Y
			}
			shift := max(0, -stripX, -padX)
			size := image.Pt(max(rightX+rightW, stripX+stripD.Size.X, padX+padD.Size.X)+shift, bottom)
			place := func(x, y int, c op.CallOp) {
				defer op.Offset(image.Pt(x+shift, y)).Push(gtx.Ops).Pop()
				c.Add(gtx.Ops)
			}
			// The touch-gesture surface, registered FIRST so everything
			// placed after it — the pad's buttons, the HOLD box — is over it
			// and wins its own presses. It runs the whole width the board
			// column was given, right across the ground the side columns
			// stand on: the wells only occupy the top of those columns, and
			// what is under and between them is room a thumb can use. The
			// widgets there keep their own taps by being drawn after (the
			// HOLD box holds when tapped, every pad button fires); the dead
			// space around them drives the piece. The room is taken
			// SYMMETRICALLY — the narrower side sets both — because a tap
			// rotates by which half of the surface it lands in, and that
			// split has to fall down the playfield's middle. On a phone this
			// roughly triples what a thumb can swipe on.
			bx0, bx1 := shift+boardX, shift+boardX+boardD.Size.X
			lo, hi := 0, size.X
			if slack := (slot.X - size.X) / 2; slack > 0 {
				lo, hi = -slack, hi+slack
			}
			ext := max(0, min(bx0-lo, hi-bx1)-gtx.Dp(1))
			fieldBot := size.Y
			if padD.Size.Y > 0 {
				fieldBot = padY // never over the pad under the board
			}
			a.gestureSurface(gtx, image.Rect(bx0-ext, 0, bx1+ext, max(fieldBot, boardD.Size.Y)), cell)
			place(leftX+(leftW-holdD.Size.X)/2, 0, holdCall)
			place(leftX+(leftW-dpadD.Size.X)/2, dpadY, dpadCall)
			place(boardX, 0, boardCall)
			place(rightX+(rightW-nextD.Size.X)/2, 0, nextCall)
			place(rightX+(rightW-faceD.Size.X)/2, faceY, faceCall)
			place(stripX, stripY, stripCall)
			place(padX, padY, padCall)
			return D{Size: size}
		})
	}
	switch {
	case view.gameOver:
		// The game-over box goes NEXT to the board, never over it: the final
		// playfield must stay fully visible. Beside it where there is width
		// to spare; under it on the compact screen, whose width there is
		// none of and whose pad has just gone away, leaving the room.
		box := func(gtx C) D {
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
				return a.gameOverBox(gtx, eng, gmode, view)
			})
		}
		if a.form.compact {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, board),
				layout.Rigid(box),
			)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, board),
			layout.Rigid(box),
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
	return view.countdown >= 0 && preStartStatus(view.status) && !view.gameOver && mode != engine.ModeGameOver
}

// preStartStatus reports whether a recorded game status is one the game has
// not started under — the pre-game window the countdown belongs to. Shared by
// the live screen (countdownVisible) and the replay, which reads the same
// statuses back off the replayed meta messages.
func preStartStatus(status string) bool {
	return status == "" ||
		status == string(config.GameStatusCreated) ||
		status == string(config.GameStatusStarting)
}

// countdownOverlay draws the big centered countdown number (or "GO!") with a
// pop-in scale + fade so each new number animates in (gold numbers, green GO!).
// n is the number (0 = GO!) and at the moment it arrived — the live screen
// takes both off the countdown consumer, the replay off the countdown messages
// as it replays them.
func (a *App) countdownOverlay(gtx C, n int, at time.Time) D {
	txt := fmt.Sprintf("%d", n)
	col := colGold
	if n == 0 {
		txt = "GO!"
		col = colGo
	}
	t := clampF(float64(gtx.Now.Sub(at))/float64(countdownAnimDur), 0, 1)
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
	headroom := eng.ShowHeadroom()
	n := max(len(view.players), 1)
	cell := fitCellPx(gtx, dims.Width, boardRows(dims, headroom), n, n*gtx.Dp(16), gtx.Dp(30), 8, 30)

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
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.boardLabel(p.Name, render.PlayerColorRGBA(i), oc.wins(p.PlayerID))),
							layout.Rigid(a.speakerMark(view.voice, p.PlayerID, unit.Dp(10))),
						)
					}),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						a.detectGarbageOn(gtx, i, snap) // landed garbage strobes on this board
						board := a.boardWidget(snap, i, cell, true, &boardFX{flash: view.specFlash[i], rows: view.specRowStrobes[i]}, gtx.Now, headroom)
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
	headroom := eng.ShowHeadroom()
	vis := boardRows(first, headroom)
	cell := fitCellPx(gtx, first.Width, vis*len(ids), 1, 0, len(ids)*gtx.Dp(34), 6, 13)
	var children []layout.FlexChild
	for _, id := range ids {
		snap := opps[id]
		label := id
		if eng.GameMode() == config.ModeTeams {
			// "team-3" → "TEAM D": with more than two teams the sidebar
			// stacks several opposing boards, so each needs its own name.
			label = "OPPOSING TEAM"
			if t, ok := engine.TeamFromBoardKey(id); ok {
				label = "TEAM " + eng.TeamName(t)
			}
		}
		children = append(children,
			layout.Rigid(a.body(label, colMuted)),
			layout.Rigid(spacer(2)),
			layout.Rigid(a.boardWidget(snap, -1, cell, false, nil, gtx.Now, headroom)),
			layout.Rigid(spacer(12)),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// spectatorTeamBoards renders every team's shared board side by side for a
// teams-mode spectator. The spectator engine consumes team 0 as its "own"
// board and each remaining team via an opponent consumer (see Engine.Start).
// Each board wears its team's color: the label, and a light tint on the empty
// squares and grid lines (boardFX.tint), so the wells tell apart at a glance.
// One team left standing is the decision itself (view.outcome): its board
// wears the winner show and every beaten one the OUT wash; all out at once is
// a draw — all washed, nobody crowned.
func (a *App) spectatorTeamBoards(gtx C, eng *engine.Engine, view gameView) D {
	// Reactive cells: the team boards side by side, scrolling below the minimum.
	dims := eng.Snapshot()
	headroom := eng.ShowHeadroom()
	teams := eng.TeamCount()
	cell := fitCellPx(gtx, dims.Width, boardRows(dims, headroom), teams, teams*gtx.Dp(16), gtx.Dp(26), 10, 40)
	opps := eng.OpponentSnapshots()
	oc := view.outcome

	type teamBoard struct {
		label string
		snap  engine.BoardSnapshot
		ok    bool
		team  int
	}
	boards := make([]teamBoard, 0, teams)
	for t := 0; t < teams; t++ {
		snap, ok := eng.Snapshot(), true
		if t != eng.TeamIdx() {
			snap, ok = opps[engine.TeamBoardKey(t)]
		}
		boards = append(boards, teamBoard{"TEAM " + eng.TeamName(t), snap, ok, t})
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
						board := a.boardWidget(b.snap, -1, cell, true, &boardFX{flash: view.specFlash[b.team], rows: view.specRowStrobes[b.team], tint: teamCol}, gtx.Now, headroom)
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

// teamScoreLine writes every team's live score and level as one line — "TEAM
// A 1200 (lvl 3) · TEAM B 940 (lvl 2)" — with the team named by `first` (the
// reader's own, where they have one) leading and the rest following in index
// order. Shared by the player's game-over box and the spectator's result box.
func teamScoreLine(view gameView, first int) string {
	order := make([]int, 0, len(view.teamScores))
	if first >= 0 && first < len(view.teamScores) {
		order = append(order, first)
	}
	for t := range view.teamScores {
		if t != first {
			order = append(order, t)
		}
	}
	parts := make([]string, 0, len(order))
	for _, t := range order {
		parts = append(parts, fmt.Sprintf("TEAM %s %d (lvl %d)", view.teamName(t), view.teamScore(t), view.teamLevel(t)))
	}
	return strings.Join(parts, " · ")
}

// gameOverBox is the panel shown beside the board once the local player is out
// (or the game is over): title, win/loss message, the final score, and the
// buttons — Pin, Share and Back to Lobby once the game is over for everyone
// (gameOverActions), Back alone while it plays on. It is laid out next to the playfield — never over it,
// so the final board stays fully visible. myTeam is the local player's team
// index (teams mode only).
func (a *App) gameOverBox(gtx C, eng *engine.Engine, gmode config.GameMode, view gameView) D {
	won := view.won
	myTeam := eng.TeamIdx()
	individual := eng.IndividualScoring()
	// Teams: a player can be out while their team plays on — show an interim
	// message (and no Back button pressure) until the game actually finishes.
	teamPlaysOn := gmode == config.ModeTeams && view.status == string(config.GameStatusInProgress) && !won
	title := "GAME OVER"
	if teamPlaysOn {
		title = "YOU'RE OUT"
	} else if eng.GoalReached() {
		title = "GOAL REACHED"
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
					case gmode == config.ModeCompetitive, individual:
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
					var scoreLine, ranking string
					switch {
					case gmode == config.ModeCooperative && individual:
						// A board scored per seat: our own score, then everyone
						// ranked — the game's verdict is the ranking.
						scoreLine = fmt.Sprintf("Your score: %d (level %d)", view.score, view.level)
						ranking = rankingLine(view.players, eng.PlayerScores(), eng.PlayerID(), view.score)
					case gmode == config.ModeCooperative:
						scoreLine = fmt.Sprintf("Score: %d (level %d)", view.score, view.level)
					case gmode == config.ModeCompetitive:
						scoreLine = fmt.Sprintf("Your score: %d (level %d)", view.score, view.level)
					case gmode == config.ModeTeams:
						// Our team first, then the rest in index order — with
						// six teams the line is long, so the one that matters
						// leads it.
						if myTeam >= 0 {
							scoreLine = teamScoreLine(view, myTeam)
						}
					}
					if scoreLine != "" {
						children = append(children, layout.Rigid(spacer(8)), layout.Rigid(func(gtx C) D {
							l := material.Body1(a.th, scoreLine)
							l.Color = colGold
							return l.Layout(gtx)
						}))
					}
					if ranking != "" {
						children = append(children, layout.Rigid(spacer(4)), layout.Rigid(func(gtx C) D {
							l := material.Body2(a.th, ranking)
							l.Color = colMuted
							return l.Layout(gtx)
						}))
					}
					children = append(children, layout.Rigid(spacer(14)), layout.Rigid(func(gtx C) D {
						return a.gameOverActions(gtx, view)
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

// holdWell is the player's hold slot beside the playfield, in the NEXT
// well's idiom (the same frame, the same cell size) so the two read as a
// pair across the playfield: the HOLD label over one tile slot — two cells
// tall, the tallest spawn tile — empty until the first hold, showing the
// set-aside piece afterwards and dimming it while the piece in play already
// came out of a hold (one hold per piece: the slot is locked until the next
// piece spawns from the queue). Tapping the box holds, as on the phone
// versions of the game (holdBoxBtn, dispatched by handlePadClicks).
func (a *App) holdWell(gtx C, held game.PieceType, has, used bool, boardCellPx int) D {
	return a.holdBoxBtn.Layout(gtx, func(gtx C) D {
		return a.holdWellBox(gtx, held, has, used, boardCellPx)
	})
}

// holdWellBox is the HOLD box itself, without its Clickable — what the
// layout plan measures (a Clickable laid out twice a frame would eat its
// taps).
func (a *App) holdWellBox(gtx C, held game.PieceType, has, used bool, boardCellPx int) D {
	cell := boardCellPx
	fw := max(cell/8, 2)
	gap := max(cell/3, 6)
	slotH := 2 * cell

	inner := func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.pixel(gtx.Metric.PxToSp(cell*11/20), "HOLD", colMuted).Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: gtx.Metric.PxToDp(gap * 3 / 4)}.Layout(gtx, func(gtx C) D {
					if has {
						// The tile's own height is the piece's rows (1 for
						// the I, 2 otherwise): center it in the fixed slot.
						h := game.Piece{Type: held}.Cells()
						rows := 1
						for _, rc := range h {
							rows = max(rows, rc[0]+1)
						}
						y := (slotH - rows*cell) / 2
						drawMiniPiece(gtx.Ops, 0, y, cell, held)
						if used {
							// Spent for this piece: wash the tile out.
							fillRect(gtx.Ops, image.Rect(0, 0, previewCols*cell, slotH), colorN{R: colPanel.R, G: colPanel.G, B: colPanel.B, A: 0xa0})
						}
					}
					return D{Size: image.Pt(previewCols*cell, slotH)}
				})
			}),
		)
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
