package nativeui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"jetris/internal/archive"
	"jetris/internal/cleanup"
	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
)

// doConnectAndLogin dials NATS per the player's connection-picker choice (cfg
// holds either a context name or a URL), provisions the streams/KV, then
// continues into the normal login flow. Runs off the UI goroutine; loggingIn
// was already set by the submit handler. On failure the player stays on the
// login screen with the error shown and can retry with a different choice.
func (a *App) doConnectAndLogin(name string, cfg config.Config) {
	// A previous Play may have connected without reaching the lobby (name
	// collision cancelled, lobby init failed) — drop that connection first so
	// every attempt connects fresh per the current picker choice.
	a.disconnect()

	if cfg.RunEmbedded {
		// "LAN mode (embedded NATS server)": bring up (or reuse) the
		// in-process server, then connect to it via the same LAN address other
		// players dial. (Not loopback: another NATS server holding a
		// 127.0.0.1:4222-specific bind would intercept a loopback dial even
		// though our 0.0.0.0 bind succeeded — a real setup on NATS developer
		// machines.)
		addr, err := a.ensureEmbeddedServer(cfg.EmbeddedPort)
		if err != nil {
			a.mu.Lock()
			a.loginErr = err.Error()
			a.loggingIn = false
			a.mu.Unlock()
			a.invalidate()
			return
		}
		cfg.NATSURL = "nats://" + addr
	}

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
	if cfg.RunEmbedded {
		// Belt and braces for the interception case above: make sure the
		// server we reached is OUR embedded server, not a stranger on the
		// same port.
		a.mu.Lock()
		srv := a.embSrv
		a.mu.Unlock()
		if srv != nil && nc.ConnectedServerId() != srv.ID() {
			nc.Close()
			a.mu.Lock()
			a.loginErr = fmt.Sprintf("another NATS server is already using port %d — connect to it via the URL option instead, or stop it", embeddedPortOrDefault(cfg.EmbeddedPort))
			a.loggingIn = false
			a.mu.Unlock()
			a.invalidate()
			return
		}
	}
	log.Printf("connected to NATS at %s", nc.ConnectedUrl())
	a.mu.Lock()
	a.nc, a.js, a.kv = nc, js, kv
	a.usingEmbedded = cfg.RunEmbedded
	a.mu.Unlock()
	a.doLogin(name, false)
}

// embeddedPortOrDefault maps the picker's port choice (0 = unset) to the port
// the embedded server actually listens on.
func embeddedPortOrDefault(port int) int {
	if port <= 0 {
		return config.DefaultEmbeddedPort
	}
	return port
}

// ensureEmbeddedServer starts the in-process JetStream-enabled nats-server on
// the given port (0 = config.DefaultEmbeddedPort) on all interfaces, storage
// in ./config.EmbeddedStoreDir, and records its shareable "<lan-ip>:<port>"
// address. Reused by later login attempts; it runs until the window closes so
// friends stay connected across the host's lobby exits — unless the player
// picked a DIFFERENT port on a fresh login, which restarts it there. Returns
// that address — which is also the one the app connects through (see
// doConnectAndLogin on why not loopback).
func (a *App) ensureEmbeddedServer(wantPort int) (string, error) {
	wantPort = embeddedPortOrDefault(wantPort)
	a.mu.Lock()
	srv := a.embSrv
	a.mu.Unlock()
	if srv != nil {
		if tcp, ok := srv.Addr().(*net.TCPAddr); ok && tcp.Port != wantPort {
			log.Printf("embedded nats-server moving from port %d to %d", tcp.Port, wantPort)
			srv.Shutdown()
			srv = nil
			a.mu.Lock()
			a.embSrv = nil
			a.mu.Unlock()
		}
	}
	if srv == nil {
		var err error
		srv, err = natspkg.StartEmbeddedServer(config.EmbeddedStoreDir, wantPort)
		if err != nil {
			return "", err
		}
		a.mu.Lock()
		a.embSrv = srv
		a.mu.Unlock()
	}
	port := wantPort
	if tcp, ok := srv.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	addr := fmt.Sprintf("%s:%d", natspkg.LanIP(), port)
	a.mu.Lock()
	a.embAddr = addr
	a.mu.Unlock()
	log.Printf("embedded nats-server serving on %s (JetStream data in ./%s)", addr, config.EmbeddedStoreDir)
	return addr, nil
}

// doCheckConn validates a connection-picker choice without committing to it:
// dial, measure the server ping (flush round trip), close. Runs off the UI
// goroutine; connChecking was already set by the click handler. The outcome
// lands in connCheckMsg (rendered green/red next to the button).
func (a *App) doCheckConn(cfg config.Config) {
	if cfg.RunEmbedded {
		// Nothing to dial: report where the embedded server serves (or would).
		a.mu.Lock()
		running := a.embSrv != nil
		addr := a.embAddr
		a.mu.Unlock()
		verb := "will serve"
		if running {
			verb = "serving"
		} else {
			addr = fmt.Sprintf("%s:%d", natspkg.LanIP(), embeddedPortOrDefault(cfg.EmbeddedPort))
		}
		a.mu.Lock()
		a.connChecking = false
		a.connCheckOK = true
		a.connCheckMsg = fmt.Sprintf("✓ %s on nats://%s · data in ./%s", verb, addr, config.EmbeddedStoreDir)
		a.mu.Unlock()
		a.invalidate()
		return
	}
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

// disconnect drops the app-owned NATS connection and clears the handles.
// Called with no lobby or engine running (from quit, or at the top of a fresh
// connect attempt), off the UI goroutine — Drain blocks. An embedded server
// keeps running (friends may be connected to it); only the usingEmbedded mark
// is cleared until the next embedded login.
func (a *App) disconnect() {
	a.mu.Lock()
	nc := a.nc
	a.nc, a.js, a.kv = nil, nil, nil
	a.usingEmbedded = false
	a.mu.Unlock()
	if nc != nil {
		nc.Drain()
	}
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

	// Seed the chat log from the lobby's snapshot rather than starting empty:
	// the chat consumer replayed the stream's backlog while nothing was
	// draining lb.Updates (the pump starts below), so those messages' pings
	// may have been dropped — and without a seed the log would stay empty
	// until the next live message.
	a.mu.Lock()
	a.lobby = lb
	a.lobbyCancel = lobbyCancel
	a.chatLog = lb.ChatLog()
	a.mu.Unlock()

	go a.pumpLobby(lobbyCtx, lb)
	return nil
}

// createGame creates a game and returns its ID. For teams mode, count is the
// number of players PER TEAM; for the other modes it is the total player
// count. maxAgents is the agent policy — how many seats idle jetris-agent
// players may take (0 = agents may not join). nextCount is how many upcoming
// pieces the game reveals (0..config.MaxNextCount). inviteOnly restricts
// joining to invited players (the invite flow sets it and then sends the
// invitations).
func (a *App) createGame(mode config.GameMode, count, maxAgents, nextCount int, inviteOnly bool) string {
	lb := a.getLobby()
	if lb == nil {
		return ""
	}
	playerCount, teamSize := count, 0
	if mode == config.ModeTeams {
		teamSize = count
		playerCount = config.TeamCount * count
	}
	gameID, err := lb.CreateGame(context.Background(), mode, playerCount, teamSize, maxAgents, nextCount, inviteOnly)
	if err != nil {
		log.Printf("create game: %v", err)
		return ""
	}
	return gameID
}

// deleteGame removes an abandoned game and all its NATS state (game stream,
// game chat messages, lobby KV listing). Dispatched from the lobby row's
// Delete button after the player confirms the "Are you sure?" prompt.
func (a *App) deleteGame(gameID string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if err := lb.DeleteGame(context.Background(), gameID); err != nil {
		log.Printf("delete game: %v", err)
	}
	a.invalidate()
}

// selfSeat applies the invite picker's "You" row: sel is "" (host without
// playing — free the seat) or a team digit ("0"/"1"; non-teams games always
// pass "0"). Moving between teams frees the old seat first. Only roster
// membership changes here — the engine and game screen come later, when the
// picker sees the game fill and hands the creator over via joinGame.
func (a *App) selfSeat(gameID, sel string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	ctx := context.Background()
	if sel == "" {
		if err := lb.UnjoinGame(ctx, gameID); err != nil {
			log.Printf("free own seat: %v", err)
		}
		a.invalidate()
		return
	}
	team := int(sel[0] - '0')
	if g, ok := lb.Games()[gameID]; ok {
		for _, p := range g.Players {
			if p.PlayerID == lb.PlayerID() && p.Team != team {
				if err := lb.UnjoinGame(ctx, gameID); err != nil {
					log.Printf("move own seat: %v", err)
					a.invalidate()
					return
				}
			}
		}
	}
	if _, err := lb.JoinGame(ctx, gameID, team); err != nil {
		log.Printf("take own seat: %v", err)
	}
	a.invalidate()
}

// uninvite retracts a pending invitation (the invitee's pop-up disappears) or
// dismisses a declined one. Dispatched from the creator's invite-status rows.
func (a *App) uninvite(gameID, inviteeID string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if err := lb.Uninvite(context.Background(), inviteeID, gameID); err != nil {
		log.Printf("uninvite %s: %v", inviteeID, err)
	}
	a.invalidate()
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
	a.fireworks = nil
	a.confirmLeave = false
	// Rejoin: our ready mark may still be set from an earlier visit to this
	// game (leaveCurrentGame clears it, but stay roster-accurate regardless).
	a.myReady = false
	if a.lobby != nil {
		for _, p := range players {
			if p.PlayerID == a.lobby.PlayerID() {
				a.myReady = p.Ready
				break
			}
		}
	}
	a.flash = map[[2]int]time.Time{}
	a.specFlash = map[int]map[[2]int]time.Time{}
	a.msgLog = nil
	a.resetMsgGroups()
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

// gameAlive reports whether a listing's status still describes a game that can
// be (re)joined — anything before finished/archived/cancelled.
func gameAlive(s config.GameStatus) bool {
	switch s {
	case config.GameStatusCreated, config.GameStatusStarting, config.GameStatusInProgress:
		return true
	}
	return false
}

// rosterHas reports whether the player holds a seat in the listing's roster.
func rosterHas(g lobby.GameListing, playerID string) bool {
	for _, p := range g.Players {
		if p.PlayerID == playerID {
			return true
		}
	}
	return false
}

// leaveCurrentGame is the "Back to Lobby" action for a seated player: clear
// our ready mark (leaving the game screen revokes readiness), release presence
// if the game is over or gone, then return to the lobby screen. The roster
// seat is KEPT while the game is alive — the lobby row shows it as
// joined/playing and its Rejoin button comes back in.
func (a *App) leaveCurrentGame() {
	lb := a.getLobby()
	eng := a.getEngine()
	if lb != nil && eng != nil && eng.Mode() != engine.ModeSpectator {
		gameID := eng.GameID()
		a.mu.Lock()
		wasReady := a.myReady
		a.mu.Unlock()
		if wasReady {
			if err := lb.SetReady(context.Background(), gameID, false); err != nil {
				log.Printf("clear ready on leave: %v", err)
			}
		}
		// Presence: while we still hold a seat in a live game we stay marked
		// in-game (and thus un-invitable); once the game is done or gone we are
		// back to a plain lobby player.
		if g, ok := lb.Games()[gameID]; !ok || !gameAlive(g.Status) || !rosterHas(g, lb.PlayerID()) {
			_ = lb.LeaveGame(context.Background(), gameID)
		}
	}
	a.returnToLobby()
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
	a.fireworks = nil
	a.confirmLeave = false
	a.countdown = -1
	a.score = 0
	a.level = 0
	a.teamScores = [config.TeamCount]int{}
	a.teamLevels = [config.TeamCount]int{}
	a.rtt = 0
	a.gameStatus = ""
	a.flash = map[[2]int]time.Time{}
	a.specFlash = map[int]map[[2]int]time.Time{}
	a.msgLog = nil
	a.resetMsgGroups()
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

// quit leaves the lobby and returns to the combined login screen. The
// app-owned NATS connection is dropped too, so the player lands back on the
// connection picker and can log in to a different server (runs off the UI
// goroutine — the quit button dispatches with `go a.quit()`).
func (a *App) quit() {
	a.mu.Lock()
	lb := a.lobby
	lobbyCancel := a.lobbyCancel
	a.lobby = nil
	a.lobbyCancel = nil
	a.screen = screenLogin
	a.mu.Unlock()

	if lb != nil {
		// Delete our presence NOW (connection still up) so other clients get an
		// immediate KV delete event, rather than waiting for the presence TTL.
		leaveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lb.Leave(leaveCtx)
		cancel()
		lb.Stop()
	}
	if lobbyCancel != nil {
		lobbyCancel()
	}
	a.disconnect()
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

// sendGameChat routes one game-screen chat line: a leading "@lobby" sends the
// rest to the lobby chat (visible to everyone), anything else to this game's
// chat subject (visible only to the game's players and spectators).
func (a *App) sendGameChat(eng *engine.Engine, text string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if rest, ok := strings.CutPrefix(text, "@lobby"); ok {
		if rest = strings.TrimSpace(rest); rest != "" {
			if err := lb.SendChat(context.Background(), rest); err != nil {
				log.Printf("send lobby chat: %v", err)
			}
		}
		return
	}
	spectator := eng.Mode() != engine.ModePlayer
	if err := lb.SendGameChat(context.Background(), eng.GameID(), text, spectator); err != nil {
		log.Printf("send game chat: %v", err)
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
	srv := a.embSrv
	a.eng = nil
	a.lobby = nil
	a.nc = nil
	a.embSrv = nil
	a.mu.Unlock()

	if engCancel != nil {
		engCancel()
	}
	if eng != nil {
		eng.Stop()
	}
	if lb != nil {
		// Remove our presence before draining so watchers see us leave at once.
		leaveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lb.Leave(leaveCtx)
		cancel()
		lb.Stop()
	}
	if lobbyCancel != nil {
		lobbyCancel()
	}
	if nc != nil {
		nc.Drain()
	}
	if srv != nil {
		srv.Shutdown()
	}
}
