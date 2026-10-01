package main

// The shared board's housekeeping every playing peer owes (guide §5 step 4,
// gameplays §3): a crewmate's falling piece none of whose cells anyone has
// rewritten for ten seconds — a live piece never stands that long; gravity
// rewrites it every row it falls — or whose seat the listing no longer holds
// was left behind by a player who crashed or walked out (or is a copy a
// stale collapse stranded beside the live piece), and any PLAYING peer may
// vacate it; and a piece that has held OUR spawn box, unmoved at the same
// sequences, for three seconds is the same case, vacated whole so we can
// spawn. The GUI's engines do both (internal/engine/roster.go
// vacateIdlePeers, engine.go deferSpawnLocked); golang-mk1 did neither, and
// a crashed crewmate's piece on a spawn box deferred that seat's spawn for
// the rest of the game. The clocks are kept PER CELL and the cells grouped
// by the piece each claims to be part of (seat, type, orientation, anchor),
// so a seat's live piece moving elsewhere never voids the clock of a stray
// of the same seat's — and a group's clock is its newest cell's.

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"
)

const (
	spawnBlockedVacateAfter = 3 * time.Second  // config.SpawnBlockedVacateAfter
	idlePieceVacateAfter    = 10 * time.Second // config.IdlePieceVacateAfter
	sweepEvery              = 500 * time.Millisecond
)

// peerCell is a foreign active cell as the stream last delivered it: its
// owner, the anchor it claims to be part of, its sequence, and when that
// sequence was first seen — the cell's idle clock.
type peerCell struct {
	pi  int
	p   active
	seq uint64
	at  time.Time
}

// pieceKey identifies the piece a foreign cell claims to be part of.
type pieceKey struct {
	pi int
	p  active
}

// peerGroup is one such piece: its cells at the sequences they hold, and
// the newest of their clocks.
type peerGroup struct {
	key    pieceKey
	cells  map[cell]uint64
	newest time.Time
}

// spawnBlocker is the deferred spawn's account of what blocks it: the
// foreign cells on our spawn box at the sequences they hold, and since
// when exactly those have — a rewrite of any of them (the piece moved)
// starts a fresh account.
type spawnBlocker struct {
	cells map[cell]uint64
	since time.Time
}

// notePeerCell records a foreign active cell delivered at a new sequence
// (its owner rewrote it: the clock restarts); the same sequence again
// leaves the clock alone. Caller holds mu.
func (g *Game) notePeerCell(at cell, wc wireCell, seq uint64, now time.Time) {
	if g.peerCells == nil {
		g.peerCells = map[cell]peerCell{}
	}
	if cur, ok := g.peerCells[at]; ok && cur.seq == seq {
		return
	}
	g.peerCells[at] = peerCell{pi: wc.Pi, p: active{wc.T, wc.R, wc.Ar, wc.Ac}, seq: seq, at: now}
}

// forgetPeerCell drops a position's clock: the cell is ours, settled or
// empty now. Caller holds mu.
func (g *Game) forgetPeerCell(at cell) { delete(g.peerCells, at) }

// rebuildPeerCells makes the clocks agree with the other-active map after a
// transform of ours moved every piece (reindexOthers): every foreign active
// cell has a clock, restarted only where its sequence changed. Caller holds
// mu.
func (g *Game) rebuildPeerCells(now time.Time) {
	fresh := make(map[cell]peerCell, len(g.othersAct))
	for at, pi := range g.othersAct {
		p := g.othersPiece[pi]
		seq := g.seqs[at]
		if cur, ok := g.peerCells[at]; ok && cur.seq == seq && cur.pi == pi {
			cur.p = p
			fresh[at] = cur
			continue
		}
		fresh[at] = peerCell{pi: pi, p: p, seq: seq, at: now}
	}
	g.peerCells = fresh
}

// peerGroups groups the foreign active cells on the board by the piece each
// claims to be part of. A cell with no clock yet (written through by a
// transform of ours ahead of its echo) starts one now. Seat order, then
// anchor, so a sweep is deterministic. Caller holds mu.
func (g *Game) peerGroups(now time.Time) []peerGroup {
	if g.peerCells == nil {
		g.peerCells = map[cell]peerCell{}
	}
	groups := map[pieceKey]*peerGroup{}
	for at, pi := range g.othersAct {
		pc, ok := g.peerCells[at]
		if !ok || pc.pi != pi {
			pc = peerCell{pi: pi, p: g.othersPiece[pi], seq: g.seqs[at], at: now}
			g.peerCells[at] = pc
		}
		k := pieceKey{pc.pi, pc.p}
		grp := groups[k]
		if grp == nil {
			grp = &peerGroup{key: k, cells: map[cell]uint64{}}
			groups[k] = grp
		}
		grp.cells[at] = pc.seq
		if pc.at.After(grp.newest) {
			grp.newest = pc.at
		}
	}
	out := make([]peerGroup, 0, len(groups))
	for _, grp := range groups {
		out = append(out, *grp)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].key, out[j].key
		if a.pi != b.pi {
			return a.pi < b.pi
		}
		if a.p.row != b.p.row {
			return a.p.row < b.p.row
		}
		return a.p.col < b.p.col
	})
	return out
}

// staleGroups is every foreign piece a playing peer may vacate: one none of
// whose cells has been rewritten for idlePieceVacateAfter, or one whose
// seat the listing no longer holds. Caller holds mu.
func (g *Game) staleGroups(now time.Time) []peerGroup {
	var out []peerGroup
	for _, grp := range g.peerGroups(now) {
		if !g.seatPresentLocked(grp.key.pi) || now.Sub(grp.newest) >= idlePieceVacateAfter {
			out = append(out, grp)
		}
	}
	return out
}

// seatPresentLocked reports whether the listing's roster holds the seat —
// true while no roster was pushed. mu held.
func (g *Game) seatPresentLocked(seat int) bool {
	if g.roster == nil {
		return true
	}
	for _, p := range g.roster {
		if p.Seat == seat {
			return true
		}
	}
	return false
}

// sweepLoop runs the idle sweep beside the piece loop on a shared board
// with company, every sweepEvery, until the game or the agent stops.
func (g *Game) sweepLoop(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.a.stopCh:
			return
		case <-g.ended:
			return
		case <-t.C:
		}
		g.sweepPeers(ctx)
	}
}

// sweepPeers vacates every stale foreign piece — while we play: an agent
// out of the game (dead, waiting for a verdict) is a spectator, and a
// spectator vacates nothing. A vacate that loses its CAS means the board
// moved under us; the next tick looks again.
func (g *Game) sweepPeers(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.dead || g.isEnded() || !g.shared() || g.seatsOnPF <= 1 {
		return
	}
	now := time.Now()
	for _, grp := range g.staleGroups(now) {
		why := "idle for " + now.Sub(grp.newest).Round(time.Second).String()
		if !g.seatPresentLocked(grp.key.pi) {
			why = "its seat is gone"
		}
		log.Printf("sweep: seat %d's piece at (%d,%d), %d cell(s), %s — vacating it", grp.key.pi, grp.key.p.row, grp.key.p.col, len(grp.cells), why)
		if !g.vacateForeignCells(ctx, grp.cells) {
			return
		}
		g.st.idleVacates++
	}
}

// spawnCells is where our next piece appears: the spawn box of our seat.
func (g *Game) spawnCells() [4]cell {
	return pieceCells(g.pieceAt(g.pieceIdx), 0, spawnRow, g.spawnC)
}

// noteSpawnBlocked is the deferred spawn's account (gameplays §3, config
// SpawnBlockedVacateAfter): the foreign cells on our spawn box, at the
// sequences they hold, are what blocks us; the same cells at the same
// sequences for spawnBlockedVacateAfter are a piece nobody is moving, and
// we vacate the piece(s) they belong to, whole. Reports whether a vacate
// landed — the caller's next spawn attempt then finds the box free. mu held.
func (g *Game) noteSpawnBlocked(ctx context.Context, cs [4]cell, now time.Time) bool {
	blockers := map[cell]uint64{}
	for _, c := range cs {
		if _, ok := g.othersAct[c]; ok {
			blockers[c] = g.seqs[c]
		}
	}
	if len(blockers) == 0 || !sameBlockers(g.spawnBlock.cells, blockers) {
		g.spawnBlock = spawnBlocker{cells: blockers, since: now}
		return false
	}
	held := now.Sub(g.spawnBlock.since)
	if held < spawnBlockedVacateAfter {
		return false
	}
	// The whole piece(s) the blockers belong to: every foreign cell claiming
	// the same seat and anchor as one of them.
	keys := map[pieceKey]bool{}
	for at := range blockers {
		if pc, ok := g.peerCells[at]; ok {
			keys[pieceKey{pc.pi, pc.p}] = true
		} else if pi, ok := g.othersAct[at]; ok {
			keys[pieceKey{pi, g.othersPiece[pi]}] = true
		}
	}
	vac := map[cell]uint64{}
	for at, pc := range g.peerCells {
		if keys[pieceKey{pc.pi, pc.p}] {
			vac[at] = pc.seq
		}
	}
	for at, seq := range blockers {
		vac[at] = seq
	}
	log.Printf("spawn box held %s by %d cell(s) nobody has moved: vacating their piece, %d cell(s)", held.Round(time.Millisecond), len(blockers), len(vac))
	g.spawnBlock = spawnBlocker{} // whatever blocks next starts a fresh account
	if !g.vacateForeignCells(ctx, vac) {
		return false
	}
	g.st.spawnUnblocks++
	return true
}

// sameBlockers reports whether two accounts name the same cells at the same
// sequences.
func sameBlockers(a, b map[cell]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for at, seq := range a {
		if s, ok := b[at]; !ok || s != seq {
			return false
		}
	}
	return true
}

// vacateForeignCells empties a foreign piece's cells as ONE atomic batch at
// the sequences they were last seen at: on the crew's board a CAS batch — a
// piece that moved fails it (look again next tick), and of two sweepers one
// commits — and on a team's board, or the crew's while its floor rises
// (survival), the txn-gated vacate with the cells guarded the same way, so
// a racing transform can never resurrect the piece from a stale snapshot.
// Reports whether it landed; a loss resyncs. Caller holds mu.
func (g *Game) vacateForeignCells(ctx context.Context, cells map[cell]uint64) bool {
	if len(cells) == 0 {
		return true
	}
	vac := make([]cellUpd, 0, len(cells))
	for at, seq := range cells {
		if g.seqs[at] != seq {
			return false // rewritten since it was grouped: the piece is live
		}
		vac = append(vac, cellUpd{at: at, guard: true})
	}
	var err error
	if g.mode == modeCooperative && g.survival == "" {
		err = g.publishBatch(ctx, vac, true)
	} else {
		err = g.publishGatedBatch(ctx, txnReg{Applied: g.txnApplied, Op: "vacate", By: g.idx}, vac)
	}
	if err != nil {
		if errors.Is(err, errCAS) {
			g.refreshRegisters(ctx)
			g.resync(ctx)
		} else {
			log.Printf("vacate: %v", err)
		}
		return false
	}
	for at := range cells {
		g.dropOwner(at)
		delete(g.peerCells, at)
	}
	return true
}
