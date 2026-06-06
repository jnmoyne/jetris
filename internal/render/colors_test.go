package render

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"jetricks/internal/game"
)

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// TestCSSAndRGBAParity guarantees the two appearance surfaces never drift: the
// RGBA Fill/Outline a native client draws must be exactly the colors the web
// client emits as CSS for the same cell.
func TestCSSAndRGBAParity(t *testing.T) {
	cases := []struct {
		name        string
		cell        game.Cell
		localIdx    int
		showOutline bool
	}{
		{"empty", game.Cell{}, 0, true},
		{"active-own", game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 0}, 0, true},
		{"active-other", game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 1}, 0, true},
		{"active-spectator", game.Cell{Active: true, PieceType: game.PieceT, PlayerIdx: 2}, -1, true},
		{"occupied-outline", game.Cell{Occupied: true, PieceType: game.PieceZ, PlayerIdx: 1}, 0, true},
		{"occupied-no-outline", game.Cell{Occupied: true, PieceType: game.PieceZ, PlayerIdx: 1}, 0, false},
		{"adversarial", game.Cell{Adversarial: true, Occupied: true, PlayerIdx: 3}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			css := CellStyleCSS(tc.cell, tc.localIdx, tc.showOutline)
			app := CellStyle(tc.cell, tc.localIdx, tc.showOutline)
			fillHex, outlineHex := hex(app.Fill), hex(app.Outline)
			if !strings.Contains(css, "background:"+fillHex+";") {
				t.Errorf("fill mismatch: CSS=%q RGBA fill=%s", css, fillHex)
			}
			if !strings.Contains(css, fmt.Sprintf("outline:%dpx solid %s;", app.OutlineW, outlineHex)) {
				t.Errorf("outline mismatch: CSS=%q RGBA outline=%s w=%d", css, outlineHex, app.OutlineW)
			}
		})
	}
}

// TestCSSUnchanged pins a couple of exact CSS strings so the web output stays
// byte-for-byte what it was before the extraction.
func TestCSSUnchanged(t *testing.T) {
	got := CellStyleCSS(game.Cell{}, 0, true)
	want := "background:#111111;outline:1px solid #1a1a1a;outline-offset:-1px"
	if got != want {
		t.Errorf("empty cell CSS = %q, want %q", got, want)
	}
	// Own active I-piece: 90% cyan over #111111, white 2px outline.
	got = CellStyleCSS(game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 0}, 0, true)
	want = "background:#02dada;outline:2px solid #ffffff;outline-offset:-1px"
	if got != want {
		t.Errorf("own active I CSS = %q, want %q", got, want)
	}
}
