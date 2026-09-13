package engine

import (
	"context"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// An open game's roster changes at any time: a player takes a free seat of
// a running game and plays on the live board, a player leaves and their seat
// is free again. Two things follow for every engine on the board. A falling
// piece can be left behind — by a player who walked away without vacating
// it, or crashed — and it must not sit on the shared board forever: every
// playing engine watches its peers' pieces and vacates one that has not
// moved for config.IdlePieceVacateAfter, or whose seat nobody holds any
// more (vacateIdlePeers). And a game across several playfields ends when
// only one still has players: the roster the lobby pushes (SetRoster) is
// what the counting follows (alivePlayfields).

// peerCell is a foreign active cell of the own shared board as this engine
// last had it delivered: the stream sequence of that write, and when it
// arrived — its idle clock. Kept per cell, not per piece: a seat's cells can
// sit at more than one anchor (a copy of its piece stranded by a stale
// collapse, beside the piece it plays on), and a clock kept per seat never
// ran out while the seat kept playing — the live piece and the stray took
// turns being "the" piece every tick, and each turn restarted it.
type peerCell struct {
	seq uint64
	at  time.Time
}

// notePeerCell records a cell delivered to the own board: a foreign active
// cell at a new sequence — its owner rewrote it — restarts its clock;
// anything else at the position (a cell of ours, a locked one, an empty one)
// forgets it. e.mu held.
func (e *Engine) notePeerCell(pos game.CellPos, c game.Cell, seq uint64, now time.Time) {
	if !e.sharedBoard() {
		return
	}
	if !c.Active || c.PlayerIdx == e.playerIdx {
		delete(e.peerCells, pos)
		return
	}
	if cur, ok := e.peerCells[pos]; ok && cur.seq == seq {
		return
	}
	e.peerCells[pos] = peerCell{seq: seq, at: now}
}

// seatPresentLocked reports whether anyone holds the given seat per the
// roster the lobby pushed; true while no roster was pushed. e.mu held.
func (e *Engine) seatPresentLocked(seat int) bool {
	if e.roster == nil {
		return true
	}
	for _, s := range e.roster {
		if s.Seat == seat {
			return true
		}
	}
	return false
}

// vacateIdlePeers runs on the housekeeping tick of a playing engine on a
// shared board: the other seats' active cells are grouped by the piece they
// claim to be — seat, type, orientation, anchor — and a group none of whose
// cells has been rewritten for the idle threshold, or whose seat nobody
// holds any more, is vacated: one batch per group, at the cells' last-seen
// sequences, so a seat's live piece moving elsewhere never voids it. A live
// piece never stands still that long — gravity rewrites it every row it
// falls, and the lock delay's resets end well short of the threshold — so
// what the clock runs out on is a piece left behind (its player crashed, or
// walked away without vacating it) or a copy of one a stale collapse
// stranded.
func (e *Engine) vacateIdlePeers(ctx context.Context) {
	if !e.sharedBoard() || e.solo() || e.getMode() != ModePlayer {
		return
	}
	now := time.Now()
	type group struct {
		seat   int
		cells  map[game.CellPos]game.Cell
		newest time.Time
	}
	e.mu.Lock()
	for pos := range e.peerCells {
		if c := e.playfield.Rows[pos.Row].Cells[pos.Col]; !c.Active || c.PlayerIdx == e.playerIdx {
			delete(e.peerCells, pos) // gone from the board: forgotten
		}
	}
	groups := map[game.Cell]*group{}
	for r, row := range e.playfield.Rows {
		for col, c := range row.Cells {
			if !c.Active || c.PlayerIdx == e.playerIdx {
				continue
			}
			pos := game.CellPos{Row: r, Col: col}
			pc, ok := e.peerCells[pos]
			if !ok {
				// On the board with no delivery noted — written through by
				// a collapse of ours ahead of its echo: its clock starts now.
				pc = peerCell{seq: e.playfield.CellLastSeq(r, col), at: now}
				e.peerCells[pos] = pc
			}
			key := pieceKey(c)
			g := groups[key]
			if g == nil {
				g = &group{seat: c.PlayerIdx, cells: map[game.CellPos]game.Cell{}}
				groups[key] = g
			}
			g.cells[pos] = game.Cell{}
			if pc.at.After(g.newest) {
				g.newest = pc.at
			}
		}
	}
	var stale []*group
	for _, g := range groups {
		if !e.seatPresentLocked(g.seat) || now.Sub(g.newest) >= e.idleVacateAfter {
			stale = append(stale, g)
		}
	}
	e.mu.Unlock()
	for _, g := range stale {
		e.vacateCells(ctx, g.cells, false)
	}
}

// vacatePiece removes a seat's falling piece from the shared board — a
// peer's stale one, or our own on the way out. On a team board the vacate
// is a gated transform (txnOpVacate): racing bulk transforms (a garbage
// application projecting the piece from a stale snapshot would resurrect
// it) are serialized by the board's txn gate, and the loser recomputes from
// converged state — and so is the crew's board's when its floor rises
// through the registers (survivalRegisters), for the same reason. Any other
// crew's board has no gate; there the vacate is one atomic batch with
// per-cell CAS expectations at the piece's last-seen sequences
// (vacateCells). locked reports whether the caller holds e.mu.
func (e *Engine) vacatePiece(ctx context.Context, idx int, locked bool) bool {
	if e.gameMode == config.ModeTeams || e.survivalRegisters() {
		return e.publishGatedTransform(ctx, txnOpVacate, locked, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
			if pf.ActivePieceForPlayer(idx) == nil {
				return nil, TxnRegister{}, false // already gone (a shrink topped it, or a prior attempt landed)
			}
			clone := pf.Clone()
			clone.ClearActiveCellsForPlayer(idx)
			return clone.Rows, TxnRegister{Applied: txn.Applied}, true
		})
	}
	if !locked {
		e.mu.Lock()
	}
	cells := map[game.CellPos]game.Cell{}
	for r, row := range e.playfield.Rows {
		for c, cell := range row.Cells {
			if cell.Active && cell.PlayerIdx == idx {
				cells[game.CellPos{Row: r, Col: c}] = game.Cell{}
			}
		}
	}
	if !locked {
		e.mu.Unlock()
	}
	return e.vacateCells(ctx, cells, locked)
}

// vacateCells empties the given cells of other seats' pieces — a stale
// group of one (vacateIdlePeers), the cells holding a spawn box
// (deferSpawnLocked), a whole piece (vacatePiece). On a team board the
// vacate is a gated transform (txnOpVacate) that empties, of the given
// cells, those the converged snapshot still shows as another seat's active
// cells — racing bulk transforms are serialized by the board's txn gate.
// The crew's board has no gate; there the vacate is one atomic batch with
// per-cell CAS expectations at the cells' last-seen sequences — a cell
// rewritten since fails the CAS and nothing happens (the next tick looks
// again), two engines vacating the same cells commit exactly one batch.
// Reports whether a batch committed. locked reports whether the caller
// holds e.mu.
func (e *Engine) vacateCells(ctx context.Context, cells map[game.CellPos]game.Cell, locked bool) bool {
	if len(cells) == 0 {
		return false
	}
	if e.gameMode == config.ModeTeams {
		return e.publishGatedTransform(ctx, txnOpVacate, locked, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
			clone := pf.Clone()
			found := false
			for pos := range cells {
				if c := &clone.Rows[pos.Row].Cells[pos.Col]; c.Active && c.PlayerIdx != e.playerIdx {
					*c = game.Cell{}
					found = true
				}
			}
			if !found {
				return nil, TxnRegister{}, false // already gone (a shrink topped it, or a prior attempt landed)
			}
			return clone.Rows, TxnRegister{Applied: txn.Applied}, true
		})
	}
	return e.publishProjectedCells(ctx, cells, nil, locked)
}

// VacateOwnPiece removes this player's falling piece from the shared board
// on the way out of a running open game — before the seat is freed
// (lobby.UnjoinGame), so the next player in the seat starts clean. Play
// stops first, so no spawn follows the vacate; the crew's board retries the
// CAS until the piece is gone (it is ours: nothing else moves it), a team
// board's gate admits exactly one vacate. A private board (competitive)
// needs nothing: it is nobody else's to play on.
func (e *Engine) VacateOwnPiece(ctx context.Context) {
	if e.getMode() != ModePlayer {
		return
	}
	e.setMode(ModeGameOver)
	e.mu.Lock()
	e.hadActivePiece = false
	hadPiece := e.playfield.ActivePieceForPlayer(e.playerIdx) != nil
	e.mu.Unlock()
	if !hadPiece || !e.sharedBoard() || e.solo() {
		return
	}
	if e.gameMode == config.ModeTeams {
		e.vacatePiece(ctx, e.playerIdx, false)
		return
	}
	e.mu.Lock()
	cells := map[game.CellPos]game.Cell{}
	for r, row := range e.playfield.Rows {
		for c, cell := range row.Cells {
			if cell.Active && cell.PlayerIdx == e.playerIdx {
				cells[game.CellPos{Row: r, Col: c}] = game.Cell{}
			}
		}
	}
	e.mu.Unlock()
	e.publishProjectedCellsWithMergeRetry(ctx, cells, nil, false)
}

// alivePlayfieldsLocked counts the playfields still in the game — with a
// player seated (per the roster the lobby pushed) who has not been
// eliminated — and names the last one: the crew's one board while anyone is
// seated, a competitive board while its player stands, a team's while any
// member does. Before a roster was pushed (an invite game's engines, tests)
// every seat is presumed held, as the game-end counting always presumed.
// e.mu held.
func (e *Engine) alivePlayfieldsLocked() (alive, last int) {
	last = -1
	switch e.gameMode {
	case config.ModeCooperative:
		if e.roster == nil || len(e.roster) > 0 {
			return 1, 0
		}
		return 0, -1
	case config.ModeTeams:
		n := e.TeamCount()
		members, dead := make([]int, n), make([]int, n)
		if e.roster == nil {
			for t := range members {
				members[t] = e.teamSize
			}
			for pid := range e.eliminatedPlayers {
				if t := e.eliminatedTeam[pid]; t >= 0 && t < n {
					dead[t]++
				}
			}
		} else {
			for _, s := range e.roster {
				if s.Team >= 0 && s.Team < n {
					members[s.Team]++
					if e.eliminatedPlayers[s.PlayerID] {
						dead[s.Team]++
					}
				}
			}
		}
		for t := 0; t < n; t++ {
			if members[t] > dead[t] {
				alive, last = alive+1, t
			}
		}
		return alive, last
	default:
		if e.roster == nil {
			alive = e.playerCount - len(e.eliminatedPlayers)
			if alive == 1 && !e.eliminatedPlayers[e.playerID] {
				last = e.playerIdx
			}
			return max(alive, 0), last
		}
		for _, s := range e.roster {
			if !e.eliminatedPlayers[s.PlayerID] {
				alive, last = alive+1, s.Seat
			}
		}
		return alive, last
	}
}

// evaluateRosterOutcome decides a multi-playfield game whose roster left
// every board but one without a standing player — the last board, or team,
// wins; none standing is a draw. Runs after every roster push and every
// elimination, once the game is on, until the outcome is decided.
func (e *Engine) evaluateRosterOutcome(ctx context.Context) {
	if e.gameMode == config.ModeCooperative || !e.gameStarted.Load() {
		return
	}
	e.mu.Lock()
	if e.outcomeDecided || e.roster == nil {
		e.mu.Unlock()
		return
	}
	alive, last := e.alivePlayfieldsLocked()
	if alive > 1 {
		e.mu.Unlock()
		return
	}
	winners, winTeam := map[string]bool{}, -1
	if alive == 1 {
		switch e.gameMode {
		case config.ModeTeams:
			winTeam = last
			for _, s := range e.roster {
				if s.Team == winTeam {
					winners[s.PlayerID] = true
				}
			}
		default:
			for _, s := range e.roster {
				if s.Seat == last {
					winners[s.PlayerID] = true
				}
			}
		}
	}
	e.mu.Unlock()
	if !e.decideOutcome(winners, winTeam, false) {
		return
	}
	if e.initialMode == ModePlayer && e.js != nil {
		go e.transitionGameToFinished(ctx)
	}
}
