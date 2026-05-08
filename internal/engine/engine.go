package engine

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/rng"
)

// Mode represents the engine's operating mode.
type Mode int

const (
	ModePlayer Mode = iota
	ModeSpectator
	ModeGameOver
)

// Engine manages a single game session.
type Engine struct {
	gameID      string
	playerID    string
	gameMode    config.GameMode
	mode        Mode
	initialMode Mode // original mode at creation (ModePlayer or ModeSpectator)
	playerIdx   int  // 0 for creator, 1 for joiner; used in cooperative mode for Cell.PlayerIdx
	playerCount int  // number of players in the game

	mu        sync.Mutex
	playfield *game.Playfield

	opponentPlayfields map[string]*game.Playfield // keyed by playerID (competitive)
	opponentPlayerID   string                     // single opponent (for 2-player join)

	seq      *rng.Sequence
	pieceIdx uint64
	metaSeq  uint64

	score             int
	totalLines        int
	level             int
	hadActivePiece    bool
	eliminatedPlayers map[string]bool // players who have topped out (competitive)
	visibleRowStart   int             // first visible row index (varies per game mode/player count)

	Updates        chan EngineUpdate
	OnGameFinished func() // called after game transitions to finished (for archiving)

	js         jetstream.JetStream
	ctx        context.Context
	cancelFn   context.CancelFunc
	moves      chan MoveType
	rowUpdated chan struct{}
}

// New creates a new engine instance. Call Start() to begin.
func New(
	js jetstream.JetStream,
	gameID, playerID, opponentPlayerID string,
	gameMode config.GameMode,
	mode Mode,
	playerIdx int,
) *Engine {
	return &Engine{
		gameID:             gameID,
		playerID:           playerID,
		opponentPlayerID:   opponentPlayerID,
		gameMode:           gameMode,
		mode:               mode,
		initialMode:        mode,
		playerIdx:          playerIdx,
		playfield:          game.NewPlayfield(config.StandardWidth),
		opponentPlayfields: make(map[string]*game.Playfield),
		Updates:            make(chan EngineUpdate, 64),
		js:                 js,
		moves:              make(chan MoveType, 8),
		rowUpdated:         make(chan struct{}, 1),
		eliminatedPlayers:  make(map[string]bool),
	}
}

// Start begins all consumer goroutines and (if ModePlayer) the gravity ticker.
func (e *Engine) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	e.cancelFn = cancel

	// 1. Fetch meta
	meta, metaSeq, err := natspkg.FetchGameMeta(ctx, e.js, e.gameID)
	if err != nil {
		cancel()
		return err
	}
	e.playerCount = meta.PlayerCount

	// Set visible row start based on mode
	if e.gameMode == config.ModeCompetitive {
		e.visibleRowStart = config.CompetitiveVisibleRowStart(meta.PlayerCount)
	} else {
		e.visibleRowStart = config.VisibleRowStart
	}

	// playerIdx was supplied by the caller (lobby.JoinGame return value)
	// at engine construction time; no discovery needed here.

	// Cooperative mode: shared wide playfield, shared RNG seed
	if e.gameMode == config.ModeCooperative {
		e.seq = rng.New(meta.Seed)
		e.pieceIdx = 0
		// Shared wide playfield with standard height
		e.playfield = game.NewPlayfieldWithHeight(
			meta.PlayerCount*config.StandardWidth,
			config.HeadroomRows+config.VisibleRows,
		)
	} else {
		e.seq = rng.New(meta.Seed)
		e.pieceIdx = meta.PieceIdx
		// Competitive: taller playfield (extra rows per player)
		e.playfield = game.NewPlayfieldWithHeight(
			config.StandardWidth,
			config.CompetitiveTotalRows(meta.PlayerCount),
		)
	}
	e.metaSeq = metaSeq

	// 2. Fetch playfield state
	playerToken := e.effectivePlayerID()
	rows, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, playerToken, e.playfield.Height)
	if err != nil {
		cancel()
		return err
	}
	var maxSeq uint64
	for _, r := range rows {
		data, _ := game.UnmarshalRow(r.Payload)
		e.playfield.Apply(r.Row, data, r.Seq)
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
	}

	// Check if there's already an active piece for this player
	e.hadActivePiece = e.playfield.ActivePieceForPlayer(e.playerIdx) != nil

	// 3. Start row consumer
	go e.runConsumer(ctx, e.playfield, playerToken, maxSeq+1, false)

	// 4. Competitive: set up known opponent and discover others via roster
	if e.gameMode == config.ModeCompetitive {
		if e.opponentPlayerID != "" {
			e.startOpponentConsumer(ctx, e.opponentPlayerID)
		}
		// Always run roster consumer to discover all opponents
		go e.runRosterConsumer(ctx)
	}

	// 6. Start events consumer and meta consumer
	go e.runEventsConsumer(ctx)
	go e.runMetaConsumer(ctx)
	go e.runCountdownConsumer(ctx)

	// 7. Start move processor and gravity if playing
	if e.mode == ModePlayer {
		// If game is already in progress and no active piece, spawn immediately
		if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil && meta.Status == config.GameStatusInProgress {
			e.spawnPiece(ctx)
		}
		go e.runMoves(ctx)
		go e.runGravity(ctx)
	}

	return nil
}

// Stop tears down all goroutines.
func (e *Engine) Stop() {
	if e.cancelFn != nil {
		e.cancelFn()
	}
}

// Playfield returns the current playfield (thread-safe copy of rows).
func (e *Engine) Playfield() *game.Playfield {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.playfield
}

// OpponentPlayfields returns all opponent playfields keyed by playerID.
func (e *Engine) OpponentPlayfields() map[string]*game.Playfield {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]*game.Playfield, len(e.opponentPlayfields))
	for k, v := range e.opponentPlayfields {
		out[k] = v
	}
	return out
}

// startOpponentConsumer creates a playfield and consumer for a single opponent.
func (e *Engine) startOpponentConsumer(ctx context.Context, oppID string) {
	e.mu.Lock()
	if _, exists := e.opponentPlayfields[oppID]; exists {
		e.mu.Unlock()
		return // already tracking this opponent
	}
	pf := game.NewPlayfieldWithHeight(config.StandardWidth, config.CompetitiveTotalRows(e.playerCount))
	e.opponentPlayfields[oppID] = pf
	e.mu.Unlock()

	oppRows, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, oppID, pf.Height)
	if err != nil {
		log.Printf("fetch opponent %s state: %v", oppID, err)
		return
	}
	var oppMaxSeq uint64
	for _, r := range oppRows {
		data, _ := game.UnmarshalRow(r.Payload)
		e.mu.Lock()
		pf.Apply(r.Row, data, r.Seq)
		e.mu.Unlock()
		if r.Seq > oppMaxSeq {
			oppMaxSeq = r.Seq
		}
	}

	go e.runConsumer(ctx, pf, oppID, oppMaxSeq+1, true)
}

func (e *Engine) GameID() string            { return e.gameID }
func (e *Engine) PlayerID() string          { return e.playerID }
func (e *Engine) Score() int                { return e.score }
func (e *Engine) Level() int                { return e.level }
func (e *Engine) Mode() Mode                { return e.mode }
func (e *Engine) GameMode() config.GameMode { return e.gameMode }

func (e *Engine) MoveLeft()  { e.dispatch(MoveLeft) }
func (e *Engine) MoveRight() { e.dispatch(MoveRight) }
func (e *Engine) MoveDown()  { e.dispatch(MoveDown) }
func (e *Engine) RotateCW()  { e.dispatch(RotateCW) }
func (e *Engine) RotateCCW() { e.dispatch(RotateCCW) }
func (e *Engine) HardDrop()  { e.dispatch(MoveHardDrop) }

func (e *Engine) dispatch(m MoveType) {
	if e.mode != ModePlayer {
		return
	}
	select {
	case e.moves <- m:
	default:
	}
}

func (e *Engine) effectivePlayerID() string {
	if e.gameMode == config.ModeCooperative {
		return config.CoopPlayfieldID
	}
	return e.playerID
}

func (e *Engine) emitUpdate(u EngineUpdate) {
	select {
	case e.Updates <- u:
	default:
	}
}

func (e *Engine) transitionToSpectator(won bool) {
	e.mode = ModeGameOver
	e.emitUpdate(EngineUpdate{Kind: UpdateGameOver, Won: won})
}

func (e *Engine) spawnPiece(ctx context.Context) {
	pt := e.seq.Piece(e.pieceIdx)
	p := game.SpawnPiece(pt, config.StandardWidth)

	// In cooperative mode, offset spawn column to player's section
	if e.gameMode == config.ModeCooperative {
		p.Col += e.playerIdx * config.StandardWidth
	}

	// Check placement (coop checks against other players' active pieces too)
	var canPlace bool
	if e.gameMode == config.ModeCooperative {
		canPlace = game.CanPlaceCoop(p, e.playfield, e.playerIdx)
	} else {
		canPlace = game.CanPlace(p, e.playfield)
	}
	if !canPlace {
		e.handleTopOut(ctx)
		return
	}

	e.playfield.SetActivePieceForPlayer(p, e.playerIdx)
	e.hadActivePiece = true
	e.publishPlayfieldRows(ctx, e.playfield.RowsWithActiveCellsForPlayer(e.playerIdx))
}

func (e *Engine) PlayerIdx() int       { return e.playerIdx }
func (e *Engine) WasPlayer() bool      { return e.initialMode == ModePlayer }
func (e *Engine) VisibleRowStart() int { return e.visibleRowStart }
func (e *Engine) PlayfieldHeight() int { return e.playfield.Height }
func (e *Engine) IsEliminated(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.eliminatedPlayers[id]
}

func (e *Engine) PieceIdx() uint64 { return e.pieceIdx }

func (e *Engine) handleTopOut(ctx context.Context) {
	// Publish game over event with score and piece count
	ev := GameEvent{Kind: EventGameOver, PlayerID: e.playerID, Score: e.score, PieceCount: e.pieceIdx}
	data, _ := json.Marshal(ev)
	_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)
	e.transitionToSpectator(false) // we topped out → we lost

	// Transition game meta to finished:
	// - Cooperative: any top-out finishes the game
	// - Competitive: only finish when this was the last elimination
	if e.gameMode == config.ModeCooperative {
		go e.transitionGameToFinished(ctx)
	}
	// Competitive finishing is handled by the last player standing (see handleGameEvent)
}

func (e *Engine) transitionGameToFinished(ctx context.Context) {
	// Retry CAS: racing publishers (e.g. publishPieceIdxUpdate) can bump the
	// meta sequence between our fetch and publish. Without retry, the game
	// stays in_progress forever and never archives.
	const maxAttempts = 10
	succeeded := false
	alreadyFinished := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		meta, metaSeq, err := natspkg.FetchGameMeta(context.Background(), e.js, e.gameID)
		if err != nil {
			return
		}
		if meta.Status == config.GameStatusFinished || meta.Status == config.GameStatusArchived {
			alreadyFinished = true
			break
		}
		meta.Status = config.GameStatusFinished
		meta.FinishedAt = time.Now()
		data, _ := json.Marshal(meta)
		if err := natspkg.PublishMeta(context.Background(), e.js, e.gameID, data, metaSeq); err != nil {
			log.Printf("transition to finished: CAS retry (attempt %d): %v", attempt, err)
			continue
		}
		succeeded = true
		break
	}
	if !succeeded && !alreadyFinished {
		log.Printf("transition to finished: gave up after %d attempts", maxAttempts)
		return
	}
	// Trigger archive callback after a delay (gives all players time to receive game over).
	// archiveAndCleanup is CAS-protected (finished→archived), so duplicate calls are safe.
	if e.OnGameFinished != nil {
		go func() {
			time.Sleep(5 * time.Second)
			e.OnGameFinished()
		}()
	}
}

func (e *Engine) publishPlayfieldRows(ctx context.Context, rowIndices []int) {
	e.publishPlayfieldRowsRetry(ctx, rowIndices, e.gameMode == config.ModeCooperative)
}

// publishPlayfieldRowsNoCAS publishes rows without CAS checks.
// Used for line clears where the cleared state is authoritative and must not
// be merged with stale NATS data.
func (e *Engine) publishPlayfieldRowsNoCAS(ctx context.Context, rowIndices []int) {
	playerID := e.effectivePlayerID()
	for _, r := range rowIndices {
		data, err := e.playfield.Rows[r].Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			continue
		}
		seq, err := natspkg.PublishSingleRowNoCAS(ctx, e.js, e.gameID, natspkg.RowUpdate{
			Row:      r,
			PlayerID: playerID,
			Payload:  data,
		})
		if err != nil {
			log.Printf("publish row %d (no-cas): %v", r, err)
			continue
		}
		// Update LastSeq so subsequent CAS publishes use the correct sequence
		e.playfield.LastSeq[r] = seq
	}
}

func (e *Engine) publishPlayfieldRowsRetry(ctx context.Context, rowIndices []int, retry bool) {
	playerID := e.effectivePlayerID()
	hadCASFailure := false
	for _, r := range rowIndices {
		if retry {
			e.publishSingleRowWithRetry(ctx, playerID, r)
		} else {
			if !e.publishSingleRowNoRetry(ctx, playerID, r) {
				hadCASFailure = true
			}
		}
	}
	// Publish CAS flash event if any rows failed (visual feedback for all players)
	if hadCASFailure && e.mode == ModePlayer {
		e.publishCASFlash(ctx)
	}
}

// publishSingleRowNoRetry returns true on success, false on CAS failure.
func (e *Engine) publishSingleRowNoRetry(ctx context.Context, playerID string, r int) bool {
	data, err := e.playfield.Rows[r].Marshal()
	if err != nil {
		return true // not a CAS failure
	}
	err = natspkg.PublishSingleRow(ctx, e.js, e.gameID, natspkg.RowUpdate{
		Row:           r,
		PlayerID:      playerID,
		Payload:       data,
		ExpectLastSeq: e.playfield.LastSeq[r],
	})
	if err != nil {
		// CAS failure — fetch latest row from NATS to correct in-memory state
		subject := config.RowSubject(e.gameID, playerID, r)
		stream, sErr := e.js.Stream(ctx, config.GameStream(e.gameID))
		if sErr != nil {
			return false
		}
		msg, gErr := stream.GetLastMsgForSubject(ctx, subject)
		if gErr != nil {
			return false
		}
		latestRow, uErr := game.UnmarshalRow(msg.Data)
		if uErr != nil {
			return false
		}
		e.playfield.Rows[r] = latestRow
		e.playfield.LastSeq[r] = msg.Sequence
		return false
	}
	return true
}

func (e *Engine) publishCASFlash(ctx context.Context) {
	e.mu.Lock()
	var flashCells [][2]int
	if p := e.playfield.ActivePieceForPlayer(e.playerIdx); p != nil {
		flashCells = p.Cells()
	}
	e.mu.Unlock()
	if len(flashCells) == 0 {
		return
	}
	ev := GameEvent{
		Kind:           EventCASFlash,
		PlayerID:       e.playerID,
		FlashCells:     flashCells,
		FlashPlayerIdx: e.playerIdx,
	}
	data, _ := json.Marshal(ev)
	_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)
}

func (e *Engine) publishSingleRowWithRetry(ctx context.Context, playerID string, r int) {
	// Save the cells we want to publish (our changes are applied in-memory)
	savedCells := make([]game.Cell, len(e.playfield.Rows[r].Cells))
	copy(savedCells, e.playfield.Rows[r].Cells)

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		data, err := e.playfield.Rows[r].Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			return
		}
		err = natspkg.PublishSingleRow(ctx, e.js, e.gameID, natspkg.RowUpdate{
			Row:           r,
			PlayerID:      playerID,
			Payload:       data,
			ExpectLastSeq: e.playfield.LastSeq[r],
		})
		if err == nil {
			return
		}
		if e.gameMode != config.ModeCooperative {
			log.Printf("publish row %d: %v", r, err)
			return
		}
		// CAS conflict in cooperative mode — fetch latest row state directly
		// from the stream and merge our cells on top.
		subject := config.RowSubject(e.gameID, playerID, r)
		stream, sErr := e.js.Stream(ctx, config.GameStream(e.gameID))
		if sErr != nil {
			log.Printf("publish row %d: get stream: %v", r, sErr)
			return
		}
		msg, gErr := stream.GetLastMsgForSubject(ctx, subject)
		if gErr != nil {
			log.Printf("publish row %d: get last msg: %v", r, gErr)
			return
		}
		// Apply the latest NATS state to our in-memory row
		latestRow, uErr := game.UnmarshalRow(msg.Data)
		if uErr != nil {
			log.Printf("publish row %d: unmarshal: %v", r, uErr)
			return
		}
		e.playfield.Rows[r] = latestRow
		e.playfield.LastSeq[r] = msg.Sequence
		// Merge our saved cells back onto the latest state
		for i, sc := range savedCells {
			if i >= len(e.playfield.Rows[r].Cells) {
				break
			}
			if sc.Active && sc.PlayerIdx == e.playerIdx {
				e.playfield.Rows[r].Cells[i] = sc
			} else if sc.Occupied && !sc.Active {
				e.playfield.Rows[r].Cells[i] = sc
			}
		}
	}
	log.Printf("publish row %d: gave up after %d retries", r, maxRetries)
}

func (e *Engine) publishPieceIdxUpdate(pieceIdx uint64) {
	// In cooperative mode, each player has independent piece tracking
	if e.gameMode == config.ModeCooperative {
		return
	}
	meta, metaSeq, err := natspkg.FetchGameMeta(e.ctx, e.js, e.gameID)
	if err != nil {
		return
	}
	meta.PieceIdx = pieceIdx
	data, _ := json.Marshal(meta)
	_ = natspkg.PublishMeta(e.ctx, e.js, e.gameID, data, metaSeq)
}

// runRosterConsumer watches for the opponent's roster entry when the creator
// starts the engine before the joiner has joined.
// runRosterConsumer watches for roster entries and starts opponent consumers
// for each new player discovered. Keeps running to discover late joiners.
func (e *Engine) runRosterConsumer(ctx context.Context) {
	filterSubject := "jetricks.game." + e.gameID + ".roster.*"

	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: filterSubject,
	})
	if err != nil {
		log.Printf("roster consumer error: %v", err)
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
			parts := strings.Split(msg.Subject(), ".")
			rosterPlayerID := parts[len(parts)-1]
			if rosterPlayerID == e.playerID {
				continue
			}

			// Start opponent consumer if not already tracking
			e.startOpponentConsumer(ctx, rosterPlayerID)
		}
	}
}
