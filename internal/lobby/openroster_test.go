package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// Seats are stable: the lowest free one is taken, a departed player's seat
// is the next one taken, and nobody else's seat moves.
func TestJoinAssignsLowestFreeSeat(t *testing.T) {
	lbs := setupLobbies(t, 3)
	a, b, c := lbs[0], lbs[1], lbs[2]
	ctx := context.Background()
	gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 3, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	for i, lb := range lbs {
		res, err := lb.JoinGame(ctx, gameID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.PlayerIdx != i {
			t.Fatalf("joiner %d got seat %d", i, res.PlayerIdx)
		}
	}
	if err := b.UnjoinGame(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	g := waitListing(t, a, gameID, "b's departure", func(g GameListing) bool { return len(g.Players) == 2 })
	if s, ok := g.SeatOf(c.PlayerID()); !ok || s.Seat != 2 {
		t.Fatalf("c's seat after b left = %+v, want seat 2 unmoved", s)
	}
	seat, _, ok := g.FreeSeat(0)
	if !ok || seat != 1 {
		t.Fatalf("free seat = %d (%v), want 1", seat, ok)
	}
	res, err := b.JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.PlayerIdx != 1 {
		t.Fatalf("b came back in seat %d, want its old 1", res.PlayerIdx)
	}
	if _, _, ok := (GameListing{Mode: config.ModeCooperative, PlayerCount: 1, Players: []PlayerSummary{{PlayerID: "x"}}}).FreeSeat(0); ok {
		t.Fatal("a full game has a free seat")
	}
}

// Teams seats are global — team × size + slot — so a cell's player index
// names one seat across every board; a departed teammate's slot is the next
// one taken on that team.
func TestTeamsSeatIsGlobal(t *testing.T) {
	lbs := setupLobbies(t, 3)
	ctx := context.Background()
	gameID, err := lbs[0].CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	r0, _ := lbs[0].JoinGame(ctx, gameID, 1)
	r1, _ := lbs[1].JoinGame(ctx, gameID, 1)
	r2, _ := lbs[2].JoinGame(ctx, gameID, 0)
	if r0.PlayerIdx != 2 || r0.TeamSlot != 0 || r1.PlayerIdx != 3 || r1.TeamSlot != 1 || r2.PlayerIdx != 0 {
		t.Fatalf("seats = %d/%d/%d, want team B's 2 and 3, team A's 0", r0.PlayerIdx, r1.PlayerIdx, r2.PlayerIdx)
	}
	if err := lbs[0].UnjoinGame(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	waitListing(t, lbs[1], gameID, "the departure", func(g GameListing) bool { return len(g.Players) == 2 })
	again, err := lbs[0].JoinGame(ctx, gameID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if again.PlayerIdx != 2 || again.TeamSlot != 0 {
		t.Fatalf("the freed team B slot came back as %+v, want seat 2 slot 0", again)
	}
}

// An open game starts on readiness — every playfield with a ready player,
// whoever else is seated and not ready yet — whatever seats stay free; an
// invite game on a full, ready table. The one toggle that moves the listing
// to starting is elected to run the countdown; a later toggle is not.
func TestReadyToStartOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    GameListing
		want bool
	}{
		{"open coop, one ready of three seats", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, Players: []PlayerSummary{{PlayerID: "a", Ready: true}}}, true},
		{"open coop, one ready and one not", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, Players: []PlayerSummary{{PlayerID: "a", Ready: true}, {PlayerID: "b", Seat: 1}}}, true},
		{"open coop, seated but nobody ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, Players: []PlayerSummary{{PlayerID: "a"}, {PlayerID: "b", Seat: 1}}}, false},
		{"open coop, nobody", GameListing{Mode: config.ModeCooperative, PlayerCount: 3}, false},
		{"open teams, a team empty", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}}}, false},
		{"open teams, a team seated but not ready", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}, {PlayerID: "b", Team: 1, Seat: 2}}}, false},
		{"open teams, one ready per team", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}, {PlayerID: "b", Ready: true, Team: 1, Seat: 2}}}, true},
		{"open teams, one ready per team and a teammate not", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}, {PlayerID: "c", Team: 0, Seat: 1, TeamSlot: 1}, {PlayerID: "b", Ready: true, Team: 1, Seat: 2}}}, true},
		{"open competitive, a board empty", GameListing{Mode: config.ModeCompetitive, PlayerCount: 3, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Seat: 0}, {PlayerID: "b", Ready: true, Seat: 1}}}, false},
		{"open competitive, a board seated but not ready", GameListing{Mode: config.ModeCompetitive, PlayerCount: 2, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Seat: 0}, {PlayerID: "b", Seat: 1}}}, false},
		{"open competitive, every board", GameListing{Mode: config.ModeCompetitive, PlayerCount: 2, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Seat: 0}, {PlayerID: "b", Ready: true, Seat: 1}}}, true},
		{"invite coop, ready but short", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, InviteOnly: true, Players: []PlayerSummary{{PlayerID: "a", Ready: true}}}, false},
		{"invite coop, full, one not ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 2, InviteOnly: true, Players: []PlayerSummary{{PlayerID: "a", Ready: true}, {PlayerID: "b", Seat: 1}}}, false},
		{"invite coop, full and ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 2, InviteOnly: true, Players: []PlayerSummary{{PlayerID: "a", Ready: true}, {PlayerID: "b", Ready: true, Seat: 1}}}, true},
	} {
		if got := tc.g.ReadyToStart(); got != tc.want {
			t.Errorf("%s: ReadyToStart() = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The bar's note: an open game's playfield with no ready player yet
	// (seated or not), nothing on a single playfield (the click is all it
	// waits for), an invite game's missing seats once everyone present is
	// ready — and nothing while its present players are not.
	for _, tc := range []struct {
		name string
		g    GameListing
		want string
	}{
		{"open teams, a team empty", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}}}, "waiting for a ready player on Team B"},
		{"open teams, a team seated but not ready", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}, {PlayerID: "b", Team: 1, Seat: 2}}}, "waiting for a ready player on Team B"},
		{"open teams, only the second team ready", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Team: 0}, {PlayerID: "b", Ready: true, Team: 1, Seat: 2}}}, "waiting for a ready player on Team A"},
		{"open teams, one ready per team", GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}, {PlayerID: "b", Ready: true, Team: 1, Seat: 2}}}, ""},
		{"open competitive, a board not ready", GameListing{Mode: config.ModeCompetitive, PlayerCount: 2, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Seat: 0}, {PlayerID: "b", Seat: 1}}}, "waiting for a ready player on every board"},
		{"open coop, nobody ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, Players: []PlayerSummary{{PlayerID: "a"}, {PlayerID: "b", Seat: 1}}}, ""},
		{"invite coop, short, one not ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, InviteOnly: true, Players: []PlayerSummary{{PlayerID: "a", Ready: true}, {PlayerID: "b", Seat: 1}}}, ""},
		{"invite coop, short, everyone ready", GameListing{Mode: config.ModeCooperative, PlayerCount: 3, InviteOnly: true, Players: []PlayerSummary{{PlayerID: "a", Ready: true}}}, "waiting for 2 more"},
	} {
		if got := tc.g.ReadyBlocker(); got != tc.want {
			t.Errorf("%s: ReadyBlocker() = %q, want %q", tc.name, got, tc.want)
		}
	}

	// On the wire: a two-seat open co-op game starts on its first ready
	// player; a joiner readying up during the countdown is not elected.
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()
	gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	res, err := a.ToggleReady(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AllReady {
		t.Fatal("the first ready player of an open game was not elected to start it")
	}
	waitListing(t, b, gameID, "starting", func(g GameListing) bool { return g.Status == config.GameStatusStarting })
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("join during the countdown: %v", err)
	}
	if res, err := b.ToggleReady(ctx, gameID); err != nil || res.AllReady {
		t.Fatalf("a joiner readying up during the countdown was elected too (err %v)", err)
	}

	// The other seat taken but not ready: the first ready player still
	// starts the game — the seated player who never clicked plays from the
	// start — and that player's own toggle, during the countdown, is not
	// elected.
	gameID, err = a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	res, err = a.ToggleReady(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AllReady {
		t.Fatal("the first ready player of an open game, the other seat taken and not ready, was not elected to start it")
	}
	waitListing(t, b, gameID, "starting", func(g GameListing) bool { return g.Status == config.GameStatusStarting })
	if res, err := b.ToggleReady(ctx, gameID); err != nil || res.AllReady {
		t.Fatalf("the other seat's player readying up during the countdown was elected too (err %v)", err)
	}
}

// A running open game takes joiners into its free seats; a running invite
// game refuses them; a game that is over takes nobody.
func TestJoinInProgress(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()
	for _, inviteOnly := range []bool{false, true} {
		gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}, InviteOnly: inviteOnly})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
			t.Fatal(err)
		}
		if inviteOnly {
			if err := a.Invite(ctx, b.PlayerID(), gameID, 0); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for b.InviteTo(gameID) == nil {
				if time.Now().After(deadline) {
					t.Fatal("b never saw its invitation")
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		a.StartGame(ctx, gameID)
		waitListing(t, b, gameID, "the in_progress mirror", func(g GameListing) bool { return g.Status == config.GameStatusInProgress })
		_, err = b.JoinGame(ctx, gameID, 0)
		if inviteOnly {
			if !errors.Is(err, ErrGameStarted) {
				t.Fatalf("join a running invite game: err %v, want ErrGameStarted", err)
			}
		} else if err != nil {
			t.Fatalf("join a running open game: %v", err)
		}
		// Over: nobody joins.
		meta, seq, err := natspkg.FetchGameMeta(ctx, a.GetJS(), gameID)
		if err != nil {
			t.Fatal(err)
		}
		meta.Status = config.GameStatusFinished
		data, _ := json.Marshal(meta)
		if err := natspkg.PublishMeta(ctx, a.GetJS(), gameID, data, seq); err != nil {
			t.Fatal(err)
		}
		if err := b.UnjoinGame(ctx, gameID); err != nil && !inviteOnly {
			t.Fatalf("leave a running open game: %v", err)
		}
	}
}

// Team names travel with the game — the meta every engine reads, the
// listing the lobby row and the join buttons read, the invitation the
// invitee's pop-up reads — and the ready hint names the team by them.
func TestTeamNamesOnTheWire(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()
	gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, TeamNames: []string{"Sharks", ""}, Rules: config.GameRules{Ghost: true}, InviteOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, a.GetJS(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TeamName(0) != "Sharks" || meta.TeamName(1) != "Blue" {
		t.Errorf("meta team names = %v", meta.TeamNames)
	}
	g := waitListing(t, b, gameID, "the listing", func(g GameListing) bool { return len(g.TeamNames) == 2 })
	if g.TeamName(0) != "Sharks" || g.TeamName(1) != "Blue" {
		t.Errorf("listing team names = %v", g.TeamNames)
	}
	if err := a.Invite(ctx, b.PlayerID(), gameID, 1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if inv := b.InviteTo(gameID); inv != nil {
			if inv.TeamName != "Blue" {
				t.Errorf("invitation names the team %q, want Blue", inv.TeamName)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b never saw its invitation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	open := GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, TeamNames: []string{"Sharks", "Jets"}, Players: []PlayerSummary{{PlayerID: "a", Ready: true, Team: 0}}}
	if got := open.ReadyBlocker(); got != "waiting for a ready player on Team Jets" {
		t.Errorf("ReadyBlocker() = %q", got)
	}
}
