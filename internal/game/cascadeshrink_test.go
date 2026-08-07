package game

import "testing"

// cascadeAnchorFor returns the anchor row of playerIdx's active piece in rows,
// or -1 if the piece is absent.
func cascadeAnchorFor(rows []Row, playerIdx int) int {
	for _, r := range rows {
		for _, c := range r.Cells {
			if c.Active && c.PlayerIdx == playerIdx {
				return c.AnchorRow
			}
		}
	}
	return -1
}

// cascadeActiveCount returns the number of active cells owned by playerIdx.
func cascadeActiveCount(rows []Row, playerIdx int) int {
	n := 0
	for _, r := range rows {
		for _, c := range r.Cells {
			if c.Active && c.PlayerIdx == playerIdx {
				n++
			}
		}
	}
	return n
}

func TestProjectShrinkCascade(t *testing.T) {
	const causer = 7

	// Small deterministic board: 10 wide, 12 rows (bottom row = 11).
	newBoard := func() *Playfield { return NewPlayfieldWithHeight(10, 12) }

	t.Run("stays put when no conflict", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 4, Col: 4}, 0)
		pf.Rows[10].Cells[0] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean shrink, got topped=%v full=%v", topped, full)
		}
		if got := cascadeAnchorFor(out, 0); got != 4 {
			t.Errorf("piece should hold its row: got anchor %d, want 4", got)
		}
		if !out[9].Cells[0].Occupied || out[9].Cells[0].Adversarial {
			t.Error("locked stack should have shifted up by 1 (row 10 -> 9)")
		}
		for c := 0; c < pf.Width; c++ {
			g := out[11].Cells[c]
			if !g.Adversarial || !g.Occupied || g.PlayerIdx != causer {
				t.Errorf("bottom row cell %d should be causer-tagged garbage, got %+v", c, g)
			}
		}
	})

	t.Run("pushed up minimally on conflict", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 4, Col: 4}, 0)
		pf.Rows[6].Cells[4] = Cell{Occupied: true, PieceType: PieceT}
		pf.Rows[6].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean shrink, got topped=%v full=%v", topped, full)
		}
		if got := cascadeAnchorFor(out, 0); got != 3 {
			t.Errorf("piece should be pushed up by exactly 1: got anchor %d, want 3", got)
		}
	})

	t.Run("multi-row rise lifts by exact amount", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 4, Col: 4}, 0)
		pf.Rows[6].Cells[4] = Cell{Occupied: true, PieceType: PieceT}
		pf.Rows[6].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(2, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean shrink, got topped=%v full=%v", topped, full)
		}
		if got := cascadeAnchorFor(out, 0); got != 2 {
			t.Errorf("piece should be pushed up by exactly 2: got anchor %d, want 2", got)
		}
	})

	t.Run("tops out when pushed off the top", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 0, Col: 4}, 0)
		pf.Rows[2].Cells[4] = Cell{Occupied: true, PieceType: PieceT}
		pf.Rows[2].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if full {
			t.Fatal("no locked cell was pushed past the top; boardFull should be false")
		}
		if len(topped) != 1 || topped[0] != 0 {
			t.Fatalf("expected topped=[0], got %v", topped)
		}
		if got := cascadeAnchorFor(out, 0); got != -1 {
			t.Errorf("doomed piece should not be stamped: found active anchor %d", got)
		}
	})

	t.Run("chained cascade lifts piece above", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 8, Col: 4}, 0) // rows 8-9
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 6, Col: 4}, 1) // rows 6-7, directly above
		pf.Rows[10].Cells[4] = Cell{Occupied: true, PieceType: PieceT}     // floor under p0
		pf.Rows[10].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean cascade, got topped=%v full=%v", topped, full)
		}
		if got := cascadeAnchorFor(out, 0); got != 7 {
			t.Errorf("lower piece should lift by 1: got anchor %d, want 7", got)
		}
		if got := cascadeAnchorFor(out, 1); got != 5 {
			t.Errorf("upper piece should be cascaded up by 1: got anchor %d, want 5", got)
		}
		if n := cascadeActiveCount(out, 1); n != 4 {
			t.Errorf("upper piece should stay intact: %d active cells, want 4", n)
		}
	})

	t.Run("cascade pushes upper piece off the top", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 2, Col: 4}, 0) // rows 2-3
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 0, Col: 4}, 1) // rows 0-1, at the ceiling
		pf.Rows[4].Cells[4] = Cell{Occupied: true, PieceType: PieceT}      // floor under p0
		pf.Rows[4].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if full {
			t.Fatal("no locked cell was pushed past the top; boardFull should be false")
		}
		if len(topped) != 1 || topped[0] != 1 {
			t.Fatalf("expected only the ceiling piece topped: got %v", topped)
		}
		if got := cascadeAnchorFor(out, 0); got != 1 {
			t.Errorf("lower piece should lift by 1 and survive: got anchor %d, want 1", got)
		}
		if got := cascadeAnchorFor(out, 1); got != -1 {
			t.Errorf("topped piece should not be stamped: found anchor %d", got)
		}
	})

	t.Run("piece in a well is lifted out, never merged", func(t *testing.T) {
		// The old shared-board "crush" scenario: a piece deep in a well while
		// the stack rises through it must come out on top of the garbage with
		// all four cells intact, and the garbage rows must stay solid.
		pf := newBoard()
		for r := 8; r <= 11; r++ {
			for c := 0; c < pf.Width; c++ {
				if c != 4 && c != 5 {
					pf.Rows[r].Cells[c] = Cell{Occupied: true, PieceType: PieceL}
				}
			}
		}
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 10, Col: 4}, 0) // in the well, rows 10-11

		out, topped, full := pf.ProjectShrinkCascade(2, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean shrink, got topped=%v full=%v", topped, full)
		}
		if got := cascadeAnchorFor(out, 0); got != 8 {
			t.Errorf("piece should ride to the top of the shifted well: got anchor %d, want 8", got)
		}
		if n := cascadeActiveCount(out, 0); n != 4 {
			t.Errorf("piece should stay intact: %d active cells, want 4", n)
		}
		for r := 10; r <= 11; r++ {
			for c := 0; c < pf.Width; c++ {
				g := out[r].Cells[c]
				if !g.Adversarial || !g.Occupied || g.Active {
					t.Errorf("garbage row %d col %d must be solid adversarial, got %+v", r, c, g)
				}
			}
		}
	})

	t.Run("no pieces: pure shift plus garbage", func(t *testing.T) {
		pf := newBoard()
		pf.Rows[10].Cells[3] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected clean shrink, got topped=%v full=%v", topped, full)
		}
		if !out[9].Cells[3].Occupied {
			t.Error("locked cell should shift from row 10 to row 9")
		}
		for c := 0; c < pf.Width; c++ {
			if !out[11].Cells[c].Adversarial {
				t.Errorf("bottom row cell %d should be garbage", c)
			}
		}
	})

	t.Run("locked cells pushed past the top set boardFull", func(t *testing.T) {
		pf := newBoard()
		pf.Rows[0].Cells[2] = Cell{Occupied: true, PieceType: PieceT}

		out, topped, full := pf.ProjectShrinkCascade(1, causer)

		if !full {
			t.Fatal("locked cell in row 0 pushed off the board: boardFull should be true")
		}
		if len(topped) != 0 {
			t.Fatalf("no falling piece involved: topped should be empty, got %v", topped)
		}
		for c := 0; c < pf.Width; c++ {
			if !out[11].Cells[c].Adversarial {
				t.Errorf("garbage should still be inserted, cell %d not adversarial", c)
			}
		}
	})

	t.Run("zero rows is a no-op", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceT, Row: 5, Col: 3}, 0)
		pf.Rows[11].Cells[0] = Cell{Occupied: true, PieceType: PieceL}

		out, topped, full := pf.ProjectShrinkCascade(0, causer)

		if len(topped) != 0 || full {
			t.Fatalf("expected no-op, got topped=%v full=%v", topped, full)
		}
		for r := range pf.Rows {
			if !pf.Rows[r].Equal(out[r]) {
				t.Fatalf("row %d changed on a zero-row shrink", r)
			}
		}
	})
}
