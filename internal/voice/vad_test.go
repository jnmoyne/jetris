package voice

import (
	"math"
	"testing"
)

// feed runs n frames at one level through the gate and reports how many
// left it open and how many began a talk-spurt.
func feed(v *VAD, level float32, n int) (opens, starts int) {
	for range n {
		open, start := v.Update(level)
		if open {
			opens++
		}
		if start {
			starts++
		}
	}
	return opens, starts
}

// The meter: digital silence is the -100 sentinel, a full-scale square wave
// is 0 dBFS and a sine reads 3 dB under its peak.
func TestLevelDb(t *testing.T) {
	if got := LevelDb(make([]int16, FrameSamples)); got != -100 {
		t.Fatalf("silence = %.1f dB, want -100", got)
	}
	square := make([]int16, FrameSamples)
	for i := range square {
		square[i] = 32767
		if i%2 == 1 {
			square[i] = -32767
		}
	}
	if got := LevelDb(square); math.Abs(float64(got)) > 0.01 {
		t.Fatalf("full-scale square = %.2f dB, want 0", got)
	}
	if got := LevelDb(SineFrames(440, -20, 1)[0]); math.Abs(float64(got)+23.01) > 0.1 {
		t.Fatalf("-20 dBFS sine = %.2f dB, want -23.0", got)
	}
	one := make([]int16, FrameSamples)
	one[0] = 1
	if got := LevelDb(one); got < -100 {
		t.Fatalf("a single stray bit read %.1f dB, below the silence sentinel", got)
	}
}

// A quiet room never opens the gate, however small the gate, and the floor
// settles on the room's level.
func TestVADSilenceStaysClosed(t *testing.T) {
	for _, gate := range []float32{12, 6, 1} {
		v := VAD{GateDb: gate}
		if opens, starts := feed(&v, -70, 200); opens != 0 || starts != 0 {
			t.Fatalf("gate %.0f: a -70 dB room opened %d frames (%d starts)", gate, opens, starts)
		}
		if f := v.FloorDb(); math.Abs(float64(f)+70) > 0.1 {
			t.Fatalf("gate %.0f: floor = %.2f after a -70 dB room, want ≈ -70", gate, f)
		}
		if opens, _ := feed(&v, -100, 50); opens != 0 || v.Open() {
			t.Fatalf("gate %.0f: digital silence opened the gate %d frames", gate, opens)
		}
	}
	// Digital silence from the first frame: the floor clamps at floorMinDb
	// and the threshold, measured from quietFloorDb, keeps the -100
	// sentinel out.
	v := VAD{GateDb: 1}
	if opens, _ := feed(&v, -100, 50); opens != 0 {
		t.Fatalf("digital silence opened a 1 dB gate %d frames", opens)
	}
	if f := v.FloorDb(); f != floorMinDb {
		t.Fatalf("floor = %.1f on digital silence, want the %d clamp", f, floorMinDb)
	}
}

// Speech opens the gate on its first loud frame — with start on that frame
// and no other — and the gate closes exactly hangFrames frames after the
// last loud one.
func TestVADOnsetAndHangover(t *testing.T) {
	v := VAD{GateDb: 12}
	feed(&v, -70, 50)
	if open, start := v.Update(-30); !open || !start {
		t.Fatalf("onset: open=%v start=%v, want both", open, start)
	}
	if opens, starts := feed(&v, -30, 19); opens != 19 || starts != 0 {
		t.Fatalf("during speech: open %d/19 frames, %d starts", opens, starts)
	}
	for i := 1; i <= hangFrames; i++ {
		if open, start := v.Update(-70); !open || start {
			t.Fatalf("hangover frame %d: open=%v start=%v, want open and no start", i, open, start)
		}
	}
	if open, _ := v.Update(-70); open {
		t.Fatalf("still open %d frames after speech stopped", hangFrames+1)
	}
	// Speech inside the hangover keeps it open without a new start.
	v.Update(-30)
	feed(&v, -70, 5)
	if open, start := v.Update(-30); !open || start {
		t.Fatalf("speech during the hangover: open=%v start=%v, want open with no start", open, start)
	}
}

// A gate of zero is an open microphone: open from the first frame, digital
// silence included, with one start and never another.
func TestVADOpenMic(t *testing.T) {
	var v VAD
	if open, start := v.Update(-100); !open || !start {
		t.Fatalf("first frame: open=%v start=%v, want both", open, start)
	}
	for _, level := range []float32{-100, -70, -30, -100, -20} {
		if opens, starts := feed(&v, level, 40); opens != 40 || starts != 0 {
			t.Fatalf("at %.0f dB: open %d/40 frames, %d starts", level, opens, starts)
		}
	}
	if th := v.ThresholdDb(); th != v.FloorDb() {
		t.Fatalf("open mic threshold = %.1f, want the floor %.1f", th, v.FloorDb())
	}
}

// In a -40 dB room, "speech" at -45 dB never opens a 12 dB gate: the floor
// tracks the room, whether the quiet passages come first or after.
func TestVADQuietSpeechInLoudRoom(t *testing.T) {
	for _, lead := range []int{0, 100} {
		v := VAD{GateDb: 12}
		feed(&v, -40, lead)
		for range 10 {
			if opens, _ := feed(&v, -45, 20); opens != 0 {
				t.Fatalf("lead %d: -45 dB opened the gate in a -40 dB room", lead)
			}
			if opens, _ := feed(&v, -40, 20); opens != 0 {
				t.Fatalf("lead %d: the -40 dB room itself opened the gate", lead)
			}
		}
		if th := v.ThresholdDb(); th < -35 {
			t.Fatalf("lead %d: threshold %.1f sits below the room's own level", lead, th)
		}
	}
}

// A three-second sentence at -25 dB, with the short dips to -38 between its
// words, never closes the gate — and the floor, which only rises while the
// gate is closed, is still the room's when the sentence ends.
func TestVADLongSentence(t *testing.T) {
	v := VAD{GateDb: 12}
	feed(&v, -60, 100)
	floor := v.FloorDb()
	for i := range 150 {
		level := float32(-25)
		if i%20 < 3 {
			level = -38
		}
		open, start := v.Update(level)
		if !open {
			t.Fatalf("frame %d at %.0f dB closed the gate", i, level)
		}
		if start != (i == 0) {
			t.Fatalf("frame %d: start=%v", i, start)
		}
	}
	if f := v.FloorDb(); math.Abs(float64(f-floor)) > 0.5 {
		t.Fatalf("floor moved from %.2f to %.2f under a sentence", floor, f)
	}
	if opens, _ := feed(&v, -60, hangFrames+1); opens != hangFrames {
		t.Fatalf("after the sentence: open %d frames, want the %d hangover", opens, hangFrames)
	}
}

// The threshold is the gate above the room's floor — or above quietFloorDb
// in a room quieter than that, digital silence included — so that every
// setting of the slider is a different threshold, from -69 to -10 dBFS in
// a quiet room.
func TestVADThreshold(t *testing.T) {
	for _, room := range []float32{-100, -90, -70} {
		v := VAD{GateDb: 12}
		feed(&v, room, 20)
		if th := v.ThresholdDb(); math.Abs(float64(th-(quietFloorDb+12))) > 0.1 {
			t.Fatalf("threshold in a %.0f dB room = %.1f, want %d", room, th, quietFloorDb+12)
		}
		last := float32(-1000)
		for gate := float32(1); gate <= 60; gate++ {
			v.GateDb = gate
			th := v.ThresholdDb()
			if th <= last {
				t.Fatalf("gate %.0f: threshold %.1f is not above gate %.0f's %.1f", gate, th, gate-1, last)
			}
			last = th
		}
		if math.Abs(float64(last-(quietFloorDb+60))) > 0.1 {
			t.Fatalf("gate 60 in a quiet room: threshold %.1f, want %d", last, quietFloorDb+60)
		}
	}
	v := VAD{GateDb: 12}
	feed(&v, -40, 20)
	if th := v.ThresholdDb(); math.Abs(float64(th+28)) > 0.1 {
		t.Fatalf("threshold over a -40 dB floor = %.1f, want -28", th)
	}
	// Speech at -45 dB clears a 12 dB gate in a quiet room and not in a
	// -40 dB one.
	quiet := VAD{GateDb: 12}
	feed(&quiet, -90, 20)
	if open, _ := quiet.Update(-45); !open {
		t.Fatal("-45 dB did not open a 12 dB gate in a quiet room")
	}
	if open, _ := v.Update(-45); open {
		t.Fatal("-45 dB opened a 12 dB gate in a -40 dB room")
	}
}
