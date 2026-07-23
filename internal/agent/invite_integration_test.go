package agent

import (
	"context"
	"testing"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/testutil"
)

// TestInviteAgents: a "human" creates an INVITE-ONLY competitive game with the
// agent policy fully closed (maxAgents 0) and invites two resident agents by
// name — the invitations alone admit them. Both accept automatically, play
// the game out, and a third, uninvited resident agent never touches it.
func TestInviteAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("plays a full invitation game against an embedded server")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	agentCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()

	human, js := startHumanLobby(t, ctx, url)

	fast := DifficultyHard.Tuning()
	fast.PieceDelay, fast.MoveDelay = 0, 0
	sloppy := sloppyTuning()

	// Three resident agents; only the first two get invited.
	results := make(chan agentOutcome, 3)
	names := []string{"inv-a", "inv-b", "inv-c"}
	tunings := []*Tuning{&fast, &sloppy, &fast}
	for i := range names {
		go func(name string, tun *Tuning, seed uint64) {
			res, err := Run(agentCtx, Config{
				NATS:        config.Config{NATSURL: url},
				Name:        name,
				Difficulty:  DifficultyHard,
				Tuning:      tun,
				AutoJoin:    true, // scanning residents must still skip the invite-only game
				WaitTimeout: time.Minute,
				Seed:        seed,
				Logf:        t.Logf,
			})
			results <- agentOutcome{res, err}
		}(names[i], tunings[i], uint64(i+1))
	}

	// Wait until all three residents are visible in the lobby, then find the
	// two invitees' full (instance-suffixed) player IDs.
	var invitees []string
	deadline := time.Now().Add(15 * time.Second)
	for {
		invitees = invitees[:0]
		for id, p := range human.Players() {
			if p.Agent && (hasPrefix(id, "inv-a-") || hasPrefix(id, "inv-b-")) {
				invitees = append(invitees, id)
			}
		}
		if len(invitees) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents not in presence: %+v", human.Players())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Invite-only, agents-closed: invitations are the only way in.
	gameID, err := human.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range invitees {
		if err := human.Invite(ctx, id, gameID, 0); err != nil {
			t.Fatal(err)
		}
	}

	waitForArchive(t, ctx, js, gameID, 90*time.Second)

	rec, ok := fetchArchive(t, ctx, js, gameID)
	if !ok {
		t.Fatal("invitation game was not archived")
	}
	if len(rec.Players) != 2 {
		t.Fatalf("archive has %d players, want the 2 invitees: %+v", len(rec.Players), rec.Players)
	}
	for _, p := range rec.Players {
		if hasPrefix(p.PlayerID, "inv-c-") {
			t.Fatalf("uninvited agent %s played the invite-only game", p.PlayerID)
		}
	}

	// Wind down: the two invitees report the game; the uninvited one reports none.
	stopAgents()
	played := map[string]int{}
	for i := 0; i < 3; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("agent run failed: %v", o.err)
			}
			played[o.res.GameID] += o.res.Games
		case <-ctx.Done():
			t.Fatal("agents did not exit after cancel")
		}
	}
	if played[gameID] != 2 {
		t.Fatalf("agents played the invite game %d times, want 2 (results: %v)", played[gameID], played)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
