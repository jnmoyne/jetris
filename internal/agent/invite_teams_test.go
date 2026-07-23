package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

// A human creates a 2v2 invite-only teams game (4 players total), invites 4
// agents balanced 2+2, and does NOT join. The game must fill and start on its
// own and archive — the creator is a bystander, not a required 5th player.
func TestInviteTeamsFillsWithoutCreator(t *testing.T) {
	if testing.Short() {
		t.Skip("full game")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	agentCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()

	human, js := startHumanLobby(t, ctx, url)

	fast := DifficultyHard.Tuning()
	fast.PieceDelay, fast.MoveDelay = 0, 0

	results := make(chan agentOutcome, 4)
	names := []string{"ta", "tb", "tc", "td"}
	for i := range names {
		go func(name string, seed uint64) {
			res, err := Run(agentCtx, Config{
				NATS: config.Config{NATSURL: url}, Name: name,
				Difficulty: DifficultyHard, Tuning: &fast,
				WaitTimeout: time.Minute, Seed: seed, Logf: t.Logf,
			})
			results <- agentOutcome{res, err}
		}(names[i], uint64(i+1))
	}

	// Wait for all four agents to be idle in the lobby, capture their IDs.
	var ids []string
	deadline := time.Now().Add(15 * time.Second)
	for {
		ids = ids[:0]
		for id, p := range human.Players() {
			if p.Agent {
				ids = append(ids, id)
			}
		}
		if len(ids) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents not all present: %v", human.Players())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 2v2 invite-only teams (playerCount 4, teamSize 2), agents closed by policy.
	gameID, err := human.CreateGame(ctx, config.ModeTeams, 4, 2, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	// Invite 2 to team 0, 2 to team 1.
	for i, id := range ids {
		if err := human.Invite(ctx, id, gameID, i%2); err != nil {
			t.Fatal(err)
		}
	}

	// The game must FILL (4 agents) and START on its own — the creator never
	// joins. (Two evenly-matched hard teams can play for minutes, so this
	// checks the start, not the finish.)
	startDeadline := time.Now().Add(60 * time.Second)
	for {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		g := human.Games()[gameID]
		meta, _, merr := natspkg.FetchGameMeta(ctx, js, gameID)
		gone := merr != nil // archived: stream deleted
		started := gone || (merr == nil && meta.Status != config.GameStatusCreated && meta.Status != config.GameStatusStarting)
		if started {
			// The game reached in_progress (or beyond) without the human.
			for _, p := range g.Players {
				if p.PlayerID == human.PlayerID() {
					t.Fatalf("creator ended up in the roster: %+v", g.Players)
				}
			}
			t.Log("game started with agents only — creator not required")
			return
		}
		if len(g.Players) > 4 {
			t.Fatalf("roster overfilled: %d players", len(g.Players))
		}
		if time.Now().After(startDeadline) {
			t.Fatalf("game did not start; roster %d/4", len(g.Players))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Over-subscribing a team must not wedge agents in a retry loop: three agents
// invited to the same team of a 2v2 — one gets in, the other two decline the
// unsatisfiable invitation and go back to scanning instead of spamming joins.
func TestInviteTeamsOversubscribedDeclines(t *testing.T) {
	if testing.Short() {
		t.Skip("agents against an embedded server")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	agentCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()

	human, _ := startHumanLobby(t, ctx, url)
	nc, _, kv, err := natspkg.Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	fast := DifficultyHard.Tuning()
	fast.PieceDelay, fast.MoveDelay = 0, 0
	results := make(chan agentOutcome, 3)
	for i, name := range []string{"os-a", "os-b", "os-c"} {
		go func(name string, seed uint64) {
			res, err := Run(agentCtx, Config{
				NATS: config.Config{NATSURL: url}, Name: name,
				Difficulty: DifficultyHard, Tuning: &fast,
				WaitTimeout: time.Minute, Seed: seed, Logf: t.Logf,
			})
			results <- agentOutcome{res, err}
		}(name, uint64(i+1))
	}

	var ids []string
	deadline := time.Now().Add(15 * time.Second)
	for {
		ids = ids[:0]
		for id, p := range human.Players() {
			if p.Agent {
				ids = append(ids, id)
			}
		}
		if len(ids) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents not present: %v", human.Players())
		}
		time.Sleep(200 * time.Millisecond)
	}

	gameID, err := human.CreateGame(ctx, config.ModeTeams, 4, 2, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	// All three invited to team 0 (capacity 2) — one too many.
	for _, id := range ids {
		if err := human.Invite(ctx, id, gameID, 0); err != nil {
			t.Fatal(err)
		}
	}

	// Within a few seconds every invitation must be answered (each agent
	// either joined — key deleted — or declined — key marked Declined), and
	// team 0 holds at most its 2 seats — no endless retry loop.
	deadline = time.Now().Add(20 * time.Second)
	for {
		pending := 0
		for _, id := range ids {
			entry, err := kv.Get(ctx, config.LobbyInviteKey(id, gameID))
			if err != nil {
				continue // deleted: accepted (consumed by the join)
			}
			var inv lobby.Invitation
			if json.Unmarshal(entry.Value(), &inv) == nil && inv.Declined {
				continue // answered: declined (kept for the inviter to see)
			}
			pending++
		}
		g := human.Games()[gameID]
		if pending == 0 {
			if g.TeamMemberCount(0) > 2 {
				t.Fatalf("team 0 over capacity: %d members", g.TeamMemberCount(0))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d invitations still pending after 20s (retry loop?)", pending)
		}
		time.Sleep(300 * time.Millisecond)
	}
	stopAgents()
	for i := 0; i < 3; i++ {
		select {
		case <-results:
		case <-ctx.Done():
			t.Fatal("agents did not exit")
		}
	}
}
