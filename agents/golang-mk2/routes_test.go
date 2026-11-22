package main

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// peerPiece puts a teammate's falling piece on the board.
func peerPiece(g *Game, seat int, p active) {
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		g.othersAct[c] = seat
	}
	g.othersPiece[seat] = p
}

// A claim said again for the same placement of the same piece — a wait
// announced, a preview refreshed — keeps its place in the right of way; a
// claim for another landing takes a new one. And a claim that says it
// waits on us ends our think pause.
func TestClaimKeepsItsPlaceWhenSaidAgain(t *testing.T) {
	g := coordTestGame()
	c := planned(1, 4, 2, 1, 9, 0)
	b, _ := json.Marshal(c)
	g.foldClaimRaw("g.crew.1", b, 10, jetstream.KeyValuePut)
	if got := g.claims[1].order(); got != 10 {
		t.Fatalf("first claim: order %d, want 10", got)
	}
	hurry := make(chan struct{})
	g.hurry = hurry
	c.WaitOn = []int{0}
	b, _ = json.Marshal(c)
	g.foldClaimRaw("g.crew.1", b, 15, jetstream.KeyValuePut)
	if got := g.claims[1]; got.order() != 10 || got.rev != 15 || !got.waitsOn(0) {
		t.Fatalf("said again: order %d rev %d waits on us %v, want 10, 15, true", got.order(), got.rev, got.waitsOn(0))
	}
	select {
	case <-hurry:
	default:
		t.Error("a claim waiting on our piece did not end the think pause")
	}
	if g.hurry != nil {
		t.Error("the hurry channel was left armed")
	}
	if !g.waitedOn(time.Now()) {
		t.Error("waitedOn: a teammate waits on us, and it says not")
	}
	c.Col, c.WaitOn = 5, nil
	b, _ = json.Marshal(c)
	g.foldClaimRaw("g.crew.1", b, 20, jetstream.KeyValuePut)
	if got := g.claims[1].order(); got != 20 {
		t.Errorf("another landing: order %d, want 20", got)
	}
	if g.waitedOn(time.Now()) {
		t.Error("waitedOn: nobody waits on us any more")
	}
}

// The optional fields are optional: a claim without them reads as a
// hard-dropping piece that waits on nobody, and one with them round-trips.
func TestClaimOptionalFields(t *testing.T) {
	var c claim
	if err := json.Unmarshal([]byte(`{"v":1,"seat":1,"piece":3,"type":2,"rot":1,"col":5,"phase":"planned"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.At != nil || c.Drop != "" || c.WaitOn != nil || c.Next != nil || c.Well != nil {
		t.Errorf("a base claim grew optional fields: %+v", c)
	}
	well := 13
	c.WaitOn, c.Next, c.Well, c.Drop = []int{0}, []nextClaim{{Piece: 4, Type: 0, Rot: 1, Col: 11}}, &well, dropSoft
	b, _ := json.Marshal(c)
	var back claim
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.waitsOn(0) || len(back.Next) != 1 || back.Next[0] != c.Next[0] || back.Well == nil || *back.Well != 13 || back.Drop != dropSoft {
		t.Errorf("round trip: %s → %+v", b, back)
	}
}

// The forecast: a piece whose claim says it lands before we move is gone;
// one that lands later, soft-drops, waits or says nothing is in the air —
// where it is, and the rows gravity takes it through until we move.
func TestForecast(t *testing.T) {
	g := coordTestGame()
	now := time.Now()
	theirs := active{2 /*T*/, 0, spawnRow, 7}
	peerPiece(g, 1, theirs)
	c := planned(1, 4, 2, 0, 9, 10)
	c.ETAms, c.seenAt = 100, now
	g.claims[1] = c
	if fc := g.forecast(now, now.Add(200*time.Millisecond)); len(fc) != 0 {
		t.Errorf("landing before we move: forecast %+v, want none", fc)
	}
	fc := g.forecast(now, now.Add(50*time.Millisecond))
	if len(fc) != 1 || fc[0].seat != 1 || len(fc[0].cells) != 4 || !fc[0].leaveAt.Equal(now.Add(100*time.Millisecond)) {
		t.Fatalf("landing after we move: forecast %+v", fc)
	}
	for _, mut := range []func(*claim){
		func(c *claim) { c.Drop = dropSoft },
		func(c *claim) { c.WaitOn = []int{0} },
	} {
		cc := c
		mut(&cc)
		g.claims[1] = cc
		if fc := g.forecast(now, now.Add(200*time.Millisecond)); len(fc) != 1 || !fc[0].leaveAt.Equal(now.Add(silentLeave)) {
			t.Errorf("a soft-dropping or waiting piece: forecast %+v, want it in the air for %v", fc, silentLeave)
		}
	}
	delete(g.claims, 1) // a seat that says nothing
	fc = g.forecast(now, now.Add(2*g.gravityNow()+time.Millisecond))
	if len(fc) != 1 || len(fc[0].cells) != 12 {
		t.Fatalf("a silent piece over two gravity steps: %d cell(s), want its four at three rows", len(fc[0].cells))
	}
}

// A placement's walk is priced by the path, and a crewmate's piece across
// the way by the wait for it to go — nothing when its claim says it will
// have gone by the time we move.
func TestRouteCostsPriceTheWaitNotTheDistance(t *testing.T) {
	g := coordTestGame()
	now := time.Now()
	theirs := active{0 /*I*/, 1, 0, 6} // vertical I in column 8 (rows 0-3)…
	peerPiece(g, 1, theirs)
	wall := pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col)[0].c
	lockColumn(g, wall, 4) // …on a stack filling the column below it
	p := active{1 /*O*/, 0, spawnRow, g.spawnC}
	near, far := [2]int{0, g.spawnC - 2}, [2]int{0, 11}

	c := planned(1, 4, 0, 1, 6, 10)
	c.ETAms, c.seenAt = 400, now
	g.claims[1] = c
	costs := g.routeCosts(p, now, 100*time.Millisecond)
	wantWait := depPenalty * 0.3 // they leave 300 ms after we mean to move
	if got := costs[far]; math.Abs(got-wantWait) > 0.5 || got < wantWait {
		t.Errorf("behind their piece: cost %.2f, want the %.2f of the wait plus a walk", got, wantWait)
	}
	if got := costs[near]; got <= 0 || got > 0.2 {
		t.Errorf("two columns away on a free board: cost %.2f, want a tie-break", got)
	}

	c.ETAms = 50 // gone before we move: the rows it stood in are the way through
	g.claims[1] = c
	costs = g.routeCosts(p, now, 100*time.Millisecond)
	if got, ok := costs[far]; !ok || got > 0.5 {
		t.Errorf("their piece gone by the time we move: cost %.2f (priced %v), want a walk's", got, ok)
	}

	now2 := g.routeCostsNow(p)
	if _, ok := now2[far]; ok {
		t.Error("routeCostsNow priced a placement behind a piece that is still there")
	}
	proj := &projection{route: now2, routeNow: true}
	if got := proj.penalty(placement{orient: far[0], col: far[1]}); got < routeBlocked {
		t.Errorf("a blocked re-plan: an unreachable placement costs %.1f, want at least %.0f", got, routeBlocked)
	}
}

// The cells a wait keeps out of are the drop shafts of the claims with the
// right of way over ours — not of the ones made after it — and a waiting
// spot is chosen outside them.
func TestKeepOutOfEarlierShafts(t *testing.T) {
	g := coordTestGame()
	mine := planned(0, 2, 1, 0, 2, 20)
	g.myClaim, g.myPrio = &mine, 20
	peerPiece(g, 1, active{0 /*I*/, 0, spawnRow, 7})
	g.claims[1] = planned(1, 4, 0, 1, 8, 10) // vertical I down column 10, claimed before ours
	keep := g.keepOut()
	if !keep[cell{5, 10}] || !keep[cell{0, 10}] || keep[cell{5, 9}] || keep[cell{g.height() - 1, 10}] {
		t.Fatalf("keep-out: want column 10 above the landing, got %d cell(s)", len(keep))
	}
	g.claims[1] = planned(1, 4, 0, 1, 8, 30) // claimed after ours: theirs to keep clear of us
	if keep := g.keepOut(); len(keep) != 0 {
		t.Errorf("a later claim's shaft is kept out of: %d cell(s)", len(keep))
	}

	closed := map[pathState]bool{{2, 3, 0}: true, {2, 9, 0}: true, {2, 7, 0}: true}
	dist := map[pathState]float64{{2, 3, 0}: 0, {2, 9, 0}: 6, {2, 7, 0}: 4}
	keepCells := map[cell]bool{{2, 10}: true, {3, 10}: true} // an O at column 9 stands in it
	spot, ok := waitingSpot(closed, dist, pathState{2, 3, 0}, placement{orient: 0, col: 11}, nil, keepCells, 1)
	if !ok || spot.col != 7 {
		t.Errorf("waiting spot at col %d (moved %v), want 7: the nearest outside the shaft", spot.col, ok)
	}
	spot, _ = waitingSpot(closed, dist, pathState{2, 3, 0}, placement{orient: 0, col: 11}, nil, nil, 1)
	if spot.col != 9 {
		t.Errorf("with nothing to keep out of: waiting spot at col %d, want 9", spot.col)
	}
}

// Who waits and for how long is the right of way's: a piece nobody claimed
// gets a short patience, an earlier claim twice what it says it needs, and
// against later claims we hold the plan — they are the ones to move.
func TestBlockPatience(t *testing.T) {
	g := coordTestGame()
	now := time.Now()
	g.myPrio = 20
	peerPiece(g, 1, active{2, 0, spawnRow, 7})
	if got := g.blockPatience([]int{1}, now); got != blockPatienceSilent {
		t.Errorf("a silent piece: patience %v, want %v", got, blockPatienceSilent)
	}
	c := planned(1, 4, 2, 0, 9, 10)
	c.ETAms, c.seenAt = 400, now
	g.claims[1] = c
	if got := g.blockPatience([]int{1}, now); got != 800*time.Millisecond {
		t.Errorf("an earlier claim 400 ms from landing: patience %v, want 800ms", got)
	}
	c.rev = 30
	g.claims[1] = c
	if got := g.blockPatience([]int{1}, now); got != depWaitTimeout {
		t.Errorf("a later claim: patience %v, want %v (theirs to move)", got, depWaitTimeout)
	}
	if got := g.ownersUnder(active{1, 0, 0, 7}); len(got) != 1 || got[0] != 1 {
		t.Errorf("owners under a piece above theirs: %v, want [1]", got)
	}
}

// The line the lookahead found best is one placement per upcoming piece,
// and its evaluations add up to the lookahead's own score — with a well
// and without.
func TestPrincipalVariationMatchesLookahead(t *testing.T) {
	rnd := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 20; trial++ {
		g := newGrid(24, 14)
		for c := 0; c < 14; c++ {
			for r := 24 - rnd.IntN(6); r < 24; r++ {
				g.set(r, c, 1)
			}
		}
		upcoming := []int{rnd.IntN(7), rnd.IntN(7), rnd.IntN(7)}
		for _, ws := range []wellSpec{noWell, {col: 13}, {col: 13, high: true}} {
			want := lookaheadWell(g, 3, upcoming, ws)
			line := principalVariation(g, 3, upcoming, ws)
			if len(line) != len(upcoming) {
				t.Fatalf("trial %d: line of %d placement(s) for %d piece(s)", trial, len(line), len(upcoming))
			}
			got, board := 0.0, g
			for _, pl := range line {
				after, lines, eroded := simulateLock(board, pl.dest)
				got += evaluateWell(after, pl.dest, lines, eroded, ws)
				board = after
			}
			if math.Abs(got-want) > 1e-6 {
				t.Errorf("trial %d well %+v: the line scores %.4f, the lookahead %.4f", trial, ws, got, want)
			}
		}
	}
}

// wellBoard is a 22-column board with `ready` rows full but for the last
// column.
func wellBoard(ready int) *grid {
	g := newGrid(24, 22)
	for r := 24 - ready; r < 24; r++ {
		for c := 0; c < 21; c++ {
			g.set(r, c, 1)
		}
	}
	return g
}

func touches(pl placement, col int) bool {
	for _, c := range pl.dest {
		if c.c == col {
			return true
		}
	}
	return false
}

// Keeping the well: an I piece goes down it once four rows are banked and
// stays out of it before — unless the stack stands high, when it takes the
// rows there are; and no other piece is laid in or across it.
func TestWellPlacements(t *testing.T) {
	on := &projection{well: 21, wellOn: true}
	high := &projection{well: 21, wellOn: true, wellHigh: true}

	best := planPlacements(wellBoard(5), 0, spawnRow, 3, 3, nil, on)[0]
	if best.lines != 4 || !touches(best, 21) {
		t.Errorf("five rows banked: the I goes to col %d for %d line(s), want the well for 4", best.col, best.lines)
	}
	best = planPlacements(wellBoard(2), 0, spawnRow, 3, 3, nil, on)[0]
	if best.lines != 0 || touches(best, 21) {
		t.Errorf("two rows banked: the I goes to col %d for %d line(s), want it kept out of the well", best.col, best.lines)
	}
	best = planPlacements(wellBoard(2), 0, spawnRow, 3, 3, nil, high)[0]
	if best.lines != 2 || !touches(best, 21) {
		t.Errorf("two rows banked, the stack high: the I goes to col %d for %d line(s), want the well for 2", best.col, best.lines)
	}
	for pt := 1; pt < 7; pt++ {
		for _, proj := range []*projection{on, high} {
			if best := planPlacements(wellBoard(5), pt, spawnRow, 3, 3, nil, proj)[0]; touches(best, 21) {
				t.Errorf("piece %d (high %v) was laid in or across the well: %+v", pt, proj.wellHigh, best.dest)
			}
		}
	}
	// Played plainly, the same board has its deep well capped by the first
	// piece that fits across it — what keeping the well is there to stop.
	g := wellBoard(5)
	covering := placement{dest: [4]cell{{18, 18}, {18, 19}, {18, 20}, {18, 21}}}
	after, lines, eroded := simulateLock(g, covering.dest)
	flat := placement{dest: [4]cell{{18, 0}, {18, 1}, {18, 2}, {18, 3}}}
	afterFlat, _, _ := simulateLock(g, flat.dest)
	ws := wellSpec{col: 21}
	if evaluateWell(after, covering.dest, lines, eroded, ws) >= evaluateWell(afterFlat, flat.dest, 0, 0, ws)-5*wellCover {
		t.Error("an I laid across the top of a five-deep well is not priced for the five cells it buries")
	}
}

// The well is kept when the agent wants one, every teammate's claim names
// the same column and nothing sits over it; a high stack is said, not a
// reason to stop.
func TestWellFor(t *testing.T) {
	g := coordTestGame()
	g.a.well = "auto"
	gr := g.toGridProjected()
	if _, on, _ := g.wellFor(gr); on {
		t.Fatal("the well is on before the teammate has said anything")
	}
	w := g.wellColumn()
	c := planned(1, 4, 2, 0, 9, 10)
	c.Well = &w
	g.claims[1] = c
	if col, on, high := g.wellFor(gr); !on || high || col != g.w-1 {
		t.Fatalf("an agreed, open well on an empty board: col %d on %v high %v", col, on, high)
	}
	other := 0
	c.Well = &other
	g.claims[1] = c
	if _, on, _ := g.wellFor(gr); on {
		t.Error("the well is on though the teammate names another column")
	}
	c.Well = &w
	g.claims[1] = c
	g.survival = "slow"
	if g.wellWanted() {
		t.Error("--well auto keeps a well in a survival game")
	}
	g.a.well = "on"
	if !g.wellWanted() {
		t.Error("--well on keeps no well in a survival game")
	}
	g.survival, g.a.well = "", "off"
	if g.wellWanted() {
		t.Error("--well off keeps a well")
	}
	g.a.well = "auto"
	for r := g.height() - 12; r < g.height(); r++ {
		fillRow(g, r, w)
	}
	if _, on, high := g.wellFor(g.toGridProjected()); !on || !high {
		t.Errorf("twelve rows banked: on %v high %v, want the well kept and the stack called high", on, high)
	}
	g.locked[cell{g.height() - 13, w}] = wireCell{O: true, T: 1} // a cell over the well
	if _, on, _ := g.wellFor(g.toGridProjected()); on {
		t.Error("the well is on with a cell sitting over it")
	}
	delete(g.locked, cell{g.height() - 13, w})
	if got := wellReady(g.toGridProjected(), w); got != 12 {
		t.Errorf("rows ready: %d, want 12", got)
	}
	cl := g.claimFor(active{2, 0, spawnRow, g.spawnC}, placement{}, phasePlanned, 0)
	if cl.Well == nil || *cl.Well != w {
		t.Errorf("our claim does not name the well: %+v", cl.Well)
	}
}

// A teammate's tentative placements for its NEXT pieces are a nudge on the
// projection — the first weighted 1, the second a half, dropped on the
// first — never a wall; nonsense is ignored.
func TestStampTentative(t *testing.T) {
	g := coordTestGame()
	c := planned(1, 4, 2, 0, 9, 10)
	c.Next = []nextClaim{{Piece: 5, Type: 1 /*O*/, Rot: 0, Col: 5}, {Piece: 6, Type: 1, Rot: 0, Col: 5}, {Piece: 7, Type: 1, Rot: 0, Col: 0}}
	g.claims[1] = c
	gr, proj := g.buildProjection()
	h := g.height()
	if proj.tent[cell{h - 1, 5}] != 1 || proj.tent[cell{h - 2, 6}] != 1 {
		t.Errorf("the piece up next: weights %v", proj.tent)
	}
	if proj.tent[cell{h - 3, 5}] != 0.5 || proj.tent[cell{h - 4, 6}] != 0.5 {
		t.Errorf("the one after, on top of it: weights %v", proj.tent)
	}
	if len(proj.tent) != 8 {
		t.Errorf("%d tentative cell(s), want the 8 of the first two pieces", len(proj.tent))
	}
	if gr.filled(h-1, 5) {
		t.Error("a tentative placement is a wall on the grid")
	}
	onIt := placement{dest: [4]cell{{h - 1, 5}, {h - 1, 6}, {h - 2, 5}, {h - 2, 6}}}
	clear := placement{dest: [4]cell{{h - 1, 0}, {h - 1, 1}, {h - 2, 0}, {h - 2, 1}}}
	if got := proj.penalty(onIt) - proj.penalty(clear); math.Abs(got-4*tentPenalty) > 1e-9 {
		t.Errorf("a placement on a teammate's next landing costs %.2f more, want %.2f", got, 4*tentPenalty)
	}
	c.Next = []nextClaim{{Piece: 5, Type: 9, Rot: 0, Col: 5}, {Piece: 4, Type: 1, Rot: 0, Col: 5}}
	g.claims[1] = c
	if _, proj := g.buildProjection(); len(proj.tent) != 0 {
		t.Errorf("a piece type that does not exist, a piece already played: %d tentative cell(s)", len(proj.tent))
	}
	// What we said for the piece now in play is worth keeping.
	g.pieceIdx = 3
	g.myIntent = &nextClaim{Piece: 3, Type: 2, Rot: 1, Col: 4}
	_, proj = g.buildProjection()
	if proj.intent == nil || *proj.intent != [2]int{1, 4} {
		t.Fatalf("intent on the projection: %v", proj.intent)
	}
	kept, other := placement{orient: 1, col: 4, dest: clear.dest}, placement{orient: 2, col: 4, dest: clear.dest}
	if got := proj.penalty(other) - proj.penalty(kept); math.Abs(got-intentBonus) > 1e-9 {
		t.Errorf("the placement we announced is worth %.2f, want %.2f", got, intentBonus)
	}
}

// A landing that would cover more than its plan expected is not locked: it
// is planned again, a bounded number of times.
func TestSpoils(t *testing.T) {
	g := coordTestGame()
	h := g.height()
	fillRow(g, h-1, 5, 6) // a two-cell gap on the floor
	bridge := active{0 /*I*/, 0, h - 3, 4}
	for _, c := range pieceCells(bridge.pt, bridge.orient, bridge.row, bridge.col) {
		if c.r != h-2 {
			t.Fatalf("test geometry: the I lies on row %d, want %d", c.r, h-2)
		}
	}
	if !g.spoils(bridge) {
		t.Fatal("an I laid over a two-cell gap, with none planned: not a spoiled landing")
	}
	if g.holeReplans != 1 {
		t.Errorf("hole re-plans counted: %d, want 1", g.holeReplans)
	}
	g.planHoles = 2
	if g.spoils(bridge) {
		t.Error("the plan expected the two holes: spoiled all the same")
	}
	g.planHoles, g.holeReplans = 0, maxHoleReplans
	if g.spoils(bridge) {
		t.Error("past the bound, the landing is taken as it is")
	}
	g.holeReplans = 0
	if g.spoils(active{1 /*O*/, 0, h - 2, 5}) {
		t.Error("an O dropped into the gap spoils nothing")
	}
}

// A collapse's batch writes every cell once: a teammate's piece shifted
// down into cells the stack held takes them — the write beats the vacate,
// whichever came first — and so does a piece shifted into the cells the
// piece under it has left.
func TestClearBatchWritesEachCellOnce(t *testing.T) {
	g := coordTestGame()
	h := g.height()
	for r := h - 5; r < h; r++ {
		fillRow(g, r) // five full rows…
	}
	delete(g.locked, cell{h - 5, 0})       // …the top one short of a cell: four clear, one stays
	theirs := active{1 /*O*/, 0, h - 8, 6} // an O a row above the stack
	peerPiece(g, 1, theirs)
	rows := g.completedRowsShared()
	if len(rows) != 4 {
		t.Fatalf("completed rows: %v, want the bottom four", rows)
	}
	newLocked, moved := g.clearProjection(rows)
	if moved[1].row != theirs.row+4 {
		t.Fatalf("their piece shifts to row %d, want %d", moved[1].row, theirs.row+4)
	}
	diff := g.diffCells(newLocked)
	np := moved[1]
	pl := wireCell{T: np.pt, A: true, R: np.orient, Ar: np.row, Ac: np.col, Pi: 1}
	for _, c := range pieceCells(np.pt, np.orient, np.row, np.col) {
		cc := pl
		diff = append(diff, cellUpd{at: c, c: &cc})
	}
	seen := map[cell]int{}
	for _, u := range diff {
		seen[u.at]++
	}
	dup := 0
	for _, n := range seen {
		if n > 1 {
			dup++
		}
	}
	if dup == 0 {
		t.Fatal("test geometry: the shifted piece lands on no cell the stack held")
	}
	merged := mergeCells(diff)
	seen = map[cell]int{}
	for _, u := range merged {
		seen[u.at]++
	}
	for at, n := range seen {
		if n != 1 {
			t.Errorf("cell %v is written %d times in the batch", at, n)
		}
	}
	for _, c := range pieceCells(np.pt, np.orient, np.row, np.col) {
		for _, u := range merged {
			if u.at == c && (u.c == nil || !u.c.A || u.c.Pi != 1) {
				t.Errorf("cell %v: the shifted piece does not hold it: %+v", c, u.c)
			}
		}
	}
	// A vacate that comes after a write for the same cell does not undo it.
	wc := wireCell{T: 2, A: true, Pi: 1}
	out := mergeCells([]cellUpd{{at: cell{3, 3}, c: &wc}, {at: cell{3, 3}, guard: true}})
	if len(out) != 1 || out[0].c == nil || !out[0].guard {
		t.Errorf("a write, then a vacate of the same cell: %+v", out)
	}
}

// With the preview looked at, a ready well is still taken: the rows banked
// beside it are not worth more kept than cleared. (They were: a row full
// but for the well counts no transitions, the empty row a clear leaves
// counts two — and the lookahead's every ply preferred the hoard.)
func TestLookaheadTakesAReadyWell(t *testing.T) {
	upcoming := []int{1, 2, 1, 1, 2, 3} // no I to come: this one, or none
	for _, ready := range []int{4, 6, 10} {
		for _, high := range []bool{false, true} {
			proj := &projection{well: 21, wellOn: true, wellHigh: high}
			best := planPlacements(wellBoard(ready), 0, spawnRow, 3, 3, upcoming, proj)[0]
			if best.lines != 4 {
				t.Errorf("%d rows banked (high %v): the I goes to rot %d col %d for %d line(s), want the well for 4", ready, high, best.orient, best.col, best.lines)
			}
		}
	}
}
