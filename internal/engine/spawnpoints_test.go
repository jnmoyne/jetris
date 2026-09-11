package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// An open game's spawn points follow the seats present (SetRoster): the
// seats there are, ranked in slot order, one extra-columns step apart, the
// group centred on the board — a seat alone spawns in the middle, a full
// house in the layout the board was built for. Coop seats by playerIdx, a
// team's by the slot within the team; a competitive board never moves.
func TestSpawnPointAmongSeatsPresent(t *testing.T) {
	spawnCol := func(e *Engine) int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.spawnPositionLocked(game.PieceT).Col
	}
	check := func(t *testing.T, e *Engine, want int, what string) {
		t.Helper()
		if got := spawnCol(e); got != want {
			t.Errorf("%s: spawns at col %d, want %d", what, got, want)
		}
	}

	// Seat 2 of a three-seat crew on an 18-wide board (extra 4).
	e := New(nil, "g", "p2", "", config.ModeCooperative, ModePlayer, 2, 0, 0)
	e.seatsPerBoard, e.extraCols = 3, 4
	check(t, e, 11, "no roster pushed (every seat presumed)")
	e.SetRoster([]Seat{{PlayerID: "p2", Seat: 2}})
	check(t, e, 7, "alone")
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p2", Seat: 2}})
	check(t, e, 9, "two of three, the higher seat")
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p1", Seat: 1}, {PlayerID: "p2", Seat: 2}})
	check(t, e, 11, "a full house")
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}})
	check(t, e, 9, "a roster that lacks us (we are present all the same)")
	e.SetRoster(nil)
	check(t, e, 11, "an empty roster")

	// Slot 2 of team B (three a side): only the team's own slots count.
	tm := New(nil, "g", "b2", "", config.ModeTeams, ModePlayer, 5, 1, 2)
	tm.seatsPerBoard, tm.extraCols = 3, 4
	tm.SetRoster([]Seat{{PlayerID: "b2", Seat: 5, Team: 1, TeamSlot: 2}, {PlayerID: "a0", Seat: 0, Team: 0}})
	check(t, tm, 7, "alone on the team board (the other team seated)")
	tm.SetRoster([]Seat{{PlayerID: "b2", Seat: 5, Team: 1, TeamSlot: 2}, {PlayerID: "b0", Seat: 3, Team: 1, TeamSlot: 0}, {PlayerID: "a0", Seat: 0, Team: 0}})
	check(t, tm, 9, "two of the team's three")

	// A competitive board is private and standard: never offset.
	c := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 1, 0, 0)
	c.seatsPerBoard, c.extraCols = 1, 4
	c.SetRoster([]Seat{{PlayerID: "me", Seat: 1}})
	check(t, c, 3, "competitive")
}

// TestSpawnPointMovesWithTheRoster plays it on the wire: seat 2 of a
// three-seat open crew, alone on the board, gets its first piece in the
// middle; a seat joining leaves the piece in play where it is, and the NEXT
// piece comes in at the new point.
func TestSpawnPointMovesWithTheRoster(t *testing.T) {
	url, _ := testutil.StartServer(t)
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
	gameID := "spawn-points-test-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 3, ExtraColumns: 4,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	e := New(js, gameID, "p2", "", config.ModeCooperative, ModePlayer, 2, 0, 0)
	e.SetRoster([]Seat{{PlayerID: "p2", Seat: 2}}) // the lobby pushes the roster before the start
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	waitUntil(t, 5*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(2) != nil
	}, "the first piece to spawn")
	if p := e.Playfield().ActivePieceForPlayer(2); p.Col != 7 {
		t.Fatalf("alone on an 18-wide board, the first piece spawned at col %d, want the middle, 7", p.Col)
	}

	// Seat 0 joins. The piece in play stays; the next one comes in beside
	// the middle, one step to the right of the newcomer's point.
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p2", Seat: 2}})
	if p := e.Playfield().ActivePieceForPlayer(2); p == nil || p.Col != 7 {
		t.Fatalf("the piece in play moved on the join: %+v", p)
	}
	gen := e.spawnGen.Load()
	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		return e.spawnGen.Load() > gen && e.Playfield().ActivePieceForPlayer(2) != nil
	}, "the next piece to spawn after the drop")
	if p := e.Playfield().ActivePieceForPlayer(2); p.Col != 9 {
		t.Errorf("with seats 0 and 2 present, seat 2's next piece spawned at col %d, want 9", p.Col)
	}

	// The full house restores the layout the board was built for.
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p1", Seat: 1}, {PlayerID: "p2", Seat: 2}})
	gen = e.spawnGen.Load()
	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		return e.spawnGen.Load() > gen && e.Playfield().ActivePieceForPlayer(2) != nil
	}, "the next piece to spawn after the second drop")
	if p := e.Playfield().ActivePieceForPlayer(2); p.Col != 11 {
		t.Errorf("with every seat held, seat 2's next piece spawned at col %d, want its section's 11", p.Col)
	}
}
