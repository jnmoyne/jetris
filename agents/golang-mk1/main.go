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
//	golang-mk1 --create --mode teams --players 2              # host a 2v2 teams game
//	golang-mk1 --create --mode teams --teams 3 --players 2    # ...or a three-way, 2 per team
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
	create := flag.Bool("create", false, "create a game and wait for opponents")
	modeStr := flag.String("mode", "competitive", "game mode when creating: cooperative, competitive or teams (with --create)")
	players := flag.Int("players", 2, "player count when creating a game (with --create; cooperative: 1 or more, a solo game plays for the high score; teams: players per team)")
	teams := flag.Int("teams", defaultTeamCount, "teams mode: how many teams play each other when creating a game (2-6; total seats = teams × --players)")
	maxAgents := flag.Int("max-agents", 0, "agent seats when creating a game, including this agent (0 = all seats)")
	extraCols := flag.Int("extra-cols", minExtraColumns, "shared-board width when creating a cooperative or teams game: columns every seat beyond the first adds to the standard 10 (4-10)")
	next := flag.Int("next", maxNextCount, "upcoming pieces the game reveals when creating a game (0-6, 0 = none)")
	holes := flag.Int("holes", 0, "holes per garbage row when creating a competitive or teams game (0-4, 0 = solid rows that never clear)")
	randomHoles := flag.Bool("random-holes", false, "every garbage row draws its own hole columns when creating a game (default: the rows of one attack share a draw)")
	guideline := flag.Bool("guideline-garbage", false, "Guideline attack table when creating a game: a single sends no garbage, a double 1 row, a triple 2, a Tetris 4 (default: one row per line)")
	hold := flag.Bool("hold", false, "the Guideline hold queue when creating a game (the agent itself never holds; the humans in the game may)")
	splitPieces := flag.Bool("split-pieces", false, "when creating a TEAMS game of two or more per team: deal the seven piece types out between the teammates, each seat playing only its own ration")
	preset := flag.Bool("guideline", false, "create the game with the GUI wizard's Guideline preset — next 6, hold, 1 hole per garbage row, Guideline attack table — overriding --next, --holes, --random-holes, --guideline-garbage and --hold")
	publish := flag.String("publish", "async", "how move batches are committed (guide §4.3): sync (await every commit ack), async (pipelined, no expectation on in-flight cells), or optimistic (pipelined with predicted sequences)")
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
	pub, err := parsePublishMode(*publish)
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
		mode := modeCompetitive
		switch *modeStr {
		case "cooperative", "coop":
			mode = modeCooperative
		case "competitive":
		case "teams":
			mode = modeTeams
		default:
			fmt.Fprintf(os.Stderr, "unknown mode %q (want cooperative, competitive or teams)\n", *modeStr)
			os.Exit(2)
		}
		host = &hosting{mode: mode, players: *players, teams: *teams, extraCols: *extraCols, maxAgents: *maxAgents, next: *next, holes: *holes, random: *randomHoles, guideline: *guideline, hold: *hold, split: *splitPieces}
		if *preset {
			// The same rules the GUI's "Guideline" radio picks (config.GuidelineRules).
			host.next, host.holes, host.random, host.guideline, host.hold = maxNextCount, 1, false, true, true
		}
	}

	a, err := newAgent(connChoice{server: *server, context: *natsCtx, user: *user, password: *password},
		*name, diff, *join, *once, *autoJoin, host, *wait, pub)
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
	// The teams-mode piece split (meta split_pieces): the same fixtures the
	// repo's internal/rng produces, plus the rule the deal must always keep —
	// all seven types dealt out, nobody empty-handed.
	splitFixtures := map[uint64]map[int][][]int{
		42:    {2: {{0, 1, 4}, {2, 3, 5, 6}}, 3: {{0, 4, 5}, {2, 3}, {1, 6}}},
		12345: {2: {{0, 1, 2, 5}, {3, 4, 6}}, 3: {{1, 2}, {0, 5, 6}, {3, 4}}},
	}
	for seed, bySeats := range splitFixtures {
		for seats, want := range bySeats {
			got := pieceSets(seed, seats)
			for slot := range want {
				if len(got[slot]) != len(want[slot]) {
					log.Fatalf("split mismatch seed %d seats %d slot %d: %v != %v", seed, seats, slot, got[slot], want[slot])
				}
				for i := range want[slot] {
					if got[slot][i] != want[slot][i] {
						log.Fatalf("split mismatch seed %d seats %d slot %d: %v != %v", seed, seats, slot, got[slot], want[slot])
					}
				}
			}
		}
	}
	for seats := 1; seats <= 9; seats++ {
		var seen [7]bool
		for slot, set := range pieceSets(12345, seats) {
			if len(set) == 0 {
				log.Fatalf("split deal of %d seats leaves slot %d empty-handed", seats, slot)
			}
			for _, pt := range set {
				seen[pt] = true
			}
		}
		for pt, ok := range seen {
			if !ok {
				log.Fatalf("split deal of %d seats leaves piece %d undealt", seats, pt)
			}
		}
	}
	// A board one cell short of a full bottom row: dropping an I flat into the
	// gap must clear it, which the planner should prefer.
	g := newGrid(24, width)
	for c := 0; c < width; c++ {
		if c < 3 || c > 6 {
			g.set(23, c, 1)
		}
	}
	ranked := planPlacements(g, 0 /*I*/, spawnRow, spawnCol, spawnCol, nil)
	if len(ranked) == 0 || ranked[0].lines == 0 {
		log.Fatalf("planner did not find the line clear")
	}
	fmt.Println("selftest OK")
}
