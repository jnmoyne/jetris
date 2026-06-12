package nativeui

import (
	"bytes"
	_ "embed"
	"image"
	_ "image/png" // decoder for the embedded logo
	"sync"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// natsIconPNG is the official nats.io "N" logo (https://nats.io).
//
//go:embed nats-icon.png
var natsIconPNG []byte

var (
	natsIconOnce sync.Once
	natsIconOp   paint.ImageOp
	natsIconOK   bool
)

// natsIcon decodes the embedded logo once.
func natsIcon() (paint.ImageOp, bool) {
	natsIconOnce.Do(func() {
		img, _, err := image.Decode(bytes.NewReader(natsIconPNG))
		if err != nil {
			return
		}
		natsIconOp = paint.NewImageOp(img)
		natsIconOK = true
	})
	return natsIconOp, natsIconOK
}

// natsLogo draws the "N" logo scaled to size dp (zero-size if decoding failed).
func natsLogo(gtx C, size unit.Dp) D {
	src, ok := natsIcon()
	if !ok {
		return D{}
	}
	sz := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(sz, sz))
	return widget.Image{Src: src, Fit: widget.Contain}.Layout(gtx)
}

// lobbyBanner is the branding strip across the top of the lobby screen:
// the NATS "N" logo flanking "Jetricks: peer to peer and made with NATS.io".
func (a *App) lobbyBanner(gtx C) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return layout.Center.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return natsLogo(gtx, 30) }),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D {
					l := material.H6(a.th, "Jetricks: peer to peer and made with ")
					l.Color = colFg
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					l := material.H6(a.th, "NATS.io")
					l.Color = colAccent
					l.Font.Weight = font.Bold
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D { return natsLogo(gtx, 30) }),
			)
		})
	})
}
