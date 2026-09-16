package nativeui

// Renders the login screen's backdrop (backdrop.go, login-backdrop.jpg): Team
// A's cyan-framed well and Team B's magenta one, each drawn by the game's own
// board drawer — the NATS palette, the four-panel cells — then stood up in
// perspective over the NATS "N" mark lying on the floor, with a neon glow
// around each frame and a soft shadow under it. Skipped unless
// FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestRenderLoginBackdrop
//	cp /tmp/login-backdrop.jpg internal/nativeui/
//
// The boards are rendered flat through the headless window; the composition
// — the floor, the perspective warps, the glow, the shadows, the JPEG — is
// plain image arithmetic below, so the artwork is reproducible from the code
// whenever the look of the cells changes.

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/render"
)

// The backdrop's size (the original artwork's) and the flat board render.
const (
	backdropW, backdropH = 1672, 941
	bdCols, bdRows       = 20, 20 // the wells: a teams board with company, square
	bdCell               = 28
	bdFlatW, bdFlatH     = 700, 720 // the flat render's window: the board, its label, room for the glow
	bdGlowRadius         = 26
)

// bdPiece is one piece on a backdrop board: falling (drawn active, its ghost
// at the bottom) or landed.
type bdPiece struct {
	p       game.Piece
	falling bool
}

// backdropBoard lays the pieces on an empty bdCols×bdRows board: the landed
// ones dropped from the top of their column and locked where they rest, in
// order, the falling ones active where they are given, with their hard-drop
// ghost where they would land. The falling pieces are the team's (their
// outline is its colour).
func backdropBoard(team int, pieces []bdPiece) (engine.BoardSnapshot, map[[2]int]game.PieceType) {
	rows := make([]game.Row, bdRows)
	for r := range rows {
		rows[r] = game.Row{Cells: make([]game.Cell, bdCols)}
	}
	fits := func(p game.Piece) bool {
		for _, rc := range p.Cells() {
			if rc[0] < 0 || rc[0] >= bdRows || rc[1] < 0 || rc[1] >= bdCols || rows[rc[0]].Cells[rc[1]].Occupied {
				return false
			}
		}
		return true
	}
	drop := func(p game.Piece) game.Piece {
		for fits(game.Piece{Type: p.Type, Orientation: p.Orientation, Row: p.Row + 1, Col: p.Col}) {
			p.Row++
		}
		return p
	}
	for _, bp := range pieces {
		if bp.falling {
			continue
		}
		p := bp.p
		p.Row = 0
		for _, rc := range drop(p).Cells() {
			rows[rc[0]].Cells[rc[1]] = game.Cell{Occupied: true, PieceType: p.Type}
		}
	}
	ghost := map[[2]int]game.PieceType{}
	for _, bp := range pieces {
		if !bp.falling {
			continue
		}
		for _, rc := range drop(bp.p).Cells() {
			ghost[[2]int{rc[0], rc[1]}] = bp.p.Type
		}
		for _, rc := range bp.p.Cells() {
			rows[rc[0]].Cells[rc[1]] = game.Cell{Active: true, PieceType: bp.p.Type, PlayerIdx: team}
		}
	}
	return engine.BoardSnapshot{Width: bdCols, Height: bdRows, Rows: rows}, ghost
}

// bdKey is the flat render's background, turned transparent after capture.
var bdKey = color.NRGBA{R: 1, G: 2, B: 3, A: 0xff}

// renderFlatBoard draws one labelled, neon-framed well through the headless
// window and returns it over a transparent background, the neon glow added,
// with the rectangle of the well's frame inside the image.
func renderFlatBoard(t *testing.T, w *headless.Window, a *App, label string, team int, pieces []bdPiece) (*image.NRGBA, image.Rectangle) {
	t.Helper()
	snap, ghost := backdropBoard(team, pieces)
	col := render.PlayerColorRGBA(team)
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(bdFlatW, bdFlatH)),
		Source:      new(input.Router).Source(),
	}
	fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, bdKey)
	layout.Center.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(22), label, col).Layout),
			layout.Rigid(spacer(10)),
			layout.Rigid(a.boardWidget(snap, -1, bdCell, false, &boardFX{tint: col, frame: col, ghost: ghost}, time.Unix(0, 0), false)),
		)
	})
	if err := w.Frame(&ops); err != nil {
		t.Fatalf("frame %s: %v", label, err)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, bdFlatW, bdFlatH))
	if err := w.Screenshot(rgba); err != nil {
		t.Fatalf("screenshot %s: %v", label, err)
	}
	// Key out the background; find what was drawn.
	img := image.NewNRGBA(rgba.Bounds())
	bbox := image.Rectangle{}
	for y := 0; y < bdFlatH; y++ {
		for x := 0; x < bdFlatW; x++ {
			c := rgba.RGBAAt(x, y)
			if c.R == bdKey.R && c.G == bdKey.G && c.B == bdKey.B {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: 0xff})
			p := image.Rect(x, y, x+1, y+1)
			if bbox.Empty() {
				bbox = p
			} else {
				bbox = bbox.Union(p)
			}
		}
	}
	// The well is the square at the bottom of what was drawn (the label sits
	// above it, narrower).
	side := bdCols*bdCell + 2*max(bdCell/8, 2)
	frame := image.Rect(bbox.Min.X, bbox.Max.Y-side, bbox.Min.X+side, bbox.Max.Y)
	addGlow(img, frame, col, bdGlowRadius, 0.85)
	return img, frame
}

// addGlow lays a neon halo of col around r: the farther from the frame the
// fainter, quadratically, gone at radius px.
func addGlow(img *image.NRGBA, r image.Rectangle, col color.NRGBA, radius int, strength float64) {
	b := img.Bounds()
	for y := max(b.Min.Y, r.Min.Y-radius); y < min(b.Max.Y, r.Max.Y+radius); y++ {
		for x := max(b.Min.X, r.Min.X-radius); x < min(b.Max.X, r.Max.X+radius); x++ {
			if (image.Point{x, y}).In(r) {
				continue
			}
			dx := math.Max(0, math.Max(float64(r.Min.X-x), float64(x-r.Max.X+1)))
			dy := math.Max(0, math.Max(float64(r.Min.Y-y), float64(y-r.Max.Y+1)))
			d := math.Hypot(dx, dy)
			if d >= float64(radius) {
				continue
			}
			t := 1 - d/float64(radius)
			blendOver(img, x, y, col, strength*t*t)
		}
	}
}

// blendOver composites col at alpha a over img's pixel (straight alpha).
func blendOver(img *image.NRGBA, x, y int, col color.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	d := img.NRGBAAt(x, y)
	da := float64(d.A) / 255
	oa := a + da*(1-a)
	if oa <= 0 {
		return
	}
	mix := func(s, dc uint8) uint8 {
		return uint8(math.Round((float64(s)*a + float64(dc)*da*(1-a)) / oa))
	}
	img.SetNRGBA(x, y, color.NRGBA{R: mix(col.R, d.R), G: mix(col.G, d.G), B: mix(col.B, d.B), A: uint8(math.Round(oa * 255))})
}

// quad is a destination quadrilateral: top-left, top-right, bottom-right,
// bottom-left, in canvas pixels.
type quad [4][2]float64

// hmat is a projective map: h[0..7] with h[8] = 1, so (x, y) ↦
// ((h0 x + h1 y + h2) / (h6 x + h7 y + 1), (h3 x + h4 y + h5) / (h6 x + h7 y + 1)).
type hmat [9]float64

// homography solves the projective map taking the four src points to the
// four dst points.
func homography(src, dst quad) hmat {
	var m [8][9]float64
	for i := 0; i < 4; i++ {
		x, y := src[i][0], src[i][1]
		u, v := dst[i][0], dst[i][1]
		m[2*i] = [9]float64{x, y, 1, 0, 0, 0, -x * u, -y * u, u}
		m[2*i+1] = [9]float64{0, 0, 0, x, y, 1, -x * v, -y * v, v}
	}
	// Gaussian elimination with partial pivoting.
	for c := 0; c < 8; c++ {
		p := c
		for r := c + 1; r < 8; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[p][c]) {
				p = r
			}
		}
		m[c], m[p] = m[p], m[c]
		for r := 0; r < 8; r++ {
			if r == c || m[r][c] == 0 {
				continue
			}
			f := m[r][c] / m[c][c]
			for k := c; k < 9; k++ {
				m[r][k] -= f * m[c][k]
			}
		}
	}
	var h hmat
	for i := 0; i < 8; i++ {
		h[i] = m[i][8] / m[i][i]
	}
	h[8] = 1
	return h
}

func (h *hmat) apply(x, y float64) (float64, float64) {
	w := h[6]*x + h[7]*y + h[8]
	return (h[0]*x + h[1]*y + h[2]) / w, (h[3]*x + h[4]*y + h[5]) / w
}

// shifted is q moved by (dx, dy).
func (q quad) shifted(dx, dy float64) quad {
	for i := range q {
		q[i][0] += dx
		q[i][1] += dy
	}
	return q
}

// rectQuad is r's corners as a quad.
func rectQuad(r image.Rectangle) quad {
	return quad{{float64(r.Min.X), float64(r.Min.Y)}, {float64(r.Max.X), float64(r.Min.Y)}, {float64(r.Max.X), float64(r.Max.Y)}, {float64(r.Min.X), float64(r.Max.Y)}}
}

// sampleNRGBA reads src bilinearly at (x, y), premultiplied, 0..1; outside
// the image it is transparent.
func sampleNRGBA(src *image.NRGBA, x, y float64) (r, g, b, a float64) {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	at := func(ix, iy int) (float64, float64, float64, float64) {
		if !(image.Point{ix, iy}).In(src.Bounds()) {
			return 0, 0, 0, 0
		}
		c := src.NRGBAAt(ix, iy)
		a := float64(c.A) / 255
		return float64(c.R) / 255 * a, float64(c.G) / 255 * a, float64(c.B) / 255 * a, a
	}
	ix, iy := int(x0), int(y0)
	r00, g00, b00, a00 := at(ix, iy)
	r10, g10, b10, a10 := at(ix+1, iy)
	r01, g01, b01, a01 := at(ix, iy+1)
	r11, g11, b11, a11 := at(ix+1, iy+1)
	lerp2 := func(v00, v10, v01, v11 float64) float64 {
		return (v00*(1-fx)+v10*fx)*(1-fy) + (v01*(1-fx)+v11*fx)*fy
	}
	return lerp2(r00, r10, r01, r11), lerp2(g00, g10, g01, g11), lerp2(b00, b10, b01, b11), lerp2(a00, a10, a01, a11)
}

// warpOnto composites src onto dst so that srcRect lands on dstQuad (the rest
// of src rides along in the same plane), supersampled 3×3 per pixel, alpha
// scaled by opacity.
func warpOnto(dst *image.NRGBA, src *image.NRGBA, srcRect image.Rectangle, dstQuad quad, opacity float64) {
	fwd := homography(rectQuad(srcRect), dstQuad)
	inv := homography(dstQuad, rectQuad(srcRect))
	// The destination footprint of the whole source image.
	bb := image.Rectangle{}
	for _, c := range rectQuad(src.Bounds()) {
		u, v := fwd.apply(c[0], c[1])
		p := image.Rect(int(math.Floor(u)), int(math.Floor(v)), int(math.Ceil(u))+1, int(math.Ceil(v))+1)
		if bb.Empty() {
			bb = p
		} else {
			bb = bb.Union(p)
		}
	}
	bb = bb.Intersect(dst.Bounds())
	const ss = 3
	for y := bb.Min.Y; y < bb.Max.Y; y++ {
		for x := bb.Min.X; x < bb.Max.X; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u, v := inv.apply(float64(x)+(float64(sx)+0.5)/ss, float64(y)+(float64(sy)+0.5)/ss)
					sr, sg, sb, sa := sampleNRGBA(src, u-0.5, v-0.5)
					r, g, b, a = r+sr, g+sg, b+sb, a+sa
				}
			}
			a /= ss * ss
			if a <= 0.002 {
				continue
			}
			col := color.NRGBA{R: uint8(math.Round(r / (ss * ss) / a * 255)), G: uint8(math.Round(g / (ss * ss) / a * 255)), B: uint8(math.Round(b / (ss * ss) / a * 255)), A: 0xff}
			blendOver(dst, x, y, col, a*opacity)
		}
	}
}

// slab gives dstQuad a thickness: a solid card of col warped to the quad
// shifted by (dx, dy), to be drawn under the face.
func slab(dst *image.NRGBA, dstQuad quad, dx, dy float64, col color.NRGBA) {
	shifted := dstQuad
	for i := range shifted {
		shifted[i][0] += dx
		shifted[i][1] += dy
	}
	card := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			card.SetNRGBA(x, y, col)
		}
	}
	warpOnto(dst, card, card.Bounds(), shifted, 1)
}

// darkened is img with its colour scaled by f (alpha kept).
func darkened(img *image.NRGBA, f float64) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{R: uint8(float64(c.R) * f), G: uint8(float64(c.G) * f), B: uint8(float64(c.B) * f), A: c.A})
		}
	}
	return out
}

// dropShadow lays a soft dark shadow under dstQuad, offset by (dx, dy) and
// blurred by blur px.
func dropShadow(dst *image.NRGBA, dstQuad quad, dx, dy float64, blur int, strength float64) {
	shifted := dstQuad
	for i := range shifted {
		shifted[i][0] += dx
		shifted[i][1] += dy
	}
	// A black card warped to the shifted quad, on its own layer.
	card := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range card.Pix {
		card.Pix[i] = 0
	}
	for i := 3; i < len(card.Pix); i += 4 {
		card.Pix[i] = 0xff
	}
	layer := image.NewNRGBA(dst.Bounds())
	warpOnto(layer, card, card.Bounds(), shifted, 1)
	// Box-blur the layer's alpha, twice, then lay black at that alpha.
	b := dst.Bounds()
	alpha := make([]float64, b.Dx()*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			alpha[y*b.Dx()+x] = float64(layer.NRGBAAt(x, y).A) / 255
		}
	}
	for pass := 0; pass < 2; pass++ {
		alpha = boxBlur(alpha, b.Dx(), b.Dy(), blur)
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			blendOver(dst, x, y, color.NRGBA{A: 0xff}, alpha[y*b.Dx()+x]*strength)
		}
	}
}

// boxBlur is a separable box blur of radius r over a w×h float grid.
func boxBlur(src []float64, w, h, r int) []float64 {
	tmp := make([]float64, len(src))
	out := make([]float64, len(src))
	n := float64(2*r + 1)
	for y := 0; y < h; y++ {
		var sum float64
		for x := -r; x <= r; x++ {
			if x >= 0 && x < w {
				sum += src[y*w+x]
			}
		}
		for x := 0; x < w; x++ {
			tmp[y*w+x] = sum / n
			if x-r >= 0 {
				sum -= src[y*w+x-r]
			}
			if x+r+1 < w {
				sum += src[y*w+x+r+1]
			}
		}
	}
	for x := 0; x < w; x++ {
		var sum float64
		for y := -r; y <= r; y++ {
			if y >= 0 && y < h {
				sum += tmp[y*w+x]
			}
		}
		for y := 0; y < h; y++ {
			out[y*w+x] = sum / n
			if y-r >= 0 {
				sum -= tmp[(y-r)*w+x]
			}
			if y+r+1 < h {
				sum += tmp[(y+r+1)*w+x]
			}
		}
	}
	return out
}

// paintBackdropFloor fills the canvas: near-black, a faint pool of light in
// the middle and the neon's colour cast on each side.
func paintBackdropFloor(dst *image.NRGBA) {
	b := dst.Bounds()
	base := [3]float64{6, 7, 11}
	pools := []struct {
		x, y, r float64
		col     [3]float64
		s       float64
	}{
		{836, 520, 980, [3]float64{22, 28, 48}, 1},    // the room's light
		{520, 300, 620, [3]float64{0, 60, 70}, 0.35},  // Team A's cyan cast
		{1120, 330, 620, [3]float64{70, 0, 60}, 0.35}, // Team B's magenta cast
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := base
			for _, p := range pools {
				d := math.Hypot(float64(x)-p.x, float64(y)-p.y) / p.r
				if d >= 1 {
					continue
				}
				t := (1 - d) * (1 - d) * p.s
				for i := range c {
					c[i] += p.col[i] * t
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{R: uint8(math.Min(255, c[0])), G: uint8(math.Min(255, c[1])), B: uint8(math.Min(255, c[2])), A: 0xff})
		}
	}
}

// The scene. The two wells stand side by side on one plane — a wall bdWallGap
// px wide between them, wallQuad where that wall lands on the canvas, each
// well's own quad the projection of its place on the wall through the same
// homography — and the mark's four panels lie on the floor under them (the
// logo's square, its tail riding along below).
const bdWallGap = 56

var (
	bdWallQuad = quad{{326, 88}, {1420, 116}, {1398, 556}, {248, 510}}
	bdQuadLogo = quad{{405, 405}, {1235, 560}, {1235, 800}, {375, 745}}
)

// wellQuads projects the two wells' places on the wall — side px square
// each, bdWallGap apart — onto the canvas.
func wellQuads(side int) (a, b quad) {
	wall := image.Rect(0, 0, 2*side+bdWallGap, side)
	h := homography(rectQuad(wall), bdWallQuad)
	project := func(r image.Rectangle) quad {
		q := rectQuad(r)
		for i := range q {
			q[i][0], q[i][1] = h.apply(q[i][0], q[i][1])
		}
		return q
	}
	return project(image.Rect(0, 0, side, side)), project(image.Rect(side+bdWallGap, 0, 2*side+bdWallGap, side))
}

func TestRenderLoginBackdrop(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render the login backdrop")
	}
	w, err := headless.NewWindow(bdFlatW, bdFlatH)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()
	a := newTestApp()

	P := func(pt game.PieceType, o, row, col int, falling bool) bdPiece {
		return bdPiece{p: game.Piece{Type: pt, Orientation: o, Row: row, Col: col}, falling: falling}
	}
	boardA, frameA := renderFlatBoard(t, w, a, "TEAM A", 0, []bdPiece{
		P(game.PieceT, 0, 2, 5, true), P(game.PieceO, 0, 3, 14, true),
		P(game.PieceZ, 0, 18, 1, false), P(game.PieceI, 0, 18, 6, false),
		P(game.PieceS, 0, 18, 13, false), P(game.PieceJ, 1, 17, 16, false),
	})
	boardB, frameB := renderFlatBoard(t, w, a, "TEAM B", 1, []bdPiece{
		P(game.PieceJ, 1, 2, 3, true), P(game.PieceI, 0, 3, 12, true),
		P(game.PieceL, 0, 18, 0, false), P(game.PieceT, 2, 18, 5, false),
		P(game.PieceZ, 1, 17, 11, false), P(game.PieceS, 0, 18, 15, false),
	})
	for name, img := range map[string]*image.NRGBA{"backdrop_flat_a": boardA, "backdrop_flat_b": boardB} {
		f, err := os.Create(dir + "/" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, img)
		f.Close()
	}

	logoImg, _, err := image.Decode(bytes.NewReader(natsIconPNG))
	if err != nil {
		t.Fatal(err)
	}
	logo := image.NewNRGBA(logoImg.Bounds())
	for y := logo.Bounds().Min.Y; y < logo.Bounds().Max.Y; y++ {
		for x := logo.Bounds().Min.X; x < logo.Bounds().Max.X; x++ {
			logo.Set(x, y, logoImg.At(x, y))
		}
	}
	// The logo's four panels sit inside its square (nats-icon.png: the tail
	// hangs below them).
	lb := logo.Bounds()
	panels := image.Rect(lb.Min.X+lb.Dx()*35/1201, lb.Min.Y+lb.Dy()*20/1200, lb.Min.X+lb.Dx()*1165/1201, lb.Min.Y+lb.Dy()*930/1200)

	bdQuadA, bdQuadB := wellQuads(frameA.Dx())
	canvas := image.NewNRGBA(image.Rect(0, 0, backdropW, backdropH))
	paintBackdropFloor(canvas)
	// The mark lies on the floor as a slab: its darkened self a little lower
	// for the thickness, then its face.
	dropShadow(canvas, bdQuadLogo, 10, 22, 18, 0.6)
	warpOnto(canvas, darkened(logo, 0.42), panels, bdQuadLogo.shifted(0, 14), 1)
	warpOnto(canvas, logo, panels, bdQuadLogo, 0.94)
	// The wells stand on it: a shadow on the floor, a dark slab edge, the
	// neon face.
	wellSlab := color.NRGBA{R: 0x12, G: 0x14, B: 0x1e, A: 0xff}
	dropShadow(canvas, bdQuadB, 28, 40, 16, 0.75)
	slab(canvas, bdQuadB, 7, 9, wellSlab)
	warpOnto(canvas, boardB, frameB, bdQuadB, 1)
	dropShadow(canvas, bdQuadA, 28, 40, 16, 0.75)
	slab(canvas, bdQuadA, 7, 9, wellSlab)
	warpOnto(canvas, boardA, frameA, bdQuadA, 1)

	f, err := os.Create(dir + "/login-backdrop.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
}
