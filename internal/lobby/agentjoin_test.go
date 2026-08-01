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
	noAgents, err := human.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, false)
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
	oneAgent, err := human.CreateGame(ctx, config.ModeCompetitive, 3, 0, 1, false)
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

	gameID, err := human.CreateGame(ctx, config.ModeCompetitive, 4, 0, 1, false)
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

	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}

	// Full roster → starting. B un-joins → back to created with one seat free.
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

	// Once the game is started (the META is what StartGame transitions — the
	// listing stays "starting" until archived), unjoin must refuse.
	a.StartGame(ctx, gameID)
	deadline = time.Now().Add(3 * time.Second)
	for {
		meta, _, err := natspkg.FetchGameMeta(ctx, a.GetJS(), gameID)
		if err == nil && meta.Status == config.GameStatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for in_progress meta")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := b.UnjoinGame(ctx, gameID); !errors.Is(err, ErrGameStarted) {
		t.Fatalf("unjoin after start: got err %v, want ErrGameStarted", err)
	}
}
