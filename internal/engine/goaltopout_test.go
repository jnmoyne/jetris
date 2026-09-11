package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// A crew's top-out before its line goal is a loss, and a top-out ends the
// game for everyone — whichever engine publishes the finish. Both were seen
// missing in an open 6-seat co-op game with a 100-line goal on jetris-eu
// (2026-09-09): the game-over screen crowned the whole crew, and the game
// went on being listed and joinable for four more hours.

// startCoopGame brings up an in-progress cooperative game of the given seat
// count, line goal and scoring on a private server.
func startCoopGame(t *testing.T, seats, goal int, scoring config.Scoring) (jetstream.JetStream, string) {
	t.Helper()
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
	gameID := "goal-topout-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: seats, ExtraColumns: 4,
		LineGoal: goal, Scoring: scoring, Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	return js, gameID
}

func metaStatus(t *testing.T, js jetstream.JetStream, gameID string) config.GameStatus {
	t.Helper()
	meta, _, err := natspkg.FetchGameMeta(context.Background(), js, gameID)
	if err != nil {
		t.Fatal(err)
	}
	return meta.Status
}

func waitMetaStatus(t *testing.T, js jetstream.JetStream, gameID string, want config.GameStatus, timeout time.Duration) {
	t.Helper()
	waitUntil(t, timeout, func() bool { return metaStatus(t, js, gameID) == want }, "meta status "+string(want))
}

// startPlayer starts one seated engine of the game and waits for its first
// piece, so it is playing when the test acts.
func startPlayer(t *testing.T, js jetstream.JetStream, gameID, id string, seat int, roster []Seat) *Engine {
	t.Helper()
	e := New(js, gameID, id, "", config.ModeCooperative, ModePlayer, seat, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	e.SetRoster(roster)
	waitUntil(t, 5*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(seat) != nil }, id+"'s first piece")
	return e
}

// The crew tops out at 0 of 100 lines: the topper's engine, a seated peer's
// and a spectator's all decide the game — lost, nobody a winner, the goal not
// reached — and the meta reaches finished.
func TestCoopGoalTopOutIsALoss(t *testing.T) {
	js, gameID := startCoopGame(t, 4, 100, config.ScoringShared)
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}, {PlayerID: "p2", Seat: 2}, {PlayerID: "p3", Seat: 3}}
	a := startPlayer(t, js, gameID, "p0", 0, roster)
	b := startPlayer(t, js, gameID, "p1", 1, roster)
	s := New(js, gameID, "watcher", "", config.ModeCooperative, ModeSpectator, 0, 0, 0)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.SetRoster(roster)

	a.handleTopOut(context.Background(), false)

	for _, e := range []*Engine{a, b, s} {
		waitUntil(t, 5*time.Second, func() bool { _, _, d := e.Winners(); return d }, e.PlayerID()+" deciding the game")
		w, wt, _ := e.Winners()
		if len(w) != 0 || wt != -1 {
			t.Fatalf("%s: Winners() = %v %d, want nobody: the crew missed its goal", e.PlayerID(), w, wt)
		}
		if e.GoalReached() {
			t.Fatalf("%s: GoalReached() after a top-out at 0 of 100 lines", e.PlayerID())
		}
		if e.initialMode == ModePlayer {
			if won, over := e.GameOutcome(); !over || won {
				t.Fatalf("%s: GameOutcome() = won %v over %v, want a loss", e.PlayerID(), won, over)
			}
		}
	}
	waitMetaStatus(t, js, gameID, config.GameStatusFinished, 5*time.Second)
}

// A peer's top-out reaches a seated engine whose topper never finished the
// game (its connection went with it): the peer moves the meta to finished
// itself and its archive hook fires — on the crew's shared board and on one
// scored per seat alike.
func TestCoopTopOutPeerFinishesTheGame(t *testing.T) {
	for _, scoring := range []config.Scoring{config.ScoringShared, config.ScoringIndividual} {
		t.Run(string("scoring="+scoring), func(t *testing.T) {
			js, gameID := startCoopGame(t, 2, 0, scoring)
			roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
			b := startPlayer(t, js, gameID, "p1", 1, roster)
			var finished atomic.Int32
			b.OnGameFinished = func() { finished.Add(1) }

			ev := GameEvent{Kind: EventGameOver, PlayerID: "p0", Score: 10, TotalScore: 10}
			data, _ := json.Marshal(ev)
			if _, err := js.Publish(context.Background(), config.EventKindSubject(gameID, string(EventGameOver), "p0"), data); err != nil {
				t.Fatal(err)
			}

			waitUntil(t, 5*time.Second, func() bool { _, over := b.GameOutcome(); return over }, "the peer's game over")
			waitMetaStatus(t, js, gameID, config.GameStatusFinished, 5*time.Second)
			waitUntil(t, 5*time.Second, func() bool { return finished.Load() == 1 }, "the peer's archive hook")
		})
	}
}

// flakyJS fronts a JetStream handle and drops the next N event/meta
// publishes and the next N stream lookups — a connection blip at the moment
// of the top-out.
type flakyJS struct {
	jetstream.JetStream
	dropPublishes atomic.Int32
	dropStreams   atomic.Int32
}

func (f *flakyJS) Publish(ctx context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	if (strings.Contains(subject, ".events.") || strings.HasSuffix(subject, ".meta")) && f.dropPublishes.Add(-1) >= 0 {
		return nil, errors.New("flaky: publish dropped")
	}
	return f.JetStream.Publish(ctx, subject, payload, opts...)
}

func (f *flakyJS) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	if f.dropStreams.Add(-1) >= 0 {
		return nil, errors.New("flaky: stream lookup failed")
	}
	return f.JetStream.Stream(ctx, name)
}

// The topper's game_over and finish ride out a blip: the first attempts
// fail, and the announcement and the finish still land.
func TestCoopTopOutFinishSurvivesTransientErrors(t *testing.T) {
	js, gameID := startCoopGame(t, 2, 100, config.ScoringShared)
	flaky := &flakyJS{JetStream: js}
	roster := []Seat{{PlayerID: "p0"}, {PlayerID: "p1", Seat: 1}}
	a := startPlayer(t, flaky, gameID, "p0", 0, roster)
	flaky.dropPublishes.Store(2)
	flaky.dropStreams.Store(2)

	a.handleTopOut(context.Background(), false)

	stream, err := js.Stream(context.Background(), config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		_, err := stream.GetLastMsgForSubject(context.Background(), config.EventKindSubject(gameID, string(EventGameOver), "p0"))
		return err == nil
	}, "the topper's game_over on the stream")
	waitMetaStatus(t, js, gameID, config.GameStatusFinished, 10*time.Second)
}

// blockSpawnDescent stacks the shared board up to the visible top — the two
// rows under the headroom, every column but the last, so no row completes —
// so a piece spawning in the headroom cannot fall: it locks where it
// spawned and leaves the spawn cells taken for the piece after it.
func blockSpawnDescent(t *testing.T, js jetstream.JetStream, gameID string, seats int) {
	t.Helper()
	width := config.SharedBoardWidth(seats, 4)
	cells := make([]game.Cell, width)
	for col := 0; col < width-1; col++ {
		cells[col] = game.Cell{Occupied: true}
	}
	for row := config.VisibleRowStart; row < config.VisibleRowStart+2; row++ {
		publishCoopRowCells(t, js, gameID, row, cells)
	}
}

// The crew's last piece locks where it spawned, and the spawn that follows
// finds its cells taken: that top-out is called from the spawn, under the
// engine's lock, and must decide and finish the game like any other. Seen
// on jetris-eu (2026-09-11, game ebc93724, a 2-seat crew with a 40-line
// goal): the stack reached the headroom, each player's last piece locked at
// its spawn cells, and neither engine published a game_over or the finish —
// each had deadlocked taking its own lock a second time, and the game
// stayed listed in progress until it was abandoned. A one-seat crew (the
// journal's lock-in) takes the same path.
func TestCoopGoalTopOutAtSpawnFinishesTheGame(t *testing.T) {
	for _, seats := range []int{1, 2} {
		t.Run("seats="+strconv.Itoa(seats), func(t *testing.T) {
			js, gameID := startCoopGame(t, seats, 40, config.ScoringShared)
			blockSpawnDescent(t, js, gameID, seats)
			roster := []Seat{{PlayerID: "p0"}}
			if seats > 1 {
				roster = append(roster, Seat{PlayerID: "p1", Seat: 1})
			}
			a := startPlayer(t, js, gameID, "p0", 0, roster)
			var finished atomic.Int32
			a.OnGameFinished = func() { finished.Add(1) }

			a.HardDrop() // nowhere to fall: the piece locks at its spawn cells

			waitMetaStatus(t, js, gameID, config.GameStatusFinished, 10*time.Second)
			stream, err := js.Stream(context.Background(), config.GameStream(gameID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.GetLastMsgForSubject(context.Background(), config.EventKindSubject(gameID, string(EventGameOver), "p0")); err != nil {
				t.Fatalf("the topper's game_over is not on the stream: %v", err)
			}
			w, wt, decided := a.Winners()
			if !decided || len(w) != 0 || wt != -1 {
				t.Fatalf("Winners() = %v %d decided %v, want nobody, decided: the crew missed its goal", w, wt, decided)
			}
			if won, over := a.GameOutcome(); !over || won {
				t.Fatalf("GameOutcome() = won %v over %v, want a loss", won, over)
			}
			waitUntil(t, 5*time.Second, func() bool { return finished.Load() == 1 }, "the topper's archive hook")
			// A deadlocked engine never answers for its board again.
			if a.Playfield() == nil {
				t.Fatal("Playfield() = nil")
			}
		})
	}
}
