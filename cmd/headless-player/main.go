// Command headless-player drives a NATIVE-engine Jetris player without a UI —
// the same lobby + engine code paths the Gio app uses — so protocol-level
// interactions between the native engine and wire agents (golang-mk1) can be
// reproduced and observed headlessly. It creates a teams game (or, with
// --mode cooperative, a two-seat co-op game — where the crude play below makes
// it the topper, and so the archiver), takes a team-0 seat, readies up, runs
// the countdown once everyone is ready, and then plays
// crude but legal Tetris: a few random lateral moves, then a hard drop, on a
// fixed cadence. Diagnostic tool: no scoring smarts, no rejoin, one game.
package main

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/archive"
	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	server := flag.String("server", "nats://127.0.0.1:4333", "NATS server URL")
	dropEvery := flag.Duration("drop-every", 2*time.Second, "cadence of the move+hard-drop cycle")
	seed := flag.Int64("seed", time.Now().UnixNano(), "RNG seed for the crude move generator")
	mode := flag.String("mode", "teams", "game to create: teams (2v2, three agent seats) or cooperative (2 seats, one agent)")
	flag.Parse()

	ctx := context.Background()
	rng := rand.New(rand.NewSource(*seed))

	nc, err := nats.Connect(*server)
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatal(err)
	}

	if err := natspkg.EnsureChatStream(ctx, js); err != nil {
		log.Fatal(err)
	}
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		log.Fatal(err)
	}
	if err := natspkg.EnsureReplayStream(ctx, js); err != nil {
		log.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		log.Fatal(err)
	}

	const playerID = "headless-native"
	lb := lobby.New(js, kv, playerID, "Headless")
	if err := lb.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer lb.Stop()
	if err := lb.WaitForInitialLoad(ctx); err != nil {
		log.Fatal(err)
	}

	// 2v2 teams (TeamCount*teamSize players), agents may take the other three
	// seats, one preview piece — or a 2-seat co-op with one agent seat.
	gameMode, playerCount, teamSize, maxAgents := config.ModeTeams, config.TeamCount*2, 2, 3
	if *mode == "cooperative" {
		gameMode, playerCount, teamSize, maxAgents = config.ModeCooperative, 2, 0, 1
	}
	gameID, err := lb.CreateGame(ctx, gameMode, playerCount, teamSize, maxAgents, 1, true, false)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("created %s game %s", gameMode, gameID)

	res, err := lb.JoinGame(ctx, gameID, 0)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("joined as playerIdx=%d team=%d slot=%d", res.PlayerIdx, res.Team, res.TeamSlot)

	e := engine.New(lb.GetJS(), gameID, playerID, "", gameMode, engine.ModePlayer, res.PlayerIdx, res.Team, res.TeamSlot)
	e.OnGameFinished = func() {
		archive.ArchiveAndCleanup(context.Background(), js, kv, e, lb, lb.Games()[gameID].Players)
	}
	// Drain the UI update channel (emitUpdate is non-blocking, but keep it warm
	// and log the interesting transitions).
	go func() {
		for u := range e.Updates {
			switch u.Kind {
			case engine.UpdateGameStatus:
				log.Printf("game status: %s", u.GameStatus)
			case engine.UpdateGameOver:
				log.Printf("game over for us: won=%v", u.Won)
			case engine.UpdatePlayerEliminated:
				log.Printf("eliminated: %s", u.EliminatedPlayerID)
			}
		}
	}()
	if err := e.Start(); err != nil {
		log.Fatal(err)
	}
	defer e.Stop()

	if err := lb.SetReady(ctx, gameID, true); err != nil {
		log.Fatal(err)
	}

	// Wait for a full, all-ready roster, then run the countdown + start (the
	// same sequence the UI performs when its ToggleReady sees AllReady).
	for {
		g, ok := lb.Games()[gameID]
		if ok && len(g.Players) >= g.PlayerCount {
			all := true
			for _, p := range g.Players {
				if !p.Ready {
					all = false
					break
				}
			}
			if all {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Print("all ready; running countdown")
	for i := 3; i >= 0; i-- {
		data := []byte(`{"seconds":` + string(rune('0'+i)) + `}`)
		_, _ = js.Publish(ctx, config.CountdownSubject(gameID), data)
		time.Sleep(700 * time.Millisecond)
	}
	lb.StartGame(ctx, gameID)
	log.Print("game started; playing")

	// Greedy play: steer the falling piece toward the shallowest column, then
	// hard drop — dumb enough to be a UI-speed "human", smart enough to
	// survive, fill rows, and trigger native clears/raises for a while.
	ticker := time.NewTicker(*dropEvery)
	defer ticker.Stop()
	for range ticker.C {
		if _, over := e.GameOutcome(); over {
			break
		}
		if e.Mode() != engine.ModePlayer {
			// Eliminated: keep the process alive so the engine spectates the
			// rest of the game (and archives if our side triggers the finish).
			continue
		}
		pf := e.Playfield()
		p := pf.ActivePieceForPlayer(e.PlayerIdx())
		if p == nil {
			continue
		}
		target := shallowestColumn(pf, rng)
		steps := target - p.Col
		for s := 0; s < abs(steps); s++ {
			if steps < 0 {
				e.MoveLeft()
			} else {
				e.MoveRight()
			}
			time.Sleep(50 * time.Millisecond)
		}
		if rng.Intn(3) == 0 {
			e.RotateCW()
			time.Sleep(50 * time.Millisecond)
		}
		e.HardDrop()
	}
	log.Print("game over; lingering 20s for archive/teardown")
	time.Sleep(20 * time.Second)
}

// shallowestColumn picks the column (with a little jitter) whose settled stack
// is lowest, ignoring active cells.
func shallowestColumn(pf *game.Playfield, rng *rand.Rand) int {
	best, bestH := 0, pf.Height+1
	for c := 0; c < pf.Width; c++ {
		h := 0
		for r := 0; r < pf.Height; r++ {
			cc := pf.Rows[r].Cells[c]
			if cc.Occupied && !cc.Active {
				h = pf.Height - r
				break
			}
		}
		if h < bestH || (h == bestH && rng.Intn(2) == 0) {
			best, bestH = c, h
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
