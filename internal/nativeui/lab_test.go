package nativeui

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

// TestLabSwitch: a new App is on Optimistic async — one batch in flight,
// the optimistic pipeline, the piece-at-once display — and Pessimistic sync
// is the classic game: sync publishing, the consumer's board. Both reach
// the engine every frame.
func TestLabSwitch(t *testing.T) {
	a := newTestApp()
	if !a.labAsync() || a.displayMode() != displayAck || a.dispMode != int(displayAck) {
		t.Fatal("a new App must start on Optimistic async, piece at once")
	}
	if publishModeOf(true) != engine.PublishOptimistic || publishModeOf(false) != engine.PublishSync {
		t.Fatal("publishModeOf: async must be optimistic, sync sync")
	}
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	renderOnce(t, a)
	if got := a.eng.PublishMode(); got != engine.PublishOptimistic {
		t.Fatalf("async: the engine publishes %v, want optimistic", got)
	}
	if got := a.eng.InflightLimit(); got != labInflight {
		t.Fatalf("async: %d in flight, want %d", got, labInflight)
	}
	a.labEnum.Value = labSync
	renderOnce(t, a)
	if a.labAsync() || a.displayMode() != displayConsumer || a.dispMode != int(displayConsumer) {
		t.Fatal("Pessimistic sync must paint the consumer's board")
	}
	if got := a.eng.PublishMode(); got != engine.PublishSync {
		t.Fatalf("sync: the engine publishes %v, want sync", got)
	}
}

// TestIntentCells: the outline is the intent piece's cells, and nothing when
// the drawn board already has the piece there.
func TestIntentCells(t *testing.T) {
	eng := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	p := game.Piece{Type: game.PieceT, Row: 3, Col: 4}
	pf := game.NewPlayfield(config.StandardWidth)
	pf.SetActivePieceForPlayer(p, 0)
	snap := engine.BoardSnapshot{Width: pf.Width, Height: pf.Height, VisibleStart: config.VisibleRowStart, Rows: game.CloneRows(pf.Rows)}

	// No piece on the engine's board: nothing to steer.
	if got := intentCells(eng, snap, 0); got != nil {
		t.Fatalf("intent with no engine piece = %v, want none", got)
	}
	eng.SeedActivePiece(p)
	// Piece where the board draws it: no outline.
	if got := intentCells(eng, snap, 0); got != nil {
		t.Fatalf("intent coinciding with the drawn piece = %v, want none", got)
	}
	// A queued move: the outline is the piece one column left.
	eng.MoveLeft()
	left := p
	left.Col--
	got := intentCells(eng, snap, 0)
	if len(got) != 4 {
		t.Fatalf("intent = %v, want 4 cells", got)
	}
	for _, rc := range left.Cells() {
		if !got[[2]int{rc[0], rc[1]}] {
			t.Fatalf("intent %v misses %v", got, rc)
		}
	}
}

// TestGameScreenLabModes: the game screen lays out under both settings of
// the switch, for a player with a piece and queued moves.
func TestGameScreenLabModes(t *testing.T) {
	for _, v := range []string{labSync, labAsync} {
		a := newTestApp()
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.eng.SeedActivePiece(game.Piece{Type: game.PieceL, Row: 2, Col: 3})
		a.eng.MoveLeft()
		a.eng.MoveDown()
		a.labEnum.Value = v
		renderOnce(t, a)
		if a.dispMode != int(a.displayMode()) {
			t.Fatalf("%s: dispMode mirrored as %d", v, a.dispMode)
		}
		if got, want := a.eng.PublishMode(), publishModeOf(v == labAsync); got != want {
			t.Fatalf("%s: engine publish mode %v, want %v", v, got, want)
		}
	}
}

// TestOptimisticBoard: position 3's board carries the player's piece where
// they are steering it and outlines where the acks have it — nothing when
// the two coincide, and the piece's cells move, not multiply.
func TestOptimisticBoard(t *testing.T) {
	eng := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	p := game.Piece{Type: game.PieceT, Row: 3, Col: 4}
	eng.SeedActivePiece(p)
	count := func(snap engine.BoardSnapshot, cells [][2]int) int {
		n := 0
		for _, rc := range cells {
			if c := snap.Rows[rc[0]].Cells[rc[1]]; c.Active && c.PlayerIdx == 0 {
				n++
			}
		}
		return n
	}

	// Nothing queued: the board as acked, the white outline on the piece,
	// no grey one (the acks have it right there).
	snap, steered, outline := optimisticBoard(eng, eng.Snapshot(), 0)
	if outline != nil || len(steered) != 4 || count(snap, p.Cells()) != 4 {
		t.Fatalf("idle: grey outline %v, white outline %v, piece cells %d; want no grey, white on the piece where it is", outline, steered, count(snap, p.Cells()))
	}

	// A queued left: the colored piece one column left, the outline where
	// the acks still have it — and only four active cells of ours.
	eng.MoveLeft()
	left := p
	left.Col--
	snap, steered, outline = optimisticBoard(eng, eng.Snapshot(), 0)
	if count(snap, left.Cells()) != 4 {
		t.Fatalf("the piece was not drawn at the intent %+v", left)
	}
	for _, rc := range left.Cells() {
		if !steered[[2]int{rc[0], rc[1]}] {
			t.Fatalf("white outline %v misses the steered cell %v", steered, rc)
		}
	}
	if len(outline) != 4 {
		t.Fatalf("grey outline = %v, want the acked piece's 4 cells", outline)
	}
	for _, rc := range p.Cells() {
		if !outline[[2]int{rc[0], rc[1]}] {
			t.Fatalf("outline %v misses the acked cell %v", outline, rc)
		}
	}
	total := 0
	for _, row := range snap.Rows {
		for _, c := range row.Cells {
			if c.Active && c.PlayerIdx == 0 {
				total++
			}
		}
	}
	if total != 4 {
		t.Fatalf("%d active cells of ours on the drawn board, want 4 (moved, not duplicated)", total)
	}
}

// TestOptimisticBoardPendingDrop: with a hard drop queued, position 3 draws
// the piece as already dropped — locked where it will land, no active cells
// of ours anywhere — with the outline where the acks still have it.
func TestOptimisticBoardPendingDrop(t *testing.T) {
	eng := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	p := game.Piece{Type: game.PieceT, Row: 3, Col: 4}
	eng.SeedActivePiece(p)
	eng.MoveLeft()
	eng.HardDrop()
	dest, ok := eng.PendingDrop()
	if !ok {
		t.Fatal("no pending drop")
	}
	snap, steered, outline := optimisticBoard(eng, eng.Snapshot(), 0)
	for _, rc := range dest.Cells() {
		if c := snap.Rows[rc[0]].Cells[rc[1]]; !c.Occupied || c.Active || c.PieceType != p.Type {
			t.Fatalf("landing cell %v = %+v, want a locked cell of the piece", rc, c)
		}
		if !steered[[2]int{rc[0], rc[1]}] {
			t.Fatalf("white outline %v misses the landing cell %v", steered, rc)
		}
	}
	for _, row := range snap.Rows {
		for _, c := range row.Cells {
			if c.Active && c.PlayerIdx == 0 {
				t.Fatal("an active cell of ours left on the board while the piece is shown dropped")
			}
		}
	}
	if len(outline) != 4 {
		t.Fatalf("outline = %v, want the acked piece's 4 cells", outline)
	}
}

// TestLabGlyphs: the faces are rectangular bitmaps of '.' and 'X'.
func TestLabGlyphs(t *testing.T) {
	for name, bm := range map[string][]string{"sad": glyphSad, "happy": glyphHappy} {
		for _, row := range bm {
			if len(row) != len(bm[0]) {
				t.Fatalf("%s: ragged bitmap", name)
			}
			for _, ch := range row {
				if ch != '.' && ch != 'X' {
					t.Fatalf("%s: bad glyph character %q", name, ch)
				}
			}
		}
	}
}
