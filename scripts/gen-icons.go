//go:build ignore

// gen-icons draws the browser page's icons — web/apple-touch-icon.png (the
// 180×180 home-screen / bookmark icon iOS asks for) and web/favicon.ico (a
// 32×32 tab icon, a PNG in the ICO container every browser reads) — in the
// game's own look: a J tetromino in its coral, each cell split into the four
// panels of the NATS mark (board.go's drawCell), on the page's black. Run with `go run scripts/gen-icons.go` from the repo root; the
// results are committed, this keeps them reproducible.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

var (
	bg    = color.NRGBA{A: 0xff}
	coral = color.NRGBA{R: 0xea, G: 0x5a, B: 0x47, A: 0xff} // render.pieceColors[PieceJ]
)

// lerp mixes c toward to by t (0..1) — board.go's lighten/darken.
func lerp(c, to color.NRGBA, t float64) color.NRGBA {
	mix := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.NRGBA{R: mix(c.R, to.R), G: mix(c.G, to.G), B: mix(c.B, to.B), A: c.A}
}

var (
	white = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	black = color.NRGBA{A: 0xff}
)

// The J piece in its spawn orientation (game.cellOffsets): (row, col).
var jCells = [][2]int{{0, 0}, {1, 0}, {1, 1}, {1, 2}}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// icon renders the piece centered on a size×size black square with cells of
// cell px, each a gap px black frame around the four panels of the NATS
// mark in tints of the piece's colour (board.go's drawCell: lighter
// top-left, the colour top-right, darker bottom-left, a touch lighter
// bottom-right).
func icon(size, cell, gap int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fill(img, img.Rect, bg)
	w, h := 3*cell, 2*cell
	ox, oy := (size-w)/2, (size-h)/2
	for _, rc := range jCells {
		x, y := ox+rc[1]*cell+gap, oy+rc[0]*cell+gap
		in := cell - 2*gap
		mx, my := x+in/2, y+in/2
		fill(img, image.Rect(x, y, mx, my), lerp(coral, white, 0.28))
		fill(img, image.Rect(mx, y, x+in, my), coral)
		fill(img, image.Rect(x, my, mx, y+in), lerp(coral, black, 0.35))
		fill(img, image.Rect(mx, my, x+in, y+in), lerp(coral, white, 0.12))
	}
	return img
}

func writePNG(path string, img image.Image) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

// writeICO wraps one PNG in the ICO container: ICONDIR, one ICONDIRENTRY,
// the PNG bytes.
func writeICO(path string, img image.Image, size int) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		log.Fatal(err)
	}
	var out bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&out, le, uint16(0))  // reserved
	binary.Write(&out, le, uint16(1))  // type: icon
	binary.Write(&out, le, uint16(1))  // one image
	out.WriteByte(byte(size))          // width (0 would mean 256)
	out.WriteByte(byte(size))          // height
	out.WriteByte(0)                   // palette colours
	out.WriteByte(0)                   // reserved
	binary.Write(&out, le, uint16(1))  // colour planes
	binary.Write(&out, le, uint16(32)) // bits per pixel
	binary.Write(&out, le, uint32(pngBuf.Len()))
	binary.Write(&out, le, uint32(6+16)) // image offset
	out.Write(pngBuf.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	writePNG("web/apple-touch-icon.png", icon(180, 44, 2))
	writeICO("web/favicon.ico", icon(32, 8, 1), 32)
}
