package render

import (
	"fmt"
	"image/color"
	"testing"

	"jetris/internal/game"
)

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// TestCellStyleDecisions pins the appearance decision logic (fill, outline,
// outline width) for each cell state so the rendering rules can't silently drift.
func TestCellStyleDecisions(t *testing.T) {
	cases := []struct {
		name        string
		cell        game.Cell
		localIdx    int
		showOutline bool
		wantFill    string
		wantOutline string
		wantW       int
	}{
		// Empty cell: board background with the plain grid line.
		{"empty", game.Cell{}, 0, true, BoardBgHex, GridLineHex, 1},
		// Own active I-piece: 90% cyan over #111111, white 2px outline.
		{"active-own", game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 0}, 0, true, "#02dada", OwnOutlineHex, 2},
		// Another player's active piece in a player's own view: no ownership outline.
		{"active-other", game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 1}, 0, true, "#02dada", GridLineHex, 1},
		// Spectator view: every active piece gets a per-player outline.
		{"active-spectator", game.Cell{Active: true, PieceType: game.PieceT, PlayerIdx: 2}, -1, true, blendHex(pieceColorHex(game.PieceT), BoardBgHex, 0.9), PlayerColorHex(2), 2},
		// Locked cell: 70% piece color, owner-colored outline when shown.
		{"occupied-outline", game.Cell{Occupied: true, PieceType: game.PieceZ, PlayerIdx: 1}, 0, true, blendHex(pieceColorHex(game.PieceZ), BoardBgHex, 0.7), PlayerColorHex(1), 2},
		// Compact opponent boards suppress ownership outlines.
		{"occupied-no-outline", game.Cell{Occupied: true, PieceType: game.PieceZ, PlayerIdx: 1}, 0, false, blendHex(pieceColorHex(game.PieceZ), BoardBgHex, 0.7), GridLineHex, 1},
		// Adversarial garbage: 80% sender color, plain grid line.
		{"adversarial", game.Cell{Adversarial: true, Occupied: true, PlayerIdx: 3}, 0, true, blendHex(PlayerColorHex(3), BoardBgHex, 0.8), GridLineHex, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := CellStyle(tc.cell, tc.localIdx, tc.showOutline)
			if got := hex(app.Fill); got != tc.wantFill {
				t.Errorf("fill = %s, want %s", got, tc.wantFill)
			}
			if got := hex(app.Outline); got != tc.wantOutline {
				t.Errorf("outline = %s, want %s", got, tc.wantOutline)
			}
			if app.OutlineW != tc.wantW {
				t.Errorf("outline width = %d, want %d", app.OutlineW, tc.wantW)
			}
		})
	}
}
