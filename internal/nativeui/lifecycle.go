package nativeui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
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
// holds either a context name or a URL; favorite is the label of the
// favorite the URL came from, "" otherwise — it only decorates the lobby
// header), provisions the streams/KV, then continues into the normal login
// flow. Runs off the UI goroutine; loggingIn was already set by the submit
// handler. On failure the player stays on the login screen with the error
// shown and can retry with a different choice.
func (a *App) doConnectAndLogin(name string, cfg config.Config, favorite string) {
	// A previous Play may have connected without reaching the lobby (name
	// collision cancelled, lobby init failed) — drop that connection first so
	// every attempt connects fresh per the current picker choice.
	a.disconnect()

	if cfg.RunEmbedded {
		// "LAN party mode (embedded NATS server)": bring up (or reuse) the
		// in-process server, then connect to it via the same LAN address other
		// players dial. (Not loopback: another NATS server holding a
		// 127.0.0.1:4222-specific bind would intercept a loopback dial even
		// though our 0.0.0.0 bind succeeded — a real setup on NATS developer
		// machines.)
		addr, err := a.ensureEmbeddedServer(cfg.EmbeddedHost, cfg.EmbeddedPort)
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
			a.loginErr = fmt.Sprintf("another NATS server is already using port %d — connect to it through the server browser instead, or stop it", embeddedPortOrDefault(cfg.EmbeddedPort))
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
	a.connLabel = connectionLabel(cfg, nc.ConnectedUrl(), favorite)
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
// in ./config.EmbeddedStoreDir, and records its shareable "<ip>:<port>"
// address. Reused by later login attempts; it runs until the window closes so
// friends stay connected across the host's lobby exits — unless the player
// picked a DIFFERENT port on a fresh login, which restarts it there. Returns
// that address — which is also the one the app connects through (see
// doConnectAndLogin on why not loopback).
//
// wantHost is the picker's IP field: it changes only the address advertised
// and dialed, never the bind (which stays every interface), so overriding a
// mis-detected LAN IP costs nothing and needs no restart. Empty re-detects.
func (a *App) ensureEmbeddedServer(wantHost string, wantPort int) (string, error) {
	wantPort = embeddedPortOrDefault(wantPort)
	if wantHost == "" {
		wantHost = natspkg.LanIP()
	}
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
	addr := net.JoinHostPort(wantHost, strconv.Itoa(port))
	a.mu.Lock()
	a.embAddr = addr
	a.mu.Unlock()
	log.Printf("embedded nats-server serving on %s (JetStream data in ./%s)", addr, config.EmbeddedStoreDir)
	return addr, nil
}

// doCheckConn probes a connection-page choice without committing to it and
// files the outcome under key (a browser entry key, or probeKeyLAN). Runs off
// the UI goroutine; connProbing was already set by the click handler.
func (a *App) doCheckConn(key string, cfg config.Config) {
	res := a.checkConn(cfg)
	a.mu.Lock()
	a.connProbing = ""
	a.connProbes[key] = res
	a.mu.Unlock()
	a.invalidate()
}

// checkConn performs the actual probe: every choice — context, URL and LAN
// mode alike — really dials NATS, really measures a core NATS ping, and
// counts the players in that server's lobby, so the check exercises the same
// path Play will take; the probe connection is closed again and provisions
// nothing. LAN mode additionally starts the embedded server first (that IS
// what it is checking), then dials it over its LAN address exactly as
// doConnectAndLogin does, including the "is this actually OUR server on that
// port?" identity check. The result's msg is the ✓/✗ line to show.
func (a *App) checkConn(cfg config.Config) probeResult {
	embedded := cfg.RunEmbedded
	if embedded {
		addr, err := a.ensureEmbeddedServer(cfg.EmbeddedHost, cfg.EmbeddedPort)
		if err != nil {
			return probeResult{msg: "✗ " + err.Error()}
		}
		cfg.NATSURL = "nats://" + addr
	}
	res, err := natspkg.CheckConnection(cfg)
	if err != nil {
		return probeResult{msg: "✗ " + err.Error()}
	}
	if embedded {
		a.mu.Lock()
		srv := a.embSrv
		a.mu.Unlock()
		if srv != nil && res.ServerID != srv.ID() {
			return probeResult{msg: fmt.Sprintf("✗ another NATS server is already using port %d — connect to it through the server browser instead, or stop it", embeddedPortOrDefault(cfg.EmbeddedPort))}
		}
	}
	server := res.ServerURL
	if embedded {
		server = "serving on " + server
	}
	return probeResult{
		ok:      true,
		msg:     fmt.Sprintf("✓ %s · Core NATS ping %s · %s", server, formatRTT(res.RTT), playersText(res.Players, res.Lobby)),
		rtt:     res.RTT,
		players: res.Players,
		lobby:   res.Lobby,
	}
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
	a.connLabel = ""
	a.mu.Unlock()
	if nc != nil {
		nc.Drain()
	}
}

// connectionLabel describes a connection for the lobby header: how the player
// chose the server (a NATS CLI context by name, or a plain URL) plus the URL
// actually reached, which for a context or a clustered URL can differ from
// what was configured. connectedURL is nc.ConnectedUrl(); when it is empty the
// configured URL stands in. A URL picked from the favorites carries the
// favorite's name after it in parentheses ("nats://host:4222 (Jetris EU)"),
// so the header names the server the way the player knows it. Any
// user:password in the URL is dropped so credentials never reach the screen.
// LAN mode names the embedded server and leaves the address out: the lobby's
// YOUR SERVER'S URL line right under the header already shows it, as the
// thing to share.
func connectionLabel(cfg config.Config, connectedURL, favorite string) string {
	if cfg.RunEmbedded {
		return "LAN mode (your embedded server)"
	}
	u := stripURLUserinfo(connectedURL)
	if u == "" {
		u = stripURLUserinfo(cfg.NATSURL)
	}
	if cfg.NATSContext != "" {
		return joinLabel("context "+cfg.NATSContext, u)
	}
	if favorite != "" {
		if u == "" {
			return favorite
		}
		return u + " (" + favorite + ")"
	}
	return u
}

func joinLabel(how, u string) string {
	if u == "" {
		return how
	}
	return how + " · " + u
}

// stripURLUserinfo returns raw without any user:password@ part. A string that
// doesn't parse as a URL is returned as is (nc.ConnectedUrl() always parses;
// this only guards typed-in URLs).
func stripURLUserinfo(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
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
// count. maxAgents is the agent policy — how many seats idle agent
// players may take (0 = agents may not join). nextCount is how many upcoming
// pieces the game reveals (0..config.MaxNextCount); holes is how many empty
// cells every garbage row is raised with (0..config.MaxGarbageHoles, 0 =
// solid rows that never clear), randomHoles whether each row draws its own
// columns, and guideline whether attacks follow the Guideline table (0/1/2/4
// rows for 1/2/3/4 lines) instead of one row per line; ghost is whether the
// game draws the hard-drop ghost preview. inviteOnly restricts joining to
// invited players (the invite flow sets it and then sends the invitations).
func (a *App) createGame(mode config.GameMode, count, maxAgents, nextCount, holes int, randomHoles, guideline, ghost, inviteOnly bool) string {
	lb := a.getLobby()
	if lb == nil {
		return ""
	}
	playerCount, teamSize := count, 0
	if mode == config.ModeTeams {
		teamSize = count
		playerCount = config.TeamCount * count
	}
	gameID, err := lb.CreateGame(context.Background(), mode, playerCount, teamSize, maxAgents, nextCount, holes, randomHoles, guideline, ghost, inviteOnly)
	a.mu.Lock()
	if err != nil {
		a.lobbyErr = "Couldn't create the game: " + err.Error()
	} else {
		a.lobbyErr = ""
	}
	a.mu.Unlock()
	if err != nil {
		log.Printf("create game: %v", err)
		a.invalidate()
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
	a.decidedAt, a.liveRank, a.liveOf, a.liveRankFinal = time.Time{}, 0, 0, false
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
	a.resetBoardFX()
	a.msgLog = nil
	a.resetMsgGroups()
	a.screen = screenGame
	a.mu.Unlock()
}

// resetBoardFX clears every client-local board overlay (CAS flashes, row
// strobes, garbage tracking, shake) for a fresh game screen. Caller holds a.mu.
func (a *App) resetBoardFX() {
	a.flash = map[[2]int]time.Time{}
	a.specFlash = map[int]map[[2]int]time.Time{}
	a.rowStrobes = map[int]rowStrobe{}
	a.specRowStrobes = map[int]map[int]rowStrobe{}
	a.specGarbageRows = map[int]int{}
	a.garbageRows = 0
	a.garbageSeen = false
	a.shakeStart = time.Time{}
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
//
// Gates on InitialMode, not Mode: a topped-out player's engine has already
// transitioned to spectating, but they still hold a seat (and a presence
// status) that leaving must settle — Mode would skip them and strand their
// presence on "in game" forever.
func (a *App) leaveCurrentGame() {
	lb := a.getLobby()
	eng := a.getEngine()
	if lb != nil && eng != nil && eng.InitialMode() != engine.ModeSpectator {
		gameID := eng.GameID()
		a.mu.Lock()
		wasReady := a.myReady
		metaStatus := config.GameStatus(a.gameStatus)
		a.mu.Unlock()
		if wasReady {
			if err := lb.SetReady(context.Background(), gameID, false); err != nil {
				log.Printf("clear ready on leave: %v", err)
			}
		}
		// Presence: while we still hold a seat in a live game we stay marked
		// in-game (and thus un-invitable); once the game is done or gone we are
		// back to a plain lobby player.
		g, ok := lb.Games()[gameID]
		if releaseSeatOnLeave(g, ok, metaStatus, lb.PlayerID()) {
			_ = lb.LeaveGame(context.Background(), gameID)
		}
	}
	a.returnToLobby()
}

// releaseSeatOnLeave decides whether leaving the game screen releases lobby
// presence (back to "in lobby") rather than keeping the in-game seat marker.
// The KV listing alone cannot answer it: its status never advances past
// created/starting (the game meta carries the live status), so a just-finished
// game still looks alive there until the archiver deletes the listing seconds
// later. The engine-reported meta status closes that gap — every client's meta
// consumer sees "finished" the moment the finish transition lands.
func releaseSeatOnLeave(g lobby.GameListing, found bool, metaStatus config.GameStatus, playerID string) bool {
	switch metaStatus {
	case config.GameStatusFinished, config.GameStatusArchived, config.GameStatusCancelled:
		return true
	}
	return !found || !gameAlive(g.Status) || !rosterHas(g, playerID)
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
	a.decidedAt, a.liveRank, a.liveOf, a.liveRankFinal = time.Time{}, 0, 0, false
	a.confirmLeave = false
	a.countdown = -1
	a.score = 0
	a.level = 0
	a.teamScores = [config.TeamCount]int{}
	a.teamLevels = [config.TeamCount]int{}
	a.rtt = 0
	a.gameStatus = ""
	a.resetBoardFX()
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
