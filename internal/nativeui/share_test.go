package nativeui

// The replay screen's Share and a share link's landing (share.go).

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/prefs"
	"jetris/internal/testutil"
	"jetris/internal/webdist"
)

// parseShare reads a share link back into its page and query.
func parseShare(t *testing.T, link string) (page string, q url.Values) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("share link %q: %v", link, err)
	}
	q = u.Query()
	u.RawQuery = ""
	return u.String(), q
}

// The share link names the page, the server and the game — from wherever
// this build is: a WebSocket server it dialed goes in as is; a nats://
// official server is swapped for its WebSocket sibling among the favorites;
// a nats:// server nobody bookmarked over WebSocket has no link, and the
// modal says why; and a LAN party's link is its own page and listener.
func TestReplayShareLink(t *testing.T) {
	const gameID = "1a2b-3c4d"
	a := newTestApp()
	a.favorites = prefs.DefaultFavorites()

	// A WebSocket URL, reached through a favorite: the favorite's label rides
	// along and the page is the GitHub Pages copy (no page of our own on the
	// desktop).
	a.connName, a.connURL = "My LAN", "ws://192.168.1.20:4223"
	link, why := a.replayShareLink(gameID)
	if why != "" {
		t.Fatalf("no link: %s", why)
	}
	page, q := parseShare(t, link)
	if page != webdist.DefaultPage+"join.html" || q.Get("server") != "ws://192.168.1.20:4223" || q.Get("name") != "My LAN" || q.Get("replay") != gameID {
		t.Fatalf("link = %s", link)
	}

	// The official servers are bookmarked both ways: on the nats:// one, the
	// link carries the wss:// one, under the favorite's label.
	a.connName, a.connURL = "Jetris EU central", "nats://eu-central.jetris.net:4222"
	link, why = a.replayShareLink(gameID)
	if why != "" {
		t.Fatalf("no link: %s", why)
	}
	if _, q = parseShare(t, link); q.Get("server") != prefs.JetrisEUWS.URL || q.Get("name") != "Jetris EU central" || q.Get("replay") != gameID {
		t.Fatalf("link = %s", link)
	}
	// A context's name means nothing to whoever opens the link: the sibling
	// favorite's label stands in.
	a.connName, a.connURL = "context prod", "nats://eu-central.jetris.net:4222"
	if link, _ = a.replayShareLink(gameID); !strings.Contains(link, "name=Jetris+EU+central") {
		t.Fatalf("link = %s, want the favorite's label", link)
	}
	// A plain URL with no name to go by is the header's NAME
	// (connectionParts): still the server.
	a.connName, a.connURL = "wss://demo.nats.io:8443", ""
	if link, why = a.replayShareLink(gameID); why != "" || !strings.Contains(link, "server=wss%3A%2F%2Fdemo.nats.io%3A8443") || strings.Contains(link, "name=") {
		t.Fatalf("link = %s (%s), want the bare server and no name", link, why)
	}

	// Plain NATS to a server nobody bookmarked over WebSocket: no link, and
	// the reason names the scheme.
	a.connName, a.connURL = "nats://10.0.0.5:4222", ""
	if link, why = a.replayShareLink(gameID); link != "" || !strings.Contains(why, "nats://") {
		t.Fatalf("link = %q why = %q, want no link and why", link, why)
	}
	// Unless the player bookmarked it.
	a.favorites = append(a.favorites, prefs.Favorite{Label: "Office", URL: "ws://10.0.0.5:4223"})
	if link, why = a.replayShareLink(gameID); why != "" || !strings.Contains(link, "server=ws%3A%2F%2F10.0.0.5%3A4223") || !strings.Contains(link, "name=Office") {
		t.Fatalf("link = %s (%s), want the bookmarked WebSocket sibling", link, why)
	}

	// A LAN party shares its own page and listener, whatever else is set.
	a.embHTTPAddr, a.embWSAddr, a.embName, a.embHTTPScheme = "192.168.1.20:8080", "192.168.1.20:4223", "Party", "https"
	link, why = a.replayShareLink(gameID)
	if why != "" {
		t.Fatalf("no link: %s", why)
	}
	if page, q = parseShare(t, link); page != "https://192.168.1.20:8080/join.html" || q.Get("server") != "wss://192.168.1.20:8080" || q.Get("name") != "Party" || q.Get("replay") != gameID {
		t.Fatalf("LAN link = %s", link)
	}
}

// The share modal lays out over the replay screen — with a link and its QR
// code, and without one — and ESC closes it before it closes the replay.
func TestReplayShareModalLayout(t *testing.T) {
	a := newTestApp()
	rv := loadedReplay(sampleReplayRecord())
	a.replayView = rv
	a.screen = screenReplay
	a.connName, a.connURL = "My LAN", "ws://192.168.1.20:4223"
	a.openShare(rv)
	if !a.shareOpen || a.shareLink == "" || a.shareCode == nil || a.shareWhy != "" {
		t.Fatalf("openShare: open %v link %q code %v why %q", a.shareOpen, a.shareLink, a.shareCode != nil, a.shareWhy)
	}
	renderOnce(t, a)
	a.connName, a.connURL = "nats://10.0.0.5:4222", ""
	a.openShare(rv)
	if a.shareLink != "" || a.shareWhy == "" {
		t.Fatalf("openShare without a link: link %q why %q", a.shareLink, a.shareWhy)
	}
	renderOnce(t, a)
	if a.screen != screenReplay || a.replayView != rv {
		t.Fatal("the modal closed the replay")
	}
}

// A share link's landing: the game the link names opens on the replay
// screen as soon as the lobby holds its record — and a game the server's
// history never produces is reported under the lobby banner, with the
// player left in the lobby.
func TestLinkedReplayOpens(t *testing.T) {
	const gameID = "g-shared"
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	if err := natspkg.EnsureReplayStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	rec := config.ArchiveRecord{GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 1,
		Players: []config.PlayerResult{{PlayerID: "alice"}}, FinishedAt: time.Now()}
	data, _ := json.Marshal(rec)
	if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(ctx, config.ReplayMarkerSubject(gameID), []byte(`{"msgs":0}`)); err != nil {
		t.Fatal(err)
	}

	a := linkApp(t, config.Config{NATSURL: url, PlayerName: "alice", ReplayGameID: gameID})
	renderOnce(t, a)
	waitFor(t, "the replay screen", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenReplay || a.loginErr != ""
	})
	a.mu.Lock()
	rv, screen, loginErr := a.replayView, a.screen, a.loginErr
	a.mu.Unlock()
	if screen != screenReplay || rv == nil || rv.rec.GameID != gameID {
		t.Fatalf("screen %v, replay %v, err %q: want the linked replay open", screen, rv, loginErr)
	}
	if a.takeLinkedReplay() != "" {
		t.Fatal("the landing must be one-shot")
	}
	renderOnce(t, a)

	// A game this server never played: the lobby, with the note.
	origWait := linkedReplayWait
	linkedReplayWait = 300 * time.Millisecond
	t.Cleanup(func() { linkedReplayWait = origWait })
	b := linkApp(t, config.Config{NATSURL: url, PlayerName: "bob", ReplayGameID: "no-such-game"})
	renderOnce(t, b)
	waitFor(t, "the lobby's note", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.lobbyErr != "" || b.loginErr != ""
	})
	b.mu.Lock()
	screen, note := b.screen, b.lobbyErr
	b.mu.Unlock()
	if screen != screenLobby || !strings.Contains(note, "no-such-game") {
		t.Fatalf("screen %v note %q, want the lobby and a note naming the game", screen, note)
	}
}
