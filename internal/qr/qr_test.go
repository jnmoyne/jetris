package qr

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

// The encoder was scripts/gen-qr.go's, whose codes phones have scanned; the
// PNGs under testdata are that script's output for these two links (version 6,
// and a version 13 code with a 16-bit character count), and the package must
// render them pixel for pixel.
func TestEncodeMatchesGolden(t *testing.T) {
	cases := []struct{ file, link string }{
		{"lan_v6.png", "http://192.168.1.20:8080/join.html?name=LAN+party&server=ws%3A%2F%2F192.168.1.20%3A4223"},
		{"long_v13.png", "http://some-quite-long-hostname.example.internal:8080/some/deep/path/join.html?name=A+much+longer+server+name+to+push+the+code+past+version+nine+and+into+the+sixteen+bit+character+count+territory+of+the+byte+mode&server=wss%3A%2F%2Fanother-long-hostname.example.internal%3A8443%2Fwith%2Fa%2Fpath%2Ftoo"},
	}
	for _, tc := range cases {
		code, err := Encode([]byte(tc.link))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		f, err := os.Open("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		want, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := code.Image(8, QuietZone)
		if got.Bounds() != want.Bounds() {
			t.Fatalf("%s: rendered %v, golden %v", tc.file, got.Bounds(), want.Bounds())
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, got); err != nil {
			t.Fatal(err)
		}
		b := got.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				gr, _, _, _ := got.At(x, y).RGBA()
				wr, _, _, _ := want.At(x, y).RGBA()
				if gr != wr {
					t.Fatalf("%s: pixel (%d,%d) differs from the golden", tc.file, x, y)
				}
			}
		}
	}
}

// The geometry the standard fixes: a version's size, and the three finders in
// their corners with a light separator around each.
func TestEncodeGeometry(t *testing.T) {
	for _, n := range []int{1, 20, 100, 300, 660} {
		code, err := Encode(bytes.Repeat([]byte("x"), n))
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if code.Size != 4*code.Version+17 {
			t.Fatalf("%d bytes: version %d is %d modules wide", n, code.Version, code.Size)
		}
		for _, c := range [][2]int{{0, 0}, {0, code.Size - 7}, {code.Size - 7, 0}} {
			for dr := 0; dr < 7; dr++ {
				for dc := 0; dc < 7; dc++ {
					edge := dr == 0 || dr == 6 || dc == 0 || dc == 6
					core := dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4
					if got := code.Dark(c[0]+dr, c[1]+dc); got != (edge || core) {
						t.Fatalf("%d bytes: finder at %v has module (%d,%d) = %v", n, c, dr, dc, got)
					}
				}
			}
		}
	}
	if _, err := Encode(bytes.Repeat([]byte("x"), 700)); err == nil {
		t.Fatal("700 bytes fit, but version 20 holds 666")
	}
}
