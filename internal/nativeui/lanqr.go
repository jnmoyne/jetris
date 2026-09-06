package nativeui

// The LAN party's join link, and the lobby's Show QR code.
//
// A LAN party is three listeners on the host's machine (lifecycle.go's
// ensureLANServers): the embedded nats-server for the desktop builds and the
// agents, its WebSocket listener for the browser build, and an HTTP server
// carrying the browser build itself (internal/webdist). The phones on the
// network need only the last: they open the page, it asks for a name, and
// the game it loads dials the WebSocket listener. The join link is that page
// with the server's address in its query string (web/join.html), and the QR
// code is the join link — put on the screen so nobody has to type an
// address on a phone.

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"jetris/internal/qr"
	"jetris/internal/webdist"
)

// The plate a QR code is drawn on: black modules on white, whatever the
// screen's theme — a scanner expects dark on light.
var (
	colQRDark  = color.NRGBA{A: 0xff}
	colQRLight = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// browserBuildReady reports whether this binary carries the browser build
// (webdist.Ready): without it the lobby hands out the NATS address alone —
// there is no page for a phone to open — and says why. A variable so the
// snapshot tests can show the lobby of a release build.
var browserBuildReady = webdist.Ready

// lanJoinLink is the LAN party's join link — the browser build's join page
// on this host, carrying the embedded server's WebSocket address and the
// server's name (the tab's Name field, so the join page and every guest's
// lobby bar call it what the host's does) — or "" while no LAN party is up.
// It is what the QR code encodes and where the page's bare "/" redirects
// (webdist.Handler), so a scanned code and a typed address land on the same
// page. Read per request by the HTTP server, so it must be cheap and lock
// only briefly.
func (a *App) lanJoinLink() string {
	page, server, name, ok := a.lanLinkParts()
	if !ok {
		return ""
	}
	link, err := webdist.JoinLink(page, server, name)
	if err != nil {
		return ""
	}
	return link
}

// lanPageURL is the LAN party page's address as a phone types it —
// https://<lan-ip>:<port>/ with the page's certificate, http:// without —
// or "" while no party is up.
func (a *App) lanPageURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.embHTTPAddr == "" {
		return ""
	}
	scheme := a.embHTTPScheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + a.embHTTPAddr + "/"
}

// handleQRModal dispatches the lobby's Show QR code button and the modal's
// OK, and reports whether the modal is up. Show QR code sits in the menu
// column, under the scrim when another modal is up; its press is then spent
// and not answered, as the bar's are (handleLobbyBarClicks). The modal also
// closes on its own should the party end under it (the connection dropped
// to a plain server): a code for a server that is gone is worse than none.
func (a *App) handleQRModal(gtx C, modal bool) bool {
	if a.qrShowBtn.Clicked(gtx) && !modal {
		a.qrOpen = true
	}
	if a.qrOKBtn.Clicked(gtx) {
		a.qrOpen = false
	}
	if a.qrOpen && a.lanJoinLink() == "" {
		a.qrOpen = false
	}
	return a.qrOpen
}

// qrOverlay is the Show QR code modal: the join link as a QR code on a white
// plate, the link itself under it for anyone who would rather type, a line
// on what scanning it does, and OK. The code is re-encoded only when the
// link changes (qrLink/qrCode); it is drawn every frame.
func (a *App) qrOverlay(gtx C) D {
	link := a.lanJoinLink()
	if link != a.qrLink || a.qrCode == nil {
		a.qrLink = link
		a.qrCode, _ = qr.Encode([]byte(link))
	}
	code := a.qrCode
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 520)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colNATSGreen, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						// The code takes what the window gives it: the modal's
						// inner width, or the height left once the title, the
						// link, the note and the button have theirs — whichever
						// is less — so a phone-sized window still shows a
						// whole code, just a smaller one. Never under 120 dp: a
						// phone's camera reads a smaller one from further off
						// than that, but a smaller one is nothing to look at.
						side := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y-gtx.Dp(240))
						side = max(side, gtx.Dp(120))
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "SCAN TO JOIN FROM A BROWSER", colNATSGreen).Layout),
							layout.Rigid(spacer(14)),
							layout.Rigid(func(gtx C) D {
								if code == nil {
									return a.body("The join link is too long for a QR code.", colErr)(gtx)
								}
								return drawQR(gtx, code, side)
							}),
							layout.Rigid(spacer(12)),
							layout.Rigid(a.body(link, colFg)),
							layout.Rigid(spacer(8)),
							layout.Rigid(a.body("Phones on this network scan the code, type a name and land in this lobby — or open "+a.lanPageURL()+" in a browser. The page has a certificate of its own: accept it once, and the voice chat's microphone works.", colMuted)),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.qrOKBtn, "OK") }),
						)
					})
				})
			})
		})
	})
}

// drawQR paints code on a white plate side px square (rounded down to whole
// modules): the quiet zone the standard asks for around it, and every run of
// dark modules along a row as one rectangle.
func drawQR(gtx C, code *qr.Code, side int) D {
	n := code.Size + 2*qr.QuietZone
	px := max(side/n, 1)
	side = px * n
	fillRect(gtx.Ops, image.Rect(0, 0, side, side), colQRLight)
	for row := 0; row < code.Size; row++ {
		y := (row + qr.QuietZone) * px
		for col := 0; col < code.Size; {
			if !code.Dark(row, col) {
				col++
				continue
			}
			start := col
			for col < code.Size && code.Dark(row, col) {
				col++
			}
			fillRect(gtx.Ops, image.Rect((start+qr.QuietZone)*px, y, (col+qr.QuietZone)*px, y+px), colQRDark)
		}
	}
	return D{Size: image.Pt(side, side)}
}
