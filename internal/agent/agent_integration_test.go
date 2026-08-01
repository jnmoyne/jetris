package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// TestAgentVsAgent plays a full competitive game between a strong agent (creator)
// and a deliberately terrible one (auto-joiner) on an embedded server, and
// checks the whole lifecycle: exactly one winner, the archive record written,
// and the per-game stream and lobby listing cleaned up.
func TestAgentVsAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("full game against an embedded server")
	}
	url, _ := testutil.StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Zero delays: play as fast as the round-trips allow.
	strong := DifficultyHard.Tuning()
	strong.PieceDelay, strong.MoveDelay = 0, 0
	weak := Tuning{BlunderRate: 1, BlunderDepth: 40} // always a bad placement, no lookahead

	type outcome struct {
		res Result
		err error
	}
	results := make(chan outcome, 2)

	go func() {
		res, err := Run(ctx, Config{
			NATS:        config.Config{NATSURL: url},
			Name:        "agent-strong",
			Difficulty:  DifficultyHard,
			Tuning:      &strong,
			Create:      true,
			Mode:        config.ModeCompetitive,
			Players:     2,
			WaitTimeout: time.Minute,
			Seed:        1,
			Logf:        t.Logf,
		})
		results <- outcome{res, err}
	}()
	go func() {
		res, err := Run(ctx, Config{
			NATS:        config.Config{NATSURL: url},
			Name:        "agent-weak",
			Difficulty:  DifficultyEasy,
			Tuning:      &weak,
			AutoJoin:    true,
			Once:        true, // auto-join a single game, then exit
			WaitTimeout: time.Minute,
			Seed:        2,
			Logf:        t.Logf,
		})
		results <- outcome{res, err}
	}()

	var outcomes []outcome
	for len(outcomes) < 2 {
		select {
		case o := <-results:
			outcomes = append(outcomes, o)
		case <-ctx.Done():
			t.Fatal("game did not finish in time")
		}
	}

	winners := 0
	gameID := ""
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("agent run failed: %v", o.err)
		}
		if o.res.Games != 1 {
			t.Fatalf("agent played %d games, want 1", o.res.Games)
		}
		if o.res.Won {
			winners++
		}
		if gameID == "" {
			gameID = o.res.GameID
		} else if gameID != o.res.GameID {
			t.Fatalf("agents played different games: %s vs %s", gameID, o.res.GameID)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winners, want exactly 1", winners)
	}

	// Post-game state: fresh connection for the assertions.
	nc, js, kv, err := natspkg.Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// The archive record for this game must exist.
	archiveStream, err := js.Stream(ctx, config.ArchiveStream)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := archiveStream.GetLastMsgForSubject(ctx, config.ArchiveSubject)
	if err != nil {
		t.Fatalf("no archive record: %v", err)
	}
	var rec config.ArchiveRecord
	if err := json.Unmarshal(msg.Data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.GameID != gameID {
		t.Errorf("archived game %s, want %s", rec.GameID, gameID)
	}
	if len(rec.Players) != 2 {
		t.Errorf("archive has %d players, want 2", len(rec.Players))
	}
	recWinners := 0
	for _, p := range rec.Players {
		if p.Winner {
			recWinners++
		}
		if !p.Agent {
			t.Errorf("archive seat %s not flagged as an agent (history filter needs it)", p.PlayerID)
		}
	}
	if recWinners != 1 {
		t.Errorf("archive has %d winners, want 1", recWinners)
	}
	if !rec.HasAgents() {
		t.Error("HasAgents() = false for an all-agent game")
	}

	// The per-game stream and the lobby listing must be gone.
	if _, err := js.Stream(ctx, config.GameStream(gameID)); err == nil {
		t.Error("game stream still exists after archiving")
	}
	if _, err := kv.Get(ctx, config.LobbyGameKey(gameID)); err == nil {
		t.Error("lobby game listing still exists after archiving")
	}
}
