// Command jetricks-agent is a headless computer player for jetricks. It plays
// all three game modes — cooperative, competitive, and teams — as an ordinary
// peer: it connects to the same NATS server as everyone else, joins (or
// creates) a game through the lobby, and plays it with the same engine the
// GUI uses — just without a window.
//
// With no --join or --create the agent is a lobby RESIDENT: it waits in the
// lobby, plays a game, returns to the lobby, and repeats until interrupted.
// By default a resident only joins games it is INVITED to (invitations are
// accepted immediately); pass --auto-join to also have it actively join any
// open game whose creator allowed agents (each game carries a max-agents
// policy). Pass --once to exit after a single game instead.
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

	"jetricks/internal/agent"
	"jetricks/internal/config"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>"
// (see .github/workflows/release.yml).
var version = "dev"

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Ctrl-C / SIGTERM cancels the run; the agent tears down (leave game, stop
	// lobby, drain) on its way out.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := agent.Run(ctx, cfg)
	if err != nil {
		log.Fatalf("jetricks-agent: %v", err)
	}
	// Per-game outcomes are logged as they happen; this is the run summary.
	log.Printf("played %d game(s), won %d", res.Games, res.Wins)
}

func parseFlags() (agent.Config, error) {
	var cfg agent.Config
	var difficulty string

	flag.StringVar(&cfg.NATS.NATSURL, "server", "", "NATS server URL (overrides --context)")
	flag.StringVar(&cfg.NATS.NATSContext, "context", "", "NATS context to connect with")
	flag.StringVar(&cfg.NATS.NATSUser, "user", "", "NATS username (used with --server)")
	flag.StringVar(&cfg.NATS.NATSPassword, "password", "", "NATS password (used with --server)")
	flag.StringVar(&cfg.Name, "name", "", "agent VERSION name (default: the stock agent codename \"mk1\"); the full player name is NAME-<instance>-<difficulty>, with a fresh instance id per connection")
	flag.StringVar(&difficulty, "difficulty", "hard", "how strongly to play: easy, medium or hard")
	var modeStr string
	flag.StringVar(&cfg.JoinGameID, "join", "", "join this specific game ID")
	flag.BoolVar(&cfg.Create, "create", false, "create a game and wait for opponents")
	flag.StringVar(&modeStr, "mode", "competitive", "game mode when creating: cooperative, competitive or teams (with --create)")
	flag.IntVar(&cfg.Players, "players", 2, "player count when creating a game (with --create; teams: players per team)")
	flag.IntVar(&cfg.MaxAgents, "max-agents", 0, "agent seats when creating a game, including this agent (0 = all seats)")
	flag.BoolVar(&cfg.AutoJoin, "auto-join", false, "actively join any open game that allows agents (default: only join games this agent is invited to)")
	flag.BoolVar(&cfg.Once, "once", false, "exit after one game instead of staying in the lobby")
	flag.DurationVar(&cfg.WaitTimeout, "wait", 10*time.Minute, "max wait for a joined game to fill and start")
	flag.BoolVar(&cfg.LingerAfterLoss, "linger", false, "after losing, stay connected until the game finishes")
	flag.Uint64Var(&cfg.Seed, "seed", 0, "random seed for difficulty blunders (0 = time-based)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("jetricks-agent %s\n", version)
		os.Exit(0)
	}

	d, err := agent.ParseDifficulty(difficulty)
	if err != nil {
		return agent.Config{}, err
	}
	cfg.Difficulty = d

	switch modeStr {
	case "cooperative", "coop":
		cfg.Mode = config.ModeCooperative
	case "competitive":
		cfg.Mode = config.ModeCompetitive
	case "teams":
		cfg.Mode = config.ModeTeams
	default:
		return agent.Config{}, fmt.Errorf("unknown mode %q (want cooperative, competitive or teams)", modeStr)
	}

	if cfg.JoinGameID != "" && cfg.Create {
		return agent.Config{}, fmt.Errorf("--join and --create are mutually exclusive")
	}
	if cfg.Name != "" {
		if err := config.ValidatePlayerName(cfg.Name); err != nil {
			return agent.Config{}, err
		}
	}
	return cfg, nil
}
