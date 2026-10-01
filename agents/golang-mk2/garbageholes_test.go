package main

import "testing"

// TestCompletedRowsGarbageHoles pins the garbage-row rule on both boards the
// agent keeps — the planner's grid and the live settled map: a solid garbage
// row never completes, a garbage row whose holes the stack filled clears
// like any other, and a row a foreign falling piece crosses never does.
func TestCompletedRowsGarbageHoles(t *testing.T) {
	gr := newGrid(4, 5)
	for c := 0; c < 5; c++ {
		gr.set(3, c, 2) // solid garbage: permanent
		gr.set(2, c, 2) // garbage whose hole (col 1) the stack filled
		gr.set(1, c, 1) // plain full row
		gr.set(0, c, 1) // full but crossed by a foreign falling piece
	}
	gr.set(2, 1, 1)
	gr.set(0, 4, 3)
	if got := gr.completedRows(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("grid completedRows = %v, want [1 2]", got)
	}

	g := &Game{w: 5, mode: modeCompetitive, locked: map[cell]wireCell{}}
	h := g.height()
	gp := garbagePayload(1)
	for c := 0; c < 5; c++ {
		g.locked[cell{h - 1, c}] = gp
		g.locked[cell{h - 2, c}] = gp
		g.locked[cell{h - 3, c}] = gp
	}
	g.locked[cell{h - 2, 3}] = wireCell{O: true, T: 0} // hole filled by the stack
	delete(g.locked, cell{h - 3, 0})                   // hole still open
	if got := g.completedRows(); len(got) != 1 || got[0] != h-2 {
		t.Fatalf("Game completedRows = %v, want [%d]", got, h-2)
	}
}

// TestGarbageHoleColumns: the raise's hole draw honours the game's count,
// stays in range, and never empties a whole row.
func TestGarbageHoleColumns(t *testing.T) {
	g := &Game{w: 10, holes: 3}
	for i := 0; i < 100; i++ {
		holes := g.garbageHoleColumns()
		if len(holes) != 3 {
			t.Fatalf("draw %d: %d holes, want 3", i, len(holes))
		}
		for c := range holes {
			if c < 0 || c >= g.w {
				t.Fatalf("draw %d: column %d out of range", i, c)
			}
		}
	}
	g.holes = 0
	if holes := g.garbageHoleColumns(); len(holes) != 0 {
		t.Errorf("0 holes: got %v", holes)
	}
	narrow := &Game{w: 3, holes: 4}
	if holes := narrow.garbageHoleColumns(); len(holes) != 2 {
		t.Errorf("3-wide board with 4 holes: got %d, want 2 (a row keeps garbage)", len(holes))
	}
}

// TestRandomHolesDrawPerRow: a random-holes game draws each garbage row's
// columns independently; by default the rows of one raise share one draw.
func TestRandomHolesDrawPerRow(t *testing.T) {
	raise := func(g *Game, n int) [][]int {
		var rows [][]int
		var holes map[int]bool
		for r := 0; r < n; r++ {
			if holes == nil || g.randomHoles {
				holes = g.garbageHoleColumns()
			}
			var cols []int
			for c := 0; c < g.w; c++ {
				if holes[c] {
					cols = append(cols, c)
				}
			}
			rows = append(rows, cols)
		}
		return rows
	}
	shared := &Game{w: 10, holes: 1}
	for i := 0; i < 30; i++ {
		rows := raise(shared, 3)
		if rows[0][0] != rows[1][0] || rows[1][0] != rows[2][0] {
			t.Fatalf("clean raise should share one draw, got %v", rows)
		}
	}
	random := &Game{w: 10, holes: 1, randomHoles: true}
	differed := false
	for i := 0; i < 40 && !differed; i++ {
		rows := raise(random, 2)
		differed = rows[0][0] != rows[1][0]
	}
	if !differed {
		t.Error("random raise: 40 two-row raises never drew different columns")
	}
}

// TestAttackRows: one row per line by default; the Guideline table
// (0/1/2/4) in a guideline_garbage game.
func TestAttackRows(t *testing.T) {
	plain, guideline := &Game{}, &Game{guideline: true}
	want := map[int]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4}
	for lines := 0; lines <= 4; lines++ {
		if got := plain.attackRows(lines); got != lines {
			t.Errorf("plain attackRows(%d) = %d, want %d", lines, got, lines)
		}
		if got := guideline.attackRows(lines); got != want[lines] {
			t.Errorf("guideline attackRows(%d) = %d, want %d", lines, got, want[lines])
		}
	}
}
