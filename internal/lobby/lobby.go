package lobby

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
)

// Lobby manages all lobby-level state.
type Lobby struct {
	playerID        string
	name            string
	kv              jetstream.KeyValue
	js              jetstream.JetStream
	Updates         chan LobbyUpdate
	mu              sync.RWMutex
	players         map[string]PlayerPresence
	games           map[string]GameListing
	archives        []config.ArchiveRecord
	status          PresenceStatus
	currentGameID   string
	cancelFn        context.CancelFunc
	initialLoadDone chan struct{}
}

// New creates a new lobby instance.
func New(
	js jetstream.JetStream,
	kv jetstream.KeyValue,
	playerID string,
	name string,
) *Lobby {
	return &Lobby{
		playerID:        playerID,
		name:            name,
		kv:              kv,
		js:              js,
		Updates:         make(chan LobbyUpdate, 16),
		players:         make(map[string]PlayerPresence),
		games:           make(map[string]GameListing),
		status:          StatusInLobby,
		initialLoadDone: make(chan struct{}),
	}
}

// Players returns a snapshot of the current player presence map.
func (l *Lobby) Players() map[string]PlayerPresence {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]PlayerPresence, len(l.players))
	for k, v := range l.players {
		out[k] = v
	}
	return out
}

// Games returns a snapshot of the current game listing map.
func (l *Lobby) Games() map[string]GameListing {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]GameListing, len(l.games))
	for k, v := range l.games {
		out[k] = v
	}
	return out
}

// Archives returns a snapshot of the archive records.
func (l *Lobby) Archives() []config.ArchiveRecord {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]config.ArchiveRecord, len(l.archives))
	copy(out, l.archives)
	return out
}

// Start begins the KV watcher, chat consumer, archive consumer, and presence heartbeat.
func (l *Lobby) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	l.cancelFn = cancel

	// Start KV watcher
	go l.runKVWatcher(ctx)

	// Start lobby chat consumer
	go l.runChatConsumer(ctx)

	// Start archive consumer
	go l.runArchiveConsumer(ctx)

	// Start heartbeat
	go l.runHeartbeat(ctx)

	return nil
}

// WaitForInitialLoad blocks until the KV watcher has delivered all existing entries.
func (l *Lobby) WaitForInitialLoad(ctx context.Context) error {
	select {
	case <-l.initialLoadDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop cancels all goroutines.
func (l *Lobby) Stop() {
	if l.cancelFn != nil {
		l.cancelFn()
	}
}

func (l *Lobby) runKVWatcher(ctx context.Context) {
	watcher, err := l.kv.WatchAll(ctx)
	if err != nil {
		log.Printf("KV watcher error: %v", err)
		return
	}
	defer func() { _ = watcher.Stop() }()

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				return
			}
			if entry == nil {
				// nil entry signals that all initial KV values have been delivered.
				select {
				case <-l.initialLoadDone:
				default:
					close(l.initialLoadDone)
				}
				continue
			}
			l.handleKVUpdate(entry)
		}
	}
}

func (l *Lobby) handleKVUpdate(entry jetstream.KeyValueEntry) {
	key := entry.Key()

	if strings.HasPrefix(key, "players.") {
		l.handlePlayerUpdate(entry)
	} else if strings.HasPrefix(key, "games.") {
		l.handleGameUpdate(entry)
	}
}

func (l *Lobby) handlePlayerUpdate(entry jetstream.KeyValueEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	playerID := strings.TrimPrefix(entry.Key(), "players.")

	switch entry.Operation() {
	case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
		delete(l.players, playerID)
	default:
		var p PlayerPresence
		if err := json.Unmarshal(entry.Value(), &p); err != nil {
			return
		}
		l.players[playerID] = p
	}

	l.emitUpdate(LobbyUpdate{Kind: LobbyUpdatePlayers})
}

func (l *Lobby) handleGameUpdate(entry jetstream.KeyValueEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	gameID := strings.TrimPrefix(entry.Key(), "games.")

	switch entry.Operation() {
	case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
		delete(l.games, gameID)
	default:
		var g GameListing
		if err := json.Unmarshal(entry.Value(), &g); err != nil {
			return
		}
		l.games[gameID] = g
	}

	l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateGames})
}

func (l *Lobby) runChatConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, l.js, natspkg.OrderedConsumerConfig{
		Stream:        config.LobbyChatStream,
		FilterSubject: config.LobbyChatSubject,
	})
	if err != nil {
		log.Printf("chat consumer error: %v", err)
		return
	}
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var cm ChatMessage
			if err := json.Unmarshal(msg.Data(), &cm); err != nil {
				continue
			}
			l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateChat, ChatMsg: &cm})
		}
	}
}

func (l *Lobby) runArchiveConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, l.js, natspkg.OrderedConsumerConfig{
		Stream:        config.ArchiveStream,
		FilterSubject: config.ArchiveSubject,
	})
	if err != nil {
		log.Printf("archive consumer error: %v", err)
		return
	}
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var rec config.ArchiveRecord
			if err := json.Unmarshal(msg.Data(), &rec); err != nil {
				continue
			}
			l.mu.Lock()
			l.archives = append(l.archives, rec)
			l.mu.Unlock()
			l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateArchive})
		}
	}
}

func (l *Lobby) emitUpdate(u LobbyUpdate) {
	select {
	case l.Updates <- u:
	default:
	}
}

// CreateGame creates a new game.
func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount int) (string, error) {
	gameID := uuid.New().String()

	if err := natspkg.EnsureGameStream(ctx, l.js, gameID); err != nil {
		return "", err
	}

	meta := config.GameMeta{
		GameID:      gameID,
		Mode:        mode,
		PlayerCount: playerCount,
		Seed:        uint64(time.Now().UnixNano()),
		Status:      config.GameStatusCreated,
		CreatorID:   l.playerID,
		CreatedAt:   time.Now(),
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err := natspkg.PublishMeta(ctx, l.js, gameID, data, 0); err != nil {
		return "", err
	}

	// Update game listing in KV (no players yet — they must click Join)
	listing := GameListing{
		GameID:      gameID,
		Mode:        mode,
		Status:      config.GameStatusCreated,
		PlayerCount: playerCount,
		Players:     nil,
		CreatedAt:   meta.CreatedAt,
	}
	listingData, _ := json.Marshal(listing)
	_, _ = l.kv.Put(ctx, config.LobbyGameKey(gameID), listingData)

	return gameID, nil
}

// JoinGame joins an existing game.
func (l *Lobby) JoinGame(ctx context.Context, gameID string) (int, error) {
	// Check if already in the game
	l.mu.RLock()
	g, exists := l.games[gameID]
	l.mu.RUnlock()
	if exists {
		for i, p := range g.Players {
			if p.PlayerID == l.playerID {
				// Already in this game — just update presence and return
				l.mu.Lock()
				l.status = StatusInGame
				l.currentGameID = gameID
				l.mu.Unlock()
				l.publishPresence(ctx)
				return i, nil
			}
		}
	}

	// Publish roster entry
	roster := PlayerSummary{PlayerID: l.playerID, Name: l.name}
	rosterData, _ := json.Marshal(roster)
	_, err := l.js.Publish(ctx, config.RosterSubject(gameID, l.playerID), rosterData)
	if err != nil {
		return 0, err
	}

	// Update presence
	l.mu.Lock()
	l.status = StatusInGame
	l.currentGameID = gameID
	l.mu.Unlock()

	l.publishPresence(ctx)

	// Update game listing with new player
	playerIdx := 0
	l.mu.RLock()
	g, exists = l.games[gameID]
	l.mu.RUnlock()
	if exists {
		g.Players = append(g.Players, roster)
		playerIdx = len(g.Players) - 1
		// Check if game is full → transition to starting
		if len(g.Players) >= g.PlayerCount {
			g.Status = config.GameStatusStarting
			// Also update meta
			go l.transitionGameStatus(context.Background(), gameID, config.GameStatusStarting)
		}
		listingData, _ := json.Marshal(g)
		_, _ = l.kv.Put(ctx, config.LobbyGameKey(gameID), listingData)
	}

	return playerIdx, nil
}

// LeaveGame leaves a game and returns to lobby.
func (l *Lobby) LeaveGame(ctx context.Context, gameID string) error {
	l.mu.Lock()
	l.status = StatusInLobby
	l.currentGameID = ""
	l.mu.Unlock()

	l.publishPresence(ctx)
	return nil
}

// ToggleReadyResult holds the state after a ToggleReady call.
type ToggleReadyResult struct {
	AllReady bool
	Players  []PlayerSummary
	MyReady  bool
}

// ToggleReady toggles the local player's ready state.
// Uses CAS on the KV entry to avoid lost updates.
func (l *Lobby) ToggleReady(ctx context.Context, gameID string) (ToggleReadyResult, error) {
	for {
		entry, err := l.kv.Get(ctx, config.LobbyGameKey(gameID))
		if err != nil {
			return ToggleReadyResult{}, err
		}
		var g GameListing
		if err := json.Unmarshal(entry.Value(), &g); err != nil {
			return ToggleReadyResult{}, err
		}

		// Don't allow toggling during countdown or in-progress
		if g.Status == config.GameStatusInProgress {
			return ToggleReadyResult{}, nil
		}

		// Toggle our ready state
		var myReady bool
		for i := range g.Players {
			if g.Players[i].PlayerID == l.playerID {
				g.Players[i].Ready = !g.Players[i].Ready
				myReady = g.Players[i].Ready
				break
			}
		}

		// Check if all players ready
		allReady := true
		for _, p := range g.Players {
			if !p.Ready {
				allReady = false
				break
			}
		}

		// CAS write
		listingData, _ := json.Marshal(g)
		_, err = l.kv.Update(ctx, config.LobbyGameKey(gameID), listingData, entry.Revision())
		if err != nil {
			continue // CAS conflict, retry
		}

		// Return a snapshot of the state we just wrote
		playersCopy := make([]PlayerSummary, len(g.Players))
		copy(playersCopy, g.Players)

		return ToggleReadyResult{
			AllReady: allReady && len(g.Players) >= g.PlayerCount,
			Players:  playersCopy,
			MyReady:  myReady,
		}, nil
	}
}

// StartGame transitions the game to in_progress after the countdown.
func (l *Lobby) StartGame(ctx context.Context, gameID string) {
	go l.transitionGameStatus(context.Background(), gameID, config.GameStatusInProgress)
}

// SendChat sends a message to the lobby chat.
func (l *Lobby) SendChat(ctx context.Context, text string) error {
	msg := ChatMessage{
		PlayerID:  l.playerID,
		Name:      l.name,
		Text:      text,
		Timestamp: time.Now(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = l.js.Publish(ctx, config.LobbyChatSubject, data)
	return err
}

func (l *Lobby) transitionGameStatus(ctx context.Context, gameID string, status config.GameStatus) {
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, l.js, gameID)
	if err != nil {
		return
	}
	meta.Status = status
	if status == config.GameStatusInProgress {
		meta.StartedAt = time.Now()
	}
	data, _ := json.Marshal(meta)
	_ = natspkg.PublishMeta(ctx, l.js, gameID, data, metaSeq)
}

// PlayerID returns the lobby player ID.
func (l *Lobby) PlayerID() string {
	return l.playerID
}

// PlayerName returns the lobby player name.
func (l *Lobby) PlayerName() string {
	return l.name
}

// GetJS returns the JetStream handle.
func (l *Lobby) GetJS() jetstream.JetStream {
	return l.js
}
