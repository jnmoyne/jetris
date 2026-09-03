package voice

import "time"

// Packets leave a sender every 20 ms but arrive whenever the network
// delivers them: bunched, late, out of order, or not at all — and the
// speakers ask for a frame every 20 ms regardless. Jitter is what sits
// between the two for one sender: a small ring that holds packets until
// their turn comes and fills the turns that have no packet, first with a
// fading copy of the last frame it played and then with silence.

const (
	// primeFrames is the fill, 60 ms, that playback waits for at the start
	// of a talk-spurt: the latency bought to absorb that much jitter.
	primeFrames = 3
	// maxFrames caps the ring: a sender more than 160 ms ahead of playback
	// has its oldest frames dropped rather than heard late.
	maxFrames = 8
	// concealMax is how many missing frames in a row are covered by
	// repeating the last one, each time half as loud, before silence: two
	// repeats paper over a lost packet without turning a lost second into
	// a buzz.
	concealMax = 2
	// dryFrames is how many empty frames, 300 ms, end a talk-spurt — after
	// which the next packet starts a new one and playback waits to prime
	// again rather than treating the silence as loss.
	dryFrames = 15
)

// Jitter is one sender's buffer. Everything but LastHeard and Team is the
// ring's own; those two are the Session's bookkeeping about the sender,
// kept here because the Session has one Jitter per sender anyway.
type Jitter struct {
	slots     [maxFrames]Packet // keyed by Seq % maxFrames
	have      [maxFrames]bool
	next      uint16              // the Seq the next Pop plays
	count     int                 // slots held
	primed    bool                // playing (or covering losses) rather than filling
	dry       int                 // empty Pops in a row since the last frame played
	last      [FrameSamples]int16 // the last frame played, for concealment
	concealed int                 // frames concealed in a row

	// LastHeard is when the sender's latest packet arrived: the Session
	// times its "talking" indicator out from it.
	LastHeard time.Time
	// Team is the room the sender was last heard in, -1 for the one every
	// player shares; the Session sets it from the packet's subject.
	Team int
}

// seqDist is how far a is ahead of b, sequence numbers wrapping at 65536:
// the difference read as a signed sixteen-bit number.
func seqDist(a, b uint16) int {
	return int(int16(a - b))
}

// Push takes a packet in. A talk-spurt's first packet, a packet for an
// empty buffer between spurts, and one too far from the expected sequence
// in either direction (a gap the buffer cannot bridge, or a burst so stale
// it belongs to another spurt) all start the buffer afresh from that
// packet's sequence. One whose turn has passed is dropped. One so far ahead
// that the ring cannot hold it has playback skip forward to make room —
// the sender is getting ahead of the speakers, and catching up now costs
// less than a growing delay.
func (j *Jitter) Push(p Packet, now time.Time) {
	d := seqDist(p.Seq, j.next)
	if p.Flags&FlagStart != 0 || (j.count == 0 && !j.primed) || d > maxFrames || d < -maxFrames {
		j.reset(p.Seq)
		d = 0
	}
	if d < 0 {
		// Late — unless nothing has played yet, in which case an earlier
		// packet is simply the spurt's true start, arrived second.
		if j.primed || !j.fits(p.Seq) {
			return
		}
		j.next, d = p.Seq, 0
	}
	for d >= maxFrames {
		j.evict(j.next)
		j.next++
		d--
	}
	i := p.Seq % maxFrames
	if !j.have[i] {
		j.count++
	}
	j.slots[i], j.have[i] = p, true
	j.LastHeard = now
	if j.count >= primeFrames {
		j.primed = true
	}
}

// Pop plays the next frame into out and reports whether it produced
// anything the mixer should hear: nothing while the buffer primes, and
// nothing once a run of missing frames has outlasted concealment.
func (j *Jitter) Pop(out []int16) bool {
	if !j.primed {
		return false
	}
	n := min(len(out), FrameSamples)
	if i := j.next % maxFrames; j.have[i] {
		p := &j.slots[i]
		DecodeFrame(CodecState{Predictor: p.Predictor, StepIndex: p.StepIndex}, p.Data[:], out[:n])
		copy(j.last[:], out[:n])
		j.have[i] = false
		j.count--
		j.next++
		j.concealed, j.dry = 0, 0
		return true
	}
	j.next++
	if j.concealed < concealMax {
		j.concealed++
		div := int32(1) << j.concealed
		for i := range n {
			out[i] = int16(int32(j.last[i]) / div)
		}
		return true
	}
	clear(out[:n])
	j.dry++
	if j.dry > dryFrames {
		// The spurt is over: whatever arrives next primes a new one.
		j.primed, j.count = false, 0
		j.have = [maxFrames]bool{}
	}
	return false
}

// Primed reports whether the buffer is playing rather than filling.
func (j *Jitter) Primed() bool {
	return j.primed
}

// reset empties the buffer to start a spurt at seq.
func (j *Jitter) reset(seq uint16) {
	j.have = [maxFrames]bool{}
	j.last = [FrameSamples]int16{}
	j.next = seq
	j.count, j.dry, j.concealed = 0, 0, 0
	j.primed = false
}

// evict drops the packet for seq, if held.
func (j *Jitter) evict(seq uint16) {
	if i := seq % maxFrames; j.have[i] {
		j.have[i] = false
		j.count--
	}
}

// fits reports whether every held packet is within the ring of a buffer
// that starts at seq.
func (j *Jitter) fits(seq uint16) bool {
	for i, ok := range j.have {
		if ok && seqDist(j.slots[i].Seq, seq) >= maxFrames {
			return false
		}
	}
	return true
}
