package ui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/cleanup"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
)

// Server is the HTTP server for the Jetricks UI.
type Server struct {
	port   int
	js     jetstream.JetStream
	kv     jetstream.KeyValue
	nc     *nats.Conn
	lobby  *lobby.Lobby
	router *http.ServeMux
	srv    *http.Server
	ctx    context.Context

	mu          sync.Mutex
	engine      *engine.Engine
	gamePlayers []lobby.PlayerSummary // players in the current game (for spectator legend)

	// Broadcaster for lobby updates
	lobbyBroadcaster *Broadcaster[lobby.LobbyUpdate]
	gameBroadcaster  *Broadcaster[engine.EngineUpdate]
}

// New creates a new UI server. The lobby is not created until the user logs in.
func New(port int, js jetstream.JetStream, kv jetstream.KeyValue, nc *nats.Conn) *Server {
	s := &Server{
		port:             port,
		js:               js,
		kv:               kv,
		nc:               nc,
		router:           http.NewServeMux(),
		lobbyBroadcaster: NewBroadcaster[lobby.LobbyUpdate](),
		gameBroadcaster:  NewBroadcaster[engine.EngineUpdate](),
	}
	s.registerRoutes()
	return s
}

// Start begins the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	s.ctx = ctx
	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.router,
	}

	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop shuts down the HTTP server and lobby if active.
func (s *Server) Stop() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
	s.lobbyBroadcaster.Close()
	s.gameBroadcaster.Close()
	s.mu.Lock()
	lb := s.lobby
	s.mu.Unlock()
	if lb != nil {
		lb.Stop()
	}
}

// initLobby creates and starts the lobby with the given player name,
// then runs cleanup. Called when the user logs in via the UI.
func (s *Server) initLobby(playerName string) error {
	lb := lobby.New(s.js, s.kv, playerName, playerName)
	if err := lb.Start(s.ctx); err != nil {
		return fmt.Errorf("start lobby: %w", err)
	}

	// Wait for initial KV load before cleanup
	initCtx, initCancel := context.WithTimeout(s.ctx, 10*time.Second)
	if err := lb.WaitForInitialLoad(initCtx); err != nil {
		log.Printf("warning: KV initial load did not complete: %v", err)
	}
	initCancel()

	cleanCtx, cleanCancel := context.WithTimeout(s.ctx, 30*time.Second)
	if err := cleanup.Run(cleanCtx, s.js, s.kv, s.nc, lb); err != nil {
		log.Printf("cleanup warning: %v", err)
	}
	cleanCancel()

	s.mu.Lock()
	s.lobby = lb
	s.mu.Unlock()

	// Pump lobby updates to broadcaster
	go func() {
		for {
			select {
			case <-s.ctx.Done():
				return
			case update, ok := <-lb.Updates:
				if !ok {
					return
				}
				s.lobbyBroadcaster.Send(update)
			}
		}
	}()

	fmt.Printf("Player: %s\n", playerName)
	return nil
}

// AttachEngine registers an active game engine.
func (s *Server) AttachEngine(e *engine.Engine) {
	s.mu.Lock()
	s.engine = e
	s.mu.Unlock()

	// Pump engine updates to broadcaster
	go func() {
		for update := range e.Updates {
			s.gameBroadcaster.Send(update)
		}
	}()
}

// DetachEngine unregisters the engine.
func (s *Server) DetachEngine() {
	s.mu.Lock()
	s.engine = nil
	s.mu.Unlock()
}

func (s *Server) getEngine() *engine.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.engine
}

func (s *Server) getGamePlayers() []lobby.PlayerSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gamePlayers
}

func (s *Server) getLobby() *lobby.Lobby {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lobby
}

func (s *Server) registerRoutes() {
	s.router.HandleFunc("GET /", s.handleRoot)
	s.router.HandleFunc("POST /login", s.handleLogin)
	s.router.HandleFunc("GET /lobby/stream", s.handleLobbyStream)
	s.router.HandleFunc("POST /lobby/chat", s.handleLobbyChat)
	s.router.HandleFunc("POST /lobby/game/create", s.handleCreateGame)
	s.router.HandleFunc("POST /lobby/game/{id}/join", s.handleJoinGame)
	s.router.HandleFunc("GET /game", s.handleGamePage)
	s.router.HandleFunc("GET /game/stream", s.handleGameStream)
	s.router.HandleFunc("POST /game/move", s.handleGameMove)
	s.router.HandleFunc("POST /game/ready", s.handleGameReady)
	s.router.HandleFunc("POST /lobby/quit", s.handleQuit)
	s.router.HandleFunc("POST /lobby/game/{id}/spectate", s.handleSpectateGame)
}
