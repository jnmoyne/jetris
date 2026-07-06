package nativeui

import (
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
				return layout.Center.Layout(gtx, func(gtx C) D {
					return a.archiveBoards(gtx, rec.Boards)
				})
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

	var children []layout.FlexChild
	for _, p := range boards {
		p := p
		snap := boardSnapshotFromPicture(p)
		children = append(children, layout.Rigid(func(gtx C) D {
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
		}))
	}
	return layout.Flex{Alignment: layout.Start}.Layout(gtx, children...)
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
