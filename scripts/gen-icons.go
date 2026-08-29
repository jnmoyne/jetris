//go:build ignore

// gen-icons draws the browser page's icons — web/apple-touch-icon.png (the
// 180×180 home-screen / bookmark icon iOS asks for) and web/favicon.ico (a
// 32×32 tab icon, a PNG in the ICO container every browser reads) — in the
// game's 8-bit chrome: a bevelled J tetromino in its own blue on the page's
// black. Run with `go run scripts/gen-icons.go` from the repo root; the
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
	blue  = color.NRGBA{R: 0x00, G: 0x00, B: 0xf0, A: 0xff} // render.pieceColors[PieceJ]
	light = color.NRGBA{R: 0x5c, G: 0x5c, B: 0xff, A: 0xff}
	dark  = color.NRGBA{R: 0x00, G: 0x00, B: 0x90, A: 0xff}
	gloss = color.NRGBA{R: 0xc8, G: 0xc8, B: 0xff, A: 0xff}
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
// cell px and a bevel of bevel px (board.go's drawCell: lighter top/left
// strips, darker bottom/right, a gloss pixel in the corner).
func icon(size, cell, bevel int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fill(img, img.Rect, bg)
	w, h := 3*cell, 2*cell
	ox, oy := (size-w)/2, (size-h)/2
	for _, rc := range jCells {
		x, y := ox+rc[1]*cell, oy+rc[0]*cell
		fill(img, image.Rect(x, y, x+cell, y+cell), blue)
		fill(img, image.Rect(x, y, x+cell, y+bevel), light)                  // top
		fill(img, image.Rect(x, y, x+bevel, y+cell), light)                  // left
		fill(img, image.Rect(x, y+cell-bevel, x+cell, y+cell), dark)         // bottom
		fill(img, image.Rect(x+cell-bevel, y, x+cell, y+cell), dark)         // right
		fill(img, image.Rect(x+bevel, y+bevel, x+2*bevel, y+2*bevel), gloss) // gloss
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
	writePNG("web/apple-touch-icon.png", icon(180, 44, 6))
	writeICO("web/favicon.ico", icon(32, 8, 1), 32)
}
