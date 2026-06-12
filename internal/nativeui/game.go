package nativeui

import (
	"fmt"
	"image"
	"sort"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
	"jetricks/internal/render"
)

// gameView is the per-frame snapshot of game scalars, taken under a.mu so the
// layout never reads fields the pump goroutine is writing.
type gameView struct {
	score, level         int
	ping                 time.Duration
	status               string
	countdown            int
	countdownAt          time.Time
	gameOver, won        bool
	myReady              bool
	players, readyPlayer []lobby.PlayerSummary
	flash                map[[2]int]time.Time
	flashActive          bool
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
	return gameView{
		score:       a.score,
		level:       a.level,
		ping:        a.ping,
		status:      a.gameStatus,
		countdown:   a.countdown,
		countdownAt: a.countdownAt,
		gameOver:    a.gameOver,
		won:         a.won,
		myReady:     a.myReady,
		players:     append([]lobby.PlayerSummary(nil), a.gamePlayers...),
		readyPlayer: append([]lobby.PlayerSummary(nil), a.readyPlayers...),
		flash:       fc,
		flashActive: len(fc) > 0,
	}
}

func (a *App) layoutGame(gtx C) D {
	eng := a.getEngine()
	if eng == nil {
		return D{}
	}
	mode := eng.Mode()
	gmode := eng.GameMode()

	// Register the whole window as a key-input target, then dispatch moves.
	st := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.boardTag)
	st.Pop()
	if mode == engine.ModePlayer {
		a.handleKeys(gtx, eng)
	}

	if a.readyBtn.Clicked(gtx) {
		go a.toggleReady()
	}
	if a.backBtn.Clicked(gtx) {
		go a.returnToLobby()
	}

	view := a.snapshotGame(gtx.Now)
	if view.flashActive {
		a.invalidate() // keep animating the flash until it expires
	}
	if countdownVisible(view, mode) && gtx.Now.Sub(view.countdownAt) < countdownAnimDur {
		a.invalidate() // keep animating the countdown pop until it settles
	}

	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Max.X = gtx.Dp(240)
			gtx.Constraints.Min.X = gtx.Dp(240)
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
			l := material.Body1(a.th, modeLabel)
			l.Color = colAccent
			return l.Layout(gtx)
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D { return a.legend(gtx, eng, view, gmode) }),
		layout.Rigid(spacer(14)),
		layout.Rigid(a.hudStat("SCORE", view.score)),
		layout.Rigid(a.hudStat("LEVEL", view.level)),
	}

	if mode == engine.ModePlayer {
		children = append(children, layout.Rigid(a.hudStatText("PING", formatPing(view.ping))))
	}

	if mode == engine.ModePlayer && !started && !view.gameOver {
		children = append(children,
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D { return a.readyArea(gtx, view) }),
		)
	}

	children = append(children,
		layout.Rigid(spacer(18)),
		layout.Rigid(func(gtx C) D {
			b := material.Button(a.th, &a.backBtn, "Back to Lobby")
			b.Background = colPanel
			return b.Layout(gtx)
		}),
	)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (a *App) legend(gtx C, eng *engine.Engine, view gameView, gmode config.GameMode) D {
	var children []layout.FlexChild

	playerRow := func(i int, p lobby.PlayerSummary) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			elim := (gmode == config.ModeCompetitive || gmode == config.ModeTeams) && eng.IsEliminated(p.PlayerID)
			name := p.Name
			textCol := colFg
			if elim {
				name += " (out)"
				textCol = colMuted
			}
			return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return swatch(gtx, render.PlayerColorRGBA(i), 12) }),
					layout.Rigid(spacer(6)),
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
	label := "READY"
	if view.myReady {
		label = "NOT READY"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(material.Button(a.th, &a.readyBtn, label).Layout),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			var rows []layout.FlexChild
			for _, p := range view.readyPlayer {
				mark := "…"
				if p.Ready {
					mark = "✓"
				}
				rows = append(rows, layout.Rigid(a.body(mark+" "+p.Name, colMuted)))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		}),
	)
}

func (a *App) gameBoardArea(gtx C, eng *engine.Engine, view gameView, mode engine.Mode, gmode config.GameMode) D {
	if mode == engine.ModeSpectator && gmode == config.ModeCompetitive {
		return a.spectatorBoards(gtx, eng, view)
	}
	if mode == engine.ModeSpectator && gmode == config.ModeTeams {
		return a.spectatorTeamBoards(gtx, eng)
	}

	snap := eng.Snapshot()
	localIdx := eng.PlayerIdx()
	if mode == engine.ModeSpectator {
		localIdx = -1
	}
	cell := gtx.Dp(unit.Dp(22))
	board := func(gtx C) D {
		return layout.Center.Layout(gtx, a.boardWidget(snap, localIdx, cell, true, view.flash, gtx.Now))
	}
	switch {
	case view.gameOver:
		return layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Expanded(board),
			layout.Stacked(func(gtx C) D { return a.gameOverBox(gtx, gmode, view) }),
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

// countdownVisible reports whether the centered pre-game countdown number should
// be drawn over the player's board.
func countdownVisible(view gameView, mode engine.Mode) bool {
	started := view.status == string(config.GameStatusInProgress)
	return view.countdown >= 0 && !started && !view.gameOver && mode == engine.ModePlayer
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

	l := material.Label(a.th, unit.Sp(float32(countdownBaseSp*scale)), txt)
	l.Color = withAlpha(col, alpha)
	l.Font.Weight = font.Bold
	return l.Layout(gtx)
}

func (a *App) spectatorBoards(gtx C, eng *engine.Engine, view gameView) D {
	opps := eng.OpponentSnapshots()
	cell := gtx.Dp(unit.Dp(16))
	var children []layout.FlexChild
	for i, p := range view.players {
		i, p := i, p
		snap, ok := opps[p.PlayerID]
		children = append(children, layout.Rigid(func(gtx C) D {
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
						return a.boardWidget(snap, i, cell, true, nil, gtx.Now)(gtx)
					}),
				)
			})
		}))
	}
	return layout.Flex{}.Layout(gtx, children...)
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
	cell := gtx.Dp(unit.Dp(10))
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
// board and team 1 via the opponent consumer (see Engine.Start).
func (a *App) spectatorTeamBoards(gtx C, eng *engine.Engine) D {
	cell := gtx.Dp(unit.Dp(14))
	teamB, okB := eng.OpponentSnapshots()[engine.TeamBoardKey(1)]
	boards := []struct {
		label string
		snap  engine.BoardSnapshot
		ok    bool
	}{
		{"TEAM A", eng.Snapshot(), true},
		{"TEAM B", teamB, okB},
	}
	var children []layout.FlexChild
	for _, b := range boards {
		b := b
		children = append(children, layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body(b.label, colMuted)),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						if !b.ok {
							return a.body("Loading…", colMuted)(gtx)
						}
						return a.boardWidget(b.snap, -1, cell, true, nil, gtx.Now)(gtx)
					}),
				)
			})
		}))
	}
	return layout.Flex{}.Layout(gtx, children...)
}

func (a *App) gameOverBox(gtx C, gmode config.GameMode, view gameView) D {
	won := view.won
	// Teams: a player can be out while their team plays on — show an interim
	// message (and no Back button pressure) until the game actually finishes.
	teamPlaysOn := gmode == config.ModeTeams && view.status == string(config.GameStatusInProgress) && !won
	title := "GAME OVER"
	if teamPlaysOn {
		title = "YOU'RE OUT"
	}
	return widget.Border{Color: colAccent, Width: unit.Dp(2), CornerRadius: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return background(gtx, colBg, func(gtx C) D {
			return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx C) D {
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						l := material.H5(a.th, title)
						l.Color = colFg
						return l.Layout(gtx)
					}),
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
					children = append(children, layout.Rigid(spacer(6)), layout.Rigid(func(gtx C) D {
						l := material.H6(a.th, msg)
						l.Color = c
						return l.Layout(gtx)
					}))
				}
				children = append(children, layout.Rigid(spacer(14)), layout.Rigid(
					material.Button(a.th, &a.backBtn, "Back to Lobby").Layout,
				))
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, children...)
			})
		})
	})
}

func (a *App) hudStat(label string, val int) layout.Widget {
	return a.hudStatText(label, fmt.Sprintf("%d", val))
}

func (a *App) hudStatText(label, val string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					l := material.Body2(a.th, label+"  ")
					l.Color = colMuted
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					l := material.H6(a.th, val)
					l.Color = colFg
					return l.Layout(gtx)
				}),
			)
		})
	}
}

// formatPing renders the publish→echo round trip for the HUD: sub-10ms with a
// decimal, whole milliseconds above, an em dash before the first measurement.
func formatPing(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	ms := float64(d) / float64(time.Millisecond)
	if ms < 10 {
		return fmt.Sprintf("%.1f ms", ms)
	}
	return fmt.Sprintf("%.0f ms", ms)
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
