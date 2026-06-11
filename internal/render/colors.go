// Package render is the single source of truth for Jetricks cell/board
// appearance: the decision logic lives in appearanceHex and is surfaced as
// RGBA values for the native UI.
package render

import (
	"fmt"
	"image/color"
	"strings"

	"jetricks/internal/game"
)

// Board background and grid-line colors. Every square's outline falls back to
// the grid line so that, literally, every square has an outline.
const (
	BoardBgHex    = "#111111"
	GridLineHex   = "#1a1a1a"
	OwnOutlineHex = "#ffffff"
)

// playerColors are the per-player outline colors (cycled modulo length).
var playerColors = []string{
	"#00ffff", // P0 cyan
	"#ff00ff", // P1 magenta
	"#ffff00", // P2 yellow
	"#ff8800", // P3 orange
	"#00ff00", // P4 green
	"#ff4444", // P5 red
	"#8888ff", // P6 light blue
	"#ff88ff", // P7 pink
	"#88ffff", // P8 light cyan
	"#ffaa44", // P9 amber
}

// pieceColors maps each tetromino type to its base fill color. Index matches
// the game.PieceType iota (I, O, T, S, Z, J, L).
var pieceColors = [...]string{
	game.PieceI: "#00f0f0", // cyan
	game.PieceO: "#f0f000", // yellow
	game.PieceT: "#a000f0", // purple
	game.PieceS: "#00f000", // green
	game.PieceZ: "#f00000", // red
	game.PieceJ: "#0000f0", // blue
	game.PieceL: "#f0a000", // orange
}

// PlayerColorHex returns the player outline color as "#rrggbb" (cycled).
func PlayerColorHex(idx int) string {
	if idx < len(playerColors) {
		return playerColors[idx]
	}
	return playerColors[((idx%len(playerColors))+len(playerColors))%len(playerColors)]
}

func pieceColorHex(pt game.PieceType) string {
	if int(pt) >= 0 && int(pt) < len(pieceColors) {
		return pieceColors[pt]
	}
	return BoardBgHex
}

// blendHex composites fg over bg at the given alpha (0..1) and returns the
// resulting "#rrggbb" hex — the opacity-over-dark-board look as a concrete color.
func blendHex(fg, bg string, alpha float64) string {
	fr, fg2, fb := hexToRGB(fg)
	br, bg2, bb := hexToRGB(bg)
	r := int(float64(fr)*alpha + float64(br)*(1-alpha) + 0.5)
	g := int(float64(fg2)*alpha + float64(bg2)*(1-alpha) + 0.5)
	b := int(float64(fb)*alpha + float64(bb)*(1-alpha) + 0.5)
	return fmt.Sprintf("#%02x%02x%02x", clamp8(r), clamp8(g), clamp8(b))
}

func hexToRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return 0, 0, 0
	}
	var r, g, b int
	fmt.Sscanf(h, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// appearanceHex is the single decision function behind the RGBA surface. It
// reproduces the original cellStyle logic exactly. localPlayerIdx
// is the viewer's player index (-1 for spectators: every active piece gets a
// per-player outline). When showOutline is false (compact opponent boards)
// ownership outlines are suppressed in favor of the plain grid line.
func appearanceHex(c game.Cell, localPlayerIdx int, showOutline bool) (fill, outline string, outlineW int) {
	fill = BoardBgHex
	outline = GridLineHex
	outlineW = 1

	switch {
	case c.Active:
		fill = blendHex(pieceColorHex(c.PieceType), BoardBgHex, 0.9)
		switch {
		case localPlayerIdx < 0:
			// Spectator: every active piece gets a per-player outline.
			outline, outlineW = PlayerColorHex(c.PlayerIdx), 2
		case c.PlayerIdx == localPlayerIdx:
			// Own piece: white outline.
			outline, outlineW = OwnOutlineHex, 2
			// Other player's active piece in a player's own view: no
			// ownership outline (grid line) — preserves the seamless board.
		}
	case c.Adversarial:
		fill = blendHex(PlayerColorHex(c.PlayerIdx%10), BoardBgHex, 0.8)
	case c.Occupied:
		fill = blendHex(pieceColorHex(c.PieceType), BoardBgHex, 0.7)
		if showOutline {
			outline, outlineW = PlayerColorHex(c.PlayerIdx), 2
		}
	}
	return fill, outline, outlineW
}

// CellAppearance is the resolved visual of one square for native rendering.
// OutlineW is 1 (grid line) or 2 (ownership/active outline). The native drawer
// fills the cell with Fill, then strokes a 1px-inset border of width OutlineW
// in Outline.
type CellAppearance struct {
	Fill     color.NRGBA
	Outline  color.NRGBA
	OutlineW int
}

// CellStyle is the canonical appearance for native rendering.
func CellStyle(c game.Cell, localPlayerIdx int, showOutline bool) CellAppearance {
	fill, outline, outlineW := appearanceHex(c, localPlayerIdx, showOutline)
	return CellAppearance{Fill: nrgbaFromHex(fill), Outline: nrgbaFromHex(outline), OutlineW: outlineW}
}

// PlayerColorRGBA returns the player outline color as an NRGBA (cycled).
func PlayerColorRGBA(idx int) color.NRGBA { return nrgbaFromHex(PlayerColorHex(idx)) }

func nrgbaFromHex(h string) color.NRGBA {
	r, g, b := hexToRGB(h)
	return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
}
