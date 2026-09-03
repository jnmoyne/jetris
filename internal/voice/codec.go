package voice

// IMA ADPCM (the Interactive Multimedia Association's, the codec of the
// WAV format's "DVI" flavour): each sample is coded as a four-bit multiple
// of an adaptive step, the step growing on large codes and shrinking on
// small ones. Four bits at 16 kHz is exactly 64 kbit/s, and the whole thing
// is a hundred lines of integer arithmetic that runs the same on every
// platform — which is what made it the choice over Opus, which would have
// wanted a native library on the desktop and a different one in the
// browser. Wideband speech at this rate is the "HD voice" of desk phones:
// good enough for a game's chatter.

// CodecState is the encoder's (or decoder's) memory between samples: the
// last reconstructed sample and the index into the step table. The encoder
// updates its own copy from the very same reconstruction the decoder makes,
// so the two stay in lockstep.
type CodecState struct {
	Predictor int16
	StepIndex uint8
}

const maxStepIndex = 88

var stepTable = [maxStepIndex + 1]int32{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

var indexTable = [16]int8{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

// EncodeFrame codes FrameSamples samples of pcm into dst[:FrameBytes],
// advancing st. Callers that packetise the frame copy st into the header
// BEFORE calling, since the header carries the state the frame starts from.
func EncodeFrame(st *CodecState, pcm []int16, dst []byte) {
	for i := 0; i+1 < len(pcm) && i/2 < len(dst); i += 2 {
		lo := encodeSample(st, pcm[i])
		hi := encodeSample(st, pcm[i+1])
		dst[i/2] = lo | hi<<4
	}
}

// DecodeFrame decodes data (FrameBytes of nibbles) into out
// (FrameSamples samples) from the state st, which is the frame's own and is
// not carried past it.
func DecodeFrame(st CodecState, data []byte, out []int16) {
	for i, b := range data {
		if 2*i+1 >= len(out) {
			break
		}
		out[2*i] = decodeSample(&st, b&0x0f)
		out[2*i+1] = decodeSample(&st, b>>4)
	}
}

func encodeSample(st *CodecState, s int16) uint8 {
	step := stepTable[st.StepIndex]
	diff := int32(s) - int32(st.Predictor)
	var nib uint8
	if diff < 0 {
		nib = 8
		diff = -diff
	}
	delta := step >> 3
	if diff >= step {
		nib |= 4
		diff -= step
		delta += step
	}
	step >>= 1
	if diff >= step {
		nib |= 2
		diff -= step
		delta += step
	}
	step >>= 1
	if diff >= step {
		nib |= 1
		delta += step
	}
	st.apply(nib, delta)
	return nib
}

func decodeSample(st *CodecState, nib uint8) int16 {
	step := stepTable[st.StepIndex]
	delta := step >> 3
	if nib&4 != 0 {
		delta += step
	}
	if nib&2 != 0 {
		delta += step >> 1
	}
	if nib&1 != 0 {
		delta += step >> 2
	}
	st.apply(nib, delta)
	return st.Predictor
}

// apply is the reconstruction both sides share: the predictor moved by the
// decoded delta, clamped to a sample, and the step index moved by the code.
func (st *CodecState) apply(nib uint8, delta int32) {
	pred := int32(st.Predictor)
	if nib&8 != 0 {
		pred -= delta
	} else {
		pred += delta
	}
	st.Predictor = clampSample(pred)
	idx := int(st.StepIndex) + int(indexTable[nib])
	st.StepIndex = uint8(min(max(idx, 0), maxStepIndex))
}

func clampSample(v int32) int16 {
	return int16(min(max(v, -32768), 32767))
}
