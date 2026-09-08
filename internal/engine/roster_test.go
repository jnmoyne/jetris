package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// startCoopPair brings up an in-progress two-seat cooperative game and
// returns one engine per seat, both with their first piece on the board and
// the idle-vacate threshold shortened to idle.
func startCoopPair(t *testing.T, idle time.Duration) (js jetstream.JetStream, gameID string, a, b *Engine) {
	t.Helper()
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err = jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gameID = "roster-coop-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2, ExtraColumns: 10,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	a = New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	b = New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 1, 0, 0)
	a.idleVacateAfter, b.idleVacateAfter = idle, idle
	for _, e := range []*Engine{a, b} {
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
	}
	waitUntil(t, 5*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(0) != nil && a.Playfield().ActivePieceForPlayer(1) != nil &&
			b.Playfield().ActivePieceForPlayer(0) != nil && b.Playfield().ActivePieceForPlayer(1) != nil
	}, "both first pieces on both boards")
	return js, gameID, a, b
}

// A two-seat open co-op game with one player present is not a solo game:
// the stream is the board (CAS on), so a second player can take the free
// seat mid-game.
func TestOpenTwoSeatCoopIsNotSolo(t *testing.T) {
	e, _, _ := setupEngineSeats(t, 2)
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if e.solo() {
		t.Fatal("a two-seat game with one player present plays solo")
	}
	if got := e.PlayerCount(); got != 2 {
		t.Fatalf("PlayerCount() = %d, want the game's 2 seats", got)
	}
}

// A peer's piece left standing on the crew's board — its player crashed —
// is vacated by a playing engine once it has stood still for the threshold,
// with one per-cell CAS batch; a piece that keeps moving never is.
func TestIdlePeerPieceVacatedCoop(t *testing.T) {
	_, _, a, b := startCoopPair(t, 400*time.Millisecond)

	// b keeps its piece moving: never idle long enough.
	stop := make(chan struct{})
	go func() {
		left := true
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				if left {
					b.MoveLeft()
				} else {
					b.MoveRight()
				}
				left = !left
			}
		}
	}()
	time.Sleep(1200 * time.Millisecond)
	close(stop)
	if a.Playfield().ActivePieceForPlayer(1) == nil {
		t.Fatal("a vacated b's piece while b was still moving it")
	}

	// b crashes: its piece stands still, and a vacates it a threshold on.
	b.Stop()
	waitUntil(t, 5*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(1) == nil
	}, "b's abandoned piece to be vacated")
	if a.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("a's own piece went with it")
	}
}

// A peer's piece on a team board is vacated the same way, through the
// board's txn gate.
func TestIdlePeerPieceVacatedTeams(t *testing.T) {
	_, _, engines := setupTeamsGame(t)
	p0, p1 := engines[0], engines[1]
	p0.idleVacateAfter = 400 * time.Millisecond
	p1.Stop()
	waitUntil(t, 5*time.Second, func() bool {
		return p0.Playfield().ActivePieceForPlayer(1) == nil
	}, "the teammate's abandoned piece to be vacated")
	if p0.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("p0's own piece went with it")
	}
}

// A seat the roster no longer holds has its piece vacated at once, idle or
// not: the player left without vacating it (a crash on the way out).
func TestSeatGoneVacatesPiece(t *testing.T) {
	_, _, a, b := startCoopPair(t, time.Hour)
	b.Stop()
	a.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}})
	waitUntil(t, 5*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(1) == nil
	}, "the departed seat's piece to be vacated")
}

// Leaving a running open game takes our own piece off the board — a CAS
// with retry, ours to move — and stops our play, before the seat is freed.
func TestVacateOwnPieceOnLeave(t *testing.T) {
	_, _, a, b := startCoopPair(t, time.Hour)
	b.VacateOwnPiece(context.Background())
	if b.Mode() != ModeGameOver {
		t.Fatalf("b's mode after leaving = %v, want game over (play stopped)", b.Mode())
	}
	waitUntil(t, 5*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(1) == nil && b.Playfield().ActivePieceForPlayer(1) == nil
	}, "b's piece to be gone from both boards")
	time.Sleep(300 * time.Millisecond)
	if b.Playfield().ActivePieceForPlayer(1) != nil {
		t.Fatal("b spawned again after leaving")
	}
	if a.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("a's piece went with it")
	}
}

// A roster that leaves every other team without a player decides a teams
// game for the team left; a competitive game for the last seated player.
func TestRosterChangeEndsMultiPlayfieldGame(t *testing.T) {
	ctx := context.Background()
	team := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, 0, 0)
	team.teamCount, team.teamSize = 2, 2
	team.gameStarted.Store(true)
	team.ctx = ctx
	team.SetRoster([]Seat{{PlayerID: "me", Seat: 0, Team: 0}, {PlayerID: "mate", Seat: 1, Team: 0, TeamSlot: 1}, {PlayerID: "b0", Seat: 2, Team: 1}})
	if _, _, decided := team.Winners(); decided {
		t.Fatal("decided while both teams are seated")
	}
	team.SetRoster([]Seat{{PlayerID: "me", Seat: 0, Team: 0}, {PlayerID: "mate", Seat: 1, Team: 0, TeamSlot: 1}})
	winners, winTeam, decided := team.Winners()
	if !decided || winTeam != 0 || !winners["me"] || !winners["mate"] {
		t.Fatalf("teams verdict = %v %d %v, want team A", winners, winTeam, decided)
	}
	if won, over := team.GameOutcome(); !won || !over {
		t.Fatal("the team left did not win")
	}

	comp := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	comp.playerCount = 3
	comp.gameStarted.Store(true)
	comp.ctx = ctx
	comp.SetRoster([]Seat{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 1}, {PlayerID: "y", Seat: 2}})
	// x tops out, y leaves: me is the last board standing.
	comp.handleGameEvent(ctx, GameEvent{Kind: EventGameOver, PlayerID: "x"})
	if _, _, decided := comp.Winners(); decided {
		t.Fatal("decided with two boards standing")
	}
	comp.SetRoster([]Seat{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 1}})
	if w, _, d := comp.Winners(); !d || !w["me"] || len(w) != 1 {
		t.Fatalf("competitive verdict = %v %v, want me", w, d)
	}

	// Before the start nothing is decided, whatever the roster.
	early := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	early.ctx = ctx
	early.SetRoster([]Seat{{PlayerID: "me", Seat: 0}})
	if _, _, decided := early.Winners(); decided {
		t.Fatal("a game that has not started was decided by its roster")
	}
}

// The last-standing rule follows the seats held once a roster is known: a
// two-of-three-seat competitive game ends when one of the two tops out.
func TestCompetitiveLastSeatedWins(t *testing.T) {
	ctx := context.Background()
	e := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	e.playerCount = 3
	e.gameStarted.Store(true)
	e.ctx = ctx
	e.SetRoster([]Seat{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 2}})
	e.handleGameEvent(ctx, GameEvent{Kind: EventGameOver, PlayerID: "x"})
	if w, _, d := e.Winners(); !d || !w["me"] {
		t.Fatalf("verdict = %v %v, want me as the last seated board", w, d)
	}
	if won, over := e.GameOutcome(); !won || !over {
		t.Fatal("the last seated board did not win")
	}
}
