package agent

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sort"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/archive"
	"jetricks/internal/cleanup"
	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
)

// Config parameterizes one agent run.
type Config struct {
	NATS       config.Config // connection choice: NATSURL wins over NATSContext (like Bootstrap)
	Name       string        // agent VERSION stem of the player name (default: Codename); the full name is <stem>-<instance>-<difficulty>, instance minted per connection
	Difficulty Difficulty
	Tuning     *Tuning // optional full override of the difficulty's tuning (tests)

	// Exactly one game-selection behavior applies: JoinGameID if set, else
	// Create if true, else auto-join. Auto-join is RESIDENT by default: the
	// agent stays in the lobby and keeps joining agent-allowed games — any mode —
	// until ctx is cancelled. Once restores play-one-game-and-exit.
	// JoinGameID and Create are always one-shot.
	JoinGameID string
	Create     bool
	Mode       config.GameMode // game mode when creating; NOTE the zero value is config.ModeCooperative (the enum's zero) — set it explicitly (the CLI's --mode default is competitive)
	Players    int             // player count when creating (default 2; teams: players PER TEAM, like the GUI)
	MaxAgents  int             // agent policy when creating: agent seats incl. this agent (0 = all seats; an agent-hosted game is agent-friendly)
	Once       bool

	WaitTimeout     time.Duration                    // max wait for a joined game to fill and start (and, one-shot, for discovery); default 10m
	LingerAfterLoss bool                             // after losing, stay connected until the game finishes
	Seed            uint64                           // blunder RNG seed (0 = time-based)
	Logf            func(format string, args ...any) // optional logger (default log.Printf)
}

// Result summarizes a run. The per-game fields describe the LAST game played;
// Games/Wins aggregate across a resident run. Won is meaningful for
// competitive (last standing) and teams (team prevailed); cooperative games
// have no winner — everyone shares the score and Won stays false.
type Result struct {
	GameID string
	Mode   config.GameMode
	Won    bool
	Score  int
	Level  int
	Pieces uint64
	Games  int
	Wins   int
}

const (
	defaultWaitTimeout = 10 * time.Minute
	defaultPlayers     = 2
	connectTimeout     = 15 * time.Second
	startPollInterval  = 50 * time.Millisecond
	lobbyScanInterval  = 500 * time.Millisecond
	// maxReplans bounds re-planning per piece; past it the agent just hard-drops
	// wherever the piece is so it can never deadlock.
	maxReplans = 3
)

// errStartTimeout marks a joined game that never started; the agent un-joins to
// free its seat and (when resident) goes back to scanning the lobby.
var errStartTimeout = errors.New("agent: game did not start in time")

// Codename identifies this GENERATION of the stock agent's play logic — the
// version component of every agent's player name. Bump it whenever the
// agent's observable behavior changes (planner, evaluation, executor,
// difficulty tunings), so lobby rosters and archived games record which
// agent version was playing.
//
//	mk1 — one-ply Dellacherie planner, sense–act executor, fair-visibility
//	      contract (no seed lookahead).
const Codename = "mk1"

// composeName builds the agent's full player name:
//
//	<version>-<instance>-<difficulty>     e.g. "mk1-3f7a-hard"
//
// version is the agent-code version stem (Config.Name overrides the stock
// Codename — third-party agents put their own codename here and bump it when
// their logic changes); instance is 4 random hex characters minted fresh for
// EVERY connection, so several copies of the same agent version can play at
// once and each connection is distinguishable in rosters, history and chat;
// difficulty is the strength label. The name doubles as the NATS player ID,
// which appears both in subject tokens AND in the presence KV key
// ("players.<name>") — KV keys allow only [-/_=.a-zA-Z0-9], so every
// component must stick to that set, and the whole must pass
// ValidatePlayerName's 32-character cap.
func composeName(stem string, d Difficulty) (string, error) {
	if stem == "" {
		stem = Codename
	}
	if err := config.ValidatePlayerName(stem); err != nil {
		return "", fmt.Errorf("agent version name %q: %w", stem, err)
	}
	var b [2]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting agent instance id: %w", err)
	}
	full := fmt.Sprintf("%s-%s-%s", stem, hex.EncodeToString(b[:]), d)
	if err := config.ValidatePlayerName(full); err != nil {
		return "", fmt.Errorf("agent name %q (version %q + instance + difficulty): %w", full, stem, err)
	}
	return full, nil
}

// Run connects to NATS, enters the lobby as an agent player, and plays
// competitive games until done: one game for --join/--create/--once, or —
// the auto-join default — resident in the lobby, joining agent-allowed games
// as they appear until ctx is cancelled. The per-game flow deliberately
// mirrors the GUI's lifecycle (nativeui/lifecycle.go): same bootstrap, same
// join wiring, same ready→countdown election, same archive-on-finish
// responsibility.
func Run(ctx context.Context, cfg Config) (Result, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	name, err := composeName(cfg.Name, cfg.Difficulty)
	if err != nil {
		return Result{}, err
	}
	cfg.Name = name
	logf("playing as %s", name)
	tn := cfg.Difficulty.Tuning()
	if cfg.Tuning != nil {
		tn = *cfg.Tuning
	}
	waitTimeout := cfg.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = defaultWaitTimeout
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	rnd := rand.New(rand.NewPCG(seed, 0x6a65747269636b73)) // "jetricks"

	// Connect and provision the shared lobby/chat/archive resources.
	connCtx, connCancel := context.WithTimeout(ctx, connectTimeout)
	nc, js, kv, err := natspkg.Bootstrap(connCtx, cfg.NATS)
	connCancel()
	if err != nil {
		return Result{}, fmt.Errorf("connect: %w", err)
	}
	defer nc.Drain()
	logf("connected to NATS at %s", nc.ConnectedUrl())

	// Fail fast on a name collision (headless: no prompt to pick another).
	nameCtx, nameCancel := context.WithTimeout(ctx, 2*time.Second)
	inUse, err := lobby.IsNameInUse(nameCtx, kv, cfg.Name)
	nameCancel()
	if err != nil {
		logf("name-in-use check: %v", err)
	}
	if inUse {
		return Result{}, fmt.Errorf("player name %q is already in use", cfg.Name)
	}

	// Lobby bring-up, mirroring the GUI's initLobby — marked as an agent, so
	// presence and roster entries carry the flag and games can enforce their
	// agent policy on us.
	lobbyCtx, lobbyCancel := context.WithCancel(ctx)
	defer lobbyCancel()
	lb := lobby.New(js, kv, cfg.Name, cfg.Name)
	lb.SetAgent(true)
	if err := lb.Start(lobbyCtx); err != nil {
		return Result{}, fmt.Errorf("lobby start: %w", err)
	}
	defer lb.Stop()

	initCtx, initCancel := context.WithTimeout(lobbyCtx, 10*time.Second)
	if err := lb.WaitForInitialLoad(initCtx); err != nil {
		logf("warning: KV initial load did not complete: %v", err)
	}
	initCancel()

	cleanCtx, cleanCancel := context.WithTimeout(lobbyCtx, 30*time.Second)
	if err := cleanup.Run(cleanCtx, js, kv, lb); err != nil {
		logf("cleanup warning: %v", err)
	}
	cleanCancel()

	resident := cfg.JoinGameID == "" && !cfg.Create && !cfg.Once
	var total Result
	for {
		gameID, inv, err := selectGame(ctx, lb, cfg, waitTimeout, resident, logf)
		if err != nil {
			if ctx.Err() != nil {
				return total, nil // interrupted while idle: a clean exit
			}
			return total, err
		}

		res, err := playOne(ctx, js, kv, lb, gameID, inv, cfg, tn, rnd, logf)
		// If we came in on an invitation and it did NOT lead to a completed
		// game, decline it so we don't re-accept the same (now unsatisfiable)
		// invitation forever — e.g. the creator over-subscribed our team, or
		// the game filled without us. A successful join already consumed it.
		if inv != nil && err != nil {
			_, _ = lb.RespondInvite(context.Background(), false)
		}
		switch {
		case err == nil:
			total.Games++
			if res.Won {
				total.Wins++
			}
			total.GameID, total.Mode, total.Won = res.GameID, res.Mode, res.Won
			total.Score, total.Level, total.Pieces = res.Score, res.Level, res.Pieces
			outcome := "LOST"
			switch {
			case res.Mode == config.ModeCooperative:
				outcome = "OVER" // cooperative has no winner; the score is shared
			case res.Won:
				outcome = "WON"
			}
			logf("game %s (%s): %s — score %d, level %d, %d pieces", res.GameID, res.Mode, outcome, res.Score, res.Level, res.Pieces)
		case ctx.Err() != nil:
			return total, nil // interrupted mid-game: a clean exit
		case resident:
			// Lost a join race, game vanished, or it never started (already
			// un-joined): log and go back to scanning.
			logf("game %s: %v — returning to the lobby", gameID, err)
			continue
		default:
			return total, err
		}

		if !resident {
			return total, nil
		}
		logf("back in the lobby (%d game(s) played, %d won) — waiting for the next one", total.Games, total.Wins)
	}
}

// playOne joins one game, plays it to the end, and tears down its engine and
// roster membership. It mirrors nativeui/lifecycle.go's joinGame/returnToLobby
// responsibilities for a single game.
func playOne(
	ctx context.Context,
	js jetstream.JetStream,
	kv jetstream.KeyValue,
	lb *lobby.Lobby,
	gameID string,
	inv *lobby.Invitation,
	cfg Config,
	tn Tuning,
	rnd *rand.Rand,
	logf func(string, ...any),
) (Result, error) {
	waitTimeout := cfg.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = defaultWaitTimeout
	}

	// The listing arrives via the KV watcher, which can lag our own
	// CreateGame's write by a beat — wait for it rather than racing it.
	var g lobby.GameListing
	found := false
	waitFor(ctx, 5*time.Second, func() bool {
		g, found = lb.Games()[gameID]
		return found
	})
	if !found {
		return Result{}, fmt.Errorf("game %s not found", gameID)
	}
	mode := g.Mode

	// The first other roster member seeds the opponent board consumer
	// (competitive only); "" is fine — the engine's roster consumer discovers
	// everyone.
	opponentID := ""
	for _, p := range g.Players {
		if p.PlayerID != cfg.Name {
			opponentID = p.PlayerID
			break
		}
	}

	// Teams: an invitation designates the team; otherwise take the team with
	// the most free seats and, if another join wins that race (ErrTeamFull),
	// try the other team once.
	team := 0
	if mode == config.ModeTeams {
		if inv != nil && inv.GameID == gameID {
			team = inv.Team
		} else if team = pickTeam(g); team < 0 {
			return Result{}, lobby.ErrTeamFull
		}
	}
	res, err := lb.JoinGame(ctx, gameID, team)
	if mode == config.ModeTeams && inv == nil && errors.Is(err, lobby.ErrTeamFull) {
		team = 1 - team
		res, err = lb.JoinGame(ctx, gameID, team)
	}
	if err != nil {
		return Result{}, fmt.Errorf("join game: %w", err)
	}
	if mode == config.ModeTeams {
		logf("joined %s game %s as player %d (team %d, slot %d)", mode, gameID, res.PlayerIdx, res.Team, res.TeamSlot)
	} else {
		logf("joined %s game %s as player %d", mode, gameID, res.PlayerIdx)
	}
	defer lb.LeaveGame(context.Background(), gameID)

	// Engine wiring, mirroring the GUI's joinGame: archive-and-clean-up when
	// (and only when) OUR engine drives the finished transition — the winner
	// in competitive, any winning-team member in teams, the topper in coop.
	e := engine.New(js, gameID, cfg.Name, opponentID, mode, engine.ModePlayer, res.PlayerIdx, res.Team, res.TeamSlot)
	// archiveStarted tells waitArchived that OUR engine is the archiver, so it
	// must wait for archiveDone (full completion) and ignore the meta status —
	// ArchiveAndCleanup flips the meta to archived early, well before the
	// board capture and record publish, and tearing down on that early signal
	// would drain the connection out from under our own in-flight archive.
	var archiveStarted atomic.Bool
	archiveDone := make(chan struct{})
	e.OnGameFinished = func() {
		archiveStarted.Store(true)
		var players []lobby.PlayerSummary
		if g, ok := lb.Games()[gameID]; ok {
			players = g.Players
		}
		archive.ArchiveAndCleanup(context.Background(), js, kv, e, lb, players)
		close(archiveDone)
	}

	// wonUpdate captures the authoritative Won flag from the engine's own
	// UpdateGameOver (0 = not seen, 1 = won, 2 = lost). The Updates channel is
	// lossy, so game-over detection itself polls e.Mode(); this is the tiebreak
	// for the result. The pump also keeps the channel drained.
	var wonUpdate atomic.Int32
	gameCtx, gameCancel := context.WithCancel(ctx)
	defer gameCancel()
	go pumpUpdates(gameCtx, e, &wonUpdate, logf)

	if err := e.Start(); err != nil {
		return Result{}, fmt.Errorf("engine start: %w", err)
	}
	defer e.Stop()

	// Ready up. Whoever's toggle completes the ready set runs the countdown —
	// skipping this when we are last would leave the game stuck forever.
	rr, err := lb.ToggleReady(ctx, gameID)
	if err != nil {
		return Result{}, fmt.Errorf("ready: %w", err)
	}
	if rr.AllReady {
		go runCountdown(js, lb, gameID)
	}

	// Wait for the first spawn (the engine spawns on the in_progress meta).
	logf("waiting for game %s to start...", gameID)
	startDeadline := time.Now().Add(waitTimeout)
	for e.Playfield().ActivePieceForPlayer(res.PlayerIdx) == nil && e.Mode() == engine.ModePlayer {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if time.Now().After(startDeadline) {
			// Free the seat before walking away, or the game can never fill.
			if err := lb.UnjoinGame(context.Background(), gameID); err != nil {
				logf("unjoin %s: %v", gameID, err)
			}
			return Result{}, errStartTimeout
		}
		sleepCtx(ctx, startPollInterval)
	}
	logf("game on (%s, difficulty %s)", mode, cfg.Difficulty)

	// Planning rules: on shared boards other players' active pieces block,
	// and our next piece spawns in our own 10-column section.
	rules := Rules{PlayerIdx: res.PlayerIdx}
	switch mode {
	case config.ModeCooperative:
		rules.Shared, rules.SectionIdx = true, res.PlayerIdx
	case config.ModeTeams:
		rules.Shared, rules.SectionIdx = true, res.TeamSlot
	}

	// Roster snapshot for teams-outcome polling: the game only starts full,
	// so the listing is complete by now.
	roster := g.Players
	if g2, ok := lb.Games()[gameID]; ok {
		roster = g2.Players
	}

	playGame(ctx, e, tn, rnd, rules, logf)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	result := Result{GameID: gameID, Mode: mode, Level: e.AchievedLevel(), Pieces: e.PieceIdx()}

	switch mode {
	case config.ModeCooperative:
		// Cooperative has no winner: any top-out ends the game for everyone
		// and the score is shared. The topper's engine (possibly ours)
		// finishes and archives; wait until whoever it is has done so.
		result.Score = e.Score()
		waitArchived(ctx, js, gameID, archiveDone, &archiveStarted, logf)

	case config.ModeTeams:
		// Our own top-out is not the outcome — the TEAM plays on. Wait for
		// the verdict: the engine's authoritative Won update (an eliminated
		// member's LOST flips to WON if the team prevails), with a poll of
		// the roster's eliminations as the lossy-channel fallback.
		won := false
		waitFor(ctx, waitTimeout, func() bool {
			if wonUpdate.Load() == 1 {
				won = true
				return true
			}
			if w, decided := teamsOutcome(e, roster, res.Team); decided {
				won = w
				return true
			}
			return gameGone(ctx, js, gameID)
		})
		if wonUpdate.Load() == 1 {
			won = true
		}
		result.Won = won
		result.Score = e.TeamScores()[res.Team]
		result.Level = e.TeamLevels()[res.Team]
		waitArchived(ctx, js, gameID, archiveDone, &archiveStarted, logf)

	default: // competitive
		// The loser's engine flips mode immediately on top-out but records
		// itself in the eliminated set only when its own game-over event
		// echoes back; give the events a moment to settle, preferring the
		// pump's authoritative UpdateGameOver if it arrived.
		won := false
		waitFor(ctx, 2*time.Second, func() bool { return wonUpdate.Load() != 0 })
		switch wonUpdate.Load() {
		case 1:
			won = true
		case 2:
			won = false
		default:
			won = !e.IsEliminated(cfg.Name)
		}
		result.Won = won
		result.Score = e.Score()

		if won {
			// Our engine drove the finished transition; OnGameFinished fires
			// ~5s later and archives. Leaving before it completes would
			// strand the game unarchived.
			logf("won — waiting for the game to archive")
			waitArchived(ctx, js, gameID, archiveDone, &archiveStarted, logf)
		} else if cfg.LingerAfterLoss {
			logf("lost — lingering until the game finishes")
			waitFor(ctx, waitTimeout, func() bool { return gameGone(ctx, js, gameID) })
		} else {
			// Short grace so our game-over event is safely committed and
			// echoed. The game continues without us; no archive to wait for.
			sleepCtx(ctx, 2*time.Second)
		}
	}

	return result, nil
}

// pickTeam returns the team with the most free seats (ties go to team 0), or
// -1 when both are full.
func pickTeam(g lobby.GameListing) int {
	free0 := g.TeamSize - g.TeamMemberCount(0)
	free1 := g.TeamSize - g.TeamMemberCount(1)
	switch {
	case free0 <= 0 && free1 <= 0:
		return -1
	case free1 > free0:
		return 1
	default:
		return 0
	}
}

// teamsOutcome derives the teams verdict from the roster and the engine's
// elimination set: decided once either team is fully eliminated; won when it
// is the other team (a draw — both dead — counts as not won).
func teamsOutcome(e *engine.Engine, players []lobby.PlayerSummary, myTeam int) (won, decided bool) {
	if len(players) == 0 {
		return false, false
	}
	myDead, otherDead := true, true
	for _, p := range players {
		if !e.IsEliminated(p.PlayerID) {
			if p.Team == myTeam {
				myDead = false
			} else {
				otherDead = false
			}
		}
	}
	switch {
	case otherDead && !myDead:
		return true, true
	case myDead:
		return false, true
	}
	return false, false
}

// gameGone reports whether the game has finished without us (meta finished/
// archived, or the stream already deleted by the archiver).
func gameGone(ctx context.Context, js jetstream.JetStream, gameID string) bool {
	m, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		return true
	}
	return m.Status == config.GameStatusFinished || m.Status == config.GameStatusArchived
}

// waitArchived blocks until the game is archived — by our own engine's
// OnGameFinished (archiveDone) or by another peer (meta archived / stream
// deleted) — bounded so a stuck archive can't wedge the agent. Once OUR archive
// has started, only archiveDone counts: ArchiveAndCleanup transitions the
// meta to archived before it captures boards and publishes the record, and
// leaving on that early signal would drain the connection under our own
// archive mid-flight.
func waitArchived(ctx context.Context, js jetstream.JetStream, gameID string, archiveDone <-chan struct{}, archiveStarted *atomic.Bool, logf func(string, ...any)) {
	ok := waitFor(ctx, 60*time.Second, func() bool {
		select {
		case <-archiveDone:
			return true
		default:
		}
		if archiveStarted.Load() {
			return false // we are the archiver; wait for our own completion
		}
		m, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
		if err != nil {
			return true // stream deleted: archived
		}
		return m.Status == config.GameStatusArchived
	})
	if !ok && ctx.Err() == nil {
		logf("warning: game %s was not archived in time", gameID)
	}
}

// selectGame resolves cfg's game-selection behavior to a joinable game ID,
// along with the invitation that led there (nil when none). When resident,
// the auto-join scan has no deadline — waiting in the lobby is the point —
// and ends only with ctx.
func selectGame(ctx context.Context, lb *lobby.Lobby, cfg Config, waitTimeout time.Duration, resident bool, logf func(string, ...any)) (string, *lobby.Invitation, error) {
	if cfg.JoinGameID != "" {
		g, ok := lb.Games()[cfg.JoinGameID]
		if !ok {
			return "", nil, fmt.Errorf("game %s not found", cfg.JoinGameID)
		}
		if err := joinable(g); err != nil {
			return "", nil, fmt.Errorf("game %s: %w", cfg.JoinGameID, err)
		}
		return cfg.JoinGameID, nil, nil
	}

	if cfg.Create {
		count := cfg.Players
		if count <= 0 {
			count = defaultPlayers
		}
		// Like the GUI's create row, the count is players PER TEAM in teams
		// mode and the total player count otherwise.
		playerCount, teamSize := count, 0
		if cfg.Mode == config.ModeTeams {
			teamSize = count
			playerCount = config.TeamCount * count
		}
		maxAgents := cfg.MaxAgents
		if maxAgents <= 0 || maxAgents > playerCount {
			maxAgents = playerCount // agent-hosted games are agent-friendly by default
		}
		gameID, err := lb.CreateGame(ctx, cfg.Mode, playerCount, teamSize, maxAgents, false)
		if err != nil {
			return "", nil, fmt.Errorf("create game: %w", err)
		}
		logf("created %s game %s for %d players (max %d agents) — waiting for opponents", cfg.Mode, gameID, playerCount, maxAgents)
		return gameID, nil, nil
	}

	// Auto-join: poll the lobby for the first agent-joinable game of any mode.
	logf("waiting for a joinable game that allows agents...")
	var deadline time.Time
	if !resident {
		deadline = time.Now().Add(waitTimeout)
	}
	for {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return "", nil, fmt.Errorf("no joinable competitive game appeared within %s", waitTimeout)
		}
		// An invitation is the strongest join signal — agents accept
		// automatically (the human who sent it explicitly chose this agent).
		if inv := lb.MyInvite(); inv != nil {
			if _, ok := lb.Games()[inv.GameID]; ok {
				logf("accepting %s's invitation to %s game %s", inv.FromName, inv.Mode, inv.GameID)
				return inv.GameID, inv, nil
			}
			// The invited game is gone; consume the stale invitation.
			_, _ = lb.RespondInvite(ctx, false)
		}
		abandoned := lb.AbandonedGames()
		games := lb.Games()
		ids := make([]string, 0, len(games))
		for id := range games {
			ids = append(ids, id)
		}
		// Oldest first, and deterministic across scans.
		sort.Slice(ids, func(i, j int) bool {
			gi, gj := games[ids[i]], games[ids[j]]
			if !gi.CreatedAt.Equal(gj.CreatedAt) {
				return gi.CreatedAt.Before(gj.CreatedAt)
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			if abandoned[id] {
				continue
			}
			if joinable(games[id]) == nil {
				return id, nil, nil
			}
		}
		sleepCtx(ctx, lobbyScanInterval)
	}
}

// joinable guards an agent join before calling lobby.JoinGame: the game (any
// mode) must not be running yet, have a free roster seat — with a free seat
// on at least one TEAM in teams mode — (JoinGame does not check overall
// fullness; joining a full or started game would "succeed" and corrupt the
// game's playerCount assumptions), and have a free agent seat under the
// creator's agent policy (which JoinGame DOES enforce atomically; this is the
// polite pre-check that keeps the scan from hammering closed games).
func joinable(g lobby.GameListing) error {
	if g.InviteOnly {
		return lobby.ErrNotInvited // joined via an invitation, never by scanning
	}
	if g.Status != config.GameStatusCreated && g.Status != config.GameStatusStarting {
		return fmt.Errorf("game is %s", g.Status)
	}
	if len(g.Players) >= g.PlayerCount {
		return errors.New("game is full")
	}
	if g.Mode == config.ModeTeams && pickTeam(g) < 0 {
		return lobby.ErrTeamFull
	}
	if g.MaxAgents <= 0 {
		return lobby.ErrAgentsNotAllowed
	}
	if g.AgentCount() >= g.MaxAgents {
		return lobby.ErrAgentSlotsFull
	}
	return nil
}

// runCountdown is the agent's copy of the GUI's (unexported) countdown driver:
// publish 5..0 to the countdown subject at one-second intervals, hold "GO!"
// briefly, then transition the game to in_progress.
func runCountdown(js jetstream.JetStream, lb *lobby.Lobby, gameID string) {
	ctx := context.Background()
	for i := 5; i > 0; i-- {
		data, _ := json.Marshal(map[string]int{"seconds": i})
		_, _ = js.Publish(ctx, config.CountdownSubject(gameID), data)
		time.Sleep(1 * time.Second)
	}
	data, _ := json.Marshal(map[string]int{"seconds": 0})
	_, _ = js.Publish(ctx, config.CountdownSubject(gameID), data)
	time.Sleep(700 * time.Millisecond)
	lb.StartGame(ctx, gameID)
}

// pumpUpdates drains the engine's (lossy) Updates channel, recording the
// authoritative Won flag if an UpdateGameOver comes through and logging the
// events a headless operator would want to see.
func pumpUpdates(ctx context.Context, e *engine.Engine, wonUpdate *atomic.Int32, logf func(string, ...any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-e.Updates:
			switch u.Kind {
			case engine.UpdateGameOver:
				if u.Won {
					wonUpdate.Store(1)
				} else {
					wonUpdate.Store(2)
				}
			case engine.UpdateCountdown:
				logf("countdown: %d", u.Countdown)
			case engine.UpdatePlayerEliminated:
				logf("player eliminated: %s", u.EliminatedPlayerID)
			}
		}
	}
}

// playGame is the per-piece loop: observe, plan, (blunder-)choose, execute,
// and re-plan when the board shifts underneath the script. It reads only what
// a human player can see in the UI: the committed boards, the roster, and its
// own falling piece — never the RNG seed or upcoming pieces.
func playGame(ctx context.Context, e *engine.Engine, tn Tuning, rnd *rand.Rand, rules Rules, logf func(string, ...any)) {
	playerIdx := e.PlayerIdx()
	var lastIdx uint64
	replans := 0

	for ctx.Err() == nil && e.Mode() == engine.ModePlayer {
		idx := e.PieceIdx()
		pf := e.Playfield()
		active := pf.ActivePieceForPlayer(playerIdx)
		if active == nil {
			// Between lock-in and the next spawn.
			sleepCtx(ctx, 20*time.Millisecond)
			continue
		}
		if idx != lastIdx {
			lastIdx, replans = idx, 0
		}

		ranked := PlanPlacements(pf, rules, *active, tn)
		placement, ok := ChoosePlacement(ranked, tn, rnd)
		if !ok || replans >= maxReplans {
			// Nowhere to go (or the board keeps shifting): drop where we are
			// rather than deadlock, and let the next piece re-plan.
			e.HardDrop()
			waitFor(ctx, tn.dropTimeout(), func() bool {
				return e.PieceIdx() > idx || e.Mode() != engine.ModePlayer
			})
			continue
		}

		sleepCtx(ctx, tn.PieceDelay)

		err := Execute(ctx, e, placement, tn, idx, pf.AdversarialRowCount())
		switch {
		case err == nil:
			// Piece locked; on to the next.
		case errors.Is(err, ErrBoardChanged), errors.Is(err, ErrStalled):
			replans++
		case errors.Is(err, ErrGameOver):
			return
		default: // ctx cancelled
			return
		}
	}
}
