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
	rows, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, e.rowSubjects(e.playfield.Height))
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
	go e.runConsumer(ctx, e.playfield, e.rowFilterSubject(), "", maxSeq+1, false)

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

// BoardSnapshot is an immutable, deep-copied view of a playfield plus the render
// dimensions a UI needs. Unlike Playfield(), the rows are clones taken under
// e.mu, so a UI goroutine can read them while the consumer mutates the live
// playfield — no data race.
type BoardSnapshot struct {
	Width        int
	Height       int
	VisibleStart int
	Rows         []game.Row
}

// Snapshot returns a race-free deep copy of the local playfield for rendering.
func (e *Engine) Snapshot() BoardSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return BoardSnapshot{
		Width:        e.playfield.Width,
		Height:       e.playfield.Height,
		VisibleStart: e.visibleRowStart,
		Rows:         game.CloneRows(e.playfield.Rows),
	}
}

// OpponentSnapshots returns race-free deep copies of all opponent playfields,
// keyed by playerID (competitive mode).
func (e *Engine) OpponentSnapshots() map[string]BoardSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]BoardSnapshot, len(e.opponentPlayfields))
	for k, pf := range e.opponentPlayfields {
		out[k] = BoardSnapshot{
			Width:        pf.Width,
			Height:       pf.Height,
			VisibleStart: e.visibleRowStart,
			Rows:         game.CloneRows(pf.Rows),
		}
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

	oppSubjects := make([]string, pf.Height)
	for i := range oppSubjects {
		oppSubjects[i] = config.CompetitiveRowSubject(e.gameID, oppID, i)
	}
	oppRows, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, oppSubjects)
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

	go e.runConsumer(ctx, pf, config.CompetitiveRowSubjectFilter(e.gameID, oppID), oppID, oppMaxSeq+1, true)
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

// rowSubject returns the subject for one of THIS engine's own playfield rows,
// using the subject scheme for the engine's game mode: cooperative shares a
// single board with no player token (ownership lives in the payload via
// Cell.PlayerIdx), competitive scopes the board by the player's ID.
func (e *Engine) rowSubject(row int) string {
	if e.gameMode == config.ModeCooperative {
		return config.CoopRowSubject(e.gameID, row)
	}
	return config.CompetitiveRowSubject(e.gameID, e.playerID, row)
}

// rowFilterSubject returns the wildcard filter matching all of this engine's
// own playfield rows.
func (e *Engine) rowFilterSubject() string {
	if e.gameMode == config.ModeCooperative {
		return config.CoopRowSubjectFilter(e.gameID)
	}
	return config.CompetitiveRowSubjectFilter(e.gameID, e.playerID)
}

// rowSubjects returns the subjects for this engine's own playfield rows
// 0..height-1, used to fetch the full playfield snapshot in one round trip.
func (e *Engine) rowSubjects(height int) []string {
	subjects := make([]string, height)
	for i := range subjects {
		subjects[i] = e.rowSubject(i)
	}
	return subjects
}

// sortedRowKeys returns the keys of a row-indexed map in publish/apply order.
// bottomFirst yields descending row indices (used for hard drops and downward
// moves so the consumer applies landing rows before vacated ones — see the
// publish helpers); otherwise ascending.
func sortedRowKeys[V any](m map[int]V, bottomFirst bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if bottomFirst {
			return keys[i] > keys[j]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (e *Engine) emitUpdate(u EngineUpdate) {
	select {
	case e.Updates <- u:
	default:
	}
}

// emitFullBoardRerender triggers a re-render of EVERY visible row from the
// current (consumer-converged) e.playfield. Used after bulk changes — line
// clears and shrinks — that republish the whole visible range. The UI always
// renders from e.playfield, so the row list is only a "which rows to repaint"
// hint; covering all visible rows in a single update guarantees the board
// reflects the new state even if individual per-row update triggers were
// dropped by the lossy Updates fan-out. visibleRowStart/Height are immutable
// after Start, so this is safe to call without holding e.mu.
func (e *Engine) emitFullBoardRerender() {
	rows := make([]int, 0, e.playfield.Height-e.visibleRowStart)
	for r := e.visibleRowStart; r < e.playfield.Height; r++ {
		rows = append(rows, r)
	}
	e.emitUpdate(EngineUpdate{Kind: UpdateLineClear, ChangedRows: rows})
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
	// Cells to flash if this spawn is ultimately dropped by CAS.
	flashCells := p.Cells()
	if e.gameMode == config.ModeCooperative {
		// In coop both players write the same shared row subjects, so two
		// near-simultaneous spawns (e.g. when meta transitions to
		// in_progress) race for the same headroom rows. Spawning MUST
		// succeed — the loser must not be left without a piece — so we
		// merge-retry on CAS failure: refetch the latest row from the
		// stream, overlay our piece cells on top, retry. This is the only
		// CAS path in the engine that retries; player moves never do.
		e.publishProjectedRowsWithMergeRetry(ctx, rows, flashCells, false)
		return
	}
	// Competitive: each player writes their own subjects, so a race is
	// extremely unlikely, but if CAS ever does reject the spawn we flash too.
	// A spawn places a brand-new piece (no old cells to clear), so ordering is
	// irrelevant: bottomFirst=false.
	e.publishProjectedRows(ctx, rows, flashCells, false)
}

func (e *Engine) PlayerIdx() int       { return e.playerIdx }
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
// rainbow flash on flashCells (the dropped step's cells, precomputed by the
// caller under e.mu) so they know the step was lost. This fires for every dropped
// CAS write — player moves, gravity ticks, and spawns alike. Pass nil flashCells
// to suppress the flash.
//
// bottomFirst MUST be true for downward moves: a piece that occupies a single row
// (the horizontal I) relocates by clearing its old row and setting its new row in
// separate messages. If the old row is applied first, the consumer briefly sees the
// player with NO active cells and fires a spurious lock-in (replacing the piece
// with the next one). Applying the lower (new) row first keeps at least one active
// cell present throughout the relocate. Multi-row pieces overlap rows when moving
// down, so they are unaffected, but ordering bottom-first is harmless for them.
func (e *Engine) publishProjectedRows(ctx context.Context, rows map[int]game.Row, flashCells [][2]int, bottomFirst bool) {
	if len(rows) == 0 {
		return
	}
	updates, err := e.buildBatchUpdates(rows, bottomFirst)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}

	if err := natspkg.PublishMoveAtomically(ctx, e.js, updates); err == nil {
		return
	} else if !errors.Is(err, natspkg.ErrCASFailure) {
		log.Printf("publish batch: %v", err)
		return
	}

	// CAS failure: drop the step. Signal the local player with a rainbow flash
	// on the dropped cells. We do NOT publish anything to the other players —
	// a CAS failure is information for the local player only.
	e.emitCASFlash(flashCells)
}

// publishProjectedRowsNoCAS publishes pre-computed rows as a SINGLE atomic
// batch without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, line-clear, shrink) where the publisher's view is the
// new ground truth. Either every row commits or none does.
//
// applyBottomFirst controls the order rows are written (and therefore the order
// the consumer applies them, since the ordered consumer replays by stream
// sequence). It MUST be true for HARD DROPS: a hard drop teleports the piece, so
// the published batch clears the piece's old (higher up = lower-index) active
// cells AND sets its new locked cells lower down (higher-index). The consumer
// detects lock-in the instant the player's last active cell disappears, and the
// completion check (handleLockIn → CompletedRows) runs then. If the vacated
// old-position rows were applied first, lock-in would fire before the landing
// rows were applied and a line completed by the drop would be missed until the
// next piece locked. Writing the bottom (landing) rows first guarantees the
// completed row is in place before lock-in fires. For in-place locks the order
// is irrelevant.
func (e *Engine) publishProjectedRowsNoCAS(ctx context.Context, rows map[int]game.Row, applyBottomFirst bool) {
	if len(rows) == 0 {
		return
	}
	updates := make([]natspkg.RowUpdate, 0, len(rows))
	for _, r := range sortedRowKeys(rows, applyBottomFirst) {
		data, err := rows[r].Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			return
		}
		updates = append(updates, natspkg.RowUpdate{
			Subject: e.rowSubject(r),
			Payload: data,
		})
	}
	if err := natspkg.PublishRowsAtomicallyNoCAS(ctx, e.js, updates); err != nil {
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
	updates := make([]natspkg.RowUpdate, 0, toRow-fromRow)
	for r := fromRow; r < toRow && r < len(rows); r++ {
		data, err := rows[r].Marshal()
		if err != nil {
			log.Printf("marshal row %d: %v", r, err)
			return
		}
		updates = append(updates, natspkg.RowUpdate{
			Subject: e.rowSubject(r),
			Payload: data,
		})
	}
	if err := natspkg.PublishRowsAtomicallyNoCAS(ctx, e.js, updates); err != nil {
		log.Printf("publish slice batch (no-cas): %v", err)
	}
}

// buildBatchUpdates converts a row projection map into a RowUpdate slice in
// apply order (see sortedRowKeys) with per-subject CAS expectations sourced from
// e.playfield.LastSeq. Each row's subject is built with the engine's
// mode-appropriate scheme.
func (e *Engine) buildBatchUpdates(rows map[int]game.Row, bottomFirst bool) ([]natspkg.RowUpdate, error) {
	updates := make([]natspkg.RowUpdate, 0, len(rows))
	for _, r := range sortedRowKeys(rows, bottomFirst) {
		data, err := rows[r].Marshal()
		if err != nil {
			return nil, err
		}
		updates = append(updates, natspkg.RowUpdate{
			Subject:       e.rowSubject(r),
			Payload:       data,
			ExpectLastSeq: e.playfield.LastSeq[r],
		})
	}
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
// This is the CAS path that retries. Player moves use publishProjectedRows
// (no retry, drop+rainbow flash on failure). If every retry is exhausted the step
// is effectively dropped, so we flash flashCells (precomputed by the caller under
// e.mu) — same feedback the player gets for any other dropped CAS write.
//
// In coop this path is used for ALL shared-row writes (spawn, gravity, lock,
// hard-drop, line-clear) so a stale local snapshot can never clobber the other
// player's mid-flight piece: CAS rejects a stale batch, and the merge re-applies
// only OUR cells on top of the latest stream state. bottomFirst controls row apply
// order for hard drops (see buildBatchUpdates / publishProjectedRowsNoCAS).
func (e *Engine) publishProjectedRowsWithMergeRetry(ctx context.Context, rows map[int]game.Row, flashCells [][2]int, bottomFirst bool) {
	if len(rows) == 0 {
		return
	}
	// Snapshot the cells we want to keep so we can re-project them on top of
	// refetched stream state. saved[r] is the row we originally projected.
	saved := make(map[int][]game.Cell, len(rows))
	for r, row := range rows {
		cellsCopy := make([]game.Cell, len(row.Cells))
		copy(cellsCopy, row.Cells)
		saved[r] = cellsCopy
	}

	// First attempt uses in-memory LastSeq.
	updates, err := e.buildBatchUpdates(rows, bottomFirst)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}
	if err := natspkg.PublishMoveAtomically(ctx, e.js, updates); err == nil {
		return
	} else if !errors.Is(err, natspkg.ErrCASFailure) {
		log.Printf("publish batch: %v", err)
		return
	}

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		merged, ok := e.refetchAndMerge(ctx, saved, bottomFirst)
		if !ok {
			return
		}
		err := natspkg.PublishMoveAtomically(ctx, e.js, merged)
		if err == nil {
			return
		}
		if !errors.Is(err, natspkg.ErrCASFailure) {
			log.Printf("publish batch retry: %v", err)
			return
		}
	}
	log.Printf("publish batch: gave up after %d retries", maxRetries)
	// Step dropped after exhausting retries — flash the local player.
	e.emitCASFlash(flashCells)
}

// refetchAndMerge fetches the latest stream message for each row in saved,
// overlays the saved cells on top, and returns a fresh batch with refreshed
// per-subject CAS expectations. Used by the merge-retry path.
func (e *Engine) refetchAndMerge(ctx context.Context, saved map[int][]game.Cell, bottomFirst bool) ([]natspkg.RowUpdate, bool) {
	stream, sErr := e.js.Stream(ctx, config.GameStream(e.gameID))
	if sErr != nil {
		return nil, false
	}
	merged := make([]natspkg.RowUpdate, 0, len(saved))
	for _, r := range sortedRowKeys(saved, bottomFirst) {
		cells := saved[r]
		subject := e.rowSubject(r)
		msg, gErr := stream.GetLastMsgForSubject(ctx, subject)
		if gErr != nil {
			return nil, false
		}
		latestRow, uErr := game.UnmarshalRow(msg.Data)
		if uErr != nil {
			return nil, false
		}
		// Re-apply OUR change on top of the latest stream state, preserving the
		// other player's cells. First VACATE our previous active cells from the
		// latest row (our move/lock/drop cleared them) — without this, our old
		// position lingers and the piece ghosts. Then overlay our new active
		// cells and any locked cells we are placing. Cells we don't touch
		// (notably the other player's active piece) are kept from latestRow.
		for i := range latestRow.Cells {
			if latestRow.Cells[i].Active && latestRow.Cells[i].PlayerIdx == e.playerIdx {
				latestRow.Cells[i] = game.Cell{}
			}
		}
		for i, sc := range cells {
			if i >= len(latestRow.Cells) {
				break
			}
			// Never overwrite the other player's mid-flight (active) cell — their
			// piece is authoritative to them. This matters for line clears, whose
			// shift would otherwise drop a locked cell onto their active piece.
			if latestRow.Cells[i].Active && latestRow.Cells[i].PlayerIdx != e.playerIdx {
				continue
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
			Subject:       subject,
			Payload:       data,
			ExpectLastSeq: msg.Sequence,
		})
	}
	return merged, true
}

// emitCASFlash signals the LOCAL player that a write was rejected by per-subject
// CAS and the affected move/spawn/gravity step was dropped — typically because
// another writer (in coop mode) updated one of the rows it touched. It fires for
// ANY dropped CAS write during gameplay, not only player-initiated moves, so the
// player gets consistent feedback whenever contention causes a step to be lost.
//
// flashCells are the cells to highlight; the caller computes them while holding
// e.mu (the publish helpers run with the lock released, and spawnPiece may invoke
// them while the consumer already holds e.mu — so this method must NOT take the
// lock). It pushes an EngineUpdate directly to e.Updates without publishing to
// NATS: a CAS failure is information for the local player, not the others.
// Spectators never flash.
func (e *Engine) emitCASFlash(flashCells [][2]int) {
	if e.mode != ModePlayer || len(flashCells) == 0 {
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
