package nativeui

import (
	_ "embed"
	"sync"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// pressStart2P is the embedded "Press Start 2P" pixel face (SIL OFL 1.1, see
// PressStart2P-OFL.txt) — the 8-bit display font used for the title, headers,
// buttons, HUD stats, and the countdown. Body text (chat, lists, editors)
// stays in the Go faces for readability.
//
//go:embed PressStart2P-Regular.ttf
var pressStart2P []byte

// pixelTypeface selects the embedded pixel face in a theme whose shaper was
// built from uiFontCollection.
const pixelTypeface font.Typeface = "PressStart2P"

var (
	fontCollOnce sync.Once
	fontColl     []font.FontFace
)

// uiFontCollection is the app's font set: the Go faces (the default) plus the
// pixel face under the pixelTypeface name. Both the window and the layout
// tests build their shaper from this, so pixel labels shape the same in each.
func uiFontCollection() []font.FontFace {
	fontCollOnce.Do(func() {
		fontColl = gofont.Collection()
		faces, err := opentype.ParseCollection(pressStart2P)
		if err != nil {
			return // pixel labels fall back to the Go faces
		}
		for i := range faces {
			faces[i].Font.Typeface = pixelTypeface
		}
		fontColl = append(fontColl, faces...)
	})
	return fontColl
}

// pixel returns a label in the pixel face. Press Start 2P renders much larger
// per point than the Go faces, so sizes here run roughly two thirds of their
// Go-face equivalents.
func (a *App) pixel(size unit.Sp, txt string, col colorN) material.LabelStyle {
	l := material.Label(a.th, size, txt)
	l.Font.Typeface = pixelTypeface
	l.Color = col
	return l
}
