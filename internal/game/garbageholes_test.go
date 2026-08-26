package game

import (
	"sort"
	"testing"

	"jetris/internal/config"
)

func garbageRow(width, causer int, holes ...int) Row {
	r := NewRow(width)
	hole := map[int]bool{}
	for _, c := range holes {
		hole[c] = true
	}
	for c := range r.Cells {
		if !hole[c] {
			r.Cells[c] = Cell{Occupied: true, PieceType: PieceO, Adversarial: true, PlayerIdx: causer}
		}
	}
	return r
}

// TestRowIsFullGarbageRule pins the garbage-row completion rule: a solid
// garbage row never completes, a garbage row raised with holes completes
// once a player's LOCKED cells fill every hole.
func TestRowIsFullGarbageRule(t *testing.T) {
	const w = 10
	if garbageRow(w, 1).IsFull() {
		t.Error("a solid garbage row must never complete")
	}
	holed := garbageRow(w, 1, 4)
	if holed.IsFull() {
		t.Error("a garbage row with an open hole is not complete")
	}
	holed.Cells[4] = Cell{Active: true, PieceType: PieceI, PlayerIdx: 0}
	if holed.IsFull() {
		t.Error("a falling piece in the hole does not complete the row")
	}
	holed.Cells[4] = Cell{Occupied: true, PieceType: PieceI, PlayerIdx: 0}
	if !holed.IsFull() {
		t.Error("a garbage row whose hole a player filled completes like any line")
	}
	two := garbageRow(w, 1, 2, 7)
	two.Cells[2] = Cell{Occupied: true, PieceType: PieceT}
	if two.IsFull() {
		t.Error("one of two holes filled is not complete")
	}
	two.Cells[7] = Cell{Occupied: true, PieceType: PieceT}
	if !two.IsFull() {
		t.Error("both holes filled completes the row")
	}
	plain := NewRow(w)
	for c := range plain.Cells {
		plain.Cells[c] = Cell{Occupied: true, PieceType: PieceT}
	}
	if !plain.IsFull() {
		t.Error("an ordinary full row completes")
	}
	if (Row{}).IsFull() || NewRow(w).IsFull() {
		t.Error("an empty row is not full")
	}
}

// TestCompletedRowsGarbageHoles: only the garbage rows whose holes are all
// filled are reported, alongside ordinary full rows; solid garbage stays.
func TestCompletedRowsGarbageHoles(t *testing.T) {
	pf := NewPlayfieldWithHeight(10, 6)
	pf.Rows[5] = garbageRow(10, 1, 3, 4, 5, 6) // holes filled below → completes
	for c := 3; c <= 6; c++ {
		pf.Rows[5].Cells[c] = Cell{Occupied: true, PieceType: PieceI}
	}
	pf.Rows[4] = garbageRow(10, 1) // solid → permanent
	pf.Rows[3] = garbageRow(10, 1, 0, 9)
	pf.Rows[3].Cells[0] = Cell{Occupied: true, PieceType: PieceL} // one hole still open
	for c := range pf.Rows[2].Cells {
		pf.Rows[2].Cells[c] = Cell{Occupied: true, PieceType: PieceT} // ordinary full row
	}
	got := CompletedRows(pf)
	if len(got) != 2 || got[0] != 2 || got[1] != 5 {
		t.Fatalf("CompletedRows = %v, want [2 5]", got)
	}
}

// TestRandomGarbageHoles: the draw is sorted, distinct, in range, and clamped
// so a garbage row always keeps adversarial cells.
func TestRandomGarbageHoles(t *testing.T) {
	if got := RandomGarbageHoles(10, 0); got != nil {
		t.Errorf("0 holes: got %v, want nil (solid rows)", got)
	}
	if got := RandomGarbageHoles(10, -3); got != nil {
		t.Errorf("negative holes: got %v, want nil", got)
	}
	for i := 0; i < 100; i++ {
		cols := RandomGarbageHoles(10, 3)
		if len(cols) != 3 || !sort.IntsAreSorted(cols) {
			t.Fatalf("draw %d: got %v, want 3 sorted columns", i, cols)
		}
		for j, c := range cols {
			if c < 0 || c >= 10 || (j > 0 && cols[j-1] == c) {
				t.Fatalf("draw %d: bad columns %v", i, cols)
			}
		}
	}
	if got := RandomGarbageHoles(10, 9); len(got) != config.MaxGarbageHoles {
		t.Errorf("holes above the cap: got %d columns, want %d", len(got), config.MaxGarbageHoles)
	}
	if got := RandomGarbageHoles(3, 4); len(got) != 2 {
		t.Errorf("holes on a 3-wide board: got %d columns, want 2 (never a whole row)", len(got))
	}
}

// TestRaiseHoles: by default every row of a raise shares one draw; random
// gives each row its own; solid games get nil.
func TestRaiseHoles(t *testing.T) {
	if got := RaiseHoles(10, 0, 3, true); got != nil {
		t.Errorf("0 holes: got %v, want nil", got)
	}
	if got := RaiseHoles(10, 2, 0, false); got != nil {
		t.Errorf("0 rows: got %v, want nil", got)
	}
	for i := 0; i < 50; i++ {
		sets := RaiseHoles(10, 2, 4, false)
		if len(sets) != 4 {
			t.Fatalf("clean raise: %d sets, want 4", len(sets))
		}
		for k := 1; k < len(sets); k++ {
			if len(sets[k]) != 2 || sets[k][0] != sets[0][0] || sets[k][1] != sets[0][1] {
				t.Fatalf("clean raise: rows should share one draw, got %v", sets)
			}
		}
	}
	differed := false
	for i := 0; i < 40 && !differed; i++ {
		sets := RaiseHoles(10, 1, 2, true)
		if len(sets) != 2 || len(sets[0]) != 1 || len(sets[1]) != 1 {
			t.Fatalf("random raise: got %v, want two 1-hole rows", sets)
		}
		differed = sets[0][0] != sets[1][0]
	}
	if !differed {
		t.Error("random raise: 40 two-row raises never drew different columns")
	}
}
