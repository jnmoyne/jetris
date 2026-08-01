package nativeui

import (
	"fmt"
	"sort"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/game"
	"jetricks/internal/render"
)

// openArchive switches to the archive viewer for a finished game, showing the
// end-of-game playfield snapshot captured in its history record.
func (a *App) openArchive(rec config.ArchiveRecord) {
	a.mu.Lock()
	r := rec // copy so the viewer is unaffected by later slice growth
	a.archiveSel = &r
	a.screen = screenArchive
	a.mu.Unlock()
	a.invalidate()
}

// closeArchive returns from the archive viewer to the lobby.
func (a *App) closeArchive() {
	a.mu.Lock()
	a.archiveSel = nil
	a.screen = screenLobby
	a.mu.Unlock()
	a.invalidate()
}

func (a *App) layoutArchive(gtx C) D {
	a.mu.Lock()
	rec := a.archiveSel
	a.mu.Unlock()
	if rec == nil {
		a.closeArchive()
		return D{}
	}
	if a.archiveBackBtn.Clicked(gtx) {
		a.closeArchive()
		return D{}
	}

	return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.lobbyBanner),
			layout.Rigid(spacer(8)),
			layout.Rigid(a.header("FINAL PLAYFIELD")),
			layout.Rigid(spacer(4)),
			layout.Rigid(a.body(archiveLine(*rec), colMuted)),
			layout.Rigid(spacer(18)),
			layout.Flexed(1, func(gtx C) D {
				boards := func(gtx C) D {
					return layout.Center.Layout(gtx, func(gtx C) D {
						return a.archiveBoards(gtx, rec.Boards)
					})
				}
				// The player roster sits to the LEFT of the boards (names in
				// their board colors, winners highlighted); the game's preserved
				// chat to the right — boards center-stage. The chat panel is
				// always there, saying so when the record has no conversation
				// (an empty game, or one archived before chat was preserved),
				// rather than silently vanishing.
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
							return a.archiveRoster(gtx, *rec)
						})
					}),
					layout.Flexed(1, boards),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Max.X = gtx.Dp(320)
						gtx.Constraints.Min.X = gtx.Dp(320)
						return a.archiveChatPanel(gtx, rec.Chat)
					}),
				}
				return layout.Flex{}.Layout(gtx, children...)
			}),
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D {
				return a.secondaryButton(gtx, &a.archiveBackBtn, "Back to Lobby")
			}),
		)
	})
}

// archiveBoards lays the stored end-of-game boards side by side, each under its
// label (player ID, team name, or nothing for the single cooperative board).
func (a *App) archiveBoards(gtx C, boards []config.BoardPicture) D {
	if len(boards) == 0 {
		return a.body("No playfield snapshot was saved for this game.", colMuted)(gtx)
	}
	// One wide cooperative board gets a larger cell; the many narrow
	// competitive/team boards get a smaller one so several fit across.
	cellDp := 16
	if len(boards) == 1 {
		cellDp = 22
	}
	cell := gtx.Dp(unit.Dp(float32(cellDp)))

	var items []layout.Widget
	for _, p := range boards {
		p := p
		snap := boardSnapshotFromPicture(p)
		items = append(items, func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						if p.Label == "" {
							return D{}
						}
						l := material.Body2(a.th, p.Label)
						l.Color = colMuted
						if p.Idx >= 0 {
							l.Color = render.PlayerColorRGBA(p.Idx)
						}
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(4)),
					layout.Rigid(a.boardWidget(snap, p.Idx, cell, true, nil, gtx.Now)),
				)
			})
		})
	}
	return a.scrollableBoards(gtx, &a.archiveBoardsList, items)
}

// archiveRoster is the player legend shown to the left of the final playfield:
// each player's name in its board color, winners marked with a trophy and
// their name in gold. Competitive players are colored by the same
// sorted-by-PlayerID index the boards use (see archive.buildBoardPictures);
// teams players are grouped under their color-matched TEAM A / TEAM B header,
// the winning team's header in gold; cooperative players share one board, so
// they list plainly (no per-player color, no winner) under a PLAYERS header.
func (a *App) archiveRoster(gtx C, rec config.ArchiveRecord) D {
	gtx.Constraints.Min.X = gtx.Dp(190)
	gtx.Constraints.Max.X = gtx.Dp(190)
	var children []layout.FlexChild
	switch rec.Mode {
	case config.ModeTeams:
		children = a.rosterTeams(rec)
	case config.ModeCooperative:
		children = a.rosterCoop(rec)
	default:
		children = a.rosterCompetitive(rec)
	}
	return bordered(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// archivePlayerRow is one legend line: a color swatch, the player's name (agent
// marker included), and — for a winner — a leading trophy and a gold name.
func (a *App) archivePlayerRow(name string, col colorN, winner bool) layout.FlexChild {
	return layout.Rigid(func(gtx C) D {
		textCol := colFg
		if winner {
			textCol = colGold
			name = "🏆 " + name
		}
		return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D { return swatch(gtx, col, 12) })
				}),
				layout.Rigid(hSpacer(8)),
				layout.Flexed(1, a.body(name, textCol)),
			)
		})
	})
}

// rosterCompetitive lists every player under a single PLAYERS header, colored
// by the board index (sorted PlayerID order), survivors flagged as winners.
func (a *App) rosterCompetitive(rec config.ArchiveRecord) []layout.FlexChild {
	players := append([]config.PlayerResult(nil), rec.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].PlayerID < players[j].PlayerID })
	children := []layout.FlexChild{layout.Rigid(a.header("PLAYERS"))}
	for i, p := range players {
		children = append(children, a.archivePlayerRow(agentName(p.PlayerID, p.Agent), render.PlayerColorRGBA(i), p.Winner))
	}
	return children
}

// rosterCoop lists the cooperative players plainly — one shared board means no
// per-player color and no winner.
func (a *App) rosterCoop(rec config.ArchiveRecord) []layout.FlexChild {
	players := append([]config.PlayerResult(nil), rec.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].PlayerID < players[j].PlayerID })
	children := []layout.FlexChild{layout.Rigid(a.header("PLAYERS"))}
	for _, p := range players {
		children = append(children, a.archivePlayerRow(agentName(p.PlayerID, p.Agent), colMuted, false))
	}
	return children
}

// rosterTeams groups players under their color-matched TEAM A / TEAM B header;
// the winning team's header and members are highlighted in gold.
func (a *App) rosterTeams(rec config.ArchiveRecord) []layout.FlexChild {
	var children []layout.FlexChild
	for t := 0; t < config.TeamCount; t++ {
		t := t
		teamCol := render.PlayerColorRGBA(t)
		won := rec.WinningTeam == t
		hdrCol := teamCol
		if won {
			hdrCol = colGold
		}
		if t > 0 {
			children = append(children, layout.Rigid(spacer(10)))
		}
		children = append(children, layout.Rigid(func(gtx C) D {
			return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, a.pixel(unit.Sp(9), "TEAM "+teamName(t), hdrCol).Layout)
		}))
		var members []config.PlayerResult
		for _, p := range rec.Players {
			if p.Team == t {
				members = append(members, p)
			}
		}
		sort.Slice(members, func(i, j int) bool { return members[i].PlayerID < members[j].PlayerID })
		for _, p := range members {
			children = append(children, a.archivePlayerRow(agentName(p.PlayerID, p.Agent), teamCol, won))
		}
	}
	return children
}

// archiveChatPanel renders the record's preserved chat history — the game's
// conversation as it stood when the game was archived (the live chat was
// purged from the chat stream at archive time; the record is its only home).
func (a *App) archiveChatPanel(gtx C, chat []config.ChatLine) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("GAME CHAT")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				if len(chat) == 0 {
					gtx.Constraints.Min = gtx.Constraints.Max
					return layout.Center.Layout(gtx, a.body("No chat was recorded for this game.", colMuted))
				}
				return material.List(a.th, &a.archiveChatList).Layout(gtx, len(chat), func(gtx C, i int) D {
					return layout.Inset{Top: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx,
						a.body(archiveChatLine(chat[i]), colFg))
				})
			})
		}),
	)
}

// archiveChatLine formats one preserved message: local wall-clock time, the
// sender (spectators marked like the live panel), and the text.
func archiveChatLine(m config.ChatLine) string {
	name := m.Name
	if m.Spectator {
		name += " (spec)"
	}
	if m.Timestamp.IsZero() {
		return fmt.Sprintf("%s: %s", name, m.Text)
	}
	return fmt.Sprintf("%s %s: %s", m.Timestamp.Local().Format("15:04"), name, m.Text)
}

// boardSnapshotFromPicture rebuilds a renderable BoardSnapshot from a stored
// BoardPicture. The picture holds only the visible region (rows renumbered from
// 0) and only its non-empty cells, so VisibleStart is 0 and absent cells stay
// empty.
func boardSnapshotFromPicture(p config.BoardPicture) engine.BoardSnapshot {
	rows := make([]game.Row, p.Height)
	for r := range rows {
		rows[r] = game.Row{Cells: make([]game.Cell, p.Width)}
	}
	for _, bc := range p.Cells {
		if bc.Row < 0 || bc.Row >= p.Height || bc.Col < 0 || bc.Col >= p.Width {
			continue
		}
		cell, err := game.UnmarshalCell(bc.Data)
		if err != nil {
			continue
		}
		rows[bc.Row].Cells[bc.Col] = cell
	}
	return engine.BoardSnapshot{Width: p.Width, Height: p.Height, VisibleStart: 0, Rows: rows}
}
