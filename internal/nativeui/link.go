package nativeui

// The NATS link's health, for the game HUD and the NATS-messages panel.
//
// A dropped socket — a tablet's WiFi blip — is otherwise invisible: nats.go
// reconnects on its own (after its reconnect wait), and meanwhile every move
// waits in the engine's queue behind the in-flight publish, which waits out
// its ack (jetstream's 5 s default timeout) before the queue moves on — so
// the game freezes for a few seconds and then catches up in a burst, with
// nothing on screen to say why. The HUD's LINK stat names the pause while it
// lasts (link.go's handlers, installed by watchLink when the app adopts a
// connection), and the NATS-messages panel keeps the events.

import (
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// watchLink installs the connection's disconnect / reconnect / close
// callbacks and starts from a healthy link. nats.go runs the callbacks on
// its own goroutine, hence the lock and the invalidate in each.
func (a *App) watchLink(nc *nats.Conn) {
	a.mu.Lock()
	a.linkDownAt, a.linkErr = time.Time{}, ""
	a.mu.Unlock()
	nc.SetDisconnectErrHandler(func(_ *nats.Conn, err error) { a.linkLost(err) })
	nc.SetReconnectHandler(func(nc *nats.Conn) { a.linkBack(nc.ConnectedUrl()) })
	nc.SetClosedHandler(func(_ *nats.Conn) { a.linkClosed() })
}

// linkLost: the connection dropped; nats.go is reconnecting.
func (a *App) linkLost(err error) {
	msg := "connection lost"
	if err != nil {
		msg += ": " + err.Error()
	}
	a.mu.Lock()
	if a.linkDownAt.IsZero() {
		a.linkDownAt = time.Now()
	}
	a.linkErr = msg
	a.mu.Unlock()
	a.linkNote(msg + " — reconnecting")
	a.invalidate()
}

// linkBack: reconnected (to url).
func (a *App) linkBack(url string) {
	a.mu.Lock()
	down := linkDownFor(a.linkDownAt, time.Now())
	a.linkDownAt, a.linkErr = time.Time{}, ""
	a.mu.Unlock()
	a.linkNote(fmt.Sprintf("reconnected to %s after %s", url, formatLinkDown(down)))
	a.invalidate()
}

// linkClosed: the connection is gone for good (nats.go gave up, or the app
// closed it on the way out).
func (a *App) linkClosed() {
	a.mu.Lock()
	if a.linkDownAt.IsZero() {
		a.linkDownAt = time.Now()
	}
	a.linkErr = "connection closed"
	a.mu.Unlock()
	a.linkNote("connection closed")
	a.invalidate()
}

// linkNote puts a link event in the NATS-messages panel (kept only while
// the panel is shown, like every message).
func (a *App) linkNote(text string) {
	a.recordStreamMsg(time.Now(), "link", []byte(text), "")
}

// linkDownFor is how long the link has been down at now: 0 for a healthy
// link (since zero), never 0 for a down one.
func linkDownFor(since, now time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return max(now.Sub(since), time.Millisecond)
}

// formatSurvived renders a survival game's time — the HUD's running clock,
// the game-over box's result, the history's headline — as minutes and
// seconds, with the hours ahead of them past the first: "3:42", "1:02:03".
func formatSurvived(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// formatLinkDown renders an outage's length for the HUD: tenths under ten
// seconds, whole seconds under a minute, minutes past that.
func formatLinkDown(d time.Duration) string {
	s := d.Seconds()
	switch {
	case s < 10:
		return fmt.Sprintf("%.1fs", s)
	case s < 60:
		return fmt.Sprintf("%ds", int(s))
	default:
		return fmt.Sprintf("%dm%02ds", int(s)/60, int(s)%60)
	}
}
