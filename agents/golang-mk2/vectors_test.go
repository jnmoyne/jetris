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
		// What a claim says beyond the landing (coord.go): the walk's
		// cost by (orientation, column), the teammates' tentative
		// landings, our own announced placement, and the crew's well.
		Route    [][3]float64 `json:"route"` // orient, col, cost; null = no route (the flat costs)
		RouteNow bool         `json:"route_now"`
		Tent     [][3]float64 `json:"tent"`   // row, col, weight
		Intent   *[2]int      `json:"intent"` // orient, col
		Well     int          `json:"well"`
		WellOn   bool         `json:"well_on"`
		WellHigh bool         `json:"well_high"`
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
		PV       [][2]int    `json:"pv"`       // the lookahead's best line from the best placement: orient, col per upcoming piece
		PVScore  float64     `json:"pv_score"` // and its total
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
		routed := i%8 == 7 // a coordinated board planned as plan() plans it: routes, tentative landings, an intent, the well
		if routed && rnd.IntN(3) != 0 {
			for r := 0; r < g.h; r++ { // the crew's well, kept open
				g.set(r, w-1, 0)
			}
		}
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
			if routed {
				proj.route, po.Route = map[[2]int]float64{}, [][3]float64{}
				for o := 0; o < 4; o++ {
					for c := -2; c < w; c++ {
						if rnd.IntN(5) != 0 {
							cost := float64(rnd.IntN(3000)) / 1000
							proj.route[[2]int{o, c}] = cost
							po.Route = append(po.Route, [3]float64{float64(o), float64(c), cost})
						}
					}
				}
				proj.routeNow = rnd.IntN(4) == 0
				po.RouteNow = proj.routeNow
				proj.tent = map[cell]float64{}
				tpt, tor, tcol := rnd.IntN(7), rnd.IntN(4), rnd.IntN(w-3)
				if g.canPlace(pieceCells(tpt, tor, spawnRow, tcol)) {
					weight := 1 / float64(1+rnd.IntN(2))
					for _, x := range pieceCells(tpt, tor, g.dropRow(tpt, tor, spawnRow, tcol), tcol) {
						proj.tent[x] = weight
						po.Tent = append(po.Tent, [3]float64{float64(x.r), float64(x.c), weight})
					}
				}
				if rnd.IntN(2) == 0 {
					proj.intent = &[2]int{rnd.IntN(4), rnd.IntN(w - 3)}
					po.Intent = proj.intent
				}
				proj.well, proj.wellOn, proj.wellHigh = w-1, rnd.IntN(4) != 0, rnd.IntN(2) == 0
				po.Well, po.WellOn, po.WellHigh = proj.well, proj.wellOn, proj.wellHigh
			}
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
		ranked := planPlacements(g, pt, row, col, spawnC, upcoming, proj)
		for _, pl := range ranked {
			p.Ranked = append(p.Ranked, rankedOut{pl.orient, pl.col, pl.dropRow, pl.lines, pl.score, pl.waitOn})
		}
		if len(ranked) > 0 && len(upcoming) > 0 {
			ws := noWell
			if proj != nil {
				ws = proj.wellSpec()
			}
			after, _, _ := simulateLock(g, ranked[0].dest)
			p.PV = [][2]int{}
			for _, x := range principalVariation(after, spawnC, upcoming, ws) {
				p.PV = append(p.PV, [2]int{x.orient, x.col})
			}
			p.PVScore = lookaheadWell(after, spawnC, upcoming, ws)
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
