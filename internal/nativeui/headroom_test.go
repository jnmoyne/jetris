package nativeui

import (
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

// headroomBoard is a standard board (config.TotalRows tall, its headroom
// above config.VisibleRowStart) with a stack climbing into the headroom and
// a piece just spawned up there — the cells the smoked glass has to show
// through.
func headroomBoard() engine.BoardSnapshot {
	rows := make([]game.Row, config.TotalRows)
	for r := range rows {
		rows[r] = game.Row{Cells: make([]game.Cell, config.StandardWidth)}
	}
	lock := func(r, c int, pt game.PieceType, pi int) {
		rows[r].Cells[c] = game.Cell{Occupied: true, PieceType: pt, PlayerIdx: pi}
	}
	// The spawned piece: an I lying across the headroom's second row.
	for c := 3; c < 7; c++ {
		rows[1].Cells[c] = game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 0}
	}
	// A tower on the left up into the headroom, a low stack on the right.
	for r := 2; r < config.TotalRows; r++ {
		lock(r, 0, game.PieceJ, 0)
		if r%2 == 0 {
			lock(r, 1, game.PieceL, 0)
		}
	}
	for r := config.TotalRows - 4; r < config.TotalRows; r++ {
		for c := 4; c < config.StandardWidth; c++ {
			lock(r, c, game.PieceType((r+c)%7), 0)
		}
	}
	return engine.BoardSnapshot{Width: config.StandardWidth, Height: config.TotalRows, VisibleStart: config.VisibleRowStart, Rows: rows}
}

// TestBoardRowsHeadroom: with the hidden rows shown a board is its headroom
// taller — boardRows counts them and drawBoard paints them — and without
// them it is the visible region it always was.
func TestBoardRowsHeadroom(t *testing.T) {
	snap := headroomBoard()
	if got, want := boardRows(snap, false), config.VisibleRows; got != want {
		t.Errorf("boardRows hidden = %d, want %d", got, want)
	}
	if got, want := boardRows(snap, true), config.TotalRows; got != want {
		t.Errorf("boardRows shown = %d, want %d", got, want)
	}
	const cell = 24
	var ops op.Ops
	draw := func(headroom bool) layout.Dimensions {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
		}
		return drawBoard(gtx, snap, 0, cell, true, nil, time.Now(), headroom)
	}
	hidden, shown := draw(false), draw(true)
	if shown.Size.X != hidden.Size.X {
		t.Errorf("showing the headroom changed the board's width: %d → %d", hidden.Size.X, shown.Size.X)
	}
	if got, want := shown.Size.Y-hidden.Size.Y, config.HeadroomRows*cell; got != want {
		t.Errorf("the headroom adds %d px, want %d (%d rows of %d)", got, want, config.HeadroomRows, cell)
	}
}
