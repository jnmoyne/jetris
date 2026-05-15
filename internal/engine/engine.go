package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
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

	// In-memory state is updated only when the consumer echoes the published
	// rows back (see runConsumer). Here we just compute the projection and
	// publish; the consumer will set hadActivePiece via the lock-in detector.
	affected := make([]int, 0, 4)
	seen := make(map[int]bool, 4)
	for _, c := range p.Cells() {
		if !seen[c[0]] {
			seen[c[0]] = true
			affected = append(affected, c[0])
		}
	}
	rows := e.playfield.ProjectMove(affected, &p, e.playerIdx)
	if e.gameMode == config.ModeCooperative {
		// In coop both players write the same shared row subjects, so two
		// near-simultaneous spawns (e.g. when meta transitions to
		// in_progress) race for the same headroom rows. Spawning MUST
		// succeed — the loser must not be left without a piece — so we
		// merge-retry on CAS failure: refetch the latest row from the
		// stream, overlay our piece cells on top, retry. This is the only
		// CAS path in the engine that retries; player moves never do.
		e.publishProjectedRowsWithMergeRetry(ctx, rows)
		return
	}
	// Competitive: each player writes their own subjects, so no race.
	// flashOnFailure=false because this isn't a player input.
	e.publishProjectedRows(ctx, rows, false)
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

// publishProjectedRows publishes pre-computed row payloads as a SINGLE atomic
// batch with per-subject CAS expectations sourced from e.playfield.LastSeq[r]
// (Nats-Expected-Last-Subject-Sequence). Either every row in the move
// commits or none does; consumers never see a torn intermediate state.
//
// e.playfield is not mutated — the consumer (runConsumer) is the single writer
// to e.playfield via pf.Apply. On CAS failure the move is DROPPED in both
// competitive and cooperative modes; the player is signalled with a local-only
// rainbow flash on their own piece outline so they know to retry the input
// themselves. The retryOnCAS parameter is retained for callers that don't
// want a flash (e.g. spawn races, where a flash would be misleading).
func (e *Engine) publishProjectedRows(ctx context.Context, rows map[int]game.Row, flashOnFailure bool) {
	if len(rows) == 0 {
		return
	}
	playerID := e.effectivePlayerID()
	updates, err := e.buildBatchUpdates(playerID, rows)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}

	if err := natspkg.PublishMoveAtomically(ctx, e.js, e.gameID, updates); err == nil {
		return
	} else if !errors.Is(err, natspkg.ErrCASFailure) {
		log.Printf("publish batch: %v", err)
		return
	}

	// CAS failure: drop the move. Signal the player with a local-only
	// rainbow flash on their own piece. We do NOT publish anything to the
	// other players — a CAS failure is information for the player who
	// authored the move, not for spectators or other players. The player
	// must retry the input themselves.
	if flashOnFailure && e.mode == ModePlayer {
		e.emitLocalCASFlash()
	}
}

// publishProjectedRowsNoCAS publishes pre-computed rows as a SINGLE atomic
// batch without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, line-clear, shrink) where the publisher's view is the
// new ground truth. Either every row commits or none does.
func (e *Engine) publishProjectedRowsNoCAS(ctx context.Context, rows map[int]game.Row) {
	if len(rows) == 0 {
		return
	}
	playerID := e.effectivePlayerID()
	updates := make([]natspkg.RowUpdate, 0, len(rows))
	for r, row := range rows {
		data, err := row.Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			return
		}
		updates = append(updates, natspkg.RowUpdate{
			Row:      r,
			PlayerID: playerID,
			Payload:  data,
		})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Row < updates[j].Row })
	if err := natspkg.PublishRowsAtomicallyNoCAS(ctx, e.js, e.gameID, updates); err != nil {
		log.Printf("publish batch (no-cas): %v", err)
	}
}

// publishProjectedRowsSliceNoCAS publishes rows[fromRow:toRow) as a SINGLE
// atomic batch without CAS. Used for whole-playfield projections (line clear,
// shrink).
func (e *Engine) publishProjectedRowsSliceNoCAS(ctx context.Context, rows []game.Row, fromRow, toRow int) {
	if fromRow >= toRow {
		return
	}
	playerID := e.effectivePlayerID()
	updates := make([]natspkg.RowUpdate, 0, toRow-fromRow)
	for r := fromRow; r < toRow && r < len(rows); r++ {
		data, err := rows[r].Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			return
		}
		updates = append(updates, natspkg.RowUpdate{
			Row:      r,
			PlayerID: playerID,
			Payload:  data,
		})
	}
	if err := natspkg.PublishRowsAtomicallyNoCAS(ctx, e.js, e.gameID, updates); err != nil {
		log.Printf("publish slice batch (no-cas): %v", err)
	}
}

// buildBatchUpdates converts a row projection map into a sorted RowUpdate
// slice (ascending row index) with per-subject CAS expectations sourced from
// e.playfield.LastSeq.
func (e *Engine) buildBatchUpdates(playerID string, rows map[int]game.Row) ([]natspkg.RowUpdate, error) {
	updates := make([]natspkg.RowUpdate, 0, len(rows))
	for r, row := range rows {
		data, err := row.Marshal()
		if err != nil {
			return nil, err
		}
		updates = append(updates, natspkg.RowUpdate{
			Row:           r,
			PlayerID:      playerID,
			Payload:       data,
			ExpectLastSeq: e.playfield.LastSeq[r],
		})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Row < updates[j].Row })
	return updates, nil
}

// publishProjectedRowsWithMergeRetry publishes a projection that MUST succeed
// even under concurrent writers — used by the coop spawn path where both
// players write the same shared row subjects. On CAS failure, refetches each
// affected row from the stream, overlays our cells on top of the latest
// stream state, and retries the batch with refreshed per-subject CAS
// expectations. e.playfield is NOT mutated here; the consumer applies the
// final committed state on echo.
//
// This is the only CAS path that retries. Player moves use publishProjectedRows
// (no retry, drop+rainbow flash on failure).
func (e *Engine) publishProjectedRowsWithMergeRetry(ctx context.Context, rows map[int]game.Row) {
	if len(rows) == 0 {
		return
	}
	playerID := e.effectivePlayerID()

	// Snapshot the cells we want to keep so we can re-project them on top of
	// refetched stream state. saved[r] is the row we originally projected.
	saved := make(map[int][]game.Cell, len(rows))
	for r, row := range rows {
		cellsCopy := make([]game.Cell, len(row.Cells))
		copy(cellsCopy, row.Cells)
		saved[r] = cellsCopy
	}

	// First attempt uses in-memory LastSeq.
	updates, err := e.buildBatchUpdates(playerID, rows)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}
	if err := natspkg.PublishMoveAtomically(ctx, e.js, e.gameID, updates); err == nil {
		return
	} else if !errors.Is(err, natspkg.ErrCASFailure) {
		log.Printf("publish batch: %v", err)
		return
	}

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		merged, ok := e.refetchAndMerge(ctx, playerID, saved)
		if !ok {
			return
		}
		err := natspkg.PublishMoveAtomically(ctx, e.js, e.gameID, merged)
		if err == nil {
			return
		}
		if !errors.Is(err, natspkg.ErrCASFailure) {
			log.Printf("publish batch retry: %v", err)
			return
		}
	}
	log.Printf("publish batch: gave up after %d retries", maxRetries)
}

// refetchAndMerge fetches the latest stream message for each row in saved,
// overlays the saved cells on top, and returns a fresh batch with refreshed
// per-subject CAS expectations. Used by the merge-retry path.
func (e *Engine) refetchAndMerge(ctx context.Context, playerID string, saved map[int][]game.Cell) ([]natspkg.RowUpdate, bool) {
	stream, sErr := e.js.Stream(ctx, config.GameStream(e.gameID))
	if sErr != nil {
		return nil, false
	}
	merged := make([]natspkg.RowUpdate, 0, len(saved))
	for r, cells := range saved {
		subject := config.RowSubject(e.gameID, playerID, r)
		msg, gErr := stream.GetLastMsgForSubject(ctx, subject)
		if gErr != nil {
			return nil, false
		}
		latestRow, uErr := game.UnmarshalRow(msg.Data)
		if uErr != nil {
			return nil, false
		}
		// Overlay our saved cells. We only re-place cells that are OURS
		// (active for this player) or new locked cells (Occupied && !Active);
		// other cells from the latest stream state are preserved.
		for i, sc := range cells {
			if i >= len(latestRow.Cells) {
				break
			}
			if sc.Active && sc.PlayerIdx == e.playerIdx {
				latestRow.Cells[i] = sc
			} else if sc.Occupied && !sc.Active {
				latestRow.Cells[i] = sc
			}
		}
		data, mErr := latestRow.Marshal()
		if mErr != nil {
			return nil, false
		}
		merged = append(merged, natspkg.RowUpdate{
			Row:           r,
			PlayerID:      playerID,
			Payload:       data,
			ExpectLastSeq: msg.Sequence,
		})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Row < merged[j].Row })
	return merged, true
}

// emitLocalCASFlash signals the LOCAL player that their last move was
// rejected by per-subject CAS — typically because another writer (in coop
// mode) updated one of the rows the move touched. It pushes an EngineUpdate
// directly to e.Updates without publishing to NATS: a CAS failure is
// information for the player who authored the move, not for the other
// players. The player must retry the input themselves.
func (e *Engine) emitLocalCASFlash() {
	e.mu.Lock()
	var flashCells [][2]int
	if p := e.playfield.ActivePieceForPlayer(e.playerIdx); p != nil {
		flashCells = p.Cells()
	}
	e.mu.Unlock()
	if len(flashCells) == 0 {
		return
	}
	e.emitUpdate(EngineUpdate{
		Kind:           UpdateCASFlash,
		FlashCells:     flashCells,
		FlashPlayerIdx: e.playerIdx,
	})
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
