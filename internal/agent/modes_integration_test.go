package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

// startHumanLobby brings up a plain (non-agent) lobby client used as the game
// creator in mode tests.
func startHumanLobby(t *testing.T, ctx context.Context, url string) (*lobby.Lobby, jetstream.JetStream) {
	t.Helper()
	nc, js, kv, err := natspkg.Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	human := lobby.New(js, kv, "human", "human")
	if err := human.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(human.Stop)
	return human, js
}

// fetchArchive returns the archive record for gameID, scanning back from the
// newest message.
func fetchArchive(t *testing.T, ctx context.Context, js jetstream.JetStream, gameID string) (config.ArchiveRecord, bool) {
	t.Helper()
	s, err := js.Stream(ctx, config.ArchiveStream)
	if err != nil {
		return config.ArchiveRecord{}, false
	}
	msg, err := s.GetLastMsgForSubject(ctx, config.ArchiveSubject)
	if err != nil {
		return config.ArchiveRecord{}, false
	}
	var rec config.ArchiveRecord
	if json.Unmarshal(msg.Data, &rec) == nil && rec.GameID == gameID {
		return rec, true
	}
	for seq := msg.Sequence - 1; seq > 0 && msg.Sequence-seq < 10; seq-- {
		m, err := s.GetMsg(ctx, seq)
		if err != nil {
			break
		}
		if json.Unmarshal(m.Data, &rec) == nil && rec.GameID == gameID {
			return rec, true
		}
	}
	return config.ArchiveRecord{}, false
}

type agentOutcome struct {
	res Result
	err error
}

// sloppyTuning plays fast and badly: no delays, always a blunder.
func sloppyTuning() Tuning {
	return Tuning{BlunderRate: 1, BlunderDepth: 40}
}

// TestCoopAgents: two agents share one cooperative board. The game ends when
// either tops out; nobody wins and the score is shared. Exercises the coop
// collision rules and shared-board CAS contention end to end.
func TestCoopAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("plays a full cooperative game against an embedded server")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	human, js := startHumanLobby(t, ctx, url)

	sloppy := sloppyTuning()
	results := make(chan agentOutcome, 2)
	for _, name := range []string{"coop-a", "coop-b"} {
		go func(name string) {
			res, err := Run(ctx, Config{
				NATS:        config.Config{NATSURL: url},
				Name:        name,
				Difficulty:  DifficultyEasy,
				Tuning:      &sloppy,
				AutoJoin:    true,
				Once:        true,
				WaitTimeout: time.Minute,
				Seed:        1,
				Logf:        t.Logf,
			})
			results <- agentOutcome{res, err}
		}(name)
	}

	gameID, err := human.CreateGame(ctx, config.ModeCooperative, 2, 0, 2, false)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("coop agent run failed: %v", o.err)
			}
			if o.res.GameID != gameID || o.res.Games != 1 {
				t.Fatalf("coop agent result %+v, want 1 game of %s", o.res, gameID)
			}
			if o.res.Mode != config.ModeCooperative {
				t.Errorf("result mode = %v, want cooperative", o.res.Mode)
			}
			if o.res.Won {
				t.Error("cooperative games have no winner; Won must be false")
			}
		case <-ctx.Done():
			t.Fatal("coop game did not finish in time")
		}
	}

	rec, ok := fetchArchive(t, ctx, js, gameID)
	if !ok {
		t.Fatal("coop game was not archived")
	}
	if rec.Mode != config.ModeCooperative || len(rec.Players) != 2 {
		t.Errorf("archive %+v, want cooperative with 2 players", rec)
	}
}

// TestTeamsAgents: a 1v1 teams game — the two agents auto-join opposite teams,
// garbage flows between the team boards, and exactly one team wins.
func TestTeamsAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("plays a full teams game against an embedded server")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	human, js := startHumanLobby(t, ctx, url)

	strong := DifficultyHard.Tuning()
	strong.PieceDelay, strong.MoveDelay = 0, 0
	sloppy := sloppyTuning()

	results := make(chan agentOutcome, 2)
	for i, tun := range []*Tuning{&strong, &sloppy} {
		go func(name string, tun *Tuning, seed uint64) {
			res, err := Run(ctx, Config{
				NATS:        config.Config{NATSURL: url},
				Name:        name,
				Difficulty:  DifficultyHard,
				Tuning:      tun,
				AutoJoin:    true,
				Once:        true,
				WaitTimeout: time.Minute,
				Seed:        seed,
				Logf:        t.Logf,
			})
			results <- agentOutcome{res, err}
		}([]string{"team-strong", "team-weak"}[i], tun, uint64(i+1))
	}

	// 1v1: one player per team.
	gameID, err := human.CreateGame(ctx, config.ModeTeams, 2, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}

	winners := 0
	for i := 0; i < 2; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("teams agent run failed: %v", o.err)
			}
			if o.res.GameID != gameID || o.res.Games != 1 {
				t.Fatalf("teams agent result %+v, want 1 game of %s", o.res, gameID)
			}
			if o.res.Mode != config.ModeTeams {
				t.Errorf("result mode = %v, want teams", o.res.Mode)
			}
			if o.res.Won {
				winners++
			}
		case <-ctx.Done():
			t.Fatal("teams game did not finish in time")
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winning agents, want exactly 1", winners)
	}

	rec, ok := fetchArchive(t, ctx, js, gameID)
	if !ok {
		t.Fatal("teams game was not archived")
	}
	if rec.Mode != config.ModeTeams || rec.WinningTeam < 0 {
		t.Errorf("archive %+v, want a teams record with a winning team", rec)
	}
}

// TestTeams2v2Agents: the reported-bug scenario — a 2v2 teams game played by
// four agents. With the spawn-deferral fix, teammates' pieces crossing each
// other's spawn sections must NOT spuriously eliminate anyone: the game
// completes with exactly one winning team, and nobody tops out after only a
// handful of pieces (a spurious spawn-block elimination fires at 1-3 pieces
// on a near-empty board; a genuine teams top-out needs the stack/garbage to
// reach the spawn rows, impossible that early).
func TestTeams2v2Agents(t *testing.T) {
	if testing.Short() {
		t.Skip("plays a full 2v2 teams game against an embedded server")
	}
	url, _ := testutil.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	human, js := startHumanLobby(t, ctx, url)

	strong := DifficultyHard.Tuning()
	strong.PieceDelay, strong.MoveDelay = 0, 0
	sloppy := sloppyTuning()

	// One strong, three sloppy. pickTeam assigns seats [0,1,0,1] by join
	// order, so whichever team the lone strong agent lands on, the OTHER team
	// is all-sloppy and tops out fast — a decisive, bounded game no matter how
	// the concurrent joins interleave. (Two strong + two sloppy could split
	// evenly strong-vs-sloppy on BOTH teams and never end.) Still two
	// teammates per board, which is what exercises the spawn-blocking fix.
	tunings := []*Tuning{&strong, &sloppy, &sloppy, &sloppy}
	names := []string{"t2-a", "t2-b", "t2-c", "t2-d"}
	results := make(chan agentOutcome, 4)
	for i := range names {
		go func(name string, tun *Tuning, seed uint64) {
			res, err := Run(ctx, Config{
				NATS:        config.Config{NATSURL: url},
				Name:        name,
				Difficulty:  DifficultyHard,
				Tuning:      tun,
				AutoJoin:    true,
				Once:        true,
				WaitTimeout: 2 * time.Minute,
				Seed:        seed,
				Logf:        t.Logf,
			})
			results <- agentOutcome{res, err}
		}(names[i], tunings[i], uint64(i+1))
	}

	// 2v2: two players per team, all four seats open to agents.
	gameID, err := human.CreateGame(ctx, config.ModeTeams, 4, 2, 4, false)
	if err != nil {
		t.Fatal(err)
	}

	winners := 0
	for i := 0; i < 4; i++ {
		select {
		case o := <-results:
			if o.err != nil {
				t.Fatalf("2v2 agent run failed: %v", o.err)
			}
			if o.res.GameID != gameID || o.res.Games != 1 {
				t.Fatalf("2v2 agent result %+v, want 1 game of %s", o.res, gameID)
			}
			if o.res.Won {
				winners++
			}
		case <-ctx.Done():
			t.Fatal("2v2 teams game did not finish in time")
		}
	}
	if winners != 2 {
		t.Fatalf("got %d winning agents, want exactly 2 (one whole team)", winners)
	}

	rec, ok := fetchArchive(t, ctx, js, gameID)
	if !ok {
		t.Fatal("2v2 teams game was not archived")
	}
	if rec.Mode != config.ModeTeams || rec.TeamSize != 2 || len(rec.Players) != 4 {
		t.Fatalf("archive %+v, want a 2v2 teams record with 4 players", rec)
	}
	if rec.WinningTeam != 0 && rec.WinningTeam != 1 {
		t.Errorf("archive winning team = %d, want 0 or 1", rec.WinningTeam)
	}
	// The discriminating assertion for the spawn bug: every player that
	// topped out (PieceCount > 0 — winners who never sent a game-over event
	// archive with 0) must have placed a meaningful number of pieces first.
	for _, p := range rec.Players {
		if p.PieceCount > 0 && p.PieceCount < 5 {
			t.Errorf("player %s topped out after only %d pieces — spurious spawn-block elimination?", p.PlayerID, p.PieceCount)
		}
	}
}
