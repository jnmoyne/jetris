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
// `lines` rows: competitive — every surviving opponent; teams — the opposing
// team's board. One goroutine per victim; each runs an independent CAS-add.
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
		opposing := 1 - e.teamIdx
		go e.bumpLedger(ctx, TeamBoardKey(opposing), config.TeamGarbageSubject(e.gameID, opposing), lines)
	}
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

// applyOwedGarbage runs on runInput — the engine's single gameplay-write
// goroutine, so it can never race the player's own move publishes — and
// applies the board's outstanding deficit (owed − applied) as ONE gated
// cascade transform. The cells are written NoCAS: the raise overrides any
// in-flight move, whose own per-subject CAS then fails against the risen
// board and is dropped + flashed. Exactly-once across duplicate signals,
// replays, teams applier races, and reconnects comes from the txn gate plus
// the cumulative registers — a loser's recompute sees deficit 0 and no-ops.
func (e *Engine) applyOwedGarbage(ctx context.Context) {
	var topped []int
	var full bool
	committed := e.publishGatedTransform(ctx, txnOpShrink, false, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
		deficit := owed.Total - txn.Applied
		if deficit <= 0 {
			return nil, TxnRegister{}, false
		}
		// Each raise draws its own hole columns (one draw for all its rows,
		// or one per row in a random-holes game); a recompute after a lost
		// gate simply draws again.
		rows, t, f := pf.ProjectShrinkCascade(deficit, owed.By, e.garbageRaiseHoles(pf.Width, e.garbageHoles, deficit, e.randomGarbageHoles))
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
