// Command golang-mk1 is a self-contained Jetris agent: a Go program that plays
// competitive Jetris by speaking the wire protocol in ../../jetris-agent-guide.md
// directly against NATS/JetStream, depending on NOTHING in the jetris repo. It
// is a Go sibling of the example-python agent, but with the repo agent's strong
// Dellacherie planner (see planner.go) and its easy/medium/hard difficulties.
//
// Usage:
//
//	golang-mk1 --server nats://localhost:4222                 # resident: waits for invitations
//	golang-mk1 --context my-context                           # connect via a NATS context
//	golang-mk1                                                # ...or the currently selected one
//	golang-mk1 --auto-join                                    # also join open agent-allowed games
//	golang-mk1 --join <gameID>                                # join one specific game
//	golang-mk1 --create --players 2 --once                    # host a game, play it, exit
//	golang-mk1 --difficulty hard --once                       # play a single game, then exit
//	golang-mk1 --selftest                                     # offline conformance checks
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.Ltime)

	server := flag.String("server", "", "NATS server URL (overrides --context)")
	natsCtx := flag.String("context", "", "NATS context to connect with (default: the selected context)")
	user := flag.String("user", "", "NATS username (used with --server)")
	password := flag.String("password", "", "NATS password (used with --server)")
	name := flag.String("name", codename, "agent VERSION stem of the player name (default: the codename)")
	difficulty := flag.String("difficulty", "hard", "play strength: easy, medium, or hard")
	join := flag.String("join", "", "join this specific game id instead of scanning the lobby")
	create := flag.Bool("create", false, "create a competitive game and wait for opponents")
	players := flag.Int("players", 2, "player count when creating a game (with --create)")
	maxAgents := flag.Int("max-agents", 0, "agent seats when creating a game, including this agent (0 = all seats)")
	next := flag.Int("next", 1, "upcoming pieces the game reveals when creating a game (0-4, 0 = none)")
	autoJoin := flag.Bool("auto-join", false, "also join open agent-allowed games (default: invited games only)")
	wait := flag.Duration("wait", 10*time.Minute, "max wait for a joined game to fill and start before un-joining it")
	once := flag.Bool("once", false, "play one game, then exit")
	selftest := flag.Bool("selftest", false, "run offline conformance checks and exit")
	flag.Parse()

	if *selftest {
		runSelftest()
		return
	}

	diff, err := validDifficulty(*difficulty)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *join != "" && *create {
		fmt.Fprintln(os.Stderr, "--join and --create are mutually exclusive")
		os.Exit(2)
	}
	var host *hosting
	if *create {
		host = &hosting{players: *players, maxAgents: *maxAgents, next: *next}
	}

	a, err := newAgent(connChoice{server: *server, context: *natsCtx, user: *user, password: *password},
		*name, diff, *join, *once, *autoJoin, host, *wait)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("shutting down…")
		a.stop()
		cancel()
	}()

	if err := a.run(ctx); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// runSelftest exercises the pieces of the agent that need no server: RNG parity
// with the game's sequence, and a sanity check that the planner clears a line
// it is handed.
func runSelftest() {
	fixtures := map[uint64][]int{
		42:    {2, 3, 4, 1, 0, 5, 6, 6, 0, 5, 4, 2, 3, 1, 2, 1, 4, 3, 6, 5, 0},
		12345: {6, 3, 2, 1, 0, 4, 5, 0, 1, 5, 3, 2, 6, 4, 3, 6, 4, 2, 5, 1, 0},
	}
	for seed, seq := range fixtures {
		for i, want := range seq {
			if got := pieceAt(seed, i); got != want {
				log.Fatalf("RNG mismatch seed %d index %d: %d != %d", seed, i, got, want)
			}
		}
	}
	// A board one cell short of a full bottom row: dropping an I flat into the
	// gap must clear it, which the planner should prefer.
	g := newGrid(24)
	for c := 0; c < width; c++ {
		if c < 3 || c > 6 {
			g.set(23, c, 1)
		}
	}
	ranked := planPlacements(g, 0 /*I*/, spawnRow, spawnCol, nil)
	if len(ranked) == 0 || ranked[0].lines == 0 {
		log.Fatalf("planner did not find the line clear")
	}
	fmt.Println("selftest OK")
}
