package nativeui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	abandoned := lb.AbandonedGames()

	// The lobby screen shows only lobby-scoped messages; per-game messages
	// (GameID != "") appear on that game's screen instead.
	a.mu.Lock()
	chat := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if m.GameID == "" {
			chat = append(chat, m)
		}
	}
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
						return a.lobbyRight(gtx, games, abandoned, sortedArchives(lb.Archives()), lb.PlayerName())
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
				layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.chatBtn, "Send") }),
			)
		}),
	)
}

func (a *App) lobbyRight(gtx C, games []lobby.GameListing, abandoned map[string]bool, archives []config.ArchiveRecord, playerName string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return a.pixel(unit.Sp(13), "LOBBY — "+playerName, colFg).Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return a.secondaryButton(gtx, &a.quitBtn, "Quit")
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			// While hosting the embedded server, show the address other
			// players should dial so the host can share it.
			addr := a.embeddedAddr()
			if addr == "" {
				return D{}
			}
			return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(a.pixel(unit.Sp(9), "YOUR SERVER  ", colMuted).Layout),
					layout.Rigid(a.pixel(unit.Sp(10), "nats://"+addr, colNATSGreen).Layout),
					layout.Rigid(a.body("  — share this address so others can join you", colMuted)),
				)
			})
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.createRow),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.header("GAMES")),
		layout.Flexed(2, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.gameList).Layout(gtx, len(games), func(gtx C, i int) D {
					return a.gameRow(gtx, games[i], abandoned[games[i].GameID])
				})
			})
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.header("GAME HISTORY")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.archiveLst).Layout(gtx, len(archives), func(gtx C, i int) D {
					for len(a.archiveBtns) <= i {
						a.archiveBtns = append(a.archiveBtns, widget.Clickable{})
					}
					btn := &a.archiveBtns[i]
					if btn.Clicked(gtx) {
						a.openArchive(archives[i])
					}
					rec := archives[i]
					return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, a.body(archiveLine(rec), colMuted)),
							layout.Rigid(spacer(8)),
							layout.Rigid(func(gtx C) D {
								return a.viewBoardButton(gtx, btn)
							}),
						)
					})
				})
			})
		}),
	)
}

// sortedArchives orders the history list by headline score (highest first);
// between two games with the same score the shorter game ranks higher, and
// remaining ties show the most recently finished game first.
func sortedArchives(recs []config.ArchiveRecord) []config.ArchiveRecord {
	sort.SliceStable(recs, func(i, j int) bool {
		si, sj := archiveScore(recs[i]), archiveScore(recs[j])
		if si != sj {
			return si > sj
		}
		di, dj := archiveDuration(recs[i]), archiveDuration(recs[j])
		if di != dj {
			return di < dj
		}
		return recs[i].FinishedAt.After(recs[j].FinishedAt)
	})
	return recs
}

// archiveScore is the headline score a finished game is ranked by: the shared
// total for cooperative, the winning-side total for teams, and the best
// player's score for competitive.
func archiveScore(r config.ArchiveRecord) int {
	switch r.Mode {
	case config.ModeCooperative:
		return r.TotalScore
	case config.ModeTeams:
		if len(r.TeamScores) > 0 {
			best := r.TeamScores[0]
			for _, s := range r.TeamScores[1:] {
				if s > best {
					best = s
				}
			}
			return best
		}
	}
	best := 0
	for _, p := range r.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

// archiveDuration is how long the game lasted; zero for records missing either
// timestamp (or with a clock skew that made finish precede start).
func archiveDuration(r config.ArchiveRecord) time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	if d := r.FinishedAt.Sub(r.StartedAt); d > 0 {
		return d
	}
	return 0
}

// archiveWhen renders a record's start date/time (in the viewer's local
// timezone) and duration, e.g. "2026-07-06 14:03 PDT · 4m32s".
func archiveWhen(r config.ArchiveRecord) string {
	if r.StartedAt.IsZero() {
		return ""
	}
	s := r.StartedAt.Local().Format("2006-01-02 15:04 MST")
	if d := archiveDuration(r); d > 0 {
		s += " · " + d.Round(time.Second).String()
	}
	return s
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
		// "teams · A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave"
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
			stats := ""
			if t < len(r.TeamScores) {
				// Older records predate per-team totals — skip stats for those.
				stats = fmt.Sprintf(" %d", r.TeamScores[t])
				if t < len(r.TeamLevels) {
					stats += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
				}
			}
			parts = append(parts, fmt.Sprintf("%s%s%s %s", teamName(t), tag, stats, strings.Join(members, ", ")))
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
		return fmt.Sprintf("co-op · total %d (lvl %d) · %s", r.TotalScore, r.FinalLevel, strings.Join(parts, ", "))
	}
	// Competitive: append each player's achieved level to their score.
	parts = parts[:0]
	for _, p := range players {
		tag := ""
		if p.Winner {
			tag = " 🏆"
		}
		parts = append(parts, fmt.Sprintf("%s %d (lvl %d)%s", p.PlayerID, p.Score, p.Level, tag))
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
		layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.createBtn, "Create Game") }),
	)
}

func (a *App) gameRow(gtx C, g lobby.GameListing, abandoned bool) D {
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
	confirming := abandoned && a.confirmDeleteID == g.GameID
	infoCol := func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(a.body(info, colFg)),
					layout.Rigid(func(gtx C) D {
						if !abandoned {
							return D{}
						}
						return a.body(" · abandoned", colErr)(gtx)
					}),
				)
			}),
			layout.Rigid(a.body(strings.Join(names, sep), colMuted)),
		)
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
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, infoCol),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
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
						return a.primaryButton(gtx, &btns.join, "Join")
					}),
					layout.Rigid(func(gtx C) D {
						if canSpectate {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
								return a.secondaryButton(gtx, &btns.spectate, "Spectate")
							})
						}
						return D{}
					}),
					layout.Rigid(func(gtx C) D {
						if !abandoned {
							return D{}
						}
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
							return a.dangerButton(gtx, &btns.del, "Delete")
						})
					}),
				)
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
	return a.primaryButton(gtx, btn, label)
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
