package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	abandoned       map[string]bool // games the periodic checker flagged as abandoned
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
		abandoned:       make(map[string]bool),
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

// AbandonedGames returns a snapshot of the game IDs the periodic checker
// currently considers abandoned.
func (l *Lobby) AbandonedGames() map[string]bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]bool, len(l.abandoned))
	for k, v := range l.abandoned {
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

	// Start abandoned-game checker
	go l.runAbandonedChecker(ctx)

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
		delete(l.abandoned, gameID)
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
	// No filter: the chat stream carries the lobby chat AND every game's chat,
	// distinguished by subject. Each message is tagged with the game ID parsed
	// from its subject ("" = lobby); the UI filters per screen.
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, l.js, natspkg.OrderedConsumerConfig{
		Stream: config.LobbyChatStream,
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
			cm.GameID = config.GameIDFromChatSubject(msg.Subject())
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

// runAbandonedChecker re-evaluates every listed game for abandonment on a
// timer, so games whose players never joined, never readied up, or walked away
// mid-game grow a Delete option in the lobby.
func (l *Lobby) runAbandonedChecker(ctx context.Context) {
	ticker := time.NewTicker(config.AbandonedCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.checkAbandoned(ctx)
		}
	}
}

// checkAbandoned rebuilds the abandoned set from scratch (so a game where
// activity resumes — e.g. a player reconnects — is un-flagged again) and emits
// a games update when the set changes.
func (l *Lobby) checkAbandoned(ctx context.Context) {
	now := time.Now()
	fresh := make(map[string]bool)
	for id, g := range l.Games() {
		if l.isAbandoned(ctx, g, now) {
			fresh[id] = true
		}
	}

	l.mu.Lock()
	changed := len(fresh) != len(l.abandoned)
	if !changed {
		for id := range fresh {
			if !l.abandoned[id] {
				changed = true
				break
			}
		}
	}
	l.abandoned = fresh
	l.mu.Unlock()

	if changed {
		l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateGames})
	}
}

// isAbandoned applies the abandonment rules to one listing: a game that never
// started is abandoned AbandonedUnstartedTimeout after creation; a started game
// is abandoned once its stream has seen no messages for AbandonedIdleTimeout.
func (l *Lobby) isAbandoned(ctx context.Context, g GameListing, now time.Time) bool {
	switch g.Status {
	case config.GameStatusCreated, config.GameStatusStarting:
		return now.Sub(g.CreatedAt) > config.AbandonedUnstartedTimeout
	case config.GameStatusInProgress:
		s, err := l.js.Stream(ctx, config.GameStream(g.GameID))
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			// The listing points at a deleted stream — the game can never make
			// progress, so offer deletion.
			return true
		}
		if err != nil {
			return false // can't tell (e.g. transient network error) — don't flag
		}
		return now.Sub(s.CachedInfo().State.LastTime) > config.AbandonedIdleTimeout
	}
	return false
}

// DeleteGame tears down an abandoned game entirely: the per-game stream, the
// game's chat messages in the shared chat stream, and the lobby KV listing
// (whose deletion removes the game from every player's list).
func (l *Lobby) DeleteGame(ctx context.Context, gameID string) error {
	if err := natspkg.DeleteGameStream(ctx, l.js, gameID); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
		return err
	}
	if err := natspkg.PurgeGameChat(ctx, l.js, gameID); err != nil {
		log.Printf("delete game %s: purge chat: %v", gameID, err)
	}
	if err := l.kv.Delete(ctx, config.LobbyGameKey(gameID)); err != nil {
		return err
	}
	l.mu.Lock()
	delete(l.abandoned, gameID)
	l.mu.Unlock()
	return nil
}

func (l *Lobby) emitUpdate(u LobbyUpdate) {
	select {
	case l.Updates <- u:
	default:
	}
}

// CreateGame creates a new game. For teams mode, teamSize is the number of
// players per team and playerCount must be the total (TeamCount*teamSize);
// other modes pass teamSize 0.
func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount, teamSize int) (string, error) {
	gameID := uuid.New().String()

	if err := natspkg.EnsureGameStream(ctx, l.js, gameID); err != nil {
		return "", err
	}

	meta := config.GameMeta{
		GameID:      gameID,
		Mode:        mode,
		PlayerCount: playerCount,
		TeamSize:    teamSize,
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
		TeamSize:    teamSize,
		Players:     nil,
		CreatedAt:   meta.CreatedAt,
	}
	listingData, _ := json.Marshal(listing)
	_, _ = l.kv.Put(ctx, config.LobbyGameKey(gameID), listingData)

	return gameID, nil
}

// ErrTeamFull is returned by JoinGame when the requested team already has
// teamSize members.
var ErrTeamFull = errors.New("team is full")

// JoinResult is the roster position assigned to a player by JoinGame.
type JoinResult struct {
	PlayerIdx int // global index in the roster (all modes)
	Team      int // teams mode: 0 = A, 1 = B
	TeamSlot  int // teams mode: section index within the team board
}

// JoinGame joins an existing game. For teams mode, team selects which team to
// join (0 or 1) and may fail with ErrTeamFull; other modes ignore it.
//
// The listing update runs as a CAS loop (like ToggleReady): team capacity
// validation and TeamSlot assignment must be atomic with the roster append or
// concurrent joins could both land on a full team / claim the same slot. The
// roster entry is published only after the CAS commit so roster and listing
// agree.
func (l *Lobby) JoinGame(ctx context.Context, gameID string, team int) (JoinResult, error) {
	var res JoinResult
	var summary PlayerSummary
	for {
		entry, err := l.kv.Get(ctx, config.LobbyGameKey(gameID))
		if err != nil {
			return JoinResult{}, err
		}
		var g GameListing
		if err := json.Unmarshal(entry.Value(), &g); err != nil {
			return JoinResult{}, err
		}

		// Already in the game — just update presence and return our position.
		already := false
		for i, p := range g.Players {
			if p.PlayerID == l.playerID {
				res = JoinResult{PlayerIdx: i, Team: p.Team, TeamSlot: p.TeamSlot}
				already = true
				break
			}
		}
		if already {
			break
		}

		summary = PlayerSummary{PlayerID: l.playerID, Name: l.name}
		if g.Mode == config.ModeTeams {
			if team < 0 || team >= config.TeamCount {
				return JoinResult{}, fmt.Errorf("invalid team %d", team)
			}
			if g.TeamMemberCount(team) >= g.TeamSize {
				return JoinResult{}, ErrTeamFull
			}
			summary.Team = team
			summary.TeamSlot = g.TeamMemberCount(team)
		}
		g.Players = append(g.Players, summary)
		res = JoinResult{PlayerIdx: len(g.Players) - 1, Team: summary.Team, TeamSlot: summary.TeamSlot}

		// Game full → transition to starting (for teams this is exactly
		// "both teams full", since per-team capacity is enforced above).
		full := len(g.Players) >= g.PlayerCount
		if full {
			g.Status = config.GameStatusStarting
		}

		listingData, _ := json.Marshal(g)
		if _, err := l.kv.Update(ctx, config.LobbyGameKey(gameID), listingData, entry.Revision()); err != nil {
			continue // CAS conflict, retry
		}

		// Publish roster entry (after the CAS commit)
		rosterData, _ := json.Marshal(summary)
		if _, err := l.js.Publish(ctx, config.RosterSubject(gameID, l.playerID), rosterData); err != nil {
			return JoinResult{}, err
		}

		if full {
			// Also update meta
			go l.transitionGameStatus(context.Background(), gameID, config.GameStatusStarting)
		}
		break
	}

	// Update presence
	l.mu.Lock()
	l.status = StatusInGame
	l.currentGameID = gameID
	l.mu.Unlock()

	l.publishPresence(ctx)

	return res, nil
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
	return l.publishChat(ctx, config.LobbyChatSubject, text, false)
}

// SendGameChat sends a message to one game's chat — seen only by that game's
// players and spectators (the UI shows a game's messages only on that game's
// screen). spectator marks the sender as watching rather than playing.
func (l *Lobby) SendGameChat(ctx context.Context, gameID, text string, spectator bool) error {
	return l.publishChat(ctx, config.GameChatSubject(gameID), text, spectator)
}

func (l *Lobby) publishChat(ctx context.Context, subject, text string, spectator bool) error {
	msg := ChatMessage{
		PlayerID:  l.playerID,
		Name:      l.name,
		Text:      text,
		Timestamp: time.Now(),
		Spectator: spectator,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = l.js.Publish(ctx, subject, data)
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
