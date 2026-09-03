package voice

// Mix sums srcs into dst, all FrameSamples long, and clamps the result to
// a sample. The sum is taken in int32 — two players at full scale overflow
// an int16 — in scratch, the caller's reusable array, so that the audio
// thread mixes without allocating. With no sources dst is silence.
func Mix(dst []int16, scratch *[FrameSamples]int32, srcs ...[]int16) {
	n := min(len(dst), FrameSamples)
	if len(srcs) == 0 {
		clear(dst[:n])
		return
	}
	clear(scratch[:n])
	for _, src := range srcs {
		for i, s := range src[:min(n, len(src))] {
			scratch[i] += int32(s)
		}
	}
	for i, s := range scratch[:n] {
		dst[i] = clampSample(s)
	}
}
