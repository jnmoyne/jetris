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
	"image"
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

// toggleReplayPin pins the replay on screen, or unpins it if it is pinned.
func (a *App) toggleReplayPin(rv *replayView) {
	a.togglePin(rv.rec.GameID, func(msg string) { a.setReplayErr(rv, msg) })
}

// togglePin pins a game's replay, or unpins it if it is pinned — the replay
// screen's Pin, and the game-over box's (the game just played, its replay
// being written as the box is drawn: the pin is a KV entry by game ID, so
// it holds whether or not the record is there yet). The KV write runs off
// the UI goroutine; the screen follows the lobby's watcher, not the click,
// so a refused write changes nothing on screen but what report says.
func (a *App) togglePin(gameID string, report func(msg string)) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
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
			report(fmt.Sprintf("Couldn't %s the replay: %v", verb, err))
		}
		a.invalidate()
	}()
}

// setGameOverNote puts a line under the game-over box's buttons (a refused
// pin), for the rest of the game screen's stay.
func (a *App) setGameOverNote(msg string) {
	a.mu.Lock()
	a.gameOverNote = msg
	a.mu.Unlock()
	a.invalidate()
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

// openShare puts the share modal up for the replay on screen.
func (a *App) openShare(rv *replayView) { a.openShareFor(rv.rec.GameID) }

// openShareFor puts the share modal up for one game's replay — the replay
// screen's Share, and the game-over box's — the link built (and its QR code
// encoded) once here rather than every frame.
func (a *App) openShareFor(gameID string) {
	link, why := a.replayShareLink(gameID)
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

// handleGameOverActions is handleReplayActions for the game screen: the
// game-over box's Pin and Share (gameOverActions) and the share modal's
// own buttons, for the game just played, and whether the modal is up.
func (a *App) handleGameOverActions(gtx C, gameID string) (modal bool) {
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
	if a.gameOverPinBtn.Clicked(gtx) {
		a.togglePin(gameID, a.setGameOverNote)
	}
	if a.gameOverShareBtn.Clicked(gtx) {
		a.openShareFor(gameID)
	}
	return a.shareOpen
}

// gameOverActions is the row under a finished game's result box — the
// player's (gameOverBox) and the spectator's (spectatorResultBox) alike:
// Pin (Unpin while it is pinned), Share and Back to Lobby, the replay
// screen's three actions brought forward to the moment the game ends (on a
// compact screen Back goes under the other two). A
// game still running for the others (the local player out, their team
// playing on) has no replay to pin or share yet, so only Back is offered.
// note, when set, goes under the row (a refused pin).
func (a *App) gameOverActions(gtx C, view gameView) D {
	back := func(gtx C) D { return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby") }
	if !view.finished {
		return back(gtx)
	}
	pin := func(gtx C) D {
		label := "Pin"
		if view.pinned {
			label = "Unpin"
		}
		return a.secondaryButton(gtx, &a.gameOverPinBtn, label)
	}
	share := func(gtx C) D { return a.secondaryButton(gtx, &a.gameOverShareBtn, "Share") }
	row := func(gtx C) D {
		if a.form.compact {
			// A phone's box has no width for three: Back goes under the two.
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(pin),
						layout.Rigid(hSpacer(8)),
						layout.Rigid(share),
					)
				}),
				layout.Rigid(spacer(8)),
				layout.Rigid(back),
			)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(pin),
			layout.Rigid(hSpacer(8)),
			layout.Rigid(share),
			layout.Rigid(hSpacer(8)),
			layout.Rigid(back),
		)
	}
	if view.gameOverNote == "" {
		return row(gtx)
	}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(row),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.body(view.gameOverNote, colErr)),
	)
}

// shareModalOver stacks the share modal over a screen: the screen scrimmed
// and its clicks swallowed, the modal centered on top.
func (a *App) shareModalOver(gtx C, screen layout.Widget) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(screen),
		layout.Expanded(func(gtx C) D {
			fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), withAlpha(colBg, 0xc0))
			return D{Size: gtx.Constraints.Max}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			return a.shareOverlay(gtx)
		}),
	)
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
								layout.Rigid(a.body("Anyone who opens the link — or scans the code — watches this replay in their browser, on this server. No name to type: they watch as a Watcher_.", colMuted)),
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
