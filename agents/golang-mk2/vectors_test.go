package main

import (
	"encoding/json"
	mrand "math/rand/v2"
	"os"
	"testing"
)

// TestWriteVectors writes the planner's answers on seeded random boards as
// JSON, for a port of this agent to check itself against
// (agents/python-mk2/tests/data/planner_vectors.json). It runs only when
// MK2_VECTORS_OUT names the file to write:
//
//	MK2_VECTORS_OUT=../python-mk2/tests/data/planner_vectors.json go test -run TestWriteVectors .
func TestWriteVectors(t *testing.T) {
	out := os.Getenv("MK2_VECTORS_OUT")
	if out == "" {
		t.Skip("MK2_VECTORS_OUT not set")
	}
	type rankedOut struct {
		Orient  int     `json:"orient"`
		Col     int     `json:"col"`
		DropRow int     `json:"drop_row"`
		Lines   int     `json:"lines"`
		Score   float64 `json:"score"`
		WaitOn  []int   `json:"wait_on"`
	}
	type projOut struct {
		Claimed   [][3]int       `json:"claimed"` // row, col, seat
		Soft      [][3]int       `json:"soft"`
		DepETA    map[string]int `json:"dep_eta"`
		OwnSpawnC int            `json:"own_spawn_c"`
		Boxes     [][4]int       `json:"boxes"` // seat, c0, c1, imminent
	}
	type planOut struct {
		H        int         `json:"h"`
		W        int         `json:"w"`
		Cells    [][3]int    `json:"cells"` // row, col, kind
		Pt       int         `json:"pt"`
		Row      int         `json:"row"`
		Col      int         `json:"col"`
		SpawnC   int         `json:"spawn_c"`
		Upcoming []int       `json:"upcoming"`
		Proj     *projOut    `json:"proj"`
		Features [4]int      `json:"features"` // row transitions, col transitions, holes, wells
		Ranked   []rankedOut `json:"ranked"`
	}
	rnd := mrand.New(mrand.NewPCG(20261111, 2))
	board := func(w int) *grid {
		g := newGrid(24, w)
		garbage := 0
		if rnd.IntN(4) == 0 {
			garbage = 1 + rnd.IntN(3)
		}
		for c := 0; c < w; c++ {
			top := 23 - garbage - rnd.IntN(9)
			for r := top + 1; r <= 23-garbage; r++ {
				if rnd.IntN(7) != 0 {
					g.set(r, c, 1)
				}
			}
		}
		hole := rnd.IntN(w)
		for r := 24 - garbage; r < 24; r++ {
			for c := 0; c < w; c++ {
				if c != hole {
					g.set(r, c, 2)
				}
			}
		}
		return g
	}
	var plans []planOut
	for i := 0; i < 160; i++ {
		w := []int{10, 14, 18}[rnd.IntN(3)]
		g := board(w)
		spawnC := (w - 4) / 2
		if rnd.IntN(3) == 0 {
			spawnC = rnd.IntN(w - 4)
		}
		var proj *projection
		var po *projOut
		if i%4 == 3 {
			// A coordinated board: a foreign falling piece, a teammate's
			// claimed landing, a silent teammate's straight drop, a spawn box.
			proj = &projection{claimed: map[cell]int{}, soft: map[cell]int{}, depETA: map[int]int{}, pieces: map[int]int{}, ownSpawnC: spawnC}
			po = &projOut{DepETA: map[string]int{}, OwnSpawnC: spawnC}
			cpt, cor, ccol := rnd.IntN(7), rnd.IntN(4), rnd.IntN(w-3)
			if g.canPlace(pieceCells(cpt, cor, spawnRow, ccol)) {
				for _, x := range pieceCells(cpt, cor, g.dropRow(cpt, cor, spawnRow, ccol), ccol) {
					g.set(x.r, x.c, gridClaimed)
					proj.claimed[x] = 1
					po.Claimed = append(po.Claimed, [3]int{x.r, x.c, 1})
				}
				proj.depETA[1] = 100 + rnd.IntN(900)
				po.DepETA["1"] = proj.depETA[1]
			}
			spt, sor, scol := rnd.IntN(7), rnd.IntN(4), rnd.IntN(w-3)
			if g.canPlace(pieceCells(spt, sor, spawnRow, scol)) {
				for _, x := range pieceCells(spt, sor, g.dropRow(spt, sor, spawnRow, scol), scol) {
					proj.soft[x] = 2
					po.Soft = append(po.Soft, [3]int{x.r, x.c, 2})
				}
			}
			c0 := rnd.IntN(w - 3)
			imminent := rnd.IntN(2) == 0
			proj.boxes = []spawnBox{{seat: 2, c0: c0, c1: c0 + 3, imminent: imminent}}
			im := 0
			if imminent {
				im = 1
			}
			po.Boxes = [][4]int{{2, c0, c0 + 3, im}}
		} else if i%4 == 1 {
			// An uncoordinated shared board: another player's falling piece.
			fpt, forr, fcol := rnd.IntN(7), rnd.IntN(4), rnd.IntN(w-3)
			if cs := pieceCells(fpt, forr, 8, fcol); g.canPlace(cs) {
				for _, x := range cs {
					g.set(x.r, x.c, 3)
				}
			}
		}
		pt := rnd.IntN(7)
		row, col := spawnRow, spawnC
		if rnd.IntN(4) == 0 {
			row, col = 2+rnd.IntN(6), rnd.IntN(w-3)
		}
		var upcoming []int
		for k := []int{0, 0, 1, 1, 2, 3, 6}[rnd.IntN(7)]; k > 0; k-- {
			upcoming = append(upcoming, rnd.IntN(7))
		}
		p := planOut{H: g.h, W: g.w, Pt: pt, Row: row, Col: col, SpawnC: spawnC, Upcoming: upcoming, Proj: po,
			Features: [4]int{g.rowTransitions(), g.colTransitions(), g.holes(), g.wells()}}
		for r := 0; r < g.h; r++ {
			for c := 0; c < g.w; c++ {
				if v := g.at(r, c); v != 0 {
					p.Cells = append(p.Cells, [3]int{r, c, int(v)})
				}
			}
		}
		for _, pl := range planPlacements(g, pt, row, col, spawnC, upcoming, proj) {
			p.Ranked = append(p.Ranked, rankedOut{pl.orient, pl.col, pl.dropRow, pl.lines, pl.score, pl.waitOn})
		}
		plans = append(plans, p)
	}
	b, err := json.Marshal(map[string]any{"plans": plans})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
