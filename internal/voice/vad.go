package voice

import "math"

// The voice activity detector is what keeps a microphone off the wire while
// its owner is not talking: fifty packets a second from every player who
// merely has a microphone open would be most of the room's traffic and all
// of its noise. It is deliberately simple — a level per frame, a floor that
// follows the room, a threshold the user's gate above the floor, and a
// hangover — because it runs on every frame and its failures have to be
// obvious to the ear: too tight clips the first syllable, too loose sends
// the fan.

// VAD is the gate for one microphone. GateDb is the user's setting: how far
// above the room's floor a frame has to rise to count as speech, in dB, or
// zero for an open microphone that sends everything. The rest is what the
// gate remembers from one frame to the next.
type VAD struct {
	GateDb float32

	floorDb    float32
	warm, hang int
	open       bool
}

const (
	// quietFloorDb is the floor the gate is measured from when the room is
	// quieter than it: a floor sits near -90 dBFS on a browser whose noise
	// suppression hands over digital silence, and at -70 to -80 on a quiet
	// desktop microphone, and a gate measured from THERE would be twelve
	// decibels of nothing — every setting of the slider under the hiss,
	// none of them any different from the last. From -70 dBFS the slider's
	// range is -69 to -10, which spans speech near a microphone, the
	// noises around it, and a shout over a room the floor cannot follow; a
	// louder room raises the reference with its floor as before.
	quietFloorDb = -70
	// hangFrames is how long the gate stays open after the last frame of
	// speech, 300 ms: the pauses between the words of a sentence.
	hangFrames = 15
	// floorRiseDb is how far the floor rises per frame, and only while the
	// gate is closed, when the room has got louder: 2.5 dB a second. Slow,
	// so that speech — during which it never rises at all — cannot drag the
	// floor up under itself.
	floorRiseDb = 0.05
	// floorFall is the fraction of the way the floor drops toward a quieter
	// frame at once: fast, so the gate tightens as soon as the room goes
	// quiet.
	floorFall = 0.3
	// floorMinDb and floorMaxDb bound the floor: digital silence must not
	// leave the gate hair-triggered, and a room at -20 dBFS is a broken
	// microphone, not a floor to gate above.
	floorMinDb = -90
	floorMaxDb = -20
	// warmFrames is how many frames, 200 ms, seed the floor with the
	// quietest so far before the slow tracking takes over — otherwise a
	// microphone opened mid-sentence would start with the sentence as its
	// floor and take a minute to come down.
	warmFrames = 10
)

// LevelDb is a frame's RMS level in dBFS: 0 for a full-scale square wave,
// -3 for a full-scale sine, and -100 for digital silence (also the lowest
// value reported, so that a single stray bit does not read as -115).
func LevelDb(pcm []int16) float32 {
	var sum float64
	for _, s := range pcm {
		sum += float64(s) * float64(s)
	}
	if sum == 0 {
		return -100
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	return float32(max(20*math.Log10(rms/32768), -100))
}

// Update feeds the gate one frame's level and reports whether the frame is
// to be sent and whether it is the first of a talk-spurt (the packet the
// receiver resets its jitter buffer on).
func (v *VAD) Update(levelDb float32) (open, start bool) {
	switch {
	case v.warm < warmFrames:
		if v.warm == 0 || levelDb < v.floorDb {
			v.floorDb = levelDb
		}
		v.warm++
	case levelDb < v.floorDb:
		v.floorDb += floorFall * (levelDb - v.floorDb)
	case !v.open:
		v.floorDb += floorRiseDb
	}
	v.floorDb = min(max(v.floorDb, floorMinDb), floorMaxDb)

	speech := v.GateDb == 0 || levelDb > v.ThresholdDb()
	switch {
	case speech:
		start = !v.open
		v.open = true
		v.hang = hangFrames
	case v.open && v.hang > 0:
		v.hang--
	default:
		v.open = false
	}
	return v.open, start
}

// FloorDb is the gate's estimate of the room: the level the microphone
// reads when nobody is talking.
func (v *VAD) FloorDb() float32 {
	return v.floorDb
}

// ThresholdDb is the level a frame has to exceed to open the gate — the
// gate above the room's floor, or above quietFloorDb where the room is
// quieter than that — or, for an open microphone, the floor itself, so a
// meter can still draw the line.
func (v *VAD) ThresholdDb() float32 {
	if v.GateDb == 0 {
		return v.floorDb
	}
	return max(v.floorDb, quietFloorDb) + v.GateDb
}

// Open reports whether the gate is open after the last Update.
func (v *VAD) Open() bool {
	return v.open
}
