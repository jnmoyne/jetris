package voice

import (
	"testing"
	"time"
)

// flat is a packet whose frame decodes to FrameSamples copies of v:
// all-zero nibbles leave the predictor where the header put it (at the
// smallest step, a zero code moves it by nothing). It tags a packet with a
// value a test can read back from the frame that played.
func flat(seq uint16, flags uint8, v int16) Packet {
	return Packet{Header: Header{VerCodec: VerCodec, Flags: flags, Seq: seq, Predictor: v}}
}

// pop plays one frame and reports whether it played and its first sample.
func pop(j *Jitter) (bool, int16) {
	var out [FrameSamples]int16
	ok := j.Pop(out[:])
	return ok, out[0]
}

// expect pops the given tags in order, each one a played frame.
func expect(t *testing.T, j *Jitter, tags ...int16) {
	t.Helper()
	for i, want := range tags {
		ok, got := pop(j)
		if !ok || got != want {
			t.Fatalf("pop %d: ok=%v value=%d, want %d", i, ok, got, want)
		}
	}
}

var t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// Packets in order: nothing plays until primeFrames are in, then each pops
// in turn and decodes exactly as DecodeFrame would from its own header.
func TestJitterInOrder(t *testing.T) {
	frames := SineFrames(440, -20, 6)
	var enc CodecState
	pkts := make([]Packet, len(frames))
	for i, pcm := range frames {
		pkts[i].Header = Header{VerCodec: VerCodec, Seq: uint16(i), Predictor: enc.Predictor, StepIndex: enc.StepIndex}
		EncodeFrame(&enc, pcm, pkts[i].Data[:])
	}
	var j Jitter
	for i := range primeFrames {
		if j.Primed() {
			t.Fatalf("primed after %d packets", i)
		}
		if ok, _ := pop(&j); ok {
			t.Fatalf("popped a frame after %d packets", i)
		}
		j.Push(pkts[i], t0.Add(time.Duration(i)*20*time.Millisecond))
	}
	if !j.Primed() {
		t.Fatalf("not primed after %d packets", primeFrames)
	}
	if j.LastHeard != t0.Add(40*time.Millisecond) {
		t.Fatalf("LastHeard = %v", j.LastHeard)
	}
	for i := primeFrames; i < len(pkts); i++ {
		j.Push(pkts[i], t0)
	}
	for i, p := range pkts {
		var want, got [FrameSamples]int16
		DecodeFrame(CodecState{Predictor: p.Predictor, StepIndex: p.StepIndex}, p.Data[:], want[:])
		if !j.Pop(got[:]) {
			t.Fatalf("pop %d produced nothing", i)
		}
		if got != want {
			t.Fatalf("pop %d decoded differently from DecodeFrame", i)
		}
	}
}

// Packets 2, 1, 3 arriving in that order play as 1, 2, 3: before playback
// has started, an earlier packet is the spurt's start, not a late one.
func TestJitterReorder(t *testing.T) {
	var j Jitter
	j.Push(flat(2, 0, 2), t0)
	j.Push(flat(1, 0, 1), t0)
	j.Push(flat(3, 0, 3), t0)
	expect(t, &j, 1, 2, 3)
	// Once playing, a packet whose turn has passed is dropped: what follows
	// 4 is the concealment of a missing 5, not the late 2.
	j.Push(flat(4, 0, 4), t0)
	j.Push(flat(2, 0, 99), t0)
	expect(t, &j, 4, 2)
}

// A lost packet is covered by the last frame at half volume, then playback
// resumes with the packet after it; two losses in a row fade further, and
// a third is silence.
func TestJitterLoss(t *testing.T) {
	var j Jitter
	for s := range uint16(5) {
		j.Push(flat(s, 0, 1000), t0)
	}
	expect(t, &j, 1000, 1000, 1000, 1000, 1000)
	j.Push(flat(6, 0, 6), t0)
	j.Push(flat(7, 0, 7), t0)
	expect(t, &j, 500, 6, 7)

	j.Push(flat(11, 0, 11), t0)
	ok, v := pop(&j)
	if !ok || v != 3 {
		t.Fatalf("first concealed frame: ok=%v value=%d, want 3 (7/2)", ok, v)
	}
	ok, v = pop(&j)
	if !ok || v != 1 {
		t.Fatalf("second concealed frame: ok=%v value=%d, want 1 (7/4)", ok, v)
	}
	ok, v = pop(&j)
	if ok || v != 0 {
		t.Fatalf("third missing frame: ok=%v value=%d, want silence and false", ok, v)
	}
	expect(t, &j, 11)
}

// Twelve packets pushed with no pops: the ring holds eight, so the four
// oldest are dropped and playback resumes from the fifth.
func TestJitterOverflowCatchUp(t *testing.T) {
	var j Jitter
	for s := range uint16(12) {
		j.Push(flat(s, 0, int16(s)), t0)
	}
	expect(t, &j, 4, 5, 6, 7, 8, 9, 10, 11)
	if j.count != 0 {
		t.Fatalf("count = %d after popping everything", j.count)
	}
}

// FlagStart empties the buffer and starts a new spurt from that packet's
// sequence number, priming again — whatever it was doing before.
func TestJitterFlagStart(t *testing.T) {
	var j Jitter
	for s := range uint16(3) {
		j.Push(flat(s, 0, int16(s)), t0)
	}
	expect(t, &j, 0)
	j.Push(flat(100, FlagStart, 100), t0)
	if j.Primed() {
		t.Fatal("primed straight after a start packet")
	}
	if ok, _ := pop(&j); ok {
		t.Fatal("played while priming a new spurt")
	}
	j.Push(flat(101, 0, 101), t0)
	j.Push(flat(102, 0, 102), t0)
	// Then the concealment of 102 — nothing of the old spurt's 1 and 2.
	expect(t, &j, 100, 101, 102, 51, 25)
	if ok, v := pop(&j); ok || v != 0 {
		t.Fatalf("after the new spurt: ok=%v value=%d, want silence", ok, v)
	}
}

// A gap too wide to bridge, or a burst too stale to be this spurt's, also
// starts afresh from the packet's sequence.
func TestJitterGapAndStaleBurst(t *testing.T) {
	var j Jitter
	for s := range uint16(3) {
		j.Push(flat(s, 0, int16(s)), t0)
	}
	expect(t, &j, 0, 1, 2)
	j.Push(flat(20, 0, 20), t0)
	if j.Primed() {
		t.Fatal("primed across a gap of 17 frames")
	}
	j.Push(flat(21, 0, 21), t0)
	j.Push(flat(22, 0, 22), t0)
	expect(t, &j, 20)
	j.Push(flat(5, 0, 5), t0)
	if j.Primed() || j.next != 5 {
		t.Fatalf("after a stale burst: primed=%v next=%d, want fresh at 5", j.Primed(), j.next)
	}
}

// Sequence numbers wrap: 0xFFFE, 0xFFFF, 0, 1 are consecutive.
func TestJitterWrap(t *testing.T) {
	var j Jitter
	j.Push(flat(0xFFFD, FlagStart, -3), t0)
	j.Push(flat(0xFFFE, 0, -2), t0)
	j.Push(flat(0xFFFF, 0, -1), t0)
	j.Push(flat(0, 0, 0), t0)
	j.Push(flat(1, 0, 1), t0)
	expect(t, &j, -3, -2, -1, 0, 1)
	// Wrap during a loss and during a reorder, too.
	var k Jitter
	k.Push(flat(0xFFFF, FlagStart, 10), t0)
	k.Push(flat(1, 0, 12), t0)
	k.Push(flat(0, 0, 11), t0)
	expect(t, &k, 10, 11, 12)
	k.Push(flat(3, 0, 14), t0)
	expect(t, &k, 6, 14)
}

// Once the frames run dry for dryFrames pops the spurt is over: the buffer
// un-primes and the next packet, whatever its sequence, starts a new one.
func TestJitterDryUnprimes(t *testing.T) {
	var j Jitter
	for s := range uint16(3) {
		j.Push(flat(s, 0, 8), t0)
	}
	expect(t, &j, 8, 8, 8)
	for i := range concealMax {
		if ok, _ := pop(&j); !ok {
			t.Fatalf("concealed frame %d did not play", i)
		}
	}
	for i := range dryFrames {
		if ok, _ := pop(&j); ok {
			t.Fatalf("dry pop %d played", i)
		}
		if !j.Primed() {
			t.Fatalf("un-primed after %d dry pops, want %d", i+1, dryFrames+1)
		}
	}
	pop(&j)
	if j.Primed() {
		t.Fatalf("still primed after %d dry pops", dryFrames+1)
	}
	j.Push(flat(500, 0, 50), t0)
	if j.next != 500 || j.Primed() {
		t.Fatalf("after the spurt: next=%d primed=%v, want a fresh start at 500", j.next, j.Primed())
	}
	j.Push(flat(501, 0, 51), t0)
	j.Push(flat(502, 0, 52), t0)
	expect(t, &j, 50, 51, 52)
}
