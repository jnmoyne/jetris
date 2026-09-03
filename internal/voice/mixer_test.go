package voice

import "testing"

// Two sources sum sample by sample, a sum past a sample's range clamps at
// either end, and no sources at all is silence — even over a dirty dst.
func TestMix(t *testing.T) {
	var scratch [FrameSamples]int32
	a, b := make([]int16, FrameSamples), make([]int16, FrameSamples)
	for i := range a {
		a[i] = int16(i)
		b[i] = int16(-2 * i)
	}
	dst := make([]int16, FrameSamples)
	Mix(dst, &scratch, a, b)
	for i, s := range dst {
		if s != int16(-i) {
			t.Fatalf("sample %d = %d, want %d", i, s, -i)
		}
	}

	loud := make([]int16, FrameSamples)
	for i := range loud {
		loud[i] = 30000
		if i%2 == 1 {
			loud[i] = -30000
		}
	}
	Mix(dst, &scratch, loud, loud, loud)
	for i, s := range dst {
		want := int16(32767)
		if i%2 == 1 {
			want = -32768
		}
		if s != want {
			t.Fatalf("sample %d = %d, want clamped %d", i, s, want)
		}
	}

	Mix(dst, &scratch)
	for i, s := range dst {
		if s != 0 {
			t.Fatalf("sample %d = %d with no sources", i, s)
		}
	}

	Mix(dst, &scratch, a)
	for i, s := range dst {
		if s != a[i] {
			t.Fatalf("sample %d = %d from a single source, want %d", i, s, a[i])
		}
	}
}
