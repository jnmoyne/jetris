package nativeui

// The login screen's backdrop: a piece of artwork — the neon "Team A" and
// "Team B" boards standing over the NATS logo — embedded in the binary and
// drawn behind the login card, scaled to cover the window (cropping the sides
// or the top/bottom as the aspect ratio demands, never letterboxing) and
// veiled just enough that the card stays the focus. One textured quad per frame; the decoded image
// and its GPU texture are cached across frames.

import (
	"bytes"
	_ "embed"
	"image"
	_ "image/jpeg" // decoder for the embedded artwork
	"sync"

	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// loginBackdropJPG is the login artwork: Team A's cyan board and Team B's
// magenta board — each a neon-framed well with a few pieces in play, drawn
// by the game's own board drawer — standing on the NATS "N" logo. Rendered
// by TestRenderLoginBackdrop (backdrop_render_test.go), so it follows the
// look of the cells: re-run it whenever that changes.
//
//go:embed login-backdrop.jpg
var loginBackdropJPG []byte

// backdropDim is the alpha of the colBg veil over the artwork — light enough
// to keep the picture, heavy enough that the title and tagline read over it.
const backdropDim = 0.28

var (
	backdropOnce sync.Once
	backdropOp   paint.ImageOp
	backdropOK   bool
)

// backdropImage decodes the embedded artwork once.
func backdropImage() (paint.ImageOp, bool) {
	backdropOnce.Do(func() {
		img, _, err := image.Decode(bytes.NewReader(loginBackdropJPG))
		if err != nil {
			return
		}
		backdropOp = paint.NewImageOp(img)
		backdropOK = true
	})
	return backdropOp, backdropOK
}

// loginBackdrop fills the window with the artwork (cover-scaled, centered)
// under a translucent colBg veil. A decoding failure leaves the plain
// background.
func (a *App) loginBackdrop(gtx C) D {
	size := gtx.Constraints.Max
	src, ok := backdropImage()
	if !ok {
		return D{Size: size}
	}
	gtx.Constraints = layout.Exact(size)
	widget.Image{Src: src, Fit: widget.Cover, Position: layout.Center}.Layout(gtx)
	fillRect(gtx.Ops, image.Rectangle{Max: size}, withAlpha(colBg, backdropDim))
	return D{Size: size}
}
