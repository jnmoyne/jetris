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
//	golang-mk1 --create --mode cooperative --pause-alone      # host a co-op game and wait, paused, for company
//	golang-mk1 --create --mode cooperative --survival normal  # host a survival game: the floor rises until the crew tops out
//	golang-mk1 --create --game-name friday-night              # host a NAMED game: its ID, its lobby row and its stream
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
	"strings"
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
	gameNameFlag := flag.String("game-name", "", "name the game when creating one: the name becomes the game's ID, so its stream is JETRIS_GAME_<name> and lobbies list it by name (empty = a generated ID)")
	modeStr := flag.String("mode", "competitive", "game mode when creating: cooperative, competitive or teams (with --create)")
	players := flag.Int("players", 2, "player count when creating a game (with --create; cooperative: 1 or more, a solo game plays for the high score; teams: players per team)")
	teams := flag.Int("teams", defaultTeamCount, "teams mode: how many teams play each other when creating a game (2-6; total seats = teams × --players)")
	maxAgents := flag.Int("max-agents", 0, "agent seats when creating a game, including this agent — on each team in teams mode (0 = all seats)")
	pauseAlone := flag.Bool("pause-alone", false, "when creating a game: an agent left as the only player in it stops playing until someone joins, instead of playing on by itself")
	extraCols := flag.Int("extra-cols", minExtraColumns, "shared-board width when creating a cooperative or teams game: columns every seat beyond the first adds to the standard 10 (4-10)")
	next := flag.Int("next", maxNextCount, "upcoming pieces the game reveals when creating a game (0-6, 0 = none)")
	holes := flag.Int("holes", 0, "holes per garbage row when creating a competitive or teams game (0-4, 0 = solid rows that never clear)")
	randomHoles := flag.Bool("random-holes", false, "every garbage row draws its own hole columns when creating a game (default: the rows of one attack share a draw)")
	guideline := flag.Bool("guideline-garbage", false, "the modern attack table when creating a game: a single sends no garbage, a double 1 row, a triple 2, a quad 4 (default: one row per line)")
	hold := flag.Bool("hold", false, "the hold queue when creating a game (the agent itself never holds; the humans in the game may)")
	splitPieces := flag.Bool("split-pieces", false, "when creating a game whose playfields have two or more seats: deal the seven piece types out between the seats of a playfield, each playing only its own ration")
	extraRows := flag.Int("extra-rows", 0, "shared-board height when creating a cooperative or teams game: rows every seat beyond the first adds below the standard 20 (0-10)")
	lineGoal := flag.Int("line-goal", 0, "the game's length in lines when creating a game: the first playfield to clear this many wins (0 = until top out)")
	individual := flag.Bool("individual", false, "when creating a cooperative game of two or more: score every seat on its own, the top score wins")
	survival := flag.String("survival", "", "when creating a cooperative game: the rising floor and its tier — easy, normal or hard — garbage rows rise on a clock that quickens with the level until the crew tops out, the time survived the result (no line goal; at least one hole per row)")
	bag := flag.String("bag", "", "piece randomizer when creating a game: the 7-bag (empty, the default), double (two of each type per bag of fourteen) or none (every piece an independent draw)")
	preset := flag.Bool("guideline", false, "create the game with the GUI wizard's Modern preset — next 6, hold, the 7-bag, 1 hole per garbage row, the modern attack table — overriding --next, --holes, --random-holes, --guideline-garbage, --hold and --bag")
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
		if normalizeBag(*bag) != *bag {
			fmt.Fprintf(os.Stderr, "--bag %q is not a bag kind: use double, none, or leave it unset for the 7-bag\n", *bag)
			os.Exit(2)
		}
		if normalizeSurvival(*survival) != *survival {
			fmt.Fprintf(os.Stderr, "--survival %q is not a tier: use easy, normal or hard, or leave it unset\n", *survival)
			os.Exit(2)
		}
		if *survival != "" && mode != modeCooperative {
			fmt.Fprintln(os.Stderr, "--survival is a single playfield's game: use it with --mode cooperative")
			os.Exit(2)
		}
		// A name is the game's ID, so it has to be one: something a stream
		// name, a subject and a KV key all take (gameName), and not "lobby",
		// which the chat stream and the voice rooms keep for the lobby's own
		// channels.
		if gn := gameName(*gameNameFlag); *gameNameFlag != "" && gn == "" {
			fmt.Fprintf(os.Stderr, "--game-name %q has nothing a game ID can be made of: use letters, digits, - or _\n", *gameNameFlag)
			os.Exit(2)
		} else if strings.EqualFold(gn, "lobby") {
			fmt.Fprintln(os.Stderr, "--game-name lobby is reserved: it is what the lobby's own chat and voice channels go by")
			os.Exit(2)
		}
		host = &hosting{gameName: gameName(*gameNameFlag), mode: mode, players: *players, teams: *teams, extraCols: *extraCols, maxAgents: *maxAgents, next: *next, holes: *holes, random: *randomHoles, guideline: *guideline, hold: *hold, split: *splitPieces, bag: *bag,
			extraRows: *extraRows, lineGoal: *lineGoal, single: *individual, survival: *survival, pauseAlone: *pauseAlone}
		if *preset {
			// The same rules the GUI's "Modern" radio picks (config.ModernRules).
			host.next, host.holes, host.random, host.guideline, host.hold, host.bag = maxNextCount, 1, false, true, true, bagSingle
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
	// The bag rule (meta bag): the double bag and no bag against the fixtures
	// internal/rng's TestBagFixtures pins — the same seeds, dealt two more ways.
	bagFixtures := map[string]map[uint64][]int{
		bagDouble: {
			42:    {6, 5, 2, 3, 0, 3, 2, 4, 1, 4, 0, 1, 6, 5, 4, 5, 4, 1, 3, 1, 3, 6, 0, 2, 5, 6, 0, 2},
			12345: {3, 5, 4, 0, 3, 0, 6, 1, 6, 1, 5, 2, 2, 4, 4, 4, 1, 1, 0, 2, 0, 3, 5, 6, 6, 5, 3, 2},
		},
		bagNone: {
			42:    {6, 1, 0, 4, 1, 2, 2, 5, 4, 6, 6, 5, 0, 0, 5, 0, 3, 0, 0, 0, 0},
			12345: {5, 4, 0, 5, 5, 0, 2, 1, 4, 4, 1, 6, 2, 1, 5, 2, 1, 6, 5, 5, 5},
		},
	}
	for kind, bySeed := range bagFixtures {
		for seed, seq := range bySeed {
			for i, want := range seq {
				if got := pieceAtBag(seed, nil, kind, i); got != want {
					log.Fatalf("bag %q mismatch seed %d index %d: %d != %d", kind, seed, i, got, want)
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
