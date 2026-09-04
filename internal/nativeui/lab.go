package nativeui

import (
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/engine"
	"jetris/internal/game"
)

// The game HUD's LAB switch — a player's, while playing — picks between two
// ways of playing the round trip a move takes through the stream:
//
//   - Pessimistic sync ☹ (labSync): the game as it always was. A move's
//     batch is published and its commit ack awaited before the next queued
//     move goes out — the moves made during that round trip go out together
//     as one batch when it returns — and the board paints only what the
//     consumer has delivered (displayConsumer): the move shows when it comes
//     back from the stream, and not before.
//
//   - Optimistic async ☺ (labAsync, the default): engine.PublishOptimistic
//     with ONE batch in flight (labInflight) — the next move goes out right
//     after the last one's commit, its expectations on the cells still in
//     flight the sequences the previous move is PREDICTED to get, and the
//     moves made while a batch is out go out together behind it — and the
//     piece is drawn where the player is steering it AT ONCE, its ghost
//     under it, the white outline marking where the acks have it
//     (displayAck). A lost race or a wrong guess flashes the outline the
//     piece snaps back onto, and the moves behind it are replayed. See
//     engine/pipeline.go.
//
// The engine can do more than the switch offers — deeper pipelines
// (SetInflightLimit), the plain async mode, the outline-only display
// (displayOutline) — and the display positions below are the model the
// board is drawn from; the switch just fixes each setting's combination.
//
// In a SOLO game (a one-seat co-op crew) the switch keeps its meaning but
// loses its guard: the engine is the only writer, so it plays on its local
// board at once and journals every write with no expectation — sync lets one
// batch out at a time, async every batch as it is made, without the limit
// (engine/solo.go). Position 3 then paints the local board, its grey
// outline where the acks have the piece, a round trip behind.
type displayMode int

const (
	// displayConsumer paints only what the consumer has delivered
	// (Engine.EchoSnapshot): the move shows when it comes back from the
	// stream, and not before. The game as it always was.
	displayConsumer displayMode = iota + 1
	// displayOutline paints the consumer's board too, with the move
	// pre-rendered over it as a white outline of where the piece is headed
	// (Engine.IntentPiece) the moment it is made; when the echo lands there
	// the outline is gone, and if the move is lost the outline flashes. Not
	// on the switch.
	displayOutline
	// displayAck draws the piece where the player is steering it AT ONCE —
	// colored, its hard-drop ghost under it — over the acked board
	// (Engine.Snapshot, the engine's write-through), and the white outline
	// marks where the ACKS have the piece so far (Engine.AckedPiece): it
	// moves only as the commits ack, a batch at a time. A lost step snaps the
	// colored piece back onto the outline, which flashes; the moves queued
	// behind it play on from there once the repair has landed
	// (optimisticBoard). A hard drop on its way paints the piece as already
	// dropped, locked where it will land (Engine.PendingDrop) — reverted the
	// same way if a step ahead of it is lost.
	displayAck
)

// The switch's two settings (labEnum's values) and the async setting's
// pipeline depth.
const (
	labSync     = "sync"
	labAsync    = "async"
	labInflight = 1
)

// labAsync reports whether the switch is on Optimistic async.
func (a *App) labAsync() bool { return a.labEnum.Value != labSync }

// displayMode is what the board paints under the switch's setting.
func (a *App) displayMode() displayMode {
	if a.labAsync() {
		return displayAck
	}
	return displayConsumer
}

// publishModeOf is the engine mode the switch stands for.
func publishModeOf(async bool) engine.PublishMode {
	if async {
		return engine.PublishOptimistic
	}
	return engine.PublishSync
}

// Blocky 8-bit faces for the switch's two rows: the sad one for pessimistic
// sync, the happy one for optimistic async.
var (
	glyphSad = []string{
		"..XXXX..",
		".X....X.",
		"X.X..X.X",
		"X......X",
		"X..XX..X",
		"X.X..X.X",
		".X....X.",
		"..XXXX..",
	}
	glyphHappy = []string{
		"..XXXX..",
		".X....X.",
		"X.X..X.X",
		"X......X",
		"X.X..X.X",
		"X..XX..X",
		".X....X.",
		"..XXXX..",
	}
)

// labToggles is the HUD section holding the switch: two radio rows under a
// MOVE PUBLISHING header, each with its face. Compact — the HUD column is
// narrow and the controls legend wants the room under it.
func (a *App) labToggles(gtx C) D {
	row := func(value, label string, face []string, col colorN) layout.Widget {
		return func(gtx C) D {
			rb := material.RadioButton(a.th, &a.labEnum, value, label)
			rb.Color = colFg
			rb.IconColor = colAccent
			rb.TextSize = unit.Sp(12)
			rb.Size = unit.Dp(18)
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(rb.Layout),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(glyphWidget(face, unit.Dp(16), col)),
			)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("MOVE PUBLISHING")),
		layout.Rigid(row(labSync, "Pessimistic sync", glyphSad, colErr)),
		layout.Rigid(row(labAsync, "Optimistic async", glyphHappy, colGo)),
	)
}

// intentCells is the pre-rendered move to draw over snap: the cells of the
// piece where the player is steering it (Engine.IntentPiece), unless the
// drawn board already has the piece exactly there — then there is nothing
// to announce and no outline. Nil when there is no piece to steer.
func intentCells(eng *engine.Engine, snap engine.BoardSnapshot, localIdx int) map[[2]int]bool {
	p, ok := eng.IntentPiece()
	if !ok {
		return nil
	}
	drawn := &game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows}
	if cur := drawn.ActivePieceForPlayer(localIdx); cur != nil && *cur == p {
		return nil
	}
	out := make(map[[2]int]bool, 4)
	for _, rc := range p.Cells() {
		if rc[0] >= 0 && rc[0] < snap.Height && rc[1] >= 0 && rc[1] < snap.Width {
			out[[2]int{rc[0], rc[1]}] = true
		}
	}
	return out
}

// optimisticBoard is position 3's board: snap (the acked replica, its rows
// the frame's own clone) with the player's piece moved to where they are
// steering it (Engine.IntentPiece) — every active cell of theirs cleared
// first, a repair's strays included — plus the two outlines: WHITE on the
// piece as drawn, the one the player is steering (steered), and GREY where
// the acks have it (acked, Engine.AckedPiece) when that is somewhere else —
// trailing behind, never competing for the eye. Untouched, no outlines,
// when there is no piece to steer.
func optimisticBoard(eng *engine.Engine, snap engine.BoardSnapshot, localIdx int) (out engine.BoardSnapshot, steered, acked map[[2]int]bool) {
	intent, ok := eng.IntentPiece()
	if !ok {
		return snap, nil, nil
	}
	ackedPiece, ok := eng.AckedPiece()
	if !ok {
		return snap, nil, nil
	}
	pf := &game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows}
	if dest, ok := eng.PendingDrop(); ok {
		// A hard drop on its way: show the piece as DROPPED, locked where
		// it is going to land — the commit paints the same cells, and a
		// lost step ahead of it reverts the whole thing (PendingDrop is off
		// while the repair runs, the piece back on the acked outline).
		pf.ClearActiveCellsForPlayer(localIdx)
		for _, rc := range dest.Cells() {
			if rc[0] >= 0 && rc[0] < pf.Height && rc[1] >= 0 && rc[1] < pf.Width {
				pf.Rows[rc[0]].Cells[rc[1]] = game.Cell{Occupied: true, PieceType: dest.Type, PlayerIdx: localIdx}
			}
		}
		return snap, pieceOutline(dest, snap), pieceOutline(ackedPiece, snap)
	}
	pf.SetActivePieceForPlayer(intent, localIdx)
	if ackedPiece == intent {
		return snap, pieceOutline(intent, snap), nil
	}
	return snap, pieceOutline(intent, snap), pieceOutline(ackedPiece, snap)
}

// pieceOutline is the outline of p's cells, within the board.
func pieceOutline(p game.Piece, snap engine.BoardSnapshot) map[[2]int]bool {
	outline := make(map[[2]int]bool, 4)
	for _, rc := range p.Cells() {
		if rc[0] >= 0 && rc[0] < snap.Height && rc[1] >= 0 && rc[1] < snap.Width {
			outline[[2]int{rc[0], rc[1]}] = true
		}
	}
	return outline
}

// Keep widget referenced for the switch's enum type.
var _ widget.Enum
