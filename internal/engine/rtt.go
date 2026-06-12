package engine

import "time"

// RTT measurement: the time between the moment the engine initiates a batch
// publish commit and the moment the ordered consumer delivers the FIRST message
// of that batch back to it. This is the full write→commit→echo loop the player
// actually experiences — every visible board change travels it — so it is
// surfaced continuously in the HUD while playing.
//
// Mechanics: every successful batch publish knows its commit-ack stream
// sequence, and an atomic batch's N messages get consecutive sequences, so the
// batch's first message has sequence commitSeq-(N-1). trackRTT records the
// publish start time under that sequence; the own-board consumer calls
// noteRTTEcho for every message it delivers, and the first batch message
// completes the measurement.
//
// The commit ack (publisher goroutine) and the echo (consumer goroutine) race:
// both can observe the commit at effectively the same instant, so the consumer
// may deliver the echo BEFORE trackRTT runs. lastEchoSeq — the highest
// own-board sequence the consumer has delivered, maintained under rttMu —
// closes that race: if the echo already passed, trackRTT completes the
// measurement immediately instead of registering an entry that would never
// match. The ordered consumer delivers strictly by stream sequence, so a
// simple high-water mark is sufficient.

// rttStale bounds how long an unmatched pending entry may linger (e.g. a
// publish whose echo was cut off by shutdown) before being pruned.
const rttStale = 10 * time.Second

// RTT returns the latest measured publish→echo round trip, or 0 if no
// measurement has completed yet.
func (e *Engine) RTT() time.Duration {
	return time.Duration(e.rttNanos.Load())
}

// trackRTT is called after a successful batch publish with the time the
// publish was initiated, the commit ack's stream sequence, and the batch size.
// It either completes the measurement immediately (echo already delivered) or
// registers the batch's first sequence for noteRTTEcho to match.
func (e *Engine) trackRTT(t0 time.Time, commitSeq uint64, n int) {
	if commitSeq == 0 || n <= 0 {
		return
	}
	firstSeq := commitSeq - uint64(n-1)
	e.rttMu.Lock()
	if e.lastEchoSeq >= firstSeq {
		e.rttMu.Unlock()
		e.setRTT(time.Since(t0))
		return
	}
	for seq, t := range e.rttPending {
		if seq <= e.lastEchoSeq || time.Since(t) > rttStale {
			delete(e.rttPending, seq)
		}
	}
	e.rttPending[firstSeq] = t0
	e.rttMu.Unlock()
}

// noteRTTEcho is called by the own-board consumer for every message it
// delivers. If the sequence is the first message of a tracked batch, the
// measurement completes and is published to the UI.
func (e *Engine) noteRTTEcho(seq uint64) {
	if seq == 0 {
		return
	}
	e.rttMu.Lock()
	if seq > e.lastEchoSeq {
		e.lastEchoSeq = seq
	}
	t0, ok := e.rttPending[seq]
	if ok {
		delete(e.rttPending, seq)
	}
	e.rttMu.Unlock()
	if ok {
		e.setRTT(time.Since(t0))
	}
}

// setRTT stores the latest measurement and notifies the UI.
func (e *Engine) setRTT(d time.Duration) {
	e.rttNanos.Store(int64(d))
	e.emitUpdate(EngineUpdate{Kind: UpdateRTT, RTT: d})
}
