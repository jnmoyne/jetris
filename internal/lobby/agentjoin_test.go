package lobby

import (
	"context"
	"errors"
	"testing"
	"time"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// A game with no agent seats rejects agents and accepts humans; one with agent
// seats accepts an agent and stamps the roster entry.
func TestAgentJoinPolicy(t *testing.T) {
	lbs := setupLobbies(t, 3)
	human, agent1, agent2 := lbs[0], lbs[1], lbs[2]
	agent1.SetAgent(true)
	agent2.SetAgent(true)
	ctx := context.Background()

	// maxAgents 0: agents may not join at all.
	noAgents, err := human.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent1.JoinGame(ctx, noAgents, 0); !errors.Is(err, ErrAgentsNotAllowed) {
		t.Fatalf("agent join on no-agents game: got err %v, want ErrAgentsNotAllowed", err)
	}
	if _, err := human.JoinGame(ctx, noAgents, 0); err != nil {
		t.Fatalf("human join on no-agents game: %v", err)
	}

	// maxAgents 1: one agent in, the second rejected, humans unaffected.
	oneAgent, err := human.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 3, MaxAgents: 1, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent1.JoinGame(ctx, oneAgent, 0); err != nil {
		t.Fatalf("first agent join: %v", err)
	}
	if _, err := agent2.JoinGame(ctx, oneAgent, 0); !errors.Is(err, ErrAgentSlotsFull) {
		t.Fatalf("second agent join: got err %v, want ErrAgentSlotsFull", err)
	}
	if _, err := human.JoinGame(ctx, oneAgent, 0); err != nil {
		t.Fatalf("human join with agent seats full: %v", err)
	}

	// The roster entry is stamped as an agent; the human's is not.
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, ok := human.Games()[oneAgent]
		if ok && len(g.Players) == 2 {
			if g.AgentCount() != 1 {
				t.Fatalf("AgentCount = %d, want 1 (players %+v)", g.AgentCount(), g.Players)
			}
			for _, p := range g.Players {
				if p.Agent != (p.PlayerID == agent1.PlayerID()) {
					t.Fatalf("agent flag wrong on %+v", p)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for listing to show both players")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Concurrent agent joins racing for one agent seat: the CAS loop must admit
// exactly one and reject the rest with ErrAgentSlotsFull.
func TestAgentsConcurrentJoinsRespectCap(t *testing.T) {
	lbs := setupLobbies(t, 4)
	human := lbs[0]
	agents := lbs[1:]
	for _, b := range agents {
		b.SetAgent(true)
	}
	ctx := context.Background()

	gameID, err := human.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 4, MaxAgents: 1, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}

	type res struct{ err error }
	results := make(chan res, len(agents))
	for _, b := range agents {
		go func(lb *Lobby) {
			_, err := lb.JoinGame(ctx, gameID, 0)
			results <- res{err}
		}(b)
	}
	var ok, full int
	for range agents {
		r := <-results
		switch {
		case r.err == nil:
			ok++
		case errors.Is(r.err, ErrAgentSlotsFull):
			full++
		default:
			t.Fatalf("unexpected join error: %v", r.err)
		}
	}
	if ok != 1 || full != len(agents)-1 {
		t.Fatalf("concurrent agent joins: %d succeeded, %d rejected; want 1 and %d", ok, full, len(agents)-1)
	}
}

// UnjoinGame frees the seat pre-start, reverts starting→created when the
// roster is no longer full, and refuses once the game has started.
func TestUnjoinGame(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}

	// An open game never moves to starting on a full roster — its players'
	// readiness starts it — so the roster fills and stays created. B un-joins
	// → one seat free, still created.
	if err := b.UnjoinGame(ctx, gameID); err != nil {
		t.Fatalf("unjoin: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, ok := a.Games()[gameID]
		if ok && len(g.Players) == 1 && g.Status == config.GameStatusCreated {
			if g.Players[0].PlayerID != a.PlayerID() {
				t.Fatalf("remaining player = %s, want %s", g.Players[0].PlayerID, a.PlayerID())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing after unjoin = %+v (ok=%v), want 1 player, created", g, ok)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The freed seat is joinable again and re-fills the game.
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("rejoin after unjoin: %v", err)
	}

	// Once the game is started — the META is what StartGame transitions, and
	// the start mirrors in_progress onto the listing — an OPEN game's seats
	// still come and go: B un-joins mid-game and the seat is free again,
	// joinable by anyone (B itself here), the game running all along.
	a.StartGame(ctx, gameID)
	deadline = time.Now().Add(3 * time.Second)
	for {
		meta, _, err := natspkg.FetchGameMeta(ctx, a.GetJS(), gameID)
		g, ok := a.Games()[gameID]
		if err == nil && meta.Status == config.GameStatusInProgress && ok && g.Status == config.GameStatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the in_progress meta and its listing mirror")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := b.UnjoinGame(ctx, gameID); err != nil {
		t.Fatalf("unjoin from a running open game: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		g, ok := a.Games()[gameID]
		if ok && len(g.Players) == 1 && g.Status == config.GameStatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing after a mid-game unjoin = %+v (ok=%v), want 1 player, in progress", g, ok)
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := b.JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatalf("join a running open game: %v", err)
	}
	if res.PlayerIdx != 1 {
		t.Fatalf("the freed seat was %d, want 1 (a's seat 0 never moved)", res.PlayerIdx)
	}

	// An INVITE game's roster is frozen once the game starts: unjoin refuses.
	invite, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{Ghost: true}, InviteOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, invite, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.Invite(ctx, b.PlayerID(), invite, 0); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if _, err := b.JoinGame(ctx, invite, 0); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b never saw its invitation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.StartGame(ctx, invite)
	deadline = time.Now().Add(3 * time.Second)
	for {
		meta, _, err := natspkg.FetchGameMeta(ctx, a.GetJS(), invite)
		if err == nil && meta.Status == config.GameStatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the invite game's in_progress meta")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := b.UnjoinGame(ctx, invite); !errors.Is(err, ErrGameStarted) {
		t.Fatalf("unjoin after an invite game's start: got err %v, want ErrGameStarted", err)
	}
}

// In teams mode the agent policy is per team: with MaxAgents 1 a 2v2 seats
// one agent on EACH team — a second agent asking for a team whose agent
// seat is taken is refused, and lands on the other team; humans are
// unaffected. The listing also carries the creator's pause-when-alone rule
// for the agents to read.
func TestAgentJoinPolicyPerTeam(t *testing.T) {
	lbs := setupLobbies(t, 3)
	human, agent1, agent2 := lbs[0], lbs[1], lbs[2]
	agent1.SetAgent(true)
	agent2.SetAgent(true)
	ctx := context.Background()

	gameID, err := human.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, MaxAgents: 1, AgentsPauseAlone: true, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent1.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("first agent on team A: %v", err)
	}
	if _, err := agent2.JoinGame(ctx, gameID, 0); !errors.Is(err, ErrAgentSlotsFull) {
		t.Fatalf("second agent on team A: got err %v, want ErrAgentSlotsFull", err)
	}
	res, err := agent2.JoinGame(ctx, gameID, 1)
	if err != nil {
		t.Fatalf("second agent on team B: %v", err)
	}
	if res.Team != 1 {
		t.Fatalf("second agent landed on team %d, want 1", res.Team)
	}
	if _, err := human.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("human on team A beside the agent: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, ok := human.Games()[gameID]
		if ok && len(g.Players) == 3 {
			if g.TeamAgentCount(0) != 1 || g.TeamAgentCount(1) != 1 || g.AgentCount() != 2 {
				t.Fatalf("agents per team = %d/%d (total %d), want 1/1 (2): %+v", g.TeamAgentCount(0), g.TeamAgentCount(1), g.AgentCount(), g.Players)
			}
			if g.AgentSeatFree(0) || g.AgentSeatFree(1) {
				t.Fatalf("a team still has an agent seat free: %+v", g.Players)
			}
			if !g.AgentsPauseAlone {
				t.Fatalf("the listing lost the pause-when-alone rule: %+v", g)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the listing to show all three players")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
