package nativeui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
)

func (a *App) layoutLobby(gtx C) D {
	lb := a.getLobby()
	if lb == nil {
		a.mu.Lock()
		a.screen = screenLogin
		a.mu.Unlock()
		return D{}
	}

	// --- event handling ---
	if a.quitBtn.Clicked(gtx) {
		go a.quit()
	}
	if a.createBtn.Clicked(gtx) {
		mode := config.ModeCooperative
		switch a.modeEnum.Value {
		case "competitive":
			mode = config.ModeCompetitive
		case "teams":
			mode = config.ModeTeams
		}
		count, err := strconv.Atoi(strings.TrimSpace(a.countEd.Text()))
		if mode == config.ModeTeams {
			// For teams the count editor means players PER TEAM.
			if err != nil || count < 1 {
				count = 1
			}
		} else if err != nil || count < 2 {
			count = 2
		}
		go a.createGame(mode, count)
	}
	a.handleChatSubmit(gtx)

	games := sortedGames(lb.Games())
	players := sortedPlayers(lb.Players())

	a.mu.Lock()
	chat := append([]lobby.ChatMessage(nil), a.chatLog...)
	a.mu.Unlock()

	// dispatch per-game buttons
	for _, g := range games {
		btns := a.gameButtons(g.GameID)
		if btns.join.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 0)
		}
		if btns.joinA.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 0)
		}
		if btns.joinB.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 1)
		}
		if btns.spectate.Clicked(gtx) {
			id := g.GameID
			go a.spectateGame(id)
		}
	}

	// --- render ---
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.lobbyBanner),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
						return a.lobbyLeft(gtx, players, chat)
					})
				}),
				layout.Flexed(2, func(gtx C) D {
					return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
						return a.lobbyRight(gtx, games, lb.Archives(), lb.PlayerName())
					})
				}),
			)
		}),
	)
}

func (a *App) lobbyLeft(gtx C, players []lobby.PlayerPresence, chat []lobby.ChatMessage) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("PLAYERS")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.playerList).Layout(gtx, len(players), func(gtx C, i int) D {
					p := players[i]
					return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{}.Layout(gtx,
							layout.Flexed(1, a.body(p.Name, colFg)),
							layout.Rigid(a.body(statusText(p.Status), colMuted)),
						)
					})
				})
			})
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.header("CHAT")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.chatList).Layout(gtx, len(chat), func(gtx C, i int) D {
					m := chat[i]
					txt := fmt.Sprintf("%s: %s", m.Name, m.Text)
					return layout.Inset{Top: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, a.body(txt, colFg))
				})
			})
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.chatEd, "Message…")
				}),
				layout.Rigid(spacer(6)),
				layout.Rigid(material.Button(a.th, &a.chatBtn, "Send").Layout),
			)
		}),
	)
}

func (a *App) lobbyRight(gtx C, games []lobby.GameListing, archives []config.ArchiveRecord, playerName string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					t := material.H6(a.th, "Lobby — "+playerName)
					t.Color = colFg
					return t.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return a.secondaryButton(gtx, &a.quitBtn, "Quit")
				}),
			)
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.createRow),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.header("GAMES")),
		layout.Flexed(2, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.gameList).Layout(gtx, len(games), func(gtx C, i int) D {
					return a.gameRow(gtx, games[i])
				})
			})
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.header("GAME HISTORY")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.archiveLst).Layout(gtx, len(archives), func(gtx C, i int) D {
					return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, a.body(archiveLine(archives[i]), colMuted))
				})
			})
		}),
	)
}

// archiveLine summarizes a finished game for the history list.
func archiveLine(r config.ArchiveRecord) string {
	if r.Mode == config.ModeTeams {
		// "teams · A 🏆 alice, bob · B carol, dave"
		parts := make([]string, 0, config.TeamCount)
		for t := 0; t < config.TeamCount; t++ {
			var members []string
			for _, p := range r.Players {
				if p.Team == t {
					members = append(members, p.PlayerID)
				}
			}
			tag := ""
			if r.WinningTeam == t {
				tag = " 🏆"
			}
			parts = append(parts, fmt.Sprintf("%s%s %s", teamName(t), tag, strings.Join(members, ", ")))
		}
		return fmt.Sprintf("teams · %s", strings.Join(parts, " · "))
	}
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].Score > players[j].Score })
	parts := make([]string, 0, len(players))
	for _, p := range players {
		tag := ""
		if p.Winner {
			tag = " 🏆"
		}
		parts = append(parts, fmt.Sprintf("%s %d%s", p.PlayerID, p.Score, tag))
	}
	if r.Mode == config.ModeCooperative {
		return fmt.Sprintf("co-op · total %d · %s", r.TotalScore, strings.Join(parts, ", "))
	}
	return fmt.Sprintf("competitive · %s", strings.Join(parts, ", "))
}

func (a *App) createRow(gtx C) D {
	countLabel := "Players:"
	if a.modeEnum.Value == "teams" {
		countLabel = "Per team:"
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			rb := material.RadioButton(a.th, &a.modeEnum, "cooperative", "Co-op")
			rb.Color = colFg
			return rb.Layout(gtx)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			rb := material.RadioButton(a.th, &a.modeEnum, "competitive", "Competitive")
			rb.Color = colFg
			return rb.Layout(gtx)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			rb := material.RadioButton(a.th, &a.modeEnum, "teams", "Teams")
			rb.Color = colFg
			return rb.Layout(gtx)
		}),
		layout.Rigid(spacer(12)),
		layout.Rigid(a.body(countLabel, colMuted)),
		layout.Rigid(spacer(4)),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Max.X = gtx.Dp(48)
			gtx.Constraints.Min.X = gtx.Dp(48)
			return a.editorBox(gtx, &a.countEd, "2")
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(material.Button(a.th, &a.createBtn, "Create Game").Layout),
	)
}

func (a *App) gameRow(gtx C, g lobby.GameListing) D {
	btns := a.gameButtons(g.GameID)
	joinable := g.Status == config.GameStatusCreated || g.Status == config.GameStatusStarting
	canJoin := joinable && len(g.Players) < g.PlayerCount
	canSpectate := g.Status == config.GameStatusInProgress ||
		(joinable && len(g.Players) >= g.PlayerCount)

	teams := g.Mode == config.ModeTeams
	var names []string
	if teams {
		// Group the roster by team: "A: alice, bob · B: carol"
		for t := 0; t < config.TeamCount; t++ {
			var team []string
			for _, p := range g.Players {
				if p.Team != t {
					continue
				}
				n := p.Name
				if p.Ready {
					n += " ✓"
				}
				team = append(team, n)
			}
			names = append(names, fmt.Sprintf("%s: %s", teamName(t), strings.Join(team, ", ")))
		}
	} else {
		for _, p := range g.Players {
			n := p.Name
			if p.Ready {
				n += " ✓"
			}
			names = append(names, n)
		}
	}
	info := fmt.Sprintf("%s · %s · %d/%d · %s", shortID(g.GameID), g.Mode.String(), len(g.Players), g.PlayerCount, g.Status)

	sep := ", "
	if teams {
		sep = " · "
	}
	return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(a.body(info, colFg)),
					layout.Rigid(a.body(strings.Join(names, sep), colMuted)),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if !canJoin {
					return D{}
				}
				if teams {
					// One join button per team, each enabled while that team has room.
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return a.teamJoinButton(gtx, &btns.joinA, g, 0)
						}),
						layout.Rigid(spacer(6)),
						layout.Rigid(func(gtx C) D {
							return a.teamJoinButton(gtx, &btns.joinB, g, 1)
						}),
					)
				}
				return material.Button(a.th, &btns.join, "Join").Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				if canSpectate {
					return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
						return a.secondaryButton(gtx, &btns.spectate, "Spectate")
					})
				}
				return D{}
			}),
		)
	})
}

// teamJoinButton renders the "Join A (1/2)"-style button for one team of a
// teams-mode listing, hidden once that team is full.
func (a *App) teamJoinButton(gtx C, btn *widget.Clickable, g lobby.GameListing, team int) D {
	n := g.TeamMemberCount(team)
	if n >= g.TeamSize {
		return D{}
	}
	label := fmt.Sprintf("Join %s (%d/%d)", teamName(team), n, g.TeamSize)
	return material.Button(a.th, btn, label).Layout(gtx)
}

// teamName renders a team index as its display letter.
func teamName(team int) string {
	if team == 0 {
		return "A"
	}
	return "B"
}

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

// secondaryButton renders a non-primary action (Spectate, Back to Lobby) so it
// reads as clearly clickable: accent-colored label and border over the panel
// background, visually distinct from the filled-accent primary buttons (Join,
// Ready) without blending into the near-black window background the way a bare
// colPanel fill did.
func (a *App) secondaryButton(gtx C, btn *widget.Clickable, label string) D {
	return widget.Border{Color: colAccent, Width: unit.Dp(1), CornerRadius: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		b := material.Button(a.th, btn, label)
		b.Background = colPanel
		b.Color = colAccent
		return b.Layout(gtx)
	})
}

func (a *App) header(txt string) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = colAccent
		return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, l.Layout)
	}
}

func (a *App) body(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = c
		return l.Layout(gtx)
	}
}

func bordered(gtx C, w layout.Widget) D {
	return widget.Border{Color: colPanel, Width: unit.Dp(1), CornerRadius: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
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

func shortID(id string) string {
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
