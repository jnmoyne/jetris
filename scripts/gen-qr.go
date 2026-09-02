//go:build ignore

// gen-qr turns a NATS server's WebSocket URL into a "join this server" link
// for the browser build's join page (web/join.html) and prints that link as a
// QR code: on the terminal, so a phone can scan it straight off the screen,
// and as a PNG to project, print or paste into a chat.
//
// Whoever scans it lands on a page that asks for one thing — their player
// name — and then drops them in that server's lobby; they never see the
// server browser. That is the LAN-party flow: put the QR on the big screen,
// everyone scans it, everyone is in the same lobby.
//
// Run it from the repo root:
//
//	go run scripts/gen-qr.go wss://demo.nats.io:8443
//	go run scripts/gen-qr.go -name "LAN party" -page http://192.168.1.20:8080/ ws://192.168.1.20:8080
//	go run scripts/gen-qr.go                      # asks for the URL and the name
//
// -page is where the browser build is served from: the GitHub Pages copy by
// default, or your own `python3 -m http.server -d dist/web 8080` for a LAN
// party (phones must be able to reach that page AND the NATS server).
//
// The QR encoder below is self-contained — byte mode, error-correction level
// M, versions 1 to 20 — so the script stays a `go run` away with no
// dependency added to the module for it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/url"
	"os"
	"strings"
)

const defaultPage = "https://jnmoyne.github.io/jetris/"

func main() {
	log.SetFlags(0)
	var (
		name   = flag.String("name", "", "name for the server, shown on the join page (optional)")
		page   = flag.String("page", defaultPage, "where the browser build is served from")
		out    = flag.String("out", "jetris-join.png", "PNG to write (empty writes none)")
		scale  = flag.Int("scale", 8, "PNG pixels per QR module")
		border = flag.Int("border", 4, "quiet zone around the code, in modules (4 is the standard minimum)")
		quiet  = flag.Bool("no-ascii", false, "don't print the code on the terminal")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: go run scripts/gen-qr.go [flags] [ws://host:port]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	server := strings.TrimSpace(flag.Arg(0))
	if server == "" {
		server = prompt("NATS server URL (ws:// or wss://): ")
	}
	if server == "" {
		log.Fatal("no server URL given")
	}
	if *name == "" && flag.NArg() == 0 {
		*name = prompt("Server name (optional): ")
	}

	link, err := joinURL(*page, server, *name)
	if err != nil {
		log.Fatal(err)
	}
	code, err := encodeQR([]byte(link))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(link)
	fmt.Println()
	if !*quiet {
		fmt.Print(code.ansi(*border))
		fmt.Println()
	}
	if *out != "" {
		if err := writePNG(*out, code, *scale, *border); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s (%d×%d modules, version %d)\n", *out, code.size, code.size, code.version)
	}
}

// prompt reads one line from the terminal, for the no-arguments run.
func prompt(q string) string {
	fmt.Print(q)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// joinURL builds the join page's link: <page>/join.html?server=…[&name=…].
// It insists on a WebSocket URL — a browser can dial nothing else, so a
// nats:// URL in the QR would take every scanner to a page that cannot
// connect — and warns about the one other combination that always fails: an
// https page may not open a plain ws:// socket (mixed content).
func joinURL(page, server, name string) (string, error) {
	su, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("server URL %q: %w", server, err)
	}
	switch su.Scheme {
	case "ws", "wss":
	case "nats", "tls":
		return "", fmt.Errorf("%s is a %s:// URL — a browser can only dial ws:// or wss://, so give the server's WebSocket listener instead (nats-server's websocket{} block, often port 8080 or 443)", server, su.Scheme)
	default:
		return "", fmt.Errorf("server URL %q: need a ws:// or wss:// URL", server)
	}

	base, err := url.Parse(page)
	if err != nil {
		return "", fmt.Errorf("page URL %q: %w", page, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("page URL %q: need a full URL, e.g. %s", page, defaultPage)
	}
	if base.Scheme == "https" && su.Scheme == "ws" {
		log.Printf("warning: %s is served over https, and a browser refuses a plain ws:// socket from an https page — use wss://, or serve the page over http", base)
	}
	// A base that already names a page ("…/index.html") resolves to its
	// directory; one that names a directory needs the trailing slash or
	// ResolveReference would drop its last element.
	if !strings.HasSuffix(base.Path, "/") && !strings.Contains(base.Path[strings.LastIndex(base.Path, "/")+1:], ".") {
		base.Path += "/"
	}
	q := url.Values{"server": {server}}
	if name != "" {
		q.Set("name", name)
	}
	join, _ := url.Parse("join.html")
	join.RawQuery = q.Encode()
	return base.ResolveReference(join).String(), nil
}

// writePNG renders the code as black modules on white, scale pixels each,
// inside a quiet zone of border modules — the margin decoders need to find
// the code at all.
func writePNG(path string, m *qr, scale, border int) error {
	if scale < 1 {
		scale = 1
	}
	px := (m.size + 2*border) * scale
	img := image.NewGray(image.Rect(0, 0, px, px))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for row := 0; row < m.size; row++ {
		for col := 0; col < m.size; col++ {
			if !m.dark(row, col) {
				continue
			}
			x0, y0 := (col+border)*scale, (row+border)*scale
			for y := y0; y < y0+scale; y++ {
				for x := x0; x < x0+scale; x++ {
					img.SetGray(x, y, color.Gray{})
				}
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ansi draws the code for a terminal, two module rows per text line: the
// upper half block ▀ paints the top row in the foreground colour and the
// bottom row in the background one. The colours are stated rather than left
// to the terminal's palette, so the code comes out dark-on-light — what a
// scanner expects — whatever theme the terminal runs.
func (m *qr) ansi(border int) string {
	const (
		black = 0 // module set
		white = 7 // quiet zone and clear modules (bright, for contrast)
	)
	n := m.size + 2*border
	at := func(row, col int) bool {
		row, col = row-border, col-border
		if row < 0 || col < 0 || row >= m.size || col >= m.size {
			return false
		}
		return m.dark(row, col)
	}
	var b strings.Builder
	for row := 0; row < n; row += 2 {
		for col := 0; col < n; col++ {
			fg, bg := white, white
			if at(row, col) {
				fg = black
			}
			if row+1 < n && at(row+1, col) {
				bg = black
			}
			fmt.Fprintf(&b, "\x1b[%d;%dm\u2580", 30+fg, 100+bg)
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

// --- QR encoding: byte mode, error-correction level M, versions 1 to 20 ---
//
// Level M recovers from about 15% damage, which is what a code read off a
// screen or a printed sheet wants; 20 versions carry 666 bytes, an order of
// magnitude more than any join link.

// ecSpec is one version's error-correction layout at level M: the
// error-correction codewords each block carries, and the two groups of
// blocks the data codewords are dealt into (the second group's blocks hold
// one codeword more, and a version may have none).
type ecSpec struct{ ecPerBlock, g1, g1data, g2, g2data int }

var ecM = [...]ecSpec{
	{}, // there is no version 0
	{10, 1, 16, 0, 0},
	{16, 1, 28, 0, 0},
	{26, 1, 44, 0, 0},
	{18, 2, 32, 0, 0},
	{24, 2, 43, 0, 0},
	{16, 4, 27, 0, 0},
	{18, 4, 31, 0, 0},
	{22, 2, 38, 2, 39},
	{22, 3, 36, 2, 37},
	{26, 4, 43, 1, 44},
	{30, 1, 50, 4, 51},
	{22, 6, 36, 2, 37},
	{22, 8, 37, 1, 38},
	{24, 4, 40, 5, 41},
	{24, 5, 41, 5, 42},
	{28, 7, 45, 3, 46},
	{28, 10, 46, 1, 47},
	{26, 9, 43, 4, 44},
	{26, 3, 44, 11, 45},
	{26, 3, 41, 13, 42},
}

// dataCodewords is how many of a version's codewords carry the message.
func (s ecSpec) dataCodewords() int { return s.g1*s.g1data + s.g2*s.g2data }

// totalCodewords is data plus error correction — every codeword the symbol
// holds.
func (s ecSpec) totalCodewords() int {
	return s.g1*(s.g1data+s.ecPerBlock) + s.g2*(s.g2data+s.ecPerBlock)
}

// qr is a finished symbol: size×size modules, dark where mod is set.
type qr struct {
	version int
	size    int
	mod     []bool // modules, row-major
	fixed   []bool // function patterns and format areas: masking leaves them alone
}

func (m *qr) dark(row, col int) bool     { return m.mod[row*m.size+col] }
func (m *qr) set(row, col int, v bool)   { m.mod[row*m.size+col] = v }
func (m *qr) setFn(row, col int, v bool) { m.set(row, col, v); m.fixed[row*m.size+col] = true }
func (m *qr) isFn(row, col int) bool     { return m.fixed[row*m.size+col] }

// encodeQR encodes data in byte mode at level M, in the smallest version that
// holds it, with the mask that scores best.
func encodeQR(data []byte) (*qr, error) {
	version, spec, err := chooseVersion(len(data))
	if err != nil {
		return nil, err
	}
	m := &qr{version: version, size: 4*version + 17}
	m.mod = make([]bool, m.size*m.size)
	m.fixed = make([]bool, m.size*m.size)
	m.drawFunctionPatterns()
	m.drawCodewords(interleave(bitstream(data, version, spec), spec))

	best, bestScore := 0, -1
	for mask := 0; mask < 8; mask++ {
		m.applyMask(mask)
		m.drawFormat(mask)
		if s := m.penalty(); bestScore < 0 || s < bestScore {
			best, bestScore = mask, s
		}
		m.applyMask(mask) // XOR is its own undo
	}
	m.applyMask(best)
	m.drawFormat(best)
	return m, nil
}

// chooseVersion picks the smallest version whose data capacity holds n bytes
// (the mode indicator and the character count come out of that capacity too).
func chooseVersion(n int) (int, ecSpec, error) {
	for v := 1; v < len(ecM); v++ {
		spec := ecM[v]
		// Sanity check against the symbol's geometry: the codeword count in
		// the table above must be exactly what the modules left over by the
		// function patterns hold. A typo in a row shows up here rather than
		// as a code that no phone can read.
		if got, want := spec.totalCodewords(), dataModules(v)/8; got != want {
			return 0, ecSpec{}, fmt.Errorf("version %d: table says %d codewords, the symbol holds %d", v, got, want)
		}
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*n <= 8*spec.dataCodewords() {
			return v, spec, nil
		}
	}
	return 0, ecSpec{}, fmt.Errorf("%d bytes is more than a version %d code holds", n, len(ecM)-1)
}

// dataModules is how many modules a version leaves for data and error
// correction, once the finder, timing, alignment, format and version patterns
// have taken theirs.
func dataModules(v int) int {
	size := 4*v + 17
	n := size * size
	n -= 3 * 8 * 8       // the three finder patterns with their separators
	n -= 2 * (size - 16) // the two timing patterns, less what the finders already covered
	n -= 31              // the format information, and the one always-dark module
	if v >= 7 {
		n -= 2 * 18 // the version information, in two copies
	}
	if pos := alignmentPositions(v); len(pos) > 0 {
		k := len(pos)
		n -= (k*k - 3) * 25 // every alignment pattern but the three the finders sit on
		n += (2*k - 4) * 5  // the ones crossing a timing pattern share five modules with it, counted twice above
	}
	return n
}

// bitstream builds the message: the byte-mode indicator, the length, the
// bytes, a terminator, and then the two alternating pad codewords until the
// version's data capacity is full.
func bitstream(data []byte, version int, spec ecSpec) []byte {
	countBits := 8
	if version >= 10 {
		countBits = 16
	}
	var b bits
	b.add(0b0100, 4) // byte mode
	b.add(uint(len(data)), countBits)
	for _, c := range data {
		b.add(uint(c), 8)
	}
	capacity := 8 * spec.dataCodewords()
	b.add(0, min(4, capacity-b.n))    // terminator
	b.add(0, (8-b.n%8)%8)             // up to the codeword boundary
	for i := 0; b.n < capacity; i++ { // pad codewords, forever alternating
		if i%2 == 0 {
			b.add(0xEC, 8)
		} else {
			b.add(0x11, 8)
		}
	}
	return b.buf
}

// bits accumulates a big-endian bit string into whole bytes.
type bits struct {
	buf []byte
	n   int
}

func (b *bits) add(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.buf = append(b.buf, 0)
		}
		if v>>uint(i)&1 == 1 {
			b.buf[b.n/8] |= 1 << uint(7-b.n%8)
		}
		b.n++
	}
}

// interleave deals the message into its blocks, computes each block's
// error-correction codewords, and reads the lot back out the way the symbol
// wants it: the first codeword of every block, then the second of every
// block, and so on, data first and error correction after. Interleaving is
// what makes the code survive a smudge — a blot that destroys a run of
// codewords costs each block only a few, within what each block can repair.
func interleave(data []byte, spec ecSpec) []byte {
	blocks := make([][]byte, 0, spec.g1+spec.g2)
	ecc := make([][]byte, 0, spec.g1+spec.g2)
	off := 0
	for _, g := range []struct{ n, size int }{{spec.g1, spec.g1data}, {spec.g2, spec.g2data}} {
		for i := 0; i < g.n; i++ {
			block := data[off : off+g.size]
			off += g.size
			blocks = append(blocks, block)
			ecc = append(ecc, rsEncode(block, spec.ecPerBlock))
		}
	}
	out := make([]byte, 0, spec.totalCodewords())
	for i := 0; i < max(spec.g1data, spec.g2data); i++ {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < spec.ecPerBlock; i++ {
		for _, e := range ecc {
			out = append(out, e[i])
		}
	}
	return out
}

// --- Reed-Solomon over GF(256), the field QR codes use (x^8+x^4+x^3+x^2+1) ---

var gfExp, gfLog [256]byte

func init() {
	x := byte(1)
	for i := 0; i < 255; i++ {
		gfExp[i] = x
		gfLog[x] = byte(i)
		if x&0x80 != 0 {
			x = x<<1 ^ 0x1D
		} else {
			x <<= 1
		}
	}
	gfExp[255] = gfExp[0]
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[(int(gfLog[a])+int(gfLog[b]))%255]
}

// rsGenerator is the generator polynomial for n error-correction codewords:
// (x-α^0)(x-α^1)…(x-α^(n-1)), coefficients highest power first.
func rsGenerator(n int) []byte {
	g := []byte{1}
	for i := 0; i < n; i++ {
		g = append(g, 0)
		for j := len(g) - 1; j > 0; j-- {
			g[j] ^= gfMul(g[j-1], gfExp[i])
		}
	}
	return g
}

// rsEncode is the remainder of the message divided by the generator: the n
// error-correction codewords appended to a block.
func rsEncode(data []byte, n int) []byte {
	gen := rsGenerator(n)
	rem := make([]byte, n)
	for _, d := range data {
		factor := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for i, g := range gen[1:] {
			rem[i] ^= gfMul(g, factor)
		}
	}
	return rem
}

// --- the symbol's fixed patterns ---

func (m *qr) drawFunctionPatterns() {
	// Timing: the alternating rows the decoder measures the module pitch on.
	for i := 0; i < m.size; i++ {
		m.setFn(6, i, i%2 == 0)
		m.setFn(i, 6, i%2 == 0)
	}
	// The three finders — the big squares a scanner locks onto — each drawn
	// with its light separator, which is why the loop runs to a distance of 4.
	for _, c := range [][2]int{{3, 3}, {3, m.size - 4}, {m.size - 4, 3}} {
		for dr := -4; dr <= 4; dr++ {
			for dc := -4; dc <= 4; dc++ {
				row, col := c[0]+dr, c[1]+dc
				if row < 0 || col < 0 || row >= m.size || col >= m.size {
					continue
				}
				d := max(abs(dr), abs(dc))
				m.setFn(row, col, d != 2 && d != 4)
			}
		}
	}
	// Alignment patterns: the smaller squares that let a decoder undo the
	// perspective of a photograph. Every combination of the version's
	// positions, less the three corners the finders already hold.
	pos := alignmentPositions(m.version)
	for i, row := range pos {
		for j, col := range pos {
			if i == 0 && j == 0 || i == 0 && j == len(pos)-1 || i == len(pos)-1 && j == 0 {
				continue
			}
			for dr := -2; dr <= 2; dr++ {
				for dc := -2; dc <= 2; dc++ {
					m.setFn(row+dr, col+dc, max(abs(dr), abs(dc)) != 1)
				}
			}
		}
	}
	m.reserveFormat()
	if m.version >= 7 {
		m.drawVersion()
	}
}

// alignmentPositions is the version's alignment-pattern centre coordinates,
// the same for rows and columns: always 6, always the last one 7 in from the
// far edge, evenly spaced in between.
func alignmentPositions(v int) []int {
	if v == 1 {
		return nil
	}
	n := v/7 + 2
	step := (v*8 + n*3 + 5) / (n*4 - 4) * 2
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, 4*v+17-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// reserveFormat marks the modules the format information will take, so data
// placement skips them; the real bits go in once the mask is chosen. The one
// module that is always dark lives among them.
func (m *qr) reserveFormat() {
	for i := 0; i <= 8; i++ {
		if i != 6 {
			m.setFn(8, i, false)
			m.setFn(i, 8, false)
		}
	}
	for i := 0; i < 8; i++ {
		m.setFn(8, m.size-1-i, false)
		m.setFn(m.size-1-i, 8, false)
	}
	m.setFn(m.size-8, 8, true) // the always-dark module
}

// drawFormat writes the two copies of the format information: the error
// correction level and the mask, protected by a BCH(15,5) code and scrambled
// with the standard mask so the field is never all-zero.
func (m *qr) drawFormat(mask int) {
	data := 0b00<<3 | mask // 00 is level M
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem >> 9 * 0x537)
	}
	v := (data<<10 | rem&0x3FF) ^ 0x5412
	bit := func(i int) bool { return v>>uint(i)&1 == 1 }

	for i := 0; i <= 5; i++ {
		m.setFn(i, 8, bit(i))
	}
	m.setFn(7, 8, bit(6))
	m.setFn(8, 8, bit(7))
	m.setFn(8, 7, bit(8))
	for i := 9; i < 15; i++ {
		m.setFn(8, 14-i, bit(i))
	}
	for i := 0; i < 8; i++ {
		m.setFn(8, m.size-1-i, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFn(m.size-15+i, 8, bit(i))
	}
	m.setFn(m.size-8, 8, true)
}

// drawVersion writes the version number (versions 7 and up carry it), BCH
// protected, in two 3×6 blocks beside the bottom-left and top-right finders.
func (m *qr) drawVersion() {
	rem := m.version
	for i := 0; i < 12; i++ {
		rem = rem<<1 ^ (rem >> 11 * 0x1F25)
	}
	v := m.version<<12 | rem&0xFFF
	for i := 0; i < 18; i++ {
		b := v>>uint(i)&1 == 1
		a, c := m.size-11+i%3, i/3
		m.setFn(c, a, b)
		m.setFn(a, c, b)
	}
}

// drawCodewords lays the codeword bits into every module the function
// patterns left: two columns at a time from the right edge leftwards (the
// vertical timing pattern's column is skipped), each pair read upwards then
// downwards, alternating.
func (m *qr) drawCodewords(cw []byte) {
	i := 0
	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < m.size; vert++ {
			for j := 0; j < 2; j++ {
				col := right - j
				row := vert
				if (right+1)&2 == 0 { // this pair of columns runs upwards
					row = m.size - 1 - vert
				}
				if m.isFn(row, col) || i >= 8*len(cw) {
					continue
				}
				m.set(row, col, cw[i/8]>>uint(7-i%8)&1 == 1)
				i++
			}
		}
	}
}

// applyMask flips the data modules the mask selects — the codes' way of
// avoiding a pattern a scanner would misread. XOR, so calling it twice with
// the same mask undoes it.
func (m *qr) applyMask(mask int) {
	for row := 0; row < m.size; row++ {
		for col := 0; col < m.size; col++ {
			if m.isFn(row, col) {
				continue
			}
			var flip bool
			switch mask {
			case 0:
				flip = (row+col)%2 == 0
			case 1:
				flip = row%2 == 0
			case 2:
				flip = col%3 == 0
			case 3:
				flip = (row+col)%3 == 0
			case 4:
				flip = (row/2+col/3)%2 == 0
			case 5:
				flip = row*col%2+row*col%3 == 0
			case 6:
				flip = (row*col%2+row*col%3)%2 == 0
			case 7:
				flip = ((row+col)%2+row*col%3)%2 == 0
			}
			if flip {
				m.set(row, col, !m.dark(row, col))
			}
		}
	}
}

// penalty scores a masked symbol the way the standard does — long runs of one
// colour, 2×2 blocks of it, the finder-like sequence, and an unbalanced
// dark/light ratio all cost points. The lowest score wins.
func (m *qr) penalty() int {
	const n1, n2, n3, n4 = 3, 3, 40, 10
	score, dark := 0, 0

	// Runs of five or more, in both directions, and the finder-like
	// sequence (dark light dark dark dark light dark) with four light
	// modules on either side of it.
	finder := []bool{true, false, true, true, true, false, true}
	line := make([]bool, m.size)
	for _, byRow := range []bool{true, false} {
		for a := 0; a < m.size; a++ {
			for b := 0; b < m.size; b++ {
				if byRow {
					line[b] = m.dark(a, b)
				} else {
					line[b] = m.dark(b, a)
				}
			}
			run := 1
			for b := 1; b < m.size; b++ {
				if line[b] == line[b-1] {
					run++
					continue
				}
				if run >= 5 {
					score += n1 + run - 5
				}
				run = 1
			}
			if run >= 5 {
				score += n1 + run - 5
			}
			for b := 0; b+7 <= m.size; b++ {
				if !equal(line[b:b+7], finder) {
					continue
				}
				if allLight(line, b-4, b) || allLight(line, b+7, b+11) {
					score += n3
				}
			}
		}
	}

	for row := 0; row < m.size; row++ {
		for col := 0; col < m.size; col++ {
			if m.dark(row, col) {
				dark++
			}
			if row+1 < m.size && col+1 < m.size &&
				m.dark(row, col) == m.dark(row, col+1) &&
				m.dark(row, col) == m.dark(row+1, col) &&
				m.dark(row, col) == m.dark(row+1, col+1) {
				score += n2
			}
		}
	}

	total := m.size * m.size
	for k := 0; abs(dark*20-total*10) > (k+1)*total; k++ {
		score += n4
	}
	return score
}

// allLight reports whether every module of line in [from, to) is light,
// counting anything past the edge as light (the quiet zone is).
func allLight(line []bool, from, to int) bool {
	for i := from; i < to; i++ {
		if i >= 0 && i < len(line) && line[i] {
			return false
		}
	}
	return true
}

func equal(a, b []bool) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
