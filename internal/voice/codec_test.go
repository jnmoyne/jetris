package voice

import (
	"math"
	"testing"
)

// A sine through the codec comes back close to itself: the ratio of the
// signal to what the codec did to it is well over 20 dB for a tone at a
// speaking level, and silence codes to silence.
func TestCodecRoundTrip(t *testing.T) {
	frames := SineFrames(440, -20, 10)
	var enc CodecState
	var sig, noise float64
	for _, pcm := range frames {
		st := enc
		var data [FrameBytes]byte
		EncodeFrame(&enc, pcm, data[:])
		var out [FrameSamples]int16
		DecodeFrame(st, data[:], out[:])
		for i := range pcm {
			d := float64(out[i]) - float64(pcm[i])
			sig += float64(pcm[i]) * float64(pcm[i])
			noise += d * d
		}
	}
	if snr := 10 * math.Log10(sig/noise); snr < 20 {
		t.Fatalf("sine round trip SNR = %.1f dB, want > 20", snr)
	}

	var st CodecState
	var data [FrameBytes]byte
	EncodeFrame(&st, make([]int16, FrameSamples), data[:])
	var out [FrameSamples]int16
	DecodeFrame(CodecState{}, data[:], out[:])
	for i, s := range out {
		if s < -8 || s > 8 {
			t.Fatalf("silence decoded to %d at sample %d", s, i)
		}
	}
	if st.StepIndex > maxStepIndex {
		t.Fatalf("step index ran off the table: %d", st.StepIndex)
	}
}

// Encoding is deterministic — the same state and samples code the same
// bytes — and the state the encoder advances to is the one a decoder of
// those bytes ends in.
func TestCodecLockstep(t *testing.T) {
	pcm := SineFrames(1000, -6, 1)[0]
	a, b := CodecState{Predictor: 123, StepIndex: 10}, CodecState{Predictor: 123, StepIndex: 10}
	var da, db [FrameBytes]byte
	EncodeFrame(&a, pcm, da[:])
	EncodeFrame(&b, pcm, db[:])
	if da != db || a != b {
		t.Fatal("encoding the same frame twice differed")
	}
	dec := CodecState{Predictor: 123, StepIndex: 10}
	var out [FrameSamples]int16
	for i, by := range da {
		out[2*i] = decodeSample(&dec, by&0x0f)
		out[2*i+1] = decodeSample(&dec, by>>4)
	}
	if dec != a {
		t.Fatalf("decoder ended at %+v, encoder at %+v", dec, a)
	}
}

// Every field of the header survives the wire, extremes included, and
// anything else is refused.
func TestPacketRoundTrip(t *testing.T) {
	p := Packet{Header: Header{VerCodec: VerCodec, Flags: FlagStart, Seq: 0xFFFF, Predictor: -32768, StepIndex: 88}}
	for i := range p.Data {
		p.Data[i] = byte(i)
	}
	b := p.Marshal(nil)
	if len(b) != PacketBytes {
		t.Fatalf("marshalled %d bytes, want %d", len(b), PacketBytes)
	}
	q, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if q != p {
		t.Fatalf("round trip = %+v, want %+v", q.Header, p.Header)
	}
	if _, err := Unmarshal(b[:PacketBytes-1]); err == nil {
		t.Fatal("a short packet unmarshalled")
	}
	bad := append([]byte(nil), b...)
	bad[0] = 0x21
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("a packet of another version unmarshalled")
	}
	bad = append([]byte(nil), b...)
	bad[6] = 89
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("a packet with a step index off the table unmarshalled")
	}
}
