package engine

import (
	"context"
	"encoding/json"

	"jetris/internal/config"
)

// GarbageRegister is the payload of a board's garbage register subject: the
// cumulative number of garbage rows OWED to the board since game start.
// Attackers advance it with per-subject CAS (read-add-publish, bounded retry),
// so simultaneous attacks serialize and converge to the exact sum. The board's
// players apply deficit = Total − TxnRegister.Applied; the register being
// cumulative makes application idempotent across duplicate signals, replays,
// and reconnects.
type GarbageRegister struct {
	Total int `json:"total"`
	By    int `json:"by"` // playerIdx of the most recent attacker (UI attribution)
}

// TxnRegister is the payload of a board's txn register — the FIRST message of
// every gated bulk-transform batch, carrying a per-subject CAS expectation
// that makes the transform exactly-once: a stale gate atomically rejects the
// whole batch, and the losing applier recomputes from converged state.
//
// Applied is the cumulative number of garbage rows applied to the board
// (advanced by shrink transforms; restated unchanged by clear/vacate
// transforms). Topped lists the players whose falling pieces the transform
// pushed off the top of the board: the register precedes the vacating cell
// messages in the batch, so a victim's consumer learns "this zero-active edge
// is a shrink top-out, not a lock-in" in-band, before the vacates arrive.
// Full means the shift pushed LOCKED rows past the top — the board is lost
// (competitive: the owner; teams: every remaining player on it).
type TxnRegister struct {
	Applied int    `json:"applied"`
	Op      string `json:"op"`
	Topped  []int  `json:"topped,omitempty"`
	Full    bool   `json:"full,omitempty"`
	By      int    `json:"by"` // playerIdx of the applier
}

// Transform op names recorded in TxnRegister.Op.
const (
	txnOpShrink = "shrink"
	txnOpClear  = "clear"
	txnOpVacate = "vacate"
)

// opponentLedger caches an opponent (or opposing-team) board's garbage
// register value and stream sequence, refreshed by that board's consumer, so
// the attacker's CAS-add bump starts from the replica instead of a read round
// trip.
type opponentLedger struct {
	reg GarbageRegister
	seq uint64
}

// handleGarbageRegisterEcho folds a garbage-register message (consumer echo or
// snapshot fetch) into the engine. On the OWN board it advances the owed-ledger
// mirror and signals runInput's garbage application when rows are owed; on an
// opponent/opposing-team board it refreshes the attacker-side ledger cache.
func (e *Engine) handleGarbageRegisterEcho(boardKey string, isOpponent bool, payload []byte, seq uint64) {
	var reg GarbageRegister
	if err := json.Unmarshal(payload, &reg); err != nil {
		return
	}
	if isOpponent {
		e.mu.Lock()
		if cur, ok := e.opponentGarbage[boardKey]; !ok || seq > cur.seq {
			e.opponentGarbage[boardKey] = opponentLedger{reg: reg, seq: seq}
		}
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	if seq > e.garbageOwedSeq {
		e.garbageOwed = reg.Total
		e.garbageOwedBy = reg.By
		e.garbageOwedSeq = seq
	}
	deficit := e.garbageOwed - e.txnApplied
	e.mu.Unlock()
	if deficit > 0 {
		e.signalGarbageApply()
	}
}

// handleTxnRegisterEcho folds a txn-register echo into the engine (own board
// only — an opponent board's gate matters only to its own players). It
// advances the applied mirror and handles remote shrink eliminations:
//
//   - Topped listing THIS player: the batch's vacating cells (delivered right
//     after this register — it's the batch's first message) are about to drive
//     our active-cell count to zero; flag toppedByShrink so the edge routes to
//     handleTopOut instead of handleLockIn.
//   - Full: the shift pushed LOCKED rows past the top — the whole board is
//     lost, our piece included (it may still be stamped, so no zero-active
//     edge would come); top out directly.
//
// The APPLIER never takes these paths off its own echo: its write-through
// already advanced txnSeq to the commit sequence, so the echo fails the
// strictly-higher guard — it handles its own elimination inline.
func (e *Engine) handleTxnRegisterEcho(ctx context.Context, isOpponent bool, payload []byte, seq uint64) {
	if isOpponent {
		return
	}
	var txn TxnRegister
	if err := json.Unmarshal(payload, &txn); err != nil {
		return
	}
	boardLost := false
	e.mu.Lock()
	if seq > e.txnSeq {
		e.txnApplied = txn.Applied
		e.txnSeq = seq
		if e.getMode() == ModePlayer {
			if txn.Full {
				boardLost = true
				e.hadActivePiece = false // the board is dead; no lock-in may fire off its remains
			}
			for _, idx := range txn.Topped {
				if idx == e.playerIdx {
					e.toppedByShrink = true
				}
			}
		}
	}
	deficit := e.garbageOwed - e.txnApplied
	e.mu.Unlock()
	if boardLost {
		e.handleTopOut(ctx, false)
		return
	}
	if deficit > 0 {
		e.signalGarbageApply()
	}
}

// signalGarbageApply nudges runInput to run one garbage-application attempt.
// Non-blocking: the channel holds at most one pending signal, and a signal
// sent before runInput starts (the Start snapshot reconcile) is retained.
func (e *Engine) signalGarbageApply() {
	select {
	case e.applyGarbage <- struct{}{}:
	default:
	}
}

// captureRegisterSnapshot routes a register message from a board snapshot
// fetch (Row < 0 entries) into the same fold paths the live consumer uses —
// including a late joiner discovering its board was already lost (Full).
func (e *Engine) captureRegisterSnapshot(ctx context.Context, boardKey string, isOpponent bool, subject string, payload []byte, seq uint64) {
	switch {
	case config.IsGarbageSubject(subject):
		e.handleGarbageRegisterEcho(boardKey, isOpponent, payload, seq)
	case config.IsTxnSubject(subject):
		e.handleTxnRegisterEcho(ctx, isOpponent, payload, seq)
	}
}
