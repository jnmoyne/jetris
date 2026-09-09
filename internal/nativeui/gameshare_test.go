package nativeui

// Sharing an open game from the lobby, and a game link's landing (share.go):
// the row's Share button and its link, the modal over the lobby, the login
// screen a nameless link waits on, and the seat the landing takes.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
	"jetris/internal/webdist"
)

// The game's share link is the replay's with &game= in place of &replay=,
// built from the same parts: the page, the server's WebSocket address and
// its name — a LAN party's own page and listener included.
func TestGameShareLink(t *testing.T) {
	const gameID = "1a2b-3c4d"
	a := newTestApp()
	a.connName, a.connURL = "My LAN", "ws://192.168.1.20:4223"
	link, why := a.gameShareLink(gameID)
	if why != "" {
		t.Fatalf("no link: %s", why)
	}
	page, q := parseShare(t, link)
	if page != webdist.DefaultPage+"join.html" || q.Get("server") != "ws://192.168.1.20:4223" || q.Get("name") != "My LAN" || q.Get("game") != gameID || q.Get("replay") != "" {
		t.Fatalf("link = %s", link)
	}
	// No WebSocket address to send a browser to: no link, and the reason.
	a.connName, a.connURL = "nats://10.0.0.5:4222", ""
	if link, why = a.gameShareLink(gameID); link != "" || !strings.Contains(why, "nats://") {
		t.Fatalf("link = %q why = %q, want no link and why", link, why)
	}
	a.embHTTPAddr, a.embWSAddr, a.embName, a.embHTTPScheme = "192.168.1.20:8080", "192.168.1.20:4223", "Party", "https"
	if link, why = a.gameShareLink(gameID); why != "" || !strings.Contains(link, "https://192.168.1.20:8080/join.html?") || !strings.Contains(link, "game="+gameID) {
		t.Fatalf("LAN link = %s (%s)", link, why)
	}
}

// Share is on every open game's row while the game takes joiners — full or
// not, before it starts and while it runs — and on no invite-only or
// abandoned one, nor a finished game's.
func TestCanShareGame(t *testing.T) {
	open := lobby.GameListing{GameID: "g", Mode: config.ModeCooperative, Status: config.GameStatusCreated, PlayerCount: 2}
	if !canShareGame(open, false) {
		t.Fatal("an open game waiting for players is not shareable")
	}
	full := open
	full.Players = []lobby.PlayerSummary{{PlayerID: "a", Seat: 0}, {PlayerID: "b", Seat: 1}}
	if !canShareGame(full, false) {
		t.Fatal("a full open game is not shareable — its seats free up")
	}
	running := open
	running.Status = config.GameStatusInProgress
	if !canShareGame(running, false) {
		t.Fatal("a running open game is not shareable — it takes joiners mid-game")
	}
	over := open
	over.Status = config.GameStatusFinished
	if canShareGame(over, false) {
		t.Fatal("a finished game is shareable")
	}
	invite := open
	invite.InviteOnly = true
	if canShareGame(invite, false) {
		t.Fatal("an invite-only game is shareable")
	}
	if canShareGame(open, true) {
		t.Fatal("an abandoned game is shareable")
	}
}

// The landing's word on the game it was sent to: a seat on the emptiest
// team of a teams game, a plain seat elsewhere — or why there is none, in
// the words of the lobby strip.
func TestLinkedJoinTeam(t *testing.T) {
	teams := lobby.GameListing{GameID: "teams-game-1234", Mode: config.ModeTeams, Status: config.GameStatusCreated,
		PlayerCount: 4, TeamCount: 2, TeamSize: 2,
		Players: []lobby.PlayerSummary{{PlayerID: "alice", Seat: 0, Team: 0}}}
	if team, why := linkedJoinTeam(teams, "bob", false); team != 1 || why != "" {
		t.Fatalf("team %d why %q, want the empty team B", team, why)
	}
	teams.Players = append(teams.Players, lobby.PlayerSummary{PlayerID: "carol", Seat: 2, Team: 1}, lobby.PlayerSummary{PlayerID: "dan", Seat: 3, Team: 1, TeamSlot: 1})
	if team, why := linkedJoinTeam(teams, "bob", false); team != 0 || why != "" {
		t.Fatalf("team %d why %q, want the one team with room", team, why)
	}
	teams.Players = append(teams.Players, lobby.PlayerSummary{PlayerID: "eve", Seat: 1, Team: 0, TeamSlot: 1})
	if _, why := linkedJoinTeam(teams, "bob", false); !strings.Contains(why, "full") || !strings.Contains(why, "teams-ga") {
		t.Fatalf("why = %q, want the game named and full", why)
	}
	// A seat already held is rejoined, whatever else.
	if team, why := linkedJoinTeam(teams, "carol", false); team != 0 || why != "" {
		t.Fatalf("rejoin: team %d why %q", team, why)
	}

	coop := lobby.GameListing{GameID: "coop-game", Mode: config.ModeCooperative, Status: config.GameStatusInProgress, PlayerCount: 2,
		Players: []lobby.PlayerSummary{{PlayerID: "alice", Seat: 0}}}
	if team, why := linkedJoinTeam(coop, "bob", false); team != 0 || why != "" {
		t.Fatalf("a running open game: team %d why %q, want a seat", team, why)
	}
	coop.Status = config.GameStatusFinished
	if _, why := linkedJoinTeam(coop, "bob", false); !strings.Contains(why, "over") {
		t.Fatalf("why = %q, want the game over", why)
	}
	invite := lobby.GameListing{GameID: "invite-game", Mode: config.ModeCompetitive, Status: config.GameStatusCreated, PlayerCount: 2, InviteOnly: true, CreatorID: "alice"}
	if _, why := linkedJoinTeam(invite, "bob", false); !strings.Contains(why, "invite only") {
		t.Fatalf("why = %q, want invite only", why)
	}
	if _, why := linkedJoinTeam(invite, "bob", true); why != "" {
		t.Fatalf("invited: why = %q, want a seat", why)
	}
	if _, why := linkedJoinTeam(invite, "alice", false); why != "" {
		t.Fatalf("the creator: why = %q, want a seat", why)
	}
	// An invite game that started takes nobody new — not even by link.
	invite.Status = config.GameStatusInProgress
	if _, why := linkedJoinTeam(invite, "bob", true); !strings.Contains(why, "any more") {
		t.Fatalf("why = %q, want no longer taking players", why)
	}
}

// The share modal lays out over the lobby, titled for the game, with the
// link and its code — and, with no link to build, with the reason.
func TestGameShareModalInLobby(t *testing.T) {
	a := newTestApp()
	a.lobby = lobby.New(nil, nil, "tester", "tester")
	a.screen = screenLobby
	a.connName, a.connURL = "My LAN", "ws://192.168.1.20:4223"
	a.openGameShare("open-game-abcdef")
	if !a.shareOpen || a.shareLink == "" || a.shareCode == nil || a.shareTitle != "SHARE THIS GAME" || !strings.Contains(a.shareNote, "open-gam") {
		t.Fatalf("openGameShare: open %v link %q code %v title %q note %q", a.shareOpen, a.shareLink, a.shareCode != nil, a.shareTitle, a.shareNote)
	}
	renderOnce(t, a)
	if !a.shareOpen || a.screen != screenLobby {
		t.Fatal("the lobby's frame closed the modal")
	}
	a.connName, a.connURL = "nats://10.0.0.5:4222", ""
	a.openGameShare("open-game-abcdef")
	if a.shareLink != "" || a.shareWhy == "" {
		t.Fatalf("openGameShare without a link: link %q why %q", a.shareLink, a.shareWhy)
	}
	renderOnce(t, a)
	// The modal belongs to the lobby it was opened over: a screen change
	// under it (a game starting, say) leaves it behind.
	a.screen = screenLogin
	renderOnce(t, a)
	if a.shareOpen {
		t.Fatal("the share modal outlived the lobby screen")
	}
}

// A game link with no name on it waits on the login screen — nobody plays
// under a name they never saw — which says which game Play joins; a name on
// the link goes straight through, as --name does.
func TestGameLinkWaitsForAName(t *testing.T) {
	a := NewWithPicker(config.Config{JoinGameID: "game-1234-5678"}, nil, "", nil)
	if a.autoLogin || a.loginEd.Text() != "" {
		t.Fatalf("auto-login %v, field %q: want the screen to wait with the field blank", a.autoLogin, a.loginEd.Text())
	}
	if a.linkedJoinGame() != "game-1234-5678" {
		t.Fatalf("linked join = %q, want the link's game", a.linkedJoinGame())
	}
	a.th = newUITheme()
	renderOnce(t, a) // the note names the game
	a = NewWithPicker(config.Config{JoinGameID: "game-1234-5678", PlayerName: "alice"}, nil, "", nil)
	if !a.autoLogin || a.loginEd.Text() != "alice" {
		t.Fatalf("with a name on the link: field %q, auto-login %v; want alice, armed", a.loginEd.Text(), a.autoLogin)
	}
	if a.takeLinkedJoin() != "game-1234-5678" || a.takeLinkedJoin() != "" {
		t.Fatal("the landing must be one-shot")
	}
}

// linkedGameServer starts a server with the lobby set up and a creator's
// lobby on it, for the games a link will be sent to.
func linkedGameServer(t *testing.T) (url string, creator *lobby.Lobby) {
	t.Helper()
	url, _ = testutil.StartServer(t)
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
	if err := natspkg.EnsureChatStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	creator = lobby.New(js, kv, "alice", "alice")
	if err := creator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(creator.Stop)
	return url, creator
}

// landWithLink sends a player to the server through a game link and waits
// for the landing to settle: the game screen, or a note in the lobby.
func landWithLink(t *testing.T, url, player, gameID string) *App {
	t.Helper()
	a := linkApp(t, config.Config{NATSURL: url, PlayerName: player, JoinGameID: gameID})
	renderOnce(t, a)
	waitFor(t, player+"'s landing", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenGame || a.lobbyErr != "" || a.loginErr != ""
	})
	return a
}

// A game link's landing: the player lands in the lobby and takes a seat in
// the game the link named — the emptiest team of a teams game — and is on
// the game screen; a game the link named that is full, or that the server
// never listed, is reported under the lobby banner with the player left in
// the lobby.
func TestLinkedGameJoins(t *testing.T) {
	url, creator := linkedGameServer(t)
	ctx := context.Background()

	// An open teams game with the creator on team A: the link lands on B.
	teamsID, err := creator.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.JoinGame(ctx, teamsID, 0); err != nil {
		t.Fatal(err)
	}
	bob := landWithLink(t, url, "bob", teamsID)
	bob.mu.Lock()
	screen, note, loginErr := bob.screen, bob.lobbyErr, bob.loginErr
	bob.mu.Unlock()
	if screen != screenGame {
		t.Fatalf("screen %v note %q err %q, want the game screen", screen, note, loginErr)
	}
	waitFor(t, "bob on the roster", func() bool {
		g, ok := creator.Games()[teamsID]
		return ok && rosterHas(g, "bob")
	})
	if p, ok := creator.Games()[teamsID].SeatOf("bob"); !ok || p.Team != 1 {
		t.Fatalf("bob's seat = %+v (found %v), want team B", p, ok)
	}
	if bob.takeLinkedJoin() != "" {
		t.Fatal("the landing must be one-shot")
	}

	// A one-seat open game the creator sits in: full, said so, and the
	// player is left in the lobby.
	fullID, err := creator.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.JoinGame(ctx, fullID, 0); err != nil {
		t.Fatal(err)
	}
	carol := landWithLink(t, url, "carol", fullID)
	carol.mu.Lock()
	screen, note = carol.screen, carol.lobbyErr
	carol.mu.Unlock()
	if screen != screenLobby || !strings.Contains(note, "full") || !strings.Contains(note, shortID(fullID)) {
		t.Fatalf("screen %v note %q, want the lobby and a note naming the full game", screen, note)
	}

	// A game this server never listed: the lobby, with the note.
	origWait := linkedJoinWait
	linkedJoinWait = 300 * time.Millisecond
	t.Cleanup(func() { linkedJoinWait = origWait })
	dan := landWithLink(t, url, "dan", "no-such-game")
	dan.mu.Lock()
	screen, note = dan.screen, dan.lobbyErr
	dan.mu.Unlock()
	if screen != screenLobby || !strings.Contains(note, "no-such-game") {
		t.Fatalf("screen %v note %q, want the lobby and a note naming the game", screen, note)
	}
}
