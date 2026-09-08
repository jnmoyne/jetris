package lobby

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// setupLobbies starts one embedded server and brings up n lobby instances on
// it (player-0..player-n-1) so team joins can be exercised across players.
func setupLobbies(t *testing.T, n int) []*Lobby {
	t.Helper()
	url, _ := testutil.StartServer(t)
	ctx := context.Background()

	lobbies := make([]*Lobby, n)
	for i := 0; i < n; i++ {
		nc, err := nats.Connect(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := natspkg.EnsureChatStream(ctx, js); err != nil {
				t.Fatal(err)
			}
		}
		kv, err := natspkg.EnsureLobbyKV(ctx, js)
		if err != nil {
			t.Fatal(err)
		}
		name := string(rune('A' + i))
		lb := New(js, kv, "player-"+name, name)
		if err := lb.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(lb.Stop)
		lobbies[i] = lb
	}
	time.Sleep(200 * time.Millisecond) // let KV watchers initialize
	return lobbies
}

func TestTeamsJoinAssignsSlotsAndRejectsFullTeam(t *testing.T) {
	lbs := setupLobbies(t, 4)
	ctx := context.Background()

	// 2v2 teams game: PlayerCount is the total, TeamSize per team.
	gameID, err := lbs[0].CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}

	// First two players take team 0 and get slots 0, 1.
	r0, err := lbs[0].JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r0.PlayerIdx != 0 || r0.Team != 0 || r0.TeamSlot != 0 {
		t.Fatalf("first join: got %+v, want idx 0 team 0 slot 0", r0)
	}
	r1, err := lbs[1].JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.PlayerIdx != 1 || r1.Team != 0 || r1.TeamSlot != 1 {
		t.Fatalf("second join: got %+v, want idx 1 team 0 slot 1", r1)
	}

	// Team 0 is full — a third join on it must be rejected.
	if _, err := lbs[2].JoinGame(ctx, gameID, 0); !errors.Is(err, ErrTeamFull) {
		t.Fatalf("join on full team: got err %v, want ErrTeamFull", err)
	}

	// Team 1 still has room.
	r2, err := lbs[2].JoinGame(ctx, gameID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Team != 1 || r2.TeamSlot != 0 {
		t.Fatalf("third join: got %+v, want team 1 slot 0", r2)
	}

	// Rejoining returns the same position instead of double-adding.
	again, err := lbs[2].JoinGame(ctx, gameID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if again != r2 {
		t.Fatalf("rejoin: got %+v, want %+v", again, r2)
	}

	// Last slot fills team 1: both teams full, the seats global and stable
	// (team × size + slot). An open game does not start on a full roster —
	// its players' readiness does: the listing stays created until the one
	// ready toggle that completes the table moves it to starting, electing
	// that one client to run the countdown.
	r3, err := lbs[3].JoinGame(ctx, gameID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r3.Team != 1 || r3.TeamSlot != 1 || r3.PlayerIdx != 3 {
		t.Fatalf("fourth join: got %+v, want team 1 slot 1 seat 3", r3)
	}
	if r2.PlayerIdx != 2 {
		t.Fatalf("third join's seat = %d, want 2", r2.PlayerIdx)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if g, ok := lbs[0].Games()[gameID]; ok && len(g.Players) == 4 {
			if g.Status != config.GameStatusCreated {
				t.Fatalf("a full open game moved to %s on its own", g.Status)
			}
			if got := g.TeamMemberCount(0); got != 2 {
				t.Fatalf("team 0 member count = %d, want 2", got)
			}
			if got := g.TeamMemberCount(1); got != 2 {
				t.Fatalf("team 1 member count = %d, want 2", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the roster to fill")
		}
		time.Sleep(20 * time.Millisecond)
	}
	elected := 0
	for _, lb := range lbs {
		res, err := lb.ToggleReady(ctx, gameID)
		if err != nil {
			t.Fatal(err)
		}
		if res.AllReady {
			elected++
		}
	}
	if elected != 1 {
		t.Fatalf("%d clients were elected to run the countdown, want exactly 1", elected)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if g, ok := lbs[0].Games()[gameID]; ok && g.Status == config.GameStatusStarting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for game to transition to starting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A teams game past the usual two: the meta and listing record the count, a
// seat on every team is joinable, and a team the game does not have is
// refused. The roster only fills — and the game only starts — once every team
// is full.
func TestTeamsThreeWayJoin(t *testing.T) {
	lbs := setupLobbies(t, 3)
	ctx := context.Background()

	// Three teams of one: PlayerCount is the total, TeamCount the teams.
	gameID, err := lbs[0].CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 3, TeamCount: 3, TeamSize: 1, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, lbs[0].GetJS(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Teams() != 3 || meta.TeamSize != 1 || meta.PlayerCount != 3 {
		t.Fatalf("meta = %d teams of %d, %d seats; want 3 of 1, 3 seats", meta.Teams(), meta.TeamSize, meta.PlayerCount)
	}

	// A team the game does not have is refused; every team it does have is
	// joinable.
	if _, err := lbs[0].JoinGame(ctx, gameID, 3); err == nil {
		t.Fatal("join on team 3 of a three-team game: want an error")
	}
	for i, lb := range lbs {
		r, err := lb.JoinGame(ctx, gameID, i)
		if err != nil {
			t.Fatalf("join team %d: %v", i, err)
		}
		if r.Team != i || r.TeamSlot != 0 {
			t.Fatalf("join team %d: got %+v, want team %d slot 0", i, r, i)
		}
	}

	// Every team seated: an open game starts once everyone present is ready
	// — here, once the last of the three toggles.
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, ok := lbs[0].Games()[gameID]
		if ok && len(g.Players) == 3 {
			if g.Teams() != 3 {
				t.Fatalf("listing Teams() = %d, want 3", g.Teams())
			}
			for team := 0; team < 3; team++ {
				if got := g.TeamMemberCount(team); got != 1 {
					t.Fatalf("team %d member count = %d, want 1", team, got)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the three-team roster to fill")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i, lb := range lbs {
		res, err := lb.ToggleReady(ctx, gameID)
		if err != nil {
			t.Fatal(err)
		}
		if res.AllReady != (i == len(lbs)-1) {
			t.Fatalf("toggle %d: elected %v, want the last toggle alone", i, res.AllReady)
		}
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if g, ok := lbs[0].Games()[gameID]; ok && g.Status == config.GameStatusStarting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the three-team game to transition to starting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTeamsConcurrentJoinsRespectCapacity(t *testing.T) {
	lbs := setupLobbies(t, 3)
	ctx := context.Background()

	// 1v1: a single slot per team — concurrent joins on team 0 must produce
	// exactly one member (the CAS loop serializes the capacity check).
	gameID, err := lbs[0].CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}

	type res struct {
		r   JoinResult
		err error
	}
	results := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func(lb *Lobby) {
			r, err := lb.JoinGame(ctx, gameID, 0)
			results <- res{r, err}
		}(lbs[i])
	}
	var ok, full int
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err == nil && r.r.Team == 0:
			ok++
		case errors.Is(r.err, ErrTeamFull):
			full++
		default:
			t.Fatalf("unexpected join result: %+v err %v", r.r, r.err)
		}
	}
	if ok != 1 || full != 1 {
		t.Fatalf("concurrent joins: %d succeeded, %d rejected; want 1 and 1", ok, full)
	}
}
