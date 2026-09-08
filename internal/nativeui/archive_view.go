package nativeui

import (
	"fmt"
	"sort"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/render"
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

const (
	// archiveRosterW and archiveChatW are the viewer's two fixed columns —
	// the player roster and the preserved conversation — and
	// archiveBoardsMinW the board area worth standing between them.
	archiveRosterW    = 190
	archiveChatW      = 320
	archiveBoardsMinW = 260
)

// archiveColumnsFit reports whether the roster, the boards and the chat panel
// can stand side by side in the width gtx measures. Where they cannot the
// three stack, because a Flex squeezes its FLEXED child first and that child
// is the boards — the one thing this screen exists to show.
func archiveColumnsFit(gtx C) bool {
	return gtx.Constraints.Max.X-gtx.Dp(archiveRosterW)-gtx.Dp(archiveChatW) >= gtx.Dp(archiveBoardsMinW)
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
			layout.Rigid(a.brandBanner("")),
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
				if !archiveColumnsFit(gtx) {
					// No room for the three of them side by side — a
					// phone's whole screen is less than the roster and the
					// chat panel together — and a Flex squeezes its FLEXED
					// child first, so the boards, which are the whole point of
					// this screen, came out nothing wide. They stack instead,
					// playfields first, the column scrolling as one.
					return material.List(a.th, &a.archiveColLst).Layout(gtx, 1, func(gtx C, _ int) D {
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(boards),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D { return a.archiveRoster(gtx, *rec) }),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D {
								// Its own height here, rather than the slack
								// of a Flexed child: the column it is in
								// scrolls, so there is no slack to take.
								gtx.Constraints.Max.Y = gtx.Dp(220)
								gtx.Constraints.Min.Y = gtx.Dp(220)
								return a.archiveChatPanel(gtx, rec.Chat)
							}),
						)
					})
				}
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
							return a.archiveRoster(gtx, *rec)
						})
					}),
					layout.Flexed(1, boards),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Max.X = gtx.Dp(archiveChatW)
						gtx.Constraints.Min.X = gtx.Dp(archiveChatW)
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
	entries := make([]labeledBoard, len(boards))
	for i, p := range boards {
		entries[i] = labeledBoard{label: p.Label, idx: p.Idx, snap: boardSnapshotFromPicture(p)}
	}
	return a.boardsStrip(gtx, &a.archiveBoardsList, entries)
}

// boardStripLabelDp is the height boardsStrip keeps over every board for its
// label (a Body2 line) and the spacer under it, so the fitted cell leaves the
// labels their room instead of pushing the boards past the bottom.
const boardStripLabelDp = unit.Dp(26)

// labeledBoard pairs a renderable board snapshot with its strip label (player
// ID, team name, or "" for a single shared board) and coloring index. The
// optional decoration is the replay ending's: a label color override and
// bold-italic emphasis (the revealed winners), and a wrap around the board
// widget itself (the winner show, or the beaten boards' OUT wash), handed
// the strip's cell size so its art scales with the board.
type labeledBoard struct {
	label    string
	idx      int
	snap     engine.BoardSnapshot
	labelCol colorN // label color when A != 0 (else the idx color)
	emph     bool   // bold italic label
	headroom bool   // draw the snapshot's headroom rows too, behind smoked glass (a replay of a game that showed its hidden rows)
	wrap     func(board layout.Widget, cellPx int) layout.Widget
}

// boardsStrip lays labeled boards side by side — the shared body of the
// archive viewer's final playfield and the replay screen. The cell is fitted
// to the boards in hand (fitCellPx): one wide board gets a larger cell,
// several narrow ones a smaller one so they fit across, and a board TALLER
// than today's — a game archived before every board became config.VisibleRows
// tall replays at the height it was played on — shrinks its cell to stand
// whole in the room the screen gives it, rather than running off the bottom
// at a fixed size. Boards too wide even so fall back to horizontal scrolling
// (scrollableBoards).
func (a *App) boardsStrip(gtx C, list *widget.List, boards []labeledBoard) D {
	maxDp := unit.Dp(16)
	if len(boards) == 1 {
		maxDp = 22
	}
	// The widest and the tallest of them: the strip gives every board the one
	// cell, so it has to be the cell they all fit at.
	cols, rows, labeled := 0, 0, false
	for _, b := range boards {
		cols = max(cols, b.snap.Width)
		rows = max(rows, boardRows(b.snap, b.headroom))
		labeled = labeled || b.label != ""
	}
	// Reserved: each board's right inset, and over it the label line with the
	// spacer under it (boardStripLabelDp) when the boards carry labels.
	reservedY := 0
	if labeled {
		reservedY = gtx.Dp(boardStripLabelDp)
	}
	cell := fitCellPx(gtx, cols, rows, len(boards), len(boards)*gtx.Dp(16), reservedY, 6, maxDp)

	var items []layout.Widget
	for _, b := range boards {
		b := b
		items = append(items, func(gtx C) D {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						if b.label == "" {
							return D{}
						}
						l := material.Body2(a.th, b.label)
						l.Color = colMuted
						if b.idx >= 0 {
							l.Color = render.PlayerColorRGBA(b.idx)
						}
						if b.labelCol.A != 0 {
							l.Color = b.labelCol
						}
						if b.emph {
							l.Font.Weight, l.Font.Style = font.Bold, font.Italic
						}
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(4)),
					layout.Rigid(func(gtx C) D {
						board := a.boardWidget(b.snap, b.idx, cell, true, nil, gtx.Now, b.headroom)
						if b.wrap != nil {
							board = b.wrap(board, cell)
						}
						return board(gtx)
					}),
				)
			})
		})
	}
	return a.scrollableBoards(gtx, list, items)
}

// archiveRoster is the player legend shown to the left of the final playfield:
// each player's name in its board color, winners marked with a trophy and
// their name in gold. Competitive players are colored by the same
// sorted-by-PlayerID index the boards use (see archive.buildBoardPictures);
// teams players are grouped under their color-matched TEAM A / TEAM B / …
// header,
// the winning team's header in gold; cooperative players share one board, so
// they list plainly (no per-player color, no winner) under a PLAYERS header.
func (a *App) archiveRoster(gtx C, rec config.ArchiveRecord) D {
	gtx.Constraints.Min.X = gtx.Dp(archiveRosterW)
	gtx.Constraints.Max.X = gtx.Dp(archiveRosterW)
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
			name = winnerMark + name
		}
		return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D { return swatch(gtx, col, 12) })
				}),
				layout.Rigid(hSpacer(8)),
				layout.Flexed(1, a.markedBody(name, textCol, false)),
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

// rosterTeams groups players under their color-matched TEAM A / TEAM B / …
// header; the winning team's header and members are highlighted in gold.
func (a *App) rosterTeams(rec config.ArchiveRecord) []layout.FlexChild {
	var children []layout.FlexChild
	for t := 0; t < rec.Teams(); t++ {
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
			return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, a.pixel(unit.Sp(9), "TEAM "+rec.TeamName(t), hdrCol).Layout)
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
