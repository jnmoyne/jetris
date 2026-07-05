package nativeui

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"jetricks/internal/archive"
	"jetricks/internal/cleanup"
	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
)

// doConnectAndLogin dials NATS per the player's connection-picker choice (cfg
// holds either a context name or a URL), provisions the streams/KV, then
// continues into the normal login flow. Runs off the UI goroutine; loggingIn
// was already set by the submit handler. On failure the player stays on the
// login screen with the error shown and can retry with a different choice.
func (a *App) doConnectAndLogin(name string, cfg config.Config) {
	connCtx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	nc, js, kv, err := natspkg.Bootstrap(connCtx, cfg)
	cancel()
	if err != nil {
		a.mu.Lock()
		a.loginErr = "connect failed: " + err.Error()
		a.loggingIn = false
		a.mu.Unlock()
		a.invalidate()
		return
	}
	if a.ctx.Err() != nil {
		nc.Close() // window closed while we were connecting
		return
	}
	log.Printf("connected to NATS at %s", nc.ConnectedUrl())
	a.mu.Lock()
	a.nc, a.js, a.kv = nc, js, kv
	a.mu.Unlock()
	a.doLogin(name, false)
}

// doCheckConn validates a connection-picker choice without committing to it:
// dial, measure the server ping (flush round trip), close. Runs off the UI
// goroutine; connChecking was already set by the click handler. The outcome
// lands in connCheckMsg (rendered green/red next to the button).
func (a *App) doCheckConn(cfg config.Config) {
	url, rtt, err := natspkg.CheckConnection(cfg)
	a.mu.Lock()
	a.connChecking = false
	if err != nil {
		a.connCheckOK = false
		a.connCheckMsg = "✗ " + err.Error()
	} else {
		a.connCheckOK = true
		a.connCheckMsg = "✓ " + url + " · ping " + formatRTT(rtt)
	}
	a.mu.Unlock()
	a.invalidate()
}

// doChangeServer drops the app-owned NATS connection and clears the handles so
// the login screen's CONNECT TO chooser reappears and the player can connect to
// a different server. Only reachable from the login screen while no login is in
// flight (the click handler re-checks loggingIn), which means no lobby or
// engine is using the connection. Runs off the UI goroutine (Drain blocks).
func (a *App) doChangeServer() {
	a.mu.Lock()
	nc := a.nc
	a.nc, a.js, a.kv = nil, nil, nil
	a.loginErr = ""
	a.connCheckMsg = ""
	a.connCheckOK = false
	a.mu.Unlock()
	if nc != nil {
		nc.Drain()
	}
	a.invalidate()
}

// doLogin runs the (blocking) name-collision check and lobby bring-up off the UI
// goroutine, then transitions to the lobby screen.
func (a *App) doLogin(name string, force bool) {
	if !force {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
		inUse, err := lobby.IsNameInUse(ctx, a.kv, name)
		cancel()
		if err != nil {
			log.Printf("login: name-in-use check: %v", err)
		}
		if inUse {
			a.mu.Lock()
			a.loginCollision = true
			a.loggingIn = false
			a.mu.Unlock()
			a.invalidate()
			return
		}
	}
	if err := a.initLobby(name); err != nil {
		a.mu.Lock()
		a.loginErr = err.Error()
		a.loggingIn = false
		a.mu.Unlock()
		a.invalidate()
		return
	}
	a.mu.Lock()
	a.loggingIn = false
	a.loginCollision = false
	a.screen = screenLobby
	a.mu.Unlock()
	a.invalidate()
}

// initLobby mirrors ui.Server.initLobby: create the lobby, start it, wait for the
// initial KV load, run cleanup, then pump its updates.
func (a *App) initLobby(name string) error {
	lobbyCtx, lobbyCancel := context.WithCancel(a.ctx)
	lb := lobby.New(a.js, a.kv, name, name)
	if err := lb.Start(lobbyCtx); err != nil {
		lobbyCancel()
		return err
	}

	initCtx, initCancel := context.WithTimeout(lobbyCtx, 10*time.Second)
	if err := lb.WaitForInitialLoad(initCtx); err != nil {
		log.Printf("warning: KV initial load did not complete: %v", err)
	}
	initCancel()

	cleanCtx, cleanCancel := context.WithTimeout(lobbyCtx, 30*time.Second)
	if err := cleanup.Run(cleanCtx, a.js, a.kv, lb); err != nil {
		log.Printf("cleanup warning: %v", err)
	}
	cleanCancel()

	a.mu.Lock()
	a.lobby = lb
	a.lobbyCancel = lobbyCancel
	a.chatLog = nil
	a.mu.Unlock()

	go a.pumpLobby(lobbyCtx, lb)
	return nil
}

// createGame creates a game. For teams mode, count is the number of players
// PER TEAM; for the other modes it is the total player count.
func (a *App) createGame(mode config.GameMode, count int) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	playerCount, teamSize := count, 0
	if mode == config.ModeTeams {
		teamSize = count
		playerCount = config.TeamCount * count
	}
	if _, err := lb.CreateGame(context.Background(), mode, playerCount, teamSize); err != nil {
		log.Printf("create game: %v", err)
	}
}

// joinGame mirrors ui.Server.handleJoinGame: join to get our player index, build
// and start the engine, wire archive-on-finish, and switch to the game screen.
// team selects which team to join in teams mode (ignored otherwise).
func (a *App) joinGame(gameID string, team int) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	g, ok := lb.Games()[gameID]
	if !ok {
		log.Printf("join game: game %s not found", gameID)
		return
	}
	opponentID := ""
	for _, p := range g.Players {
		if p.PlayerID != lb.PlayerID() {
			opponentID = p.PlayerID
			break
		}
	}

	res, err := lb.JoinGame(context.Background(), gameID, team)
	if err != nil {
		// ErrTeamFull in particular: someone else grabbed the last slot first.
		log.Printf("join game: %v", err)
		return
	}

	e := engine.New(lb.GetJS(), gameID, lb.PlayerID(), opponentID, g.Mode, engine.ModePlayer, res.PlayerIdx, res.Team, res.TeamSlot)
	e.OnStreamMsg = a.recordStreamMsg // feeds the "Show NATS messages" panel
	engCtx, engCancel := context.WithCancel(a.ctx)
	e.OnGameFinished = func() {
		// Archive/clean up the finished game's stream + KV. Do NOT return to the
		// lobby here: this callback only fires on the player who triggers the
		// finish (the winner in competitive, the topper in coop), so returning to
		// the lobby would yank just that one player out while everyone else sits
		// on the game-over screen. Every player stays on YOU WON!/YOU LOST until
		// they click Back; only the engine is detached here.
		archive.ArchiveAndCleanup(context.Background(), a.js, a.kv, e, a.getLobby(), a.snapshotGamePlayers())
	}

	// Refresh roster after joining so the legend includes us (see handleJoinGame).
	players := g.Players
	if g2, ok := lb.Games()[gameID]; ok {
		players = g2.Players
	}

	a.startGameScreen(e, engCtx, engCancel, players, string(g.Status))
	go a.pumpEngine(engCtx, e)
	if err := e.Start(); err != nil {
		log.Printf("engine start: %v", err)
	}
	a.invalidate()
}

// spectateGame mirrors ui.Server.handleSpectateGame.
func (a *App) spectateGame(gameID string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	g, ok := lb.Games()[gameID]
	if !ok {
		return
	}
	e := engine.New(lb.GetJS(), gameID, lb.PlayerID(), "", g.Mode, engine.ModeSpectator, 0, 0, 0)
	e.OnStreamMsg = a.recordStreamMsg // feeds the "Show NATS messages" panel
	engCtx, engCancel := context.WithCancel(a.ctx)

	a.startGameScreen(e, engCtx, engCancel, g.Players, string(g.Status))
	go a.pumpEngine(engCtx, e)
	if err := e.Start(); err != nil {
		log.Printf("spectate engine start: %v", err)
	}
	a.invalidate()
}

func (a *App) startGameScreen(e *engine.Engine, engCtx context.Context, engCancel context.CancelFunc, players []lobby.PlayerSummary, status string) {
	a.mu.Lock()
	a.eng = e
	a.engCancel = engCancel
	a.gamePlayers = players
	a.readyPlayers = players
	a.score = 0
	a.level = 0
	a.teamScores = [config.TeamCount]int{}
	a.teamLevels = [config.TeamCount]int{}
	a.rtt = 0
	a.gameStatus = status
	a.countdown = -1
	a.gameOver = false
	a.won = false
	a.myReady = false
	a.flash = map[[2]int]time.Time{}
	a.msgLog = nil
	a.screen = screenGame
	a.mu.Unlock()
}

func (a *App) toggleReady() {
	lb := a.getLobby()
	eng := a.getEngine()
	if lb == nil || eng == nil {
		return
	}
	res, err := lb.ToggleReady(context.Background(), eng.GameID())
	if err != nil {
		log.Printf("toggle ready: %v", err)
		return
	}
	a.mu.Lock()
	a.myReady = res.MyReady
	a.readyPlayers = res.Players
	a.mu.Unlock()
	if res.AllReady {
		go a.runCountdown(eng.GameID())
	}
	a.invalidate()
}

// runCountdown mirrors ui.Server.runCountdown: publish 5..0 to the countdown
// subject then transition the game to in-progress.
func (a *App) runCountdown(gameID string) {
	ctx := context.Background()
	for i := 5; i > 0; i-- {
		data, _ := json.Marshal(map[string]int{"seconds": i})
		_, _ = a.js.Publish(ctx, config.CountdownSubject(gameID), data)
		time.Sleep(1 * time.Second)
	}
	data, _ := json.Marshal(map[string]int{"seconds": 0})
	_, _ = a.js.Publish(ctx, config.CountdownSubject(gameID), data)
	time.Sleep(700 * time.Millisecond) // hold "GO!" on screen before the game starts

	lb := a.getLobby()
	if lb != nil {
		lb.StartGame(ctx, gameID)
	}
}

// returnToLobby stops the active engine and returns to the lobby screen. Safe to
// call more than once.
func (a *App) returnToLobby() {
	a.mu.Lock()
	eng := a.eng
	cancel := a.engCancel
	a.eng = nil
	a.engCancel = nil
	a.gameOver = false
	a.won = false
	a.countdown = -1
	a.score = 0
	a.level = 0
	a.teamScores = [config.TeamCount]int{}
	a.teamLevels = [config.TeamCount]int{}
	a.rtt = 0
	a.gameStatus = ""
	a.flash = map[[2]int]time.Time{}
	a.msgLog = nil
	if a.lobby != nil {
		a.screen = screenLobby
	} else {
		a.screen = screenLogin
	}
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if eng != nil {
		eng.Stop()
	}
	a.invalidate()
}

// quit leaves the lobby and returns to the login screen.
func (a *App) quit() {
	a.mu.Lock()
	lb := a.lobby
	lobbyCancel := a.lobbyCancel
	a.lobby = nil
	a.lobbyCancel = nil
	a.screen = screenLogin
	a.mu.Unlock()

	if lb != nil {
		lb.Stop()
	}
	if lobbyCancel != nil {
		lobbyCancel()
	}
	a.invalidate()
}

func (a *App) sendChat(text string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if err := lb.SendChat(context.Background(), text); err != nil {
		log.Printf("send chat: %v", err)
	}
}

// teardown stops the engine and lobby when the window closes, and drains the
// NATS connection if the app dialed it itself (picker path).
func (a *App) teardown() {
	a.mu.Lock()
	eng := a.eng
	engCancel := a.engCancel
	lb := a.lobby
	lobbyCancel := a.lobbyCancel
	nc := a.nc
	a.eng = nil
	a.lobby = nil
	a.nc = nil
	a.mu.Unlock()

	if engCancel != nil {
		engCancel()
	}
	if eng != nil {
		eng.Stop()
	}
	if lb != nil {
		lb.Stop()
	}
	if lobbyCancel != nil {
		lobbyCancel()
	}
	if nc != nil {
		nc.Drain()
	}
}
