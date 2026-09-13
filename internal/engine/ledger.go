package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// Attacker side of the garbage protocol. A line clear OWES garbage rows to
// every victim board; the debt is recorded durably in the victim board's
// garbage register (cumulative total) rather than as a fire-and-forget event.
// Only the register's latest total matters (it subsumes every earlier one),
// so a slow or momentarily disconnected victim can never lose an attack — it
// reconciles the deficit from the register whenever it catches up.
// Simultaneous attackers serialize through per-subject CAS on the
// register and converge to the exact sum.

// ledgerBumpMaxAttempts bounds the attacker's CAS-add loop. Conflicts only
// come from other attackers' bumps landing first, so a handful of refresh
// cycles always converges.
const ledgerBumpMaxAttempts = 20

// bumpVictimLedgers advances the garbage register of every victim board by
// `lines` rows: competitive — every surviving opponent; teams — ONE opposing
// team's board, the rotation's next (nextGarbageTarget). One goroutine per
// victim; each runs an independent CAS-add.
func (e *Engine) bumpVictimLedgers(ctx context.Context, lines int) {
	if lines <= 0 {
		return
	}
	switch e.gameMode {
	case config.ModeCompetitive:
		e.mu.Lock()
		victims := make([]string, 0, len(e.opponentPlayfields))
		for oppID := range e.opponentPlayfields {
			if !e.eliminatedPlayers[oppID] {
				victims = append(victims, oppID)
			}
		}
		e.mu.Unlock()
		for _, oppID := range victims {
			go e.bumpLedger(ctx, oppID, config.CompetitiveGarbageSubject(e.gameID, oppID), lines)
		}
	case config.ModeTeams:
		opposing := e.nextGarbageTarget()
		go e.bumpLedger(ctx, TeamBoardKey(opposing), config.TeamGarbageSubject(e.gameID, opposing), lines)
	}
}

// nextGarbageTarget picks the opposing team our next attack lands on. Between
// two teams there is only ever one answer — the other one — and the rotor
// never moves off it. Past two, an attack still weighs what it always did:
// rather than every opponent taking the full raise (which would multiply the
// garbage in play by the number of teams and end a six-way game in a minute),
// each raise goes to ONE opponent and consecutive raises rotate through them,
// so a team both sends and receives what it would in a duel. The rotor is per
// engine, so a team's several players spread their own attacks independently.
func (e *Engine) nextGarbageTarget() int {
	n := e.TeamCount()
	e.mu.Lock()
	defer e.mu.Unlock()
	// Step over our own index: the rotor counts the OTHER n-1 teams.
	t := (e.teamIdx + 1 + e.attackRotor%max(n-1, 1)) % n
	e.attackRotor++
	return t
}

// bumpLedger CAS-adds `lines` to one victim board's garbage register. The
// first attempt starts from the cached register (kept fresh by that board's
// consumer); a lost race refreshes straight from the stream and re-adds.
func (e *Engine) bumpLedger(ctx context.Context, cacheKey, subject string, lines int) {
	e.mu.Lock()
	cached := e.opponentGarbage[cacheKey]
	e.mu.Unlock()
	total, seq := cached.reg.Total, cached.seq

	for attempt := 0; attempt < ledgerBumpMaxAttempts; attempt++ {
		if attempt > 0 {
			// Small per-player offset desynchronizes simultaneous attackers'
			// retry loops, then refresh the register from the stream.
			backoff := time.Duration(attempt+e.playerIdx) * time.Millisecond
			if backoff > 10*time.Millisecond {
				backoff = 10 * time.Millisecond
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			msgs, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, []string{subject})
			if err != nil {
				log.Printf("garbage ledger %s: refresh: %v", subject, err)
				return
			}
			total, seq = 0, 0
			if len(msgs) > 0 {
				var reg GarbageRegister
				if json.Unmarshal(msgs[0].Payload, &reg) == nil {
					total = reg.Total
				}
				seq = msgs[0].Seq
			}
		}

		reg := GarbageRegister{Total: total + lines, By: e.playerIdx}
		payload, err := json.Marshal(reg)
		if err != nil {
			return
		}
		commitSeq, err := natspkg.PublishMoveAtomically(ctx, e.js, []natspkg.CellUpdate{{
			Subject:       subject,
			Payload:       payload,
			ExpectLastSeq: seq, // per-subject CAS: the add serializes
		}})
		if err == nil {
			e.mu.Lock()
			if cur, ok := e.opponentGarbage[cacheKey]; !ok || commitSeq > cur.seq {
				e.opponentGarbage[cacheKey] = opponentLedger{reg: reg, seq: commitSeq}
			}
			e.mu.Unlock()
			return
		}
		if !errors.Is(err, natspkg.ErrCASFailure) && !errors.Is(err, natspkg.ErrBatchTransient) {
			log.Printf("garbage ledger %s: publish: %v", subject, err)
			return
		}
		// Lost the add race — refresh and re-add.
	}
	log.Printf("garbage ledger %s: gave up after %d attempts", subject, ledgerBumpMaxAttempts)
}

// publishSurvivalRaise is the rising floor's raise on a shared survival
// board (survivalRegisters), fired by runInput's raise clock: ONE write to
// the crew's garbage register — the rows the next raise brings added to the
// total owed, the count of raises advanced — under a per-subject CAS at the
// register's last-seen sequence (0 for a register never written). Every
// engine on the board keeps the same clock and fires within a round trip of
// the others, and the CAS lets exactly one raise land per tick; a lost race
// is NOT retried — the winner's echo is what every clock re-arms on, this
// one's included (handleGarbageRegisterEcho), the explicit contrast with an
// attack's CAS-add (bumpLedger), where every attacker's rows must land. The
// rows are then applied by whichever engine's gated shrink wins
// (applyOwedGarbage), like an attack's. The register as the clock knew it
// when it fired — owed rows, sequence, raises — comes from runInput: the
// write must carry THAT expectation, not the mirror's at publish time, or
// a peer's raise folded in between would be added to instead of lost to.
// Off runInput, on a goroutine of its own: the write touches no cell, so it
// races nothing the pipeline publishes, and the round trip stalls no move.
func (e *Engine) publishSurvivalRaise(ctx context.Context, owed int, seq uint64, raises int) {
	rows := game.SurvivalRaiseRows(e.survival, e.seed, raises+1)
	if rows <= 0 {
		return
	}
	payload, err := json.Marshal(GarbageRegister{Total: owed + rows, Raises: raises + 1, By: e.playerIdx})
	if err != nil {
		return
	}
	subject := e.garbageSubject()
	_, err = natspkg.PublishMoveAtomically(ctx, e.js, []natspkg.CellUpdate{{
		Subject:       subject,
		Payload:       payload,
		ExpectLastSeq: seq, // per-subject CAS: the first clock to fire raises, the rest lose
	}})
	switch {
	case err == nil:
	case errors.Is(err, natspkg.ErrCASFailure), errors.Is(err, natspkg.ErrBatchTransient):
		// A peer's raise landed first (or the server balked): its echo, or
		// the housekeeping backstop, re-arms this clock.
	default:
		log.Printf("survival raise %s: publish: %v", subject, err)
	}
}

// applyOwedGarbage runs on runInput — the engine's single gameplay-write
// goroutine, so it can never race the player's own move publishes — and
// applies the board's outstanding deficit (owed − applied) as ONE gated
// cascade transform. The cells are written NoCAS: the raise overrides any
// in-flight move, whose own per-subject CAS then fails against the risen
// board and is dropped + flashed. Exactly-once across duplicate signals,
// replays, teams applier races, and reconnects comes from the txn gate plus
// the cumulative registers — a loser's recompute sees deficit 0 and no-ops.
func (e *Engine) applyOwedGarbage(ctx context.Context) {
	// A barrier: the raise is computed from converged state, never over
	// pipelined steps (pipeline.go).
	e.settlePipeline(ctx)
	var topped []int
	var full bool
	committed := e.publishGatedTransform(ctx, txnOpShrink, false, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
		deficit := owed.Total - txn.Applied
		if deficit <= 0 {
			return nil, TxnRegister{}, false
		}
		// Each raise draws its own hole columns (one draw for all its rows,
		// or one per row in a random-holes game); a recompute after a lost
		// gate simply draws again. The rising floor's holes follow the seed
		// and the rows' ordinals instead — the board's count of rows applied
		// so far — so the well moves every four rows whichever raises the
		// rows came in, and a recompute draws exactly the same
		// (game.SurvivalHoles).
		holes := e.garbageRaiseHoles(pf.Width, e.garbageHoles, deficit, e.randomGarbageHoles)
		if e.survival != config.SurvivalNone {
			holes = game.SurvivalHoles(pf.Width, e.garbageHoles, txn.Applied, deficit, e.randomGarbageHoles, e.seed)
		}
		rows, t, f := pf.ProjectShrinkCascade(deficit, owed.By, holes)
		topped, full = t, f
		if f || slices.Contains(t, e.playerIdx) {
			// Our own piece is about to be removed by this batch (or the
			// board is full). Clear hadActivePiece BEFORE the publish so the
			// batch's own vacate echoes cannot fire a spurious lock-in; if
			// the gate rejects the batch, the next echo showing the piece
			// still active restores the flag (the edge check re-assigns it).
			e.mu.Lock()
			e.hadActivePiece = false
			e.mu.Unlock()
		}
		return rows, TxnRegister{Applied: owed.Total, Topped: t, Full: f}, true
	})
	if !committed {
		return
	}

	// The shift touches most of the board; force a full re-render so no row
	// is left stale if per-row triggers were dropped.
	e.emitFullBoardRerender()

	// Squeezed off the top (or the whole board is full): we're out. Remote
	// teammates listed in Topped learn it from the txn echo (toppedByShrink
	// routing); the applier handles itself directly — its own write-through
	// already advanced the txn mirror, so its echo is a no-op.
	if full || slices.Contains(topped, e.playerIdx) {
		e.handleTopOut(ctx, false)
	}
}
