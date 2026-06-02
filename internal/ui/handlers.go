package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/starfederation/datastar-go/datastar"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/game"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
)

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	if s.getLobby() == nil {
		fmt.Fprint(w, loginPageHTML())
		return
	}
	fmt.Fprint(w, lobbyPageHTML())
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		PlayerName string `json:"playerName"`
		ForceLogin bool   `json:"forceLogin"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		log.Printf("login read signals: %v", err)
		return
	}

	name := strings.TrimSpace(signals.PlayerName)
	if err := config.ValidatePlayerName(name); err != nil {
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(
			fmt.Sprintf(`<div id="login-error" class="error">%s</div>`, htmlEscape(err.Error())),
			datastar.WithSelectorID("login-error"),
		)
		return
	}

	// If the user did not already confirm, check whether another active
	// player in the lobby is using the same name and ask the user to
	// confirm before proceeding. Stale presence entries (LastSeen older
	// than 3× heartbeat) are ignored so a previous unclean exit doesn't
	// block re-entry.
	if !signals.ForceLogin {
		checkCtx, checkCancel := context.WithTimeout(r.Context(), 2*time.Second)
		inUse, err := lobby.IsNameInUse(checkCtx, s.kv, name)
		checkCancel()
		if err != nil {
			log.Printf("login: name-in-use check: %v", err)
			// Fall through — don't block login on a transient KV error.
		}
		if inUse {
			sse := datastar.NewSSE(w, r)
			_ = sse.PatchElements(
				renderNameCollisionPopup(name),
				datastar.WithSelectorID("name-collision"),
			)
			return
		}
	}

	if err := s.initLobby(name); err != nil {
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(
			fmt.Sprintf(`<div id="login-error" class="error">%s</div>`, htmlEscape(err.Error())),
			datastar.WithSelectorID("login-error"),
		)
		return
	}

	sse := datastar.NewSSE(w, r)
	_ = sse.ExecuteScript(`window.location.href = '/'`)
}

// renderNameCollisionPopup builds the modal asking the user to confirm
// joining the lobby under a name that's already taken by another active
// player. Confirming sets forceLogin=true and re-posts /login. Cancelling
// closes the popup so the user can pick a different name.
func renderNameCollisionPopup(name string) string {
	return fmt.Sprintf(`<div id="name-collision" class="modal-overlay">
  <div class="modal">
    <p>looks like there is already a user with this name in the lobby, are you sure you want to join with this name?</p>
    <p class="hint" style="margin-top:8px;color:#888">Name: <strong>%s</strong></p>
    <div style="margin-top:18px;display:flex;gap:10px;justify-content:center">
      <button class="btn" data-on:click="$forceLogin = true; @post('/login')">Yes, join</button>
      <button class="btn btn-secondary" data-on:click="$forceLogin = false; document.getElementById('name-collision').innerHTML = ''">Cancel</button>
    </div>
  </div>
</div>`, htmlEscape(name))
}

func (s *Server) handleGamePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, gamePageHTML())
}

func (s *Server) handleLobbyStream(w http.ResponseWriter, r *http.Request) {
	lb := s.getLobby()
	if lb == nil {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	sse := datastar.NewSSE(w, r)

	// Send initial state
	_ = sse.PatchElements(renderPlayerList(lb.Players()), datastar.WithSelectorID("player-list"))
	_ = sse.PatchElements(renderGameList(lb.Games()), datastar.WithSelectorID("game-list"))
	if archives := lb.Archives(); len(archives) > 0 {
		_ = sse.PatchElements(renderArchiveTable(archives), datastar.WithSelectorID("archive-table"))
	}

	ch, unsub := s.lobbyBroadcaster.Subscribe()
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case update, ok := <-ch:
			if !ok {
				return
			}
			switch update.Kind {
			case lobby.LobbyUpdatePlayers:
				_ = sse.PatchElements(renderPlayerList(lb.Players()), datastar.WithSelectorID("player-list"))
			case lobby.LobbyUpdateGames:
				_ = sse.PatchElements(renderGameList(lb.Games()), datastar.WithSelectorID("game-list"))
			case lobby.LobbyUpdateArchive:
				_ = sse.PatchElements(renderArchiveTable(lb.Archives()), datastar.WithSelectorID("archive-table"))
			case lobby.LobbyUpdateChat:
				if update.ChatMsg != nil {
					_ = sse.PatchElements(
						renderChatLineInner(*update.ChatMsg),
						datastar.WithSelectorID("chat-messages"),
						datastar.WithModeAppend(),
					)
				}
			}
		}
	}
}

func (s *Server) handleGameStream(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)

	// handleJoinGame sends the /game redirect before it finishes attaching
	// the engine (to avoid an in-flight fetch being aborted by a #game-list
	// re-render). Give the engine a short window to appear before bailing.
	e := s.getEngine()
	if e == nil {
		deadline := time.Now().Add(2 * time.Second)
		for e == nil && time.Now().Before(deadline) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(25 * time.Millisecond):
			}
			e = s.getEngine()
		}
	}
	if e == nil {
		_ = sse.PatchElements(`<p>No active game. <a href="/">Go to lobby</a></p>`, datastar.WithSelectorID("game-board"))
		return
	}

	// Hide controls and ready button for spectators, show player legend
	if e.Mode() == engine.ModeSpectator {
		_ = sse.PatchElements(`<div id="ready-area"></div>`, datastar.WithSelectorID("ready-area"))
		_ = sse.PatchElements(`<div class="controls"></div>`, datastar.WithSelectorID("controls"))
		_ = sse.PatchElements(`<div style="color:#ffcc00;margin:10px 0">Spectating</div>`, datastar.WithSelectorID("game-status"))
		_ = sse.PatchElements(renderPlayerLegend(s.getGamePlayers(), e.GameMode()), datastar.WithSelectorID("player-legend"))
	}

	// Show ready list if game hasn't started yet
	if e.Mode() == engine.ModePlayer {
		games := s.getLobby().Games()
		if g, ok := games[e.GameID()]; ok && g.Status != config.GameStatusInProgress {
			_ = sse.PatchElements(renderReadyList(g.Players), datastar.WithSelectorID("ready-list"))
		}
	}

	// Send initial board state.
	// Spectator + competitive: each player has their own playfield, so render a
	// multi-board layout (one full-size board per player) rather than the
	// spectator's own empty playfield.
	if e.Mode() == engine.ModeSpectator && e.GameMode() == config.ModeCompetitive {
		_ = sse.PatchElements(renderSpectatorCompetitiveContainer(e, s.getGamePlayers()), datastar.WithSelectorID("game-board"))
	} else {
		pf := e.Playfield()
		renderPlayerIdx := e.PlayerIdx()
		if e.Mode() == engine.ModeSpectator {
			renderPlayerIdx = -1 // spectators see per-player colored outlines
		}
		_ = sse.PatchElements(renderBoard(pf, renderPlayerIdx, e.VisibleRowStart()), datastar.WithSelectorID("game-board"))
	}
	_ = sse.PatchElements(renderScoreInner(e.Score()), datastar.WithSelectorID("score"))
	_ = sse.PatchElements(renderLevelInner(e.Level()), datastar.WithSelectorID("level"))

	// In competitive mode, show player status list (playing/eliminated)
	if e.GameMode() == config.ModeCompetitive {
		_ = sse.PatchElements(renderCompetitivePlayerStatus(s.getGamePlayers(), e), datastar.WithSelectorID("player-legend"))
	}
	// In cooperative mode, show per-player colored legend for both players and spectators
	if e.GameMode() == config.ModeCooperative {
		_ = sse.PatchElements(renderPlayerLegend(s.getGamePlayers(), e.GameMode()), datastar.WithSelectorID("player-legend"))
	}

	ch, unsub := s.gameBroadcaster.Subscribe()
	defer unsub()

	lobbyCh, lobbyUnsub := s.lobbyBroadcaster.Subscribe()
	defer lobbyUnsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case lobbyUpdate, ok := <-lobbyCh:
			if !ok {
				continue
			}
			// Re-render ready list AND refresh s.gamePlayers/legend when the
			// game listing changes (new player joined, ready toggled, etc).
			// During the ready phase we MUST track late-arriving players so
			// the legend on every screen reflects the current roster, not
			// the snapshot taken at handleJoinGame time.
			if lobbyUpdate.Kind == lobby.LobbyUpdateGames {
				eng := s.getEngine()
				if eng != nil && eng.Mode() == engine.ModePlayer {
					if lb := s.getLobby(); lb != nil {
						games := lb.Games()
						if g, ok := games[eng.GameID()]; ok && g.Status != config.GameStatusInProgress {
							_ = sse.PatchElements(renderReadyList(g.Players), datastar.WithSelectorID("ready-list"))
							s.mu.Lock()
							s.gamePlayers = g.Players
							s.mu.Unlock()
							if eng.GameMode() == config.ModeCompetitive {
								_ = sse.PatchElements(renderCompetitivePlayerStatus(s.getGamePlayers(), eng), datastar.WithSelectorID("player-legend"))
							} else {
								_ = sse.PatchElements(renderPlayerLegend(s.getGamePlayers(), eng.GameMode()), datastar.WithSelectorID("player-legend"))
							}
						}
					}
				}
			}
		case update, ok := <-ch:
			if !ok {
				return
			}
			eng := s.getEngine()
			if eng == nil {
				return
			}
			switch update.Kind {
			case engine.UpdatePlayfield, engine.UpdateLineClear:
				pf := eng.Playfield()
				playerIdx := eng.PlayerIdx()
				if eng.Mode() == engine.ModeSpectator {
					playerIdx = -1
				}
				for _, row := range update.ChangedRows {
					if row >= 0 && row < eng.PlayfieldHeight() {
						_ = sse.PatchElements(
							renderBoardRowInner(pf.Rows[row], row, playerIdx, nil),
							datastar.WithSelectorf("#row-%d", row),
						)
					}
				}
			case engine.UpdateScore:
				_ = sse.PatchElements(renderScoreInner(update.Score), datastar.WithSelectorID("score"))
			case engine.UpdateLevel:
				_ = sse.PatchElements(renderLevelInner(update.Level), datastar.WithSelectorID("level"))
			case engine.UpdateGameOver:
				result := ""
				if eng.GameMode() == config.ModeCompetitive {
					if update.Won {
						result = `<div style="color:#00ff88;font-size:1.5em;margin-top:10px">YOU WON!</div>`
					} else {
						result = `<div style="color:#ff4444;font-size:1.5em;margin-top:10px">YOU LOST</div>`
					}
				}
				_ = sse.PatchElements(
					fmt.Sprintf(`<div class="game-over-overlay"><div class="game-over-text">GAME OVER</div>%s<div style="margin-top:15px"><a href="/" class="btn">Back to Lobby</a></div></div>`, result),
					datastar.WithSelectorID("game-over"),
				)
				// Archive is triggered by engine.OnGameFinished callback (doesn't depend on SSE connection)
			case engine.UpdateOpponentField:
				oppID := update.OpponentID
				opfs := eng.OpponentPlayfields()
				if opf, ok := opfs[oppID]; ok {
					if eng.Mode() == engine.ModeSpectator && eng.GameMode() == config.ModeCompetitive {
						pIdx := -1
						for i, p := range s.getGamePlayers() {
							if p.PlayerID == oppID {
								pIdx = i
								break
							}
						}
						_ = sse.PatchElements(
							renderSpectatorPlayerBoard(opf, oppID, pIdx, eng.VisibleRowStart()),
							datastar.WithSelectorf("#sb-%s", oppID),
						)
					} else {
						_ = sse.PatchElements(
							renderOpponentBoard(opf, oppID, eng.VisibleRowStart()),
							datastar.WithSelectorf("#opp-%s", oppID),
						)
					}
				}
			case engine.UpdateCountdown:
				if eng.Mode() == engine.ModeSpectator {
					continue // spectators don't see the countdown
				}
				if update.Countdown > 0 {
					_ = sse.PatchElements(
						fmt.Sprintf(`<div id="ready-area"><div class="countdown">%d</div></div>`, update.Countdown),
						datastar.WithSelectorID("ready-area"),
					)
				} else {
					_ = sse.PatchElements(
						`<div id="ready-area"><div class="countdown" style="color:#00ff88">GO!</div></div>`,
						datastar.WithSelectorID("ready-area"),
					)
				}
			case engine.UpdateCASFlash:
				// Local feedback that the player's last move was rejected by
				// per-subject CAS — they must retry the input. Rather than
				// imperatively scripting DOM styles, re-render the affected rows
				// server-side with the cell-flash class on the touched cells; the
				// .cell-flash CSS keyframes play the ~600ms rainbow outline once.
				// A CAS rejection produces no state change, so no follow-up
				// playfield render clobbers the animation.
				pf := eng.Playfield()
				playerIdx := eng.PlayerIdx()
				if eng.Mode() == engine.ModeSpectator {
					playerIdx = -1
				}
				flashByRow := make(map[int]map[int]bool)
				for _, rc := range update.FlashCells {
					row, col := rc[0], rc[1]
					if row < 0 || row >= eng.PlayfieldHeight() {
						continue
					}
					if flashByRow[row] == nil {
						flashByRow[row] = make(map[int]bool)
					}
					flashByRow[row][col] = true
				}
				for row, cols := range flashByRow {
					_ = sse.PatchElements(
						renderBoardRowInner(pf.Rows[row], row, playerIdx, cols),
						datastar.WithSelectorf("#row-%d", row),
					)
				}
			case engine.UpdatePlayerEliminated:
				// Re-render competitive player status list
				if eng.GameMode() == config.ModeCompetitive {
					_ = sse.PatchElements(renderCompetitivePlayerStatus(s.getGamePlayers(), eng), datastar.WithSelectorID("player-legend"))
				}
			case engine.UpdateGameStatus:
				if update.GameStatus == "in_progress" {
					// Game started — hide the ready area
					_ = sse.PatchElements(`<div id="ready-area"></div>`, datastar.WithSelectorID("ready-area"))
					// Refresh gamePlayers from lobby listing before rendering
					if lb := s.getLobby(); lb != nil {
						if g, ok := lb.Games()[eng.GameID()]; ok {
							s.mu.Lock()
							s.gamePlayers = g.Players
							s.mu.Unlock()
						}
					}
					// Show player legend (competitive status list or coop color legend)
					if eng.GameMode() == config.ModeCompetitive {
						_ = sse.PatchElements(renderCompetitivePlayerStatus(s.getGamePlayers(), eng), datastar.WithSelectorID("player-legend"))
					} else if eng.GameMode() == config.ModeCooperative {
						_ = sse.PatchElements(renderPlayerLegend(s.getGamePlayers(), eng.GameMode()), datastar.WithSelectorID("player-legend"))
					}
				}
				_ = sse.PatchElements(
					htmlEscape(update.GameStatus),
					datastar.WithSelectorID("game-status"),
					datastar.WithModeInner(),
				)
			}
		}
	}
}

// All @post handlers must return SSE responses for Datastar to process them.

func (s *Server) handleLobbyChat(w http.ResponseWriter, r *http.Request) {
	// ReadSignals MUST be called before NewSSE
	var signals struct {
		ChatText string `json:"chatText"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		log.Printf("chat read signals: %v", err)
		return
	}
	text := strings.TrimSpace(signals.ChatText)
	if text == "" {
		return
	}

	sse := datastar.NewSSE(w, r)

	if err := s.getLobby().SendChat(r.Context(), text); err != nil {
		log.Printf("send chat: %v", err)
		return
	}
	// Clear the input signal
	_ = sse.MarshalAndPatchSignals(map[string]any{"chatText": ""})
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	// ReadSignals MUST be called before NewSSE
	var signals struct {
		GameMode    string `json:"gameMode"`
		PlayerCount int    `json:"playerCount"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		log.Printf("create game read signals: %v", err)
	}

	mode := config.ModeCooperative
	if signals.GameMode == "competitive" {
		mode = config.ModeCompetitive
	}
	playerCount := signals.PlayerCount
	if playerCount < 2 {
		playerCount = 2
	}
	// Cap at max supported by TotalRows (TotalRows - HeadroomRows - VisibleRows)
	maxPlayers := config.TotalRows - config.HeadroomRows - config.VisibleRows
	if playerCount > maxPlayers {
		playerCount = maxPlayers
	}

	sse := datastar.NewSSE(w, r)

	_, err := s.getLobby().CreateGame(r.Context(), mode, playerCount)
	if err != nil {
		log.Printf("create game: %v", err)
		_ = sse.PatchElements(
			fmt.Sprintf(`<div class="error">Error: %s</div>`, htmlEscape(err.Error())),
			datastar.WithSelectorID("game-list"),
			datastar.WithModePrepend(),
		)
		return
	}

	// Game created — the lobby SSE stream will update the game list via KV watcher.
	// Return a no-op response so Datastar completes the @post cleanly.
	_ = sse.MarshalAndPatchSignals(map[string]any{})
}

func (s *Server) handleJoinGame(w http.ResponseWriter, r *http.Request) {
	_ = datastar.ReadSignals(r, &struct{}{})
	gameID := r.PathValue("id")

	// Get game info BEFORE joining (in-memory map has it from KV watcher)
	games := s.getLobby().Games()
	g, ok := games[gameID]
	if !ok {
		log.Printf("join game: game %s not found", gameID)
		return
	}

	opponentID := ""
	for _, p := range g.Players {
		if p.PlayerID != s.getLobby().PlayerID() {
			opponentID = p.PlayerID
			break
		}
	}

	// Send the redirect script FIRST. Why: JoinGame writes to KV, which fires
	// our own lobby KV watcher and pushes a #game-list re-render on the
	// /lobby/stream SSE connection. When this join fills the roster
	// (len(Players) == PlayerCount), the rendered button flips Join → Spectate
	// and Datastar's per-element AbortController tears down the subtree that
	// owns this in-flight @post fetch — cancelling it before any subsequent
	// write can reach the browser. Flushing the navigation event up front
	// makes it immune to that abort.
	sse := datastar.NewSSE(w, r)
	_ = sse.ExecuteScript(`window.location.href = '/game'`)

	// Detached context: the browser will cancel r.Context() as soon as it
	// navigates away from /.
	ctx := context.Background()

	// Join the game to get our authoritative player index from the KV
	// roster. The engine's spawn column and cell-ownership depend on it.
	playerIdx, err := s.getLobby().JoinGame(ctx, gameID)
	if err != nil {
		log.Printf("join game: %v", err)
		return
	}

	e := engine.New(
		s.getLobby().GetJS(),
		gameID,
		s.getLobby().PlayerID(),
		opponentID,
		g.Mode,
		engine.ModePlayer,
		playerIdx,
	)
	// Set archive callback — runs when game finishes regardless of browser connection
	e.OnGameFinished = func() { s.archiveAndCleanup(e) }
	// Refetch the listing AFTER JoinGame so s.gamePlayers includes the
	// player who just joined. The `g` snapshot above was taken before
	// JoinGame appended us to the KV roster, so reusing it here would leave
	// the in-game player legend stale (showing only the pre-existing
	// players) until UpdateGameStatus=in_progress refreshes it.
	if g2, ok := s.getLobby().Games()[gameID]; ok {
		s.mu.Lock()
		s.gamePlayers = g2.Players
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		s.gamePlayers = g.Players
		s.mu.Unlock()
	}
	s.AttachEngine(e)

	if err := e.Start(); err != nil {
		log.Printf("engine start: %v", err)
	}
}

func (s *Server) handleGameReady(w http.ResponseWriter, r *http.Request) {
	_ = datastar.ReadSignals(r, &struct{}{})
	sse := datastar.NewSSE(w, r)

	e := s.getEngine()
	if e == nil {
		return
	}

	result, err := s.getLobby().ToggleReady(r.Context(), e.GameID())
	if err != nil {
		log.Printf("toggle ready: %v", err)
		return
	}

	// Use the state returned by ToggleReady (not the stale in-memory map)
	_ = sse.PatchElements(renderReadyList(result.Players), datastar.WithSelectorID("ready-list"))
	if result.MyReady {
		_ = sse.PatchElements(
			`<button id="ready-btn" class="btn btn-secondary" style="font-size:1.3em;padding:12px 30px;width:100%;min-width:200px" data-on:click="@post('/game/ready')">NOT READY</button>`,
			datastar.WithSelectorID("ready-btn"),
		)
	} else {
		_ = sse.PatchElements(
			`<button id="ready-btn" class="btn" style="font-size:1.3em;padding:12px 30px;animation:pulse 1.5s infinite;width:100%;min-width:200px" data-on:click="@post('/game/ready')">READY</button>`,
			datastar.WithSelectorID("ready-btn"),
		)
	}

	if result.AllReady {
		// Start countdown — broadcast to all players via game broadcaster
		go s.runCountdown(e.GameID())
	}
}

func (s *Server) runCountdown(gameID string) {
	ctx := context.Background()
	// Publish countdown to NATS so all players' engines pick it up
	for i := 5; i > 0; i-- {
		data, _ := json.Marshal(map[string]int{"seconds": i})
		_, _ = s.js.Publish(ctx, config.CountdownSubject(gameID), data)
		time.Sleep(1 * time.Second)
	}
	// GO!
	data, _ := json.Marshal(map[string]int{"seconds": 0})
	_, _ = s.js.Publish(ctx, config.CountdownSubject(gameID), data)

	// Transition to in_progress
	s.getLobby().StartGame(ctx, gameID)
}

func (s *Server) handleGameMove(w http.ResponseWriter, r *http.Request) {
	e := s.getEngine()
	if e == nil {
		http.Error(w, "no active game", http.StatusBadRequest)
		return
	}

	// Support both Datastar signals (JSON) and plain form data (keyboard fetch)
	var move string
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") || strings.Contains(contentType, "datastar") {
		var signals struct {
			Move string `json:"move"`
		}
		if err := json.NewDecoder(r.Body).Decode(&signals); err == nil {
			move = signals.Move
		}
	}
	if move == "" {
		// Try form value (from keyboard fetch)
		move = r.FormValue("move")
	}

	switch move {
	case "left":
		e.MoveLeft()
	case "right":
		e.MoveRight()
	case "down":
		e.MoveDown()
	case "rotate_cw":
		e.RotateCW()
	case "rotate_ccw":
		e.RotateCCW()
	case "hard_drop":
		e.HardDrop()
	}

	// For keyboard fetch calls, return 204. For Datastar @post, return empty SSE.
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements("")
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	_ = datastar.ReadSignals(r, &struct{}{})
	sse := datastar.NewSSE(w, r)

	lb := s.getLobby()
	if lb != nil {
		lb.Stop()
		s.mu.Lock()
		s.lobby = nil
		s.mu.Unlock()
	}

	_ = sse.ExecuteScript(`window.location.href = '/'`)
}

func (s *Server) handleSpectateGame(w http.ResponseWriter, r *http.Request) {
	_ = datastar.ReadSignals(r, &struct{}{})
	sse := datastar.NewSSE(w, r)
	gameID := r.PathValue("id")

	games := s.getLobby().Games()
	g, ok := games[gameID]
	if !ok {
		return
	}

	e := engine.New(
		s.getLobby().GetJS(),
		gameID,
		s.getLobby().PlayerID(),
		"",
		g.Mode,
		engine.ModeSpectator,
		0, // spectators don't spawn pieces; idx is unused
	)
	s.mu.Lock()
	s.gamePlayers = g.Players
	s.mu.Unlock()
	s.AttachEngine(e)

	if err := e.Start(); err != nil {
		log.Printf("spectate engine start: %v", err)
	}

	_ = sse.ExecuteScript(`window.location.href = '/game'`)
}

func (s *Server) archiveAndCleanup(eng *engine.Engine) {
	ctx := context.Background()

	// Use CAS on game meta to transition finished → archived.
	// Only the first caller succeeds; others see a CAS failure and skip.
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, s.js, eng.GameID())
	if err != nil {
		return // stream might already be deleted
	}
	if meta.Status != config.GameStatusFinished {
		return // already archived by another instance
	}
	meta.Status = config.GameStatusArchived
	archiveData, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, s.js, eng.GameID(), archiveData, metaSeq); err != nil {
		return // CAS failed — another instance won the race
	}

	// Collect all players' results from EventGameOver events on the game stream.
	// eventSenders tracks who published their own EventGameOver — i.e., who
	// topped out. In competitive mode, the winner is the player who did NOT
	// top out. On a draw (all topped out), there is no winner.
	playerResults := make(map[string]config.PlayerResult)
	eventSenders := make(map[string]bool)
	// Add our own data first
	playerResults[eng.PlayerID()] = config.PlayerResult{
		PlayerID:   eng.PlayerID(),
		Score:      eng.Score(),
		PieceCount: eng.PieceIdx(),
	}
	// Read EventGameOver events from others
	evtCh, evtCancel, err := natspkg.NewOrderedConsumer(ctx, s.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(eng.GameID()),
		FilterSubject: config.EventsSubject(eng.GameID()),
	})
	if err == nil {
		done := false
		for !done {
			select {
			case msg, ok := <-evtCh:
				if !ok {
					done = true
					break
				}
				var ev engine.GameEvent
				if json.Unmarshal(msg.Data(), &ev) == nil && ev.Kind == engine.EventGameOver {
					eventSenders[ev.PlayerID] = true
					if _, exists := playerResults[ev.PlayerID]; !exists {
						playerResults[ev.PlayerID] = config.PlayerResult{
							PlayerID:   ev.PlayerID,
							Score:      ev.Score,
							PieceCount: ev.PieceCount,
						}
					}
				}
			default:
				done = true
			}
		}
		evtCancel()
	}
	// Also add players from the game listing who might not have topped out
	gamePlayers := s.getGamePlayers()
	for _, p := range gamePlayers {
		if _, exists := playerResults[p.PlayerID]; !exists {
			playerResults[p.PlayerID] = config.PlayerResult{PlayerID: p.PlayerID}
		}
	}
	// Determine winners in competitive: any player who did NOT send an
	// EventGameOver survived to the end and wins. On a simultaneous top-out
	// draw, every player sent an event, so there is no winner.
	if meta.Mode == config.ModeCompetitive {
		for id, pr := range playerResults {
			if !eventSenders[id] {
				pr.Winner = true
				playerResults[id] = pr
			}
		}
	}

	var results []config.PlayerResult
	for _, pr := range playerResults {
		results = append(results, pr)
	}

	record := config.ArchiveRecord{
		GameID:      eng.GameID(),
		Mode:        meta.Mode,
		PlayerCount: meta.PlayerCount,
		StartedAt:   meta.StartedAt,
		FinishedAt:  meta.FinishedAt,
		Players:     results,
	}
	if meta.Mode == config.ModeCooperative {
		record.TotalScore = eng.Score()
	}

	data, _ := json.Marshal(record)
	if _, err := s.js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		log.Printf("archive: publish: %v", err)
		return
	}

	// Delete the game stream and KV entry
	_ = natspkg.DeleteGameStream(ctx, s.js, eng.GameID())
	_ = s.kv.Delete(ctx, config.LobbyGameKey(eng.GameID()))

	// Leave game in lobby
	lb := s.getLobby()
	if lb != nil {
		_ = lb.LeaveGame(ctx, eng.GameID())
	}
	s.DetachEngine()
}

// --- HTML Rendering ---

func loginPageHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Jetricks - Login</title>
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js"></script>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: 'Courier New', monospace; background: #0a0a0a; color: #e0e0e0; min-height: 100vh; display: flex; align-items: center; justify-content: center; }
.login-box { text-align: center; }
h1 { color: #00ff88; font-size: 3em; margin-bottom: 30px; }
.login-form { display: flex; flex-direction: column; align-items: center; gap: 15px; }
.login-form input { background: #222; border: 1px solid #444; color: #e0e0e0; padding: 12px 20px; font-family: inherit; font-size: 1.2em; border-radius: 4px; width: 300px; text-align: center; }
.login-form input:focus { outline: none; border-color: #00ff88; }
.btn { display: inline-block; background: #00ff88; color: #0a0a0a; border: none; padding: 12px 30px; cursor: pointer; font-family: inherit; font-weight: bold; font-size: 1.1em; border-radius: 4px; }
.btn:hover { background: #00cc66; }
.btn-secondary { background: #444; color: #e0e0e0; }
.btn-secondary:hover { background: #555; }
.error { color: #ff4444; padding: 10px; background: #1a0000; border: 1px solid #440000; border-radius: 4px; margin-top: 10px; }
.hint { color: #666; font-size: 0.85em; margin-top: 5px; }
.modal-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.75); display: flex; align-items: center; justify-content: center; z-index: 100; }
.modal { background: #1a1a1a; border: 1px solid #00ff88; border-radius: 8px; padding: 24px; max-width: 420px; text-align: center; box-shadow: 0 0 30px rgba(0,255,136,0.25); }
</style>
</head>
<body>
<div class="login-box" data-signals="{playerName: '', forceLogin: false}">
  <h1>JETRICKS</h1>
  <div class="login-form">
    <input type="text" data-bind="playerName" placeholder="Enter your player name" autocomplete="off"
           data-on:keydown="evt.key === 'Enter' && (($forceLogin = false), @post('/login'))">
    <button class="btn" data-on:click="$forceLogin = false; @post('/login')">Play</button>
    <div class="hint">No spaces, dots, or wildcards allowed</div>
    <div id="login-error"></div>
  </div>
  <div id="name-collision"></div>
</div>
</body>
</html>`
}

func lobbyPageHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Jetricks</title>
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js"></script>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: 'Courier New', monospace; background: #0a0a0a; color: #e0e0e0; min-height: 100vh; }
.container { display: flex; height: 100vh; }
.sidebar { width: 250px; background: #1a1a1a; padding: 20px; border-right: 1px solid #333; overflow-y: auto; padding-bottom: 220px; }
.main { flex: 1; padding: 20px; overflow-y: auto; }
.chat-area { position: fixed; bottom: 0; left: 0; width: 250px; background: #1a1a1a; padding: 10px; border-top: 1px solid #333; }
h1 { color: #00ff88; font-size: 2em; margin-bottom: 20px; }
h2 { color: #00cc66; font-size: 1.2em; margin-bottom: 10px; }
.player-item { padding: 5px 0; border-bottom: 1px solid #222; }
.player-status { color: #888; font-size: 0.8em; }
.game-card { background: #1a1a1a; border: 1px solid #333; border-radius: 8px; padding: 15px; margin-bottom: 10px; }
.game-card:hover { border-color: #00ff88; }
.btn { display: inline-block; background: #00ff88; color: #0a0a0a; border: none; padding: 10px 20px; cursor: pointer; font-family: inherit; font-weight: bold; border-radius: 4px; text-decoration: none; }
.btn:hover { background: #00cc66; }
.btn-secondary { background: #333; color: #e0e0e0; }
.btn-secondary:hover { background: #444; }
.game-list { display: grid; gap: 10px; }
.chat-messages { height: 150px; overflow-y: auto; margin-bottom: 10px; font-size: 0.85em; }
.chat-msg { padding: 2px 0; }
.chat-msg .name { color: #00ff88; }
.chat-input { display: flex; gap: 5px; }
.chat-input input { flex: 1; background: #222; border: 1px solid #444; color: #e0e0e0; padding: 5px; font-family: inherit; }
.create-form { margin-bottom: 20px; display: flex; gap: 10px; align-items: center; }
.create-form select { background: #222; border: 1px solid #444; color: #e0e0e0; padding: 8px; font-family: inherit; border-radius: 4px; }
.error { color: #ff4444; padding: 10px; background: #1a0000; border: 1px solid #440000; border-radius: 4px; margin-bottom: 10px; }
.archive { width: 100%; border-collapse: collapse; font-size: 0.9em; }
.archive th { text-align: left; color: #00cc66; padding: 5px 10px; border-bottom: 1px solid #333; }
.archive td { padding: 5px 10px; border-bottom: 1px solid #222; }
</style>
</head>
<body>
<div class="container" data-signals="{gameMode: 'cooperative', playerCount: 2, chatText: ''}" data-init="@get('/lobby/stream')">
  <div class="sidebar">
    <h2>Players Online</h2>
    <div id="player-list"><div style="color:#666">Loading...</div></div>
    <div class="chat-area">
      <h2>Chat</h2>
      <div id="chat-messages" class="chat-messages"></div>
      <div class="chat-input">
        <input type="text" data-bind="chatText" placeholder="Type a message..." autocomplete="off"
               data-on:keydown="evt.key === 'Enter' && @post('/lobby/chat')">
        <button class="btn" style="padding:5px 10px" data-on:click="@post('/lobby/chat')">Send</button>
      </div>
    </div>
  </div>
  <div class="main">
    <div style="display:flex;justify-content:space-between;align-items:center">
      <h1>JETRICKS</h1>
      <button class="btn btn-secondary" style="padding:8px 16px;font-size:0.9em" data-on:click="@post('/lobby/quit')">Quit</button>
    </div>
    <div class="create-form">
      <select data-bind="gameMode">
        <option value="cooperative">Cooperative</option>
        <option value="competitive">Competitive</option>
      </select>
      <label style="color:#888">Players: <input type="number" data-bind="playerCount" min="2" value="2" style="width:50px;background:#222;border:1px solid #444;color:#e0e0e0;padding:5px;font-family:inherit;border-radius:4px"></label>
      <button class="btn" data-on:click="@post('/lobby/game/create')">Create Game</button>
    </div>
    <h2>Games</h2>
    <div id="game-list" class="game-list"><div style="color:#666">Loading...</div></div>
    <h2 style="margin-top:20px">Game History</h2>
    <div id="archive-table"><div style="color:#666">No completed games yet</div></div>
  </div>
</div>
</body>
</html>`
}

func gamePageHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Jetricks - Game</title>
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js"></script>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: 'Courier New', monospace; background: #0a0a0a; color: #e0e0e0; display: flex; justify-content: center; padding-top: 20px; }
.game-container { display: flex; gap: 30px; }
.board-wrapper { position: relative; }
.board { border: 2px solid #333; background: #111; border-collapse: collapse; }
.board tr { height: 24px; }
.board td { width: 24px; height: 24px; border: 1px solid #1a1a1a; }
/* Per-square fill and outline colors are computed and emitted server-side as
   inline styles (see cellStyle in handlers.go); the stylesheet only owns
   structure here. */
.opponents { display: flex; flex-direction: column; gap: 10px; }
.opponent-board { }
.opp-label { color: #888; font-size: 0.8em; text-align: center; margin-bottom: 2px; }
.opp-board { border: 1px solid #333; border-collapse: collapse; }
.opp-board tr { height: 12px; }
.opp-board td { width: 12px; height: 12px; border: 1px solid #1a1a1a; padding: 0; }
/* CAS-rejection feedback: a one-shot ~600ms rainbow outline pulse applied to the
   touched cells when the server re-renders the affected rows. */
.cell-flash { position: relative; z-index: 3; animation: cas-flash 0.6s steps(1) 1; }
@keyframes cas-flash {
  0%   { outline: 3px solid #ff0000; outline-offset: -1px; box-shadow: 0 0 8px #ff0000; }
  16%  { outline: 3px solid #ff7f00; outline-offset: -1px; box-shadow: 0 0 8px #ff7f00; }
  33%  { outline: 3px solid #ffff00; outline-offset: -1px; box-shadow: 0 0 8px #ffff00; }
  50%  { outline: 3px solid #00cc00; outline-offset: -1px; box-shadow: 0 0 8px #00cc00; }
  66%  { outline: 3px solid #0099ff; outline-offset: -1px; box-shadow: 0 0 8px #0099ff; }
  83%  { outline: 3px solid #4b0082; outline-offset: -1px; box-shadow: 0 0 8px #4b0082; }
  100% { outline: 3px solid #9400d3; outline-offset: -1px; box-shadow: 0 0 8px #9400d3; }
}
.ready-player { padding: 4px 0; display: flex; align-items: center; gap: 8px; }
.ready-yes { color: #00ff88; }
.ready-no { color: #ff4444; }
.countdown { font-size: 3em; color: #ffcc00; text-align: center; margin: 20px 0; font-weight: bold; }
.player-legend { margin-bottom: 15px; }
.player-legend-item { display: flex; align-items: center; gap: 8px; padding: 3px 0; }
.player-legend-swatch { width: 14px; height: 14px; border-radius: 2px; }
.hud { min-width: 150px; }
.hud h2 { color: #00ff88; margin-bottom: 10px; }
.hud-item { margin-bottom: 15px; }
.hud-label { color: #888; font-size: 0.9em; }
.hud-value { color: #00ff88; font-size: 1.5em; font-weight: bold; }
.controls { margin-top: 20px; text-align: center; }
.controls button { background: #333; color: #e0e0e0; border: 1px solid #555; padding: 10px 15px; margin: 3px; cursor: pointer; font-family: inherit; border-radius: 4px; font-size: 1.2em; }
.controls button:hover { background: #555; }
.controls button:active { background: #00ff88; color: #0a0a0a; }
.game-over-overlay { position: absolute; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.8); display: flex; flex-direction: column; align-items: center; justify-content: center; z-index: 10; }
.game-over-text { color: #ff0000; font-size: 2em; font-weight: bold; }
#game-status { color: #ffcc00; margin-top: 10px; }
@keyframes pulse { 0%,100% { box-shadow: 0 0 10px #00ff88; } 50% { box-shadow: 0 0 25px #00ff88, 0 0 50px #00ff8844; } }
</style>
</head>
<body>
<div class="game-container" data-init="@get('/game/stream')">
  <div class="hud">
    <h2>JETRICKS</h2>
    <div id="player-legend"></div>
    <div class="hud-item">
      <div class="hud-label">SCORE</div>
      <div id="score" class="hud-value">0</div>
    </div>
    <div class="hud-item">
      <div class="hud-label">LEVEL</div>
      <div id="level" class="hud-value">0</div>
    </div>
    <div id="game-status"></div>
    <div id="ready-area" style="margin:20px 0">
      <div class="hud-label">WAITING FOR PLAYERS</div>
      <div id="ready-list" style="margin:10px 0"></div>
      <button id="ready-btn" class="btn" style="font-size:1.3em;padding:12px 30px;animation:pulse 1.5s infinite;width:100%;min-width:200px" data-on:click="@post('/game/ready')">READY</button>
    </div>
    <div id="controls" class="controls">
      <div>
        <button onclick="sendMove('rotate_ccw')">↶</button>
        <button onclick="sendMove('rotate_cw')">↷</button>
      </div>
      <div>
        <button onclick="sendMove('left')">←</button>
        <button onclick="sendMove('down')">↓</button>
        <button onclick="sendMove('right')">→</button>
      </div>
      <div>
        <button onclick="sendMove('hard_drop')">⏬ Drop</button>
      </div>
    </div>
    <div style="margin-top:15px">
      <a href="/" style="color:#888">← Back to Lobby</a>
    </div>
  </div>
  <div class="board-wrapper">
    <div id="game-board"><div style="color:#666;padding:20px">Loading board...</div></div>
    <div id="game-over"></div>
  </div>
  <div id="opponents" class="opponents"></div>
</div>
<script>
function sendMove(move) {
  fetch('/game/move', {
    method: 'POST',
    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
    body: 'move=' + move
  });
}
document.addEventListener('keydown', function(e) {
  const moves = {
    'ArrowLeft': 'left', 'ArrowRight': 'right', 'ArrowDown': 'down',
    'ArrowUp': 'rotate_cw', 'KeyZ': 'rotate_ccw', 'KeyX': 'rotate_cw',
    'Space': 'hard_drop'
  };
  const move = moves[e.code];
  if (move) {
    e.preventDefault();
    sendMove(move);
  }
});
</script>
</body>
</html>`
}

// --- Render functions ---

func renderPlayerList(players map[string]lobby.PlayerPresence) string {
	var sb strings.Builder
	sb.WriteString(`<div id="player-list">`)
	if len(players) == 0 {
		sb.WriteString(`<div style="color:#666">No players online</div>`)
	}
	for _, p := range players {
		status := "In Lobby"
		switch p.Status {
		case lobby.StatusInGame:
			status = "In Game"
		case lobby.StatusSpectating:
			status = "Spectating"
		}
		fmt.Fprintf(&sb, `<div class="player-item">%s <span class="player-status">(%s)</span></div>`,
			htmlEscape(p.PlayerID), status)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderGameList(games map[string]lobby.GameListing) string {
	var sb strings.Builder
	sb.WriteString(`<div id="game-list">`)

	sorted := make([]lobby.GameListing, 0, len(games))
	for _, g := range games {
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	for _, g := range sorted {
		mode := "Co-op"
		if g.Mode == config.ModeCompetitive {
			mode = "Competitive"
		}
		playerNames := make([]string, len(g.Players))
		for i, p := range g.Players {
			playerNames[i] = htmlEscape(p.PlayerID)
			if p.Ready {
				playerNames[i] += " ✓"
			}
		}
		idShort := g.GameID
		if len(idShort) > 8 {
			idShort = idShort[:8]
		}
		fmt.Fprintf(&sb, `<div class="game-card"><div><strong>%s</strong> — %s</div><div>Players: %d/%d — %s</div><div>Status: %s</div>`,
			htmlEscape(idShort), mode,
			len(g.Players), g.PlayerCount,
			strings.Join(playerNames, ", "),
			string(g.Status))

		if (g.Status == config.GameStatusCreated || g.Status == config.GameStatusStarting) && len(g.Players) < g.PlayerCount {
			fmt.Fprintf(&sb, `<div style="margin-top:8px"><button class="btn" data-on:click="@post('/lobby/game/%s/join')">Join</button></div>`,
				g.GameID)
		}
		if g.Status == config.GameStatusInProgress || ((g.Status == config.GameStatusCreated || g.Status == config.GameStatusStarting) && len(g.Players) >= g.PlayerCount) {
			fmt.Fprintf(&sb, `<div style="margin-top:8px"><button class="btn btn-secondary" data-on:click="@post('/lobby/game/%s/spectate')">Spectate</button></div>`,
				g.GameID)
		}
		sb.WriteString(`</div>`)
	}
	if len(games) == 0 {
		sb.WriteString(`<div style="color:#666">No games yet. Create one!</div>`)
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderChatLineInner(msg lobby.ChatMessage) string {
	// Use a unique ID based on timestamp to prevent duplicate rendering
	msgID := fmt.Sprintf("chat-%d", msg.Timestamp.UnixNano())
	return fmt.Sprintf(`<div id="%s" class="chat-msg"><span class="name">%s:</span> %s</div>`,
		msgID, htmlEscape(msg.PlayerID), htmlEscape(msg.Text))
}

func renderBoard(pf *game.Playfield, playerIdx int, visibleRowStart int) string {
	var sb strings.Builder
	sb.WriteString(`<table id="game-board" class="board">`)
	for r := visibleRowStart; r < pf.Height; r++ {
		sb.WriteString(renderBoardRowInner(pf.Rows[r], r, playerIdx, nil))
	}
	sb.WriteString(`</table>`)
	return sb.String()
}

// renderBoardRowInner renders a single row. playerIdx is the local player's index
// (-1 for spectators, in which case all active pieces get per-player colored
// outlines). flashCols, when non-nil, marks columns that should carry the
// cell-flash CSS animation class (used for the CAS-rejection feedback).
func renderBoardRowInner(row game.Row, rowIndex int, playerIdx int, flashCols map[int]bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<tr id="row-%d">`, rowIndex)
	for col, c := range row.Cells {
		if flashCols[col] {
			fmt.Fprintf(&sb, `<td class="cell-flash" style="%s"></td>`, cellStyle(c, playerIdx, true))
		} else {
			fmt.Fprintf(&sb, `<td style="%s"></td>`, cellStyle(c, playerIdx, true))
		}
	}
	sb.WriteString(`</tr>`)
	return sb.String()
}

// renderSpectatorCompetitiveContainer renders a flex row of full-size player
// boards for a spectator watching a competitive game. Players whose playfields
// haven't yet been loaded by the engine's opponent consumer get a loading
// placeholder that will be replaced once UpdateOpponentField fires.
func renderSpectatorCompetitiveContainer(e *engine.Engine, players []lobby.PlayerSummary) string {
	pfs := e.OpponentPlayfields()
	var sb strings.Builder
	sb.WriteString(`<div id="game-board" style="display:flex;gap:20px;align-items:flex-start">`)
	for i, p := range players {
		if pf, ok := pfs[p.PlayerID]; ok {
			sb.WriteString(renderSpectatorPlayerBoard(pf, p.PlayerID, i, e.VisibleRowStart()))
		} else {
			fmt.Fprintf(&sb, `<div id="sb-%s" class="spectator-board"><div class="opp-label" style="color:%s">%s</div><div style="color:#666;padding:20px">Loading...</div></div>`,
				htmlEscape(p.PlayerID), playerColor(i), htmlEscape(p.PlayerID))
		}
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// renderSpectatorPlayerBoard renders one player's full-size playfield in a
// labeled container. Row IDs are omitted — we always full-replace the container
// on updates rather than patching individual rows (row IDs would also collide
// across multiple boards in the same DOM).
func renderSpectatorPlayerBoard(pf *game.Playfield, playerID string, playerIdx int, visibleRowStart int) string {
	var sb strings.Builder
	color := "#00ff88"
	if playerIdx >= 0 {
		color = playerColor(playerIdx)
	}
	fmt.Fprintf(&sb, `<div id="sb-%s" class="spectator-board"><div class="opp-label" style="color:%s">%s</div><table class="board">`,
		htmlEscape(playerID), color, htmlEscape(playerID))
	for r := visibleRowStart; r < pf.Height; r++ {
		sb.WriteString(`<tr>`)
		for _, c := range pf.Rows[r].Cells {
			// Spectator view (-1): every active piece keeps a per-player outline.
			fmt.Fprintf(&sb, `<td style="%s"></td>`, cellStyle(c, -1, true))
		}
		sb.WriteString(`</tr>`)
	}
	sb.WriteString(`</table></div>`)
	return sb.String()
}

func renderOpponentBoard(pf *game.Playfield, oppID string, visibleRowStart int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<div id="opp-%s" class="opponent-board"><div class="opp-label">%s</div><table class="board opp-board">`,
		htmlEscape(oppID), htmlEscape(oppID))
	for r := visibleRowStart; r < pf.Height; r++ {
		fmt.Fprintf(&sb, `<tr>`)
		for _, c := range pf.Rows[r].Cells {
			// Compact board: fill only, no ownership outline (just the grid line).
			fmt.Fprintf(&sb, `<td style="%s"></td>`, cellStyle(c, -1, false))
		}
		sb.WriteString(`</tr>`)
	}
	sb.WriteString(`</table></div>`)
	return sb.String()
}

func renderArchiveTable(archives []config.ArchiveRecord) string {
	// Split into coop and competitive
	var coop, comp []config.ArchiveRecord
	for _, a := range archives {
		if a.Mode == config.ModeCooperative {
			coop = append(coop, a)
		} else {
			comp = append(comp, a)
		}
	}

	// Sort: highest score first, then oldest timestamp first for ties
	sort.Slice(coop, func(i, j int) bool {
		if coop[i].TotalScore != coop[j].TotalScore {
			return coop[i].TotalScore > coop[j].TotalScore
		}
		return coop[i].StartedAt.Before(coop[j].StartedAt)
	})
	sort.Slice(comp, func(i, j int) bool {
		si, sj := compWinnerScore(comp[i]), compWinnerScore(comp[j])
		if si != sj {
			return si > sj
		}
		return comp[i].StartedAt.Before(comp[j].StartedAt)
	})

	var sb strings.Builder
	sb.WriteString(`<div id="archive-table" style="display:flex;gap:20px">`)

	// Cooperative column
	sb.WriteString(`<div style="flex:1"><h3 style="color:#00cc66;margin-bottom:8px">Cooperative</h3>`)
	sb.WriteString(`<table class="archive"><tr><th>Played</th><th>Players</th><th>Duration</th><th>Score</th></tr>`)
	for _, a := range coop {
		var names []string
		for _, p := range a.Players {
			names = append(names, htmlEscape(p.PlayerID))
		}
		dur := a.FinishedAt.Sub(a.StartedAt).Round(time.Second)
		fmt.Fprintf(&sb, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td></tr>`,
			formatPlayedAt(a.StartedAt), strings.Join(names, ", "), dur, a.TotalScore)
	}
	if len(coop) == 0 {
		sb.WriteString(`<tr><td colspan="4" style="color:#666">No games yet</td></tr>`)
	}
	sb.WriteString(`</table></div>`)

	// Competitive column
	sb.WriteString(`<div style="flex:1"><h3 style="color:#00cc66;margin-bottom:8px">Competitive</h3>`)
	sb.WriteString(`<table class="archive"><tr><th>Played</th><th>Players</th><th>Duration</th><th>Lines</th><th>Winner</th></tr>`)
	for _, a := range comp {
		var names []string
		var linesParts []string
		winner := ""
		for _, p := range a.Players {
			names = append(names, htmlEscape(p.PlayerID))
			linesParts = append(linesParts, fmt.Sprintf("%s:%d", htmlEscape(p.PlayerID), p.Score))
			if p.Winner {
				winner = htmlEscape(p.PlayerID)
			}
		}
		dur := a.FinishedAt.Sub(a.StartedAt).Round(time.Second)
		fmt.Fprintf(&sb, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><strong>%s</strong></td></tr>`,
			formatPlayedAt(a.StartedAt), strings.Join(names, ", "), dur, strings.Join(linesParts, " / "), winner)
	}
	if len(comp) == 0 {
		sb.WriteString(`<tr><td colspan="5" style="color:#666">No games yet</td></tr>`)
	}
	sb.WriteString(`</table></div>`)

	sb.WriteString(`</div>`)
	return sb.String()
}

// formatPlayedAt renders a game's start time in the server's local timezone.
// Same-day games show just the time; older games show date + time.
func formatPlayedAt(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	local := t.Local()
	now := time.Now()
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("15:04")
	}
	if local.Year() == now.Year() {
		return local.Format("Jan 2 15:04")
	}
	return local.Format("2006-01-02 15:04")
}

func compWinnerScore(a config.ArchiveRecord) int {
	best := 0
	for _, p := range a.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

func renderReadyList(players []lobby.PlayerSummary) string {
	var sb strings.Builder
	sb.WriteString(`<div id="ready-list">`)
	for _, p := range players {
		cls := "ready-no"
		icon := "✗"
		if p.Ready {
			cls = "ready-yes"
			icon = "✓"
		}
		fmt.Fprintf(&sb, `<div class="ready-player"><span class="%s">%s</span> %s</div>`,
			cls, icon, htmlEscape(p.PlayerID))
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderCompetitivePlayerStatus(players []lobby.PlayerSummary, eng *engine.Engine) string {
	var sb strings.Builder
	sb.WriteString(`<div id="player-legend" class="player-legend">`)
	sb.WriteString(renderGameModeLabel(config.ModeCompetitive))
	sb.WriteString(`<div class="hud-label">PLAYERS</div>`)
	for i, p := range players {
		color := playerColor(i)
		if eng.IsEliminated(p.PlayerID) {
			fmt.Fprintf(&sb, `<div class="player-legend-item"><div class="player-legend-swatch" style="background:%s;opacity:0.3"></div><span style="color:#666;text-decoration:line-through">%s</span></div>`,
				color, htmlEscape(p.PlayerID))
		} else {
			fmt.Fprintf(&sb, `<div class="player-legend-item"><div class="player-legend-swatch" style="background:%s"></div><span>%s</span></div>`,
				color, htmlEscape(p.PlayerID))
		}
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderGameModeLabel(mode config.GameMode) string {
	label := "Cooperative"
	if mode == config.ModeCompetitive {
		label = "Competitive"
	}
	return fmt.Sprintf(`<div class="game-mode-label" style="color:#ffcc00;font-size:0.95em;margin-bottom:10px;text-transform:uppercase;letter-spacing:1px">%s</div>`, label)
}

var playerColors = []string{
	"#00ffff", // P0 cyan
	"#ff00ff", // P1 magenta
	"#ffff00", // P2 yellow
	"#ff8800", // P3 orange
	"#00ff00", // P4 green
	"#ff4444", // P5 red
	"#8888ff", // P6 light blue
	"#ff88ff", // P7 pink
	"#88ffff", // P8 light cyan
	"#ffaa44", // P9 amber
}

func playerColor(idx int) string {
	if idx < len(playerColors) {
		return playerColors[idx]
	}
	return playerColors[idx%len(playerColors)]
}

func renderPlayerLegend(players []lobby.PlayerSummary, mode config.GameMode) string {
	var sb strings.Builder
	sb.WriteString(`<div id="player-legend" class="player-legend">`)
	sb.WriteString(renderGameModeLabel(mode))
	sb.WriteString(`<div class="hud-label">PLAYERS</div>`)
	for i, p := range players {
		fmt.Fprintf(&sb, `<div class="player-legend-item"><div class="player-legend-swatch" style="background:%s"></div><span>%s</span></div>`,
			playerColor(i), htmlEscape(p.PlayerID))
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

func renderScoreInner(score int) string {
	return fmt.Sprintf(`<div id="score" class="hud-value">%d</div>`, score)
}

func renderLevelInner(level int) string {
	return fmt.Sprintf(`<div id="level" class="hud-value">%d</div>`, level)
}

// Board background and grid-line colors. Every square's outline falls back to the
// grid line so that, literally, every square has an outline.
const (
	boardBg    = "#111111"
	gridLine   = "#1a1a1a"
	ownOutline = "#ffffff"
)

// pieceColors maps each tetromino type to its base fill color. Index matches the
// game.PieceType iota (I, O, T, S, Z, J, L).
var pieceColors = [...]string{
	game.PieceI: "#00f0f0", // cyan
	game.PieceO: "#f0f000", // yellow
	game.PieceT: "#a000f0", // purple
	game.PieceS: "#00f000", // green
	game.PieceZ: "#f00000", // red
	game.PieceJ: "#0000f0", // blue
	game.PieceL: "#f0a000", // orange
}

func pieceColor(pt game.PieceType) string {
	if int(pt) >= 0 && int(pt) < len(pieceColors) {
		return pieceColors[pt]
	}
	return boardBg
}

// blend composites fg over bg at the given alpha (0..1) and returns the resulting
// "#rrggbb" hex. This reproduces the old opacity-over-dark-board look as a single
// concrete color, so the server can emit each square's true fill.
func blend(fg, bg string, alpha float64) string {
	fr, fg2, fb := hexToRGB(fg)
	br, bg2, bb := hexToRGB(bg)
	r := int(float64(fr)*alpha + float64(br)*(1-alpha) + 0.5)
	g := int(float64(fg2)*alpha + float64(bg2)*(1-alpha) + 0.5)
	b := int(float64(fb)*alpha + float64(bb)*(1-alpha) + 0.5)
	return fmt.Sprintf("#%02x%02x%02x", clamp8(r), clamp8(g), clamp8(b))
}

func hexToRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return 0, 0, 0
	}
	var r, g, b int
	fmt.Sscanf(h, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// cellStyle computes the inline CSS (background + outline) for a single square,
// the single source of truth for cell appearance. localPlayerIdx is the viewer's
// player index (-1 for spectators: every active piece gets a per-player outline).
// When showOutline is false (compact opponent boards) ownership outlines are
// suppressed in favor of the plain grid line.
func cellStyle(c game.Cell, localPlayerIdx int, showOutline bool) string {
	fill := boardBg
	outline := gridLine
	outlineW := 1

	switch {
	case c.Active:
		fill = blend(pieceColor(c.PieceType), boardBg, 0.9)
		switch {
		case localPlayerIdx < 0:
			// Spectator: every active piece gets a per-player outline.
			outline, outlineW = playerColor(c.PlayerIdx), 2
		case c.PlayerIdx == localPlayerIdx:
			// Own piece: white outline.
			outline, outlineW = ownOutline, 2
			// Other player's active piece in a player's own view: no
			// ownership outline (grid line) — preserves the seamless board.
		}
	case c.Adversarial:
		fill = blend(playerColor(c.PlayerIdx%10), boardBg, 0.8)
	case c.Occupied:
		fill = blend(pieceColor(c.PieceType), boardBg, 0.7)
		if showOutline {
			outline, outlineW = playerColor(c.PlayerIdx), 2
		}
	}

	return fmt.Sprintf("background:%s;outline:%dpx solid %s;outline-offset:-1px", fill, outlineW, outline)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
