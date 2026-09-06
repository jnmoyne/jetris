package nativeui

// Pinning and sharing a replay.
//
// A PIN keeps a replay for good: the archivers purge the replays that fall
// out of the keep set (top of the bucket or recent — config.ReplayKeepSet),
// and a pinned game is in that set whatever its rank or age, until someone
// unpins it. The pin is a lobby KV entry (config.LobbyPinKey), so every
// lobby sees pins and unpins live and the history marks the pinned rows.
//
// SHARE is a link to this very replay: the join page with the server AND the
// game named (webdist.ReplayLink), shown as a QR code and as text with a
// Copy link button. Whoever opens it types a name — the one thing a link
// cannot know — and lands on the replay screen with this game playing
// (config.Config.ReplayGameID → openLinkedReplay). The link needs the
// server's WebSocket address, which is all a browser can dial: the address
// this build dialed when it is one, the LAN party's own listener, or the
// WebSocket sibling of a nats:// favorite (the official servers are listed
// both ways); with none of those there is no link, and the modal says why.

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"jetris/internal/lobby"
	"jetris/internal/prefs"
	"jetris/internal/qr"
	"jetris/internal/webdist"
)

// shareCopiedFor is how long the Copy link button reads COPIED after a copy.
const shareCopiedFor = 2 * time.Second

// linkedReplayWait bounds how long a share link's landing waits for the
// game's archive record to arrive off the archive stream before giving up
// with a note in the lobby.
var linkedReplayWait = 15 * time.Second

// replayPinned reports whether the replay on screen is pinned — the lobby's
// word, which follows the KV live.
func (a *App) replayPinned(gameID string) bool {
	lb := a.getLobby()
	return lb != nil && lb.IsPinned(gameID)
}

// isPinned is replayPinned over the history on screen: nothing is pinned in
// the How to play tour's fixture history.
func (a *App) isPinned(lb *lobby.Lobby, gameID string) bool {
	if a.tutorialUp() || lb == nil {
		return false
	}
	return lb.IsPinned(gameID)
}

// toggleReplayPin pins the replay, or unpins it if it is pinned. The KV
// write runs off the UI goroutine; the screen follows the lobby's watcher,
// not the click, so a refused write changes nothing on screen but the
// status line.
func (a *App) toggleReplayPin(rv *replayView) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	gameID := rv.rec.GameID
	pinned := lb.IsPinned(gameID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if pinned {
			err = lb.UnpinReplay(ctx, gameID)
		} else {
			err = lb.PinReplay(ctx, gameID)
		}
		if err != nil {
			verb := "pin"
			if pinned {
				verb = "unpin"
			}
			log.Printf("replay %s: %s: %v", gameID, verb, err)
			a.setReplayErr(rv, fmt.Sprintf("Couldn't %s the replay: %v", verb, err))
		}
		a.invalidate()
	}()
}

// replayShareLink builds the share link for one game's replay, or says why
// there is none (why is "" when link is set). The page and the server come
// from wherever this build is: the LAN party's own page and listener; in
// the browser, this page and the server it dialed; on the desktop, the
// GitHub Pages copy of the game (webdist.DefaultPage) and the server's
// WebSocket address — the dialed one when it is a ws:// or wss:// URL, else
// the WebSocket sibling of the same host among the favorites.
func (a *App) replayShareLink(gameID string) (link, why string) {
	if page, server, name, ok := a.lanLinkParts(); ok {
		return shareLinkOrWhy(page, server, name, gameID)
	}
	a.mu.Lock()
	nc, connName, connURL := a.nc, a.connName, a.connURL
	favorites := append([]prefs.Favorite(nil), a.favorites...)
	a.mu.Unlock()
	// The server: what the connection actually reached; failing that, the
	// lobby header's URL — which for a plain URL with no name to go by is
	// the header's name (connectionParts).
	dialed := ""
	if nc != nil {
		dialed = stripURLUserinfo(nc.ConnectedUrl())
	}
	if dialed == "" {
		dialed = connURL
	}
	if dialed == "" && strings.Contains(connName, "://") {
		dialed = connName
	}
	if dialed == "" {
		return "", "Not connected to a server."
	}
	page := currentPageURL()
	if page == "" {
		page = webdist.DefaultPage
	}
	// The name the join page shows over the address: the favorite's label
	// (connName is one when connURL stands apart from it), never a bare
	// URL or a context's name, which mean nothing to whoever opens it.
	label := ""
	if connURL != "" && !strings.HasPrefix(connName, "context ") {
		label = connName
	}
	u, err := url.Parse(dialed)
	if err != nil {
		return "", "The server's address could not be read: " + err.Error()
	}
	if u.Scheme == "ws" || u.Scheme == "wss" {
		return shareLinkOrWhy(page, dialed, label, gameID)
	}
	// Reached over plain NATS: the same host's WebSocket listener, if a
	// favorite names it (the official servers are bookmarked both ways).
	if sibling, ok := webSocketSibling(u.Hostname(), favorites); ok {
		if label == "" {
			label = sibling.Label
		}
		return shareLinkOrWhy(page, sibling.URL, label, gameID)
	}
	return "", fmt.Sprintf("This server was reached over %s://, and a browser can only open a replay through the server's WebSocket (ws:// or wss://) address, which isn't known here. Bookmark the server's WebSocket address in the server browser, or connect through it, and share again.", u.Scheme)
}

// shareLinkOrWhy is webdist.ReplayLink with its error as the modal's line.
func shareLinkOrWhy(page, server, name, gameID string) (link, why string) {
	link, err := webdist.ReplayLink(page, server, name, gameID)
	if err != nil {
		return "", "No share link: " + err.Error()
	}
	return link, ""
}

// webSocketSibling finds a favorite dialing host over WebSocket — the
// official servers are bookmarked over WebSocket and over plain NATS both,
// so a desktop on the nats:// one still knows where a browser would go.
// The player's own list first, then the shipped defaults (a deleted default
// still tells the truth about the server).
func webSocketSibling(host string, favorites []prefs.Favorite) (prefs.Favorite, bool) {
	if host == "" {
		return prefs.Favorite{}, false
	}
	for _, f := range append(favorites, prefs.DefaultFavorites()...) {
		u, err := url.Parse(f.URL)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
			continue
		}
		if strings.EqualFold(u.Hostname(), host) {
			return f, true
		}
	}
	return prefs.Favorite{}, false
}

// openShare puts the share modal up for the replay on screen, the link
// built (and its QR code encoded) once here rather than every frame.
func (a *App) openShare(rv *replayView) {
	link, why := a.replayShareLink(rv.rec.GameID)
	a.shareLink, a.shareWhy, a.shareCode = link, why, nil
	if link != "" {
		a.shareCode, _ = qr.Encode([]byte(link))
	}
	a.shareCopiedAt = time.Time{}
	a.shareOpen = true
}

// handleReplayActions dispatches the replay screen's PIN and SHARE buttons
// and the share modal's own (Copy link, OK), and reports whether the modal
// is up. The buttons under the modal's scrim are not answered while it is.
func (a *App) handleReplayActions(gtx C, rv *replayView) (modal bool) {
	if a.shareOpen {
		if a.shareOKBtn.Clicked(gtx) {
			a.shareOpen = false
		}
		if a.shareCopyBtn.Clicked(gtx) && a.shareLink != "" {
			a.shareCopyOK = copyText(gtx, a.shareLink)
			a.shareCopiedAt = gtx.Now
		}
		return a.shareOpen
	}
	if a.replayPinBtn.Clicked(gtx) {
		a.toggleReplayPin(rv)
	}
	if a.replayShareBtn.Clicked(gtx) {
		a.openShare(rv)
	}
	return a.shareOpen
}

// shareOverlay is the share modal: the replay's link as a QR code on a
// white plate, the link itself under it, Copy link and OK — or, when no
// link can be built, the reason and OK. Drawn under App.mu (the replay
// screen's layout holds it), so it reads only what openShare stored.
func (a *App) shareOverlay(gtx C) D {
	link, why, code := a.shareLink, a.shareWhy, a.shareCode
	copied := !a.shareCopiedAt.IsZero() && gtx.Now.Sub(a.shareCopiedAt) < shareCopiedFor
	if copied {
		animate(gtx) // the COPIED readout reverts on its own
	}
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 520)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colNATSGreen, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						side := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y-gtx.Dp(260))
						side = max(side, gtx.Dp(120))
						kids := []layout.FlexChild{
							layout.Rigid(a.pixel(unit.Sp(13), "SHARE THIS REPLAY", colNATSGreen).Layout),
							layout.Rigid(spacer(14)),
						}
						if link == "" {
							kids = append(kids,
								layout.Rigid(a.body(why, colErr)),
								layout.Rigid(spacer(16)),
								layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.shareOKBtn, "OK") }),
							)
						} else {
							kids = append(kids,
								layout.Rigid(func(gtx C) D {
									if code == nil {
										return a.body("The link is too long for a QR code — copy it instead.", colErr)(gtx)
									}
									return drawQR(gtx, code, side)
								}),
								layout.Rigid(spacer(12)),
								layout.Rigid(a.body(link, colFg)),
								layout.Rigid(spacer(8)),
								layout.Rigid(a.body("Anyone who opens the link — or scans the code — types a name and watches this replay in their browser, on this server.", colMuted)),
								layout.Rigid(spacer(16)),
								layout.Rigid(func(gtx C) D {
									return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
										layout.Rigid(func(gtx C) D {
											label := "Copy link"
											if copied {
												label = "Copied!"
												if !a.shareCopyOK {
													label = "Couldn't copy"
												}
											}
											return a.primaryButton(gtx, &a.shareCopyBtn, label)
										}),
										layout.Rigid(hSpacer(12)),
										layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.shareOKBtn, "OK") }),
									)
								}),
							)
						}
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, kids...)
					})
				})
			})
		})
	})
}

// takeLinkedReplay returns the game ID a share link (or --replay) asked to
// open, once: the landing is one-shot, like the auto-login it rides on, so
// quitting to the login screen and playing again lands in the lobby.
func (a *App) takeLinkedReplay() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := a.linkedReplay
	a.linkedReplay = ""
	return id
}

// openLinkedReplay opens the replay a share link named, as soon as the game's
// archive record has arrived (the lobby's archive consumer replays the whole
// archive stream, and the record may still be on its way when the lobby is
// first drawn). A game the server's history never produces within
// linkedReplayWait is reported under the lobby banner: the link is most
// likely for another server. Runs off the UI goroutine.
func (a *App) openLinkedReplay(gameID string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	deadline := time.Now().Add(linkedReplayWait)
	for {
		if a.ctx != nil && a.ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		still := a.lobby == lb && a.screen == screenLobby
		a.mu.Unlock()
		if !still {
			return // the player moved on (or quit) before the record came
		}
		if rec, ok := lb.ArchiveFor(gameID); ok {
			a.startReplay(rec)
			return
		}
		if time.Now().After(deadline) {
			a.mu.Lock()
			a.lobbyErr = "No game " + gameID + " in this server's history — the replay link may be for another server."
			a.mu.Unlock()
			a.invalidate()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// lanLinkParts is the LAN party's join link in its parts — the page, the
// WebSocket server and the server's name — or ok false while no party is
// up. lanJoinLink is these joined; the replay share link names a game too.
func (a *App) lanLinkParts() (page, server, name string, ok bool) {
	a.mu.Lock()
	httpAddr, wsAddr, embName, scheme := a.embHTTPAddr, a.embWSAddr, a.embName, a.embHTTPScheme
	a.mu.Unlock()
	if httpAddr == "" || wsAddr == "" {
		return "", "", "", false
	}
	// An https page proxies the browser build's WebSocket on its own
	// origin (webdist.Options), so the socket is the page's address and the
	// guest's one accepted certificate covers both; a plain http page sends
	// the browser to the server's own listener.
	page, server = "http://"+httpAddr+"/", "ws://"+wsAddr
	if scheme == "https" {
		page, server = "https://"+httpAddr+"/", "wss://"+httpAddr
	}
	return page, server, embeddedNameOrDefault(embName), true
}
