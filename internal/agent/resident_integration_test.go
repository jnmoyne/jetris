package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// TestResidentAgents exercises the resident lifecycle end to end: two resident
// agents idle in the lobby, skip a game whose creator disallowed agents, fill and
// play two consecutive agent-allowed games created by a "human" lobby client,
// and exit cleanly when interrupted, each reporting two games played.
func TestResidentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("plays two full games against an embedded server")
	}
	url, _ := testutil.StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	agentCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()

	// The "human": a plain (non-agent) lobby client that creates games.
	nc, js, kv, err := natspkg.Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	human := lobby.New(js, kv, "human", "human")
	if err := human.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer human.Stop()

	// Two resident agents (auto-join, no Once), rigged for a fast game.
	fast := DifficultyHard.Tuning()
	fast.PieceDelay, fast.MoveDelay = 0, 0
	sloppy := fast
	sloppy.BlunderRate, sloppy.BlunderDepth = 1, 40

	type outcome struct {
		res Result
		err error
	}
	results := make(chan outcome, 2)
	for i, tun := range []*Tuning{&fast, &sloppy} {
		go func(name string, tun *Tuning, seed uint64) {
			res, err := Run(agentCtx, Config{
				NATS:        config.Config{NATSURL: url},
				Name:        name,
				Difficulty:  DifficultyHard,
				Tuning:      tun,
				AutoJoin:    true,
				WaitTimeout: time.Minute,
				Seed:        seed,
				Logf:        t.Logf,
			})
			results <- outcome{res, err}
		}([]string{"agent-res-a", "agent-res-b"}[i], tun, uint64(i+1))
	}

	// A no-agents game: the resident agents must never touch it.
	noAgents, err := human.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}

	// Both agents must be visible in the lobby's presence list, flagged as agents
	// (their difficulty-suffixed names must be valid presence KV keys).
	presenceDeadline := time.Now().Add(15 * time.Second)
	for {
		agents := 0
		for _, p := range human.Players() {
			if p.Agent {
				agents++
			}
		}
		if agents == 2 {
			break
		}
		if time.Now().After(presenceDeadline) {
			t.Fatalf("lobby presence shows %d agents, want 2: %+v", agents, human.Players())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Two agent-allowed games in sequence; each should be filled, played and
	// archived by the resident agents.
	for round := 1; round <= 2; round++ {
		gameID, err := human.CreateGame(ctx, config.ModeCompetitive, 2, 0, 2, false)
		if err != nil {
			t.Fatal(err)
		}
		waitForArchive(t, ctx, js, gameID, 90*time.Second)
		t.Logf("round %d: game %s archived", round, gameID)
	}

	// The no-agents game must still be empty and untouched.
	if g, ok := human.Games()[noAgents]; !ok {
		t.Error("no-agents game listing disappeared")
	} else if len(g.Players) != 0 {
		t.Errorf("no-agents game has %d players, want 0: %+v", len(g.Players), g.Players)
	}

	// Interrupt the residents; both must exit cleanly with two games each.
	stopAgents()
	wins := 0
	for i := 0; i < 2; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("resident agent run failed: %v", o.err)
			}
			if o.res.Games != 2 {
				t.Errorf("resident agent played %d games, want 2", o.res.Games)
			}
			wins += o.res.Wins
		case <-ctx.Done():
			t.Fatal("resident agents did not exit after cancel")
		}
	}
	if wins != 2 {
		t.Errorf("total wins across residents = %d, want 2 (one per game)", wins)
	}
}

// waitForArchive polls the archive stream until a record for gameID appears.
func waitForArchive(t *testing.T, ctx context.Context, js jetstream.JetStream, gameID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("game %s was not archived within %s", gameID, timeout)
		}
		if s, err := js.Stream(ctx, config.ArchiveStream); err == nil {
			// Records are few in tests; scan from the newest backwards.
			if msg, err := s.GetLastMsgForSubject(ctx, config.ArchiveSubject); err == nil {
				var rec config.ArchiveRecord
				if json.Unmarshal(msg.Data, &rec) == nil && rec.GameID == gameID {
					return
				}
				// Older records may sit behind the newest; walk back a few.
				for seq := msg.Sequence - 1; seq > 0 && msg.Sequence-seq < 10; seq-- {
					m, err := s.GetMsg(ctx, seq)
					if err != nil {
						break
					}
					if json.Unmarshal(m.Data, &rec) == nil && rec.GameID == gameID {
						return
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}
