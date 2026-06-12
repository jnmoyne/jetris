package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	initialMode Mode // original mode at creation (ModePlayer or ModeSpectator)
	playerIdx   int  // 0 for creator, 1 for joiner; used on shared boards for Cell.PlayerIdx
	playerCount int  // number of players in the game
	teamIdx     int  // teams mode: which team this player is on (0 = A, 1 = B)
	teamSlot    int  // teams mode: section index within the team board (spawn column offset)
	teamSize    int  // teams mode: players per team (from meta at Start)

	// mode/score/level/totalLines/pieceIdx are read and written from several
	// goroutines (the row/meta/events consumers, runInput, the UI, tests) with
	// no single lock covering every path — transitionToSpectator in particular
	// runs both under and without e.mu — so they are atomic rather than e.mu-
	// guarded. e.mu still guards the structured state (playfield, the maps).
	mode atomic.Int32 // current Mode

	mu        sync.Mutex
	playfield *game.Playfield

	opponentPlayfields map[string]*game.Playfield // keyed by playerID (competitive)
	opponentPlayerID   string                     // single opponent (for 2-player join)

	seq      *rng.Sequence
	pieceIdx atomic.Uint64
	metaSeq  uint64

	score             atomic.Int64
	totalLines        atomic.Int64
	level             atomic.Int64
	hadActivePiece    bool            // only touched by the own-rows consumer goroutine
	eliminatedPlayers map[string]bool // players who have topped out (competitive/teams); guarded by e.mu
	eliminatedTeam    map[string]int  // teams: eliminated player → team; guarded by e.mu
	teamOutcomeDone   bool            // teams: win/loss/draw already decided; guarded by e.mu
	expectedGarbage   int             // teams: cumulative adversarial rows owed to this team's board; guarded by e.mu
	visibleRowStart   int             // first visible row index (varies per game mode/player count)

	Updates        chan EngineUpdate
	OnGameFinished func() // called after game transitions to finished (for archiving)

	js          jetstream.JetStream
	ctx         context.Context
	cancelFn    context.CancelFunc
	moves       chan MoveType
	cellUpdated chan struct{}

	// Ping measurement (see ping.go): time from initiating a batch publish to
	// the consumer delivering the batch's first message back.
	pingMu      sync.Mutex
	pingPending map[uint64]time.Time // batch first-seq → publish start time
	lastEchoSeq uint64               // highest own-board seq seen by the consumer; guarded by pingMu
	pingNanos   atomic.Int64         // latest measurement, for Ping()
}

// New creates a new engine instance. Call Start() to begin. teamIdx/teamSlot
// come from lobby.JoinGame's JoinResult and are only meaningful in teams mode
// (spectators and other modes pass 0, 0).
func New(
	js jetstream.JetStream,
	gameID, playerID, opponentPlayerID string,
	gameMode config.GameMode,
	mode Mode,
	playerIdx, teamIdx, teamSlot int,
) *Engine {
	e := &Engine{
		gameID:             gameID,
		playerID:           playerID,
		opponentPlayerID:   opponentPlayerID,
		gameMode:           gameMode,
		initialMode:        mode,
		playerIdx:          playerIdx,
		teamIdx:            teamIdx,
		teamSlot:           teamSlot,
		playfield:          game.NewPlayfield(config.StandardWidth),
		opponentPlayfields: make(map[string]*game.Playfield),
		Updates:            make(chan EngineUpdate, 64),
		js:                 js,
		moves:              make(chan MoveType, 8),
		cellUpdated:        make(chan struct{}, 1),
		eliminatedPlayers:  make(map[string]bool),
		eliminatedTeam:     make(map[string]int),
		pingPending:        make(map[uint64]time.Time),
	}
	e.setMode(mode)
	return e
}

// Start begins all consumer goroutines and (if ModePlayer) the combined
// input+gravity goroutine.
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
	e.teamSize = meta.TeamSize

	// Set visible row start based on mode
	switch e.gameMode {
	case config.ModeCompetitive:
		e.visibleRowStart = config.CompetitiveVisibleRowStart(meta.PlayerCount)
	case config.ModeTeams:
		e.visibleRowStart = config.TeamVisibleRowStart(meta.TeamSize)
	default:
		e.visibleRowStart = config.VisibleRowStart
	}

	// playerIdx was supplied by the caller (lobby.JoinGame return value)
	// at engine construction time; no discovery needed here.

	switch e.gameMode {
	case config.ModeCooperative:
		// Cooperative mode: shared wide playfield, shared RNG seed
		e.seq = rng.New(meta.Seed)
		e.pieceIdx.Store(0)
		// Shared wide playfield with standard height
		e.playfield = game.NewPlayfieldWithHeight(
			meta.PlayerCount*config.StandardWidth,
			config.HeadroomRows+config.VisibleRows,
		)
	case config.ModeTeams:
		// Teams: shared per-team board, coop-style RNG (every player gets the
		// full deterministic 7-bag from the shared seed with an independent
		// pieceIdx, so both teams see the identical, fair piece sequence)
		e.seq = rng.New(meta.Seed)
		e.pieceIdx.Store(0)
		e.playfield = game.NewPlayfieldWithHeight(
			config.TeamBoardWidth(meta.TeamSize),
			config.TeamTotalRows(meta.TeamSize),
		)
	default:
		e.seq = rng.New(meta.Seed)
		e.pieceIdx.Store(meta.PieceIdx)
		// Competitive: taller playfield (extra rows per player)
		e.playfield = game.NewPlayfieldWithHeight(
			config.StandardWidth,
			config.CompetitiveTotalRows(meta.PlayerCount),
		)
	}
	e.metaSeq = metaSeq

	// 2. Fetch playfield state (one message per cell; never-written cells are
	// simply absent and stay empty)
	cells, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, e.cellSubjects())
	if err != nil {
		cancel()
		return err
	}
	var maxSeq uint64
	for _, c := range cells {
		data, _ := game.UnmarshalCell(c.Payload)
		e.playfield.Apply(c.Row, c.Col, data, c.Seq)
		if c.Seq > maxSeq {
			maxSeq = c.Seq
		}
	}

	// Check if there's already an active piece for this player
	e.hadActivePiece = e.playfield.ActivePieceForPlayer(e.playerIdx) != nil

	// 3. Start cell consumer
	go e.runConsumer(ctx, e.playfield, e.cellFilterSubject(), "", maxSeq+1, false)

	// 4. Competitive: set up known opponent and discover others via roster
	if e.gameMode == config.ModeCompetitive {
		if e.opponentPlayerID != "" {
			e.startOpponentConsumer(ctx, e.opponentPlayerID)
		}
		// Always run roster consumer to discover all opponents
		go e.runRosterConsumer(ctx)
	}

	// Teams: one consumer over the opposing team's shared board (rendered in
	// the opponent sidebar). The roster is fixed before the game starts and
	// elimination events carry the player's team, so no roster consumer is
	// needed. Spectators consume team 0 as their "own" board (teamIdx defaults
	// to 0) and team 1 here.
	if e.gameMode == config.ModeTeams {
		e.startTeamBoardConsumer(ctx, 1-e.teamIdx)
	}

	// 6. Start events consumer and meta consumer
	go e.runEventsConsumer(ctx)
	go e.runMetaConsumer(ctx)
	go e.runCountdownConsumer(ctx)

	// 7. Start the combined input+gravity goroutine if playing. Gravity and
	// player input share one goroutine so a player's own gravity drop and move
	// never publish to their cell subjects concurrently and lose the per-subject
	// CAS race (in either game mode).
	if e.getMode() == ModePlayer {
		// If game is already in progress and no active piece, spawn immediately
		if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil && meta.Status == config.GameStatusInProgress {
			e.spawnPiece(ctx, false) // Start holds no lock
		}
		go e.runInput(ctx)
	}

	return nil
}

// Stop tears down all goroutines.
func (e *Engine) Stop() {
	if e.cancelFn != nil {
		e.cancelFn()
	}
}

// Playfield returns a race-free deep copy of the current playfield, taken under
// e.mu so callers (UI, tests) can read it while the consumer and publish
// write-through keep mutating the live playfield.
func (e *Engine) Playfield() *game.Playfield {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.playfield.Clone()
}

// OpponentPlayfields returns race-free deep copies of all opponent playfields
// keyed by playerID.
func (e *Engine) OpponentPlayfields() map[string]*game.Playfield {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]*game.Playfield, len(e.opponentPlayfields))
	for k, v := range e.opponentPlayfields {
		out[k] = v.Clone()
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

// TeamBoardKey is the opponentPlayfields/OpponentSnapshots key under which a
// team board consumer files the given team's board (teams mode).
func TeamBoardKey(team int) string { return "team-" + strconv.Itoa(team) }

// startTeamBoardConsumer creates a playfield and consumer for the given team's
// shared board (teams mode). It is the team-board analog of
// startOpponentConsumer and files the board in opponentPlayfields under
// TeamBoardKey(team) so OpponentSnapshots flows to the UI unchanged.
func (e *Engine) startTeamBoardConsumer(ctx context.Context, team int) {
	key := TeamBoardKey(team)
	e.mu.Lock()
	if _, exists := e.opponentPlayfields[key]; exists {
		e.mu.Unlock()
		return
	}
	pf := game.NewPlayfieldWithHeight(config.TeamBoardWidth(e.teamSize), config.TeamTotalRows(e.teamSize))
	e.opponentPlayfields[key] = pf
	e.mu.Unlock()

	subjects := make([]string, 0, pf.Height*pf.Width)
	for r := 0; r < pf.Height; r++ {
		for c := 0; c < pf.Width; c++ {
			subjects = append(subjects, config.TeamCellSubject(e.gameID, team, r, c))
		}
	}
	cells, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, subjects)
	if err != nil {
		log.Printf("fetch team %d board state: %v", team, err)
		return
	}
	var maxSeq uint64
	for _, c := range cells {
		data, _ := game.UnmarshalCell(c.Payload)
		e.mu.Lock()
		pf.Apply(c.Row, c.Col, data, c.Seq)
		e.mu.Unlock()
		if c.Seq > maxSeq {
			maxSeq = c.Seq
		}
	}

	go e.runConsumer(ctx, pf, config.TeamCellSubjectFilter(e.gameID, team), key, maxSeq+1, true)
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

	oppSubjects := make([]string, 0, pf.Height*pf.Width)
	for r := 0; r < pf.Height; r++ {
		for c := 0; c < pf.Width; c++ {
			oppSubjects = append(oppSubjects, config.CompetitiveCellSubject(e.gameID, oppID, r, c))
		}
	}
	oppCells, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, oppSubjects)
	if err != nil {
		log.Printf("fetch opponent %s state: %v", oppID, err)
		return
	}
	var oppMaxSeq uint64
	for _, c := range oppCells {
		data, _ := game.UnmarshalCell(c.Payload)
		e.mu.Lock()
		pf.Apply(c.Row, c.Col, data, c.Seq)
		e.mu.Unlock()
		if c.Seq > oppMaxSeq {
			oppMaxSeq = c.Seq
		}
	}

	go e.runConsumer(ctx, pf, config.CompetitiveCellSubjectFilter(e.gameID, oppID), oppID, oppMaxSeq+1, true)
}

func (e *Engine) GameID() string            { return e.gameID }
func (e *Engine) PlayerID() string          { return e.playerID }
func (e *Engine) Score() int                { return int(e.score.Load()) }
func (e *Engine) Level() int                { return int(e.level.Load()) }
func (e *Engine) Mode() Mode                { return e.getMode() }
func (e *Engine) GameMode() config.GameMode { return e.gameMode }

// getMode/setMode read and write the atomic mode field.
func (e *Engine) getMode() Mode  { return Mode(e.mode.Load()) }
func (e *Engine) setMode(m Mode) { e.mode.Store(int32(m)) }

func (e *Engine) MoveLeft()  { e.dispatch(MoveLeft) }
func (e *Engine) MoveRight() { e.dispatch(MoveRight) }
func (e *Engine) MoveDown()  { e.dispatch(MoveDown) }
func (e *Engine) RotateCW()  { e.dispatch(RotateCW) }
func (e *Engine) RotateCCW() { e.dispatch(RotateCCW) }
func (e *Engine) HardDrop()  { e.dispatch(MoveHardDrop) }

// dispatch hands a player input to the engine's single input goroutine. Inputs
// are SERIALIZED and BUFFERED: they queue on the buffered e.moves channel and
// runInput processes them one at a time, and because each move's publish blocks
// on its batch commit ack (and applies the write-through) before the next move
// is dequeued, a new move issued while the previous one is still awaiting its
// commit ack waits in the buffer — the engine never has two of a player's input
// batches in flight at once. The non-blocking send means that if a player
// somehow outruns the ack round-trip by more than the buffer depth the excess
// input is dropped rather than blocking the UI goroutine (never reached at human
// input rates).
func (e *Engine) dispatch(m MoveType) {
	if e.getMode() != ModePlayer {
		return
	}
	select {
	case e.moves <- m:
	default:
	}
}

// sharedBoard reports whether this engine's own playfield is shared with other
// players (cooperative's single board, or a team's board in teams mode). Shared
// boards use Cell.PlayerIdx ownership, coop collision (CanPlaceCoop), and
// merge-retry for engine-driven writes.
func (e *Engine) sharedBoard() bool {
	return e.gameMode == config.ModeCooperative || e.gameMode == config.ModeTeams
}

// cellSubject returns the subject for one cell of THIS engine's own playfield,
// using the subject scheme for the engine's game mode: cooperative shares a
// single board with no player token (ownership lives in the payload via
// Cell.PlayerIdx), teams shares a board scoped by team index, competitive
// scopes the board by the player's ID.
func (e *Engine) cellSubject(row, col int) string {
	switch e.gameMode {
	case config.ModeCooperative:
		return config.CoopCellSubject(e.gameID, row, col)
	case config.ModeTeams:
		return config.TeamCellSubject(e.gameID, e.teamIdx, row, col)
	}
	return config.CompetitiveCellSubject(e.gameID, e.playerID, row, col)
}

// cellFilterSubject returns the wildcard filter matching all of this engine's
// own playfield cells.
func (e *Engine) cellFilterSubject() string {
	switch e.gameMode {
	case config.ModeCooperative:
		return config.CoopCellSubjectFilter(e.gameID)
	case config.ModeTeams:
		return config.TeamCellSubjectFilter(e.gameID, e.teamIdx)
	}
	return config.CompetitiveCellSubjectFilter(e.gameID, e.playerID)
}

// cellSubjects returns the subjects for every cell of this engine's own
// playfield (row-major), used to fetch the full playfield snapshot.
func (e *Engine) cellSubjects() []string {
	subjects := make([]string, 0, e.playfield.Height*e.playfield.Width)
	for r := 0; r < e.playfield.Height; r++ {
		for c := 0; c < e.playfield.Width; c++ {
			subjects = append(subjects, e.cellSubject(r, c))
		}
	}
	return subjects
}

// cellCategory ranks a cell's NEW content for publish ordering: active cells
// first, locked/occupied cells second, empty (vacate) cells last. See
// orderedCellKeys for why.
func cellCategory(c game.Cell) int {
	switch {
	case c.Active:
		return 0
	case c.Occupied:
		return 1
	default:
		return 2
	}
}

// orderedCellKeys returns the keys of a cell-projection map in publish/apply
// order: by category of the cell's NEW content (active, then locked, then
// empty), tie-broken by ascending (row, col) for determinism.
//
// The ordered consumer applies a batch's messages one at a time, and lock-in
// fires the instant a player's active-cell count hits zero — so the order
// within a batch matters even though the batch commits atomically. This single
// rule keeps two invariants for every write path:
//
//   - A relocating piece (gravity, lateral move, rotation, hard drop with the
//     piece staying active) never transiently has ZERO active cells: its new
//     active cells are all applied before its old positions are vacated, so no
//     spurious lock-in fires (this covers the single-row horizontal I, which
//     has no overlap between old and new footprints).
//   - A lock (in-place or hard drop) fires lock-in exactly once, at the LAST
//     message that removes the player's final active cell — by which point all
//     locked/landing cells are already applied, so a line completed by the
//     lock is detected at that lock, not one piece later.
//
// The same argument covers a coop line clear (the other player's shifted
// active piece is applied before its old cells are vacated) and a competitive
// shrink (the re-stamped piece first, the rising stack second, vacates last).
func orderedCellKeys(m map[game.CellPos]game.Cell) []game.CellPos {
	keys := make([]game.CellPos, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := cellCategory(m[keys[i]]), cellCategory(m[keys[j]])
		if ci != cj {
			return ci < cj
		}
		if keys[i].Row != keys[j].Row {
			return keys[i].Row < keys[j].Row
		}
		return keys[i].Col < keys[j].Col
	})
	return keys
}

// diffCells returns the cells of a row projection that differ from the live
// board — only those are published, so a move costs ~4-8 cell messages (the
// new footprint plus the vacated old positions) instead of whole rows. Call
// with e.mu held (it reads the live cur rows).
func diffCells(cur []game.Row, projected map[int]game.Row) map[game.CellPos]game.Cell {
	out := make(map[game.CellPos]game.Cell)
	for r, row := range projected {
		if r < 0 || r >= len(cur) {
			continue
		}
		for c := range row.Cells {
			if c >= len(cur[r].Cells) {
				break
			}
			if row.Cells[c] != cur[r].Cells[c] {
				out[game.CellPos{Row: r, Col: c}] = row.Cells[c]
			}
		}
	}
	return out
}

// changedCells returns the cells of projected[fromRow:toRow) whose content
// differs from cur. Used so a line clear (coop/competitive) or competitive
// shrink republishes only the cells that actually changed — a low stack
// changes only a handful — instead of the whole visible range, which on the
// shared coop board sharply cuts the per-subject CAS contention that was
// exhausting the merge-retry. Call with e.mu held (it reads the live cur rows).
func changedCells(cur, projected []game.Row, fromRow, toRow int) map[game.CellPos]game.Cell {
	out := make(map[game.CellPos]game.Cell)
	for r := fromRow; r < toRow && r < len(projected) && r < len(cur); r++ {
		for c := range projected[r].Cells {
			if c >= len(cur[r].Cells) {
				break
			}
			if projected[r].Cells[c] != cur[r].Cells[c] {
				out[game.CellPos{Row: r, Col: c}] = projected[r].Cells[c]
			}
		}
	}
	return out
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
	e.setMode(ModeGameOver)
	e.emitUpdate(EngineUpdate{Kind: UpdateGameOver, Won: won})
}

// spawnPiece publishes a freshly spawned piece. locked reports whether the
// caller already holds e.mu (handleLockIn and the meta consumer spawn under the
// lock; Start spawns with the lock released) so the publish write-through can
// avoid re-locking.
func (e *Engine) spawnPiece(ctx context.Context, locked bool) {
	pt := e.seq.Piece(e.pieceIdx.Load())
	p := game.SpawnPiece(pt, config.StandardWidth)

	// On a shared board, offset spawn column to the player's section: coop
	// sections are laid out by playerIdx, team boards by the slot within the team.
	switch e.gameMode {
	case config.ModeCooperative:
		p.Col += e.playerIdx * config.StandardWidth
	case config.ModeTeams:
		p.Col += e.teamSlot * config.StandardWidth
	}

	if !locked {
		e.mu.Lock()
	}

	// Check placement (shared boards check against other players' active pieces too)
	var canPlace bool
	if e.sharedBoard() {
		canPlace = game.CanPlaceCoop(p, e.playfield, e.playerIdx)
	} else {
		canPlace = game.CanPlace(p, e.playfield)
	}
	if !canPlace {
		// e.mu is held here in both cases (acquired above when !locked).
		e.handleTopOut(ctx, true)
		if !locked {
			e.mu.Unlock()
		}
		return
	}

	// Here we compute the projection, diff it to cells, and publish; the
	// publish write-through (applyPublishedCells) advances e.playfield on
	// commit, and the consumer sets hadActivePiece via the lock-in detector
	// when the echo arrives.
	affected := make([]int, 0, 4)
	seen := make(map[int]bool, 4)
	for _, c := range p.Cells() {
		if !seen[c[0]] {
			seen[c[0]] = true
			affected = append(affected, c[0])
		}
	}
	rows := e.playfield.ProjectMove(affected, &p, e.playerIdx)
	cells := diffCells(e.playfield.Rows, rows)
	// Cells to flash if this spawn is ultimately dropped by CAS.
	flashCells := p.Cells()
	if !locked {
		e.mu.Unlock()
	}
	if e.sharedBoard() {
		// On a shared board (coop, teams) all players write the same shared
		// cell subjects, so two near-simultaneous spawns (e.g. when meta
		// transitions to in_progress) race for the headroom cells. Spawning
		// MUST succeed — the loser must not be left without a piece — so we
		// merge-retry on CAS failure: refetch the latest cells from the
		// stream, keep ours where allowed, retry. This is the only
		// CAS path in the engine that retries; player moves never do.
		e.publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, locked)
		return
	}
	// Competitive: each player writes their own subjects, so a race is
	// extremely unlikely, but if CAS ever does reject the spawn we flash too.
	e.publishProjectedCells(ctx, cells, flashCells, locked)
}

func (e *Engine) PlayerIdx() int       { return e.playerIdx }
func (e *Engine) TeamIdx() int         { return e.teamIdx }
func (e *Engine) TeamSlot() int        { return e.teamSlot }
func (e *Engine) TeamSize() int        { return e.teamSize }
func (e *Engine) VisibleRowStart() int { return e.visibleRowStart }
func (e *Engine) PlayfieldHeight() int { return e.playfield.Height }
func (e *Engine) IsEliminated(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.eliminatedPlayers[id]
}

func (e *Engine) PieceIdx() uint64 { return e.pieceIdx.Load() }

// handleTopOut handles this player topping out. locked reports whether the
// caller already holds e.mu (spawnPiece always does at its top-out branch;
// applyOpponentShrink calls with the lock released).
func (e *Engine) handleTopOut(ctx context.Context, locked bool) {
	if e.gameMode == config.ModeTeams {
		e.handleTeamTopOut(ctx, locked)
		return
	}

	// Publish game over event with score and piece count
	ev := GameEvent{Kind: EventGameOver, PlayerID: e.playerID, Score: int(e.score.Load()), PieceCount: e.pieceIdx.Load()}
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

// handleTeamTopOut implements teams-mode per-player elimination: the topped-out
// player vacates any of their active cells from the shared team board and
// becomes a spectator, but the TEAM plays on. The team only loses when all its
// members have topped out (evaluated by every engine in handleGameEvent as the
// elimination events arrive); this engine therefore never transitions the game
// to finished here.
func (e *Engine) handleTeamTopOut(ctx context.Context, locked bool) {
	if !locked {
		e.mu.Lock()
	}
	// Clear hadActivePiece BEFORE publishing the vacate: the vacate drives our
	// active-cell count to zero and must not read as a lock-in (which would
	// spawn a next piece for an eliminated player).
	e.hadActivePiece = false
	var cells map[game.CellPos]game.Cell
	if p := e.playfield.ActivePieceForPlayer(e.playerIdx); p != nil {
		affected := affectedRowsUnion(p, nil)
		rows := e.playfield.ProjectMove(affected, nil, e.playerIdx)
		cells = diffCells(e.playfield.Rows, rows)
	}
	e.eliminatedPlayers[e.playerID] = true
	e.eliminatedTeam[e.playerID] = e.teamIdx
	if !locked {
		e.mu.Unlock()
	}

	// Vacate our piece so teammates don't play around a dead piece. Shared
	// board: merge-retry, whose skip rule protects teammates' in-flight pieces.
	if len(cells) > 0 {
		e.publishProjectedCellsWithMergeRetry(ctx, cells, nil, locked)
	}

	ev := GameEvent{
		Kind:       EventGameOver,
		PlayerID:   e.playerID,
		Team:       e.teamIdx,
		Score:      int(e.score.Load()),
		PieceCount: e.pieceIdx.Load(),
	}
	data, _ := json.Marshal(ev)
	_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)
	e.transitionToSpectator(false) // out for now; flips to won if our team prevails
	e.emitUpdate(EngineUpdate{
		Kind:               UpdatePlayerEliminated,
		EliminatedPlayerID: e.playerID,
		Team:               e.teamIdx,
	})
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

// publishProjectedCells publishes pre-computed cell payloads as a SINGLE atomic
// batch with per-subject CAS expectations sourced from e.playfield's per-cell
// LastSeq (Nats-Expected-Last-Subject-Sequence). Either every cell in the move
// commits or none does; consumers never see a torn intermediate state. Batch
// order follows orderedCellKeys (active, locked, empty) so the consumer never
// observes the piece with zero active cells mid-batch.
//
// On success the committed cells are written through into e.playfield via
// applyPublishedCells (content + the inferred per-subject sequences) so the next
// step is projected from up-to-date state; pf.Apply's strictly-higher-sequence
// rule makes the later consumer echo a no-op. On CAS failure the move is DROPPED
// in both competitive and cooperative modes; the player is signalled with a local-only
// rainbow flash on flashCells (the dropped step's cells, precomputed by the
// caller under e.mu) so they know the step was lost. This fires for every dropped
// CAS write — player moves, gravity ticks, and spawns alike. Pass nil flashCells
// to suppress the flash.
func (e *Engine) publishProjectedCells(ctx context.Context, cells map[game.CellPos]game.Cell, flashCells [][2]int, locked bool) {
	if len(cells) == 0 {
		return
	}
	keys := orderedCellKeys(cells)
	updates, err := e.buildBatchUpdates(keys, cells, locked)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}

	t0 := time.Now()
	seq, err := natspkg.PublishMoveAtomically(ctx, e.js, updates)
	if err == nil {
		// Write the committed cells straight into e.playfield (content + the
		// inferred per-subject sequences) so the next move/gravity tick is
		// projected from — and CAS-checked against — up-to-date state without
		// waiting for the consumer echo. keys is in the same order as updates.
		e.trackPing(t0, seq, len(updates))
		e.applyPublishedCells(keys, func(k game.CellPos) game.Cell { return cells[k] }, seq, locked)
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

// applyPublishedCells write-throughs a successfully committed batch into
// e.playfield: it advances both the board content (pf.Rows) AND the per-subject
// CAS expectation (pf.LastSeq) for the affected cells immediately, using the
// stream sequences inferred from the batch commit ack rather than waiting for
// the consumer to echo our own write back.
//
// orderedKeys lists the cell positions in the exact order the messages were
// added to the batch (commit message last); get returns each cell's committed
// content; commitSeq is the commit ack's stream sequence. The batch's N messages
// got consecutive stream sequences, so message i got commitSeq-(N-1-i).
// pf.Apply's stale-guard then makes the later consumer echo of the same sequence
// a no-op.
//
// This is what keeps a player from losing a per-subject CAS race against their
// OWN just-committed write (gravity vs. input, a write right after a NoCAS
// line-clear/shrink, etc.). locked reports whether the caller already holds
// e.mu: spawnPiece and the line-clear publish run under the consumer's lock;
// every other publish path runs with the lock released.
func (e *Engine) applyPublishedCells(orderedKeys []game.CellPos, get func(game.CellPos) game.Cell, commitSeq uint64, locked bool) {
	if commitSeq == 0 || len(orderedKeys) == 0 {
		return
	}
	if !locked {
		e.mu.Lock()
		defer e.mu.Unlock()
	}
	n := len(orderedKeys)
	for i, k := range orderedKeys {
		e.playfield.Apply(k.Row, k.Col, get(k), commitSeq-uint64(n-1-i))
	}
}

// publishProjectedCellsNoCAS publishes pre-computed cells as an atomic batch
// without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, line-clear, shrink) where the publisher's view is the
// new ground truth.
//
// Batch order follows orderedCellKeys: landing/locked cells are applied before
// the vacated old-position cells, so when lock-in fires (at the message that
// removes the player's last active cell) the landed cells are already in place
// and a line completed by the drop is detected at this lock, not one piece
// later.
//
// A batch larger than the server's atomic-batch limit (1000 messages — only
// reachable on degenerate many-player boards) is split into sequential atomic
// chunks along the already-ordered key list; the category order remains a
// correct total order across chunk boundaries, at the cost of a briefly
// visible intermediate board between chunks.
func (e *Engine) publishProjectedCellsNoCAS(ctx context.Context, cells map[game.CellPos]game.Cell, locked bool) {
	if len(cells) == 0 {
		return
	}
	keys := orderedCellKeys(cells)
	const maxBatchMsgs = 1000
	for start := 0; start < len(keys); start += maxBatchMsgs {
		chunk := keys[start:min(start+maxBatchMsgs, len(keys))]
		updates := make([]natspkg.CellUpdate, 0, len(chunk))
		for _, k := range chunk {
			data, err := cells[k].Marshal()
			if err != nil {
				log.Printf("marshal cell (%d,%d): %v", k.Row, k.Col, err)
				return
			}
			updates = append(updates, natspkg.CellUpdate{
				Subject: e.cellSubject(k.Row, k.Col),
				Payload: data,
			})
		}
		t0 := time.Now()
		seq, err := natspkg.PublishCellsAtomicallyNoCAS(ctx, e.js, updates)
		if err != nil {
			log.Printf("publish batch (no-cas): %v", err)
			return
		}
		e.trackPing(t0, seq, len(updates))
		e.applyPublishedCells(chunk, func(k game.CellPos) game.Cell { return cells[k] }, seq, locked)
	}
}

// buildBatchUpdates converts a cell projection map into a CellUpdate slice in
// the given key order (computed by the caller via orderedCellKeys) with
// per-subject CAS expectations sourced from e.playfield's per-cell LastSeq.
// Each cell's subject is built with the engine's mode-appropriate scheme.
//
// LastSeq is mutated by the consumer (and the publish write-through) under e.mu,
// so it is snapshotted under the lock — unless the caller already holds it
// (locked) — to give a race-free CAS expectation. The publish helpers run with
// e.mu released except spawn/clear (which pass locked=true).
func (e *Engine) buildBatchUpdates(keys []game.CellPos, cells map[game.CellPos]game.Cell, locked bool) ([]natspkg.CellUpdate, error) {
	expect := make([]uint64, len(keys))
	if !locked {
		e.mu.Lock()
	}
	for i, k := range keys {
		expect[i] = e.playfield.CellLastSeq(k.Row, k.Col)
	}
	if !locked {
		e.mu.Unlock()
	}
	updates := make([]natspkg.CellUpdate, 0, len(keys))
	for i, k := range keys {
		data, err := cells[k].Marshal()
		if err != nil {
			return nil, err
		}
		updates = append(updates, natspkg.CellUpdate{
			Subject:       e.cellSubject(k.Row, k.Col),
			Payload:       data,
			ExpectLastSeq: expect[i],
		})
	}
	return updates, nil
}

// publishProjectedCellsWithMergeRetry publishes a projection that MUST succeed
// even under concurrent writers — used by the coop paths where both players
// write the same shared cell subjects. On CAS failure, refetches the latest
// stream state of every affected cell and retries with our content kept where
// allowed and refreshed per-subject CAS expectations.
//
// This is the CAS path that retries. Player moves use publishProjectedCells
// (no retry, drop+rainbow flash on failure). If every retry is exhausted the step
// is effectively dropped, so we flash flashCells (precomputed by the caller under
// e.mu) — same feedback the player gets for any other dropped CAS write.
//
// In coop this path is used for ALL engine-driven shared-cell writes (spawn,
// gravity, lock, hard-drop, line-clear) so a stale local snapshot can never
// clobber the other player's mid-flight piece: CAS rejects a stale batch, and
// the merge keeps our cells except where the latest stream state holds the
// other player's active piece. Per-cell CAS makes contention much rarer than
// the old per-row scheme (two pieces in the same row no longer conflict — only
// writes to the SAME cell do), but the retry still guards spawn races and
// clear-vs-move races.
func (e *Engine) publishProjectedCellsWithMergeRetry(ctx context.Context, cells map[game.CellPos]game.Cell, flashCells [][2]int, locked bool) {
	if len(cells) == 0 {
		return
	}

	// First attempt uses in-memory LastSeq.
	keys := orderedCellKeys(cells)
	updates, err := e.buildBatchUpdates(keys, cells, locked)
	if err != nil {
		log.Printf("build batch: %v", err)
		return
	}
	t0 := time.Now()
	seq, err := natspkg.PublishMoveAtomically(ctx, e.js, updates)
	if err == nil {
		// First-attempt commit: write through the cells we published.
		e.trackPing(t0, seq, len(updates))
		e.applyPublishedCells(keys, func(k game.CellPos) game.Cell { return cells[k] }, seq, locked)
		return
	} else if !errors.Is(err, natspkg.ErrCASFailure) {
		log.Printf("publish batch: %v", err)
		return
	}

	const maxRetries = 16
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Escalating, per-player-offset backoff before re-fetching: it breaks
			// lockstep with the OTHER player's retry loop (so one wins each round
			// instead of both losing the CAS race repeatedly) and lets a contention
			// burst on the shared cells settle. Paid only while actually retrying —
			// the common case commits on the first attempt above. Without this, two
			// players hammering the same cells can exhaust the retries and drop the
			// step (a dropped spawn would leave a player stuck).
			backoff := time.Duration(attempt+e.playerIdx) * 200 * time.Microsecond
			if backoff > 2*time.Millisecond {
				backoff = 2 * time.Millisecond
			}
			time.Sleep(backoff)
		}
		merged, mergedCells, mergedKeys, ok := e.refetchAndMerge(ctx, keys, cells)
		if !ok {
			return
		}
		if len(merged) == 0 {
			// Every cell we wanted to write is currently covered by the other
			// player's mid-flight piece — nothing we may publish. Drop the step.
			log.Printf("publish batch: all cells blocked by other player's piece, step dropped")
			e.emitCASFlash(flashCells)
			return
		}
		t0 := time.Now()
		seq, err := natspkg.PublishMoveAtomically(ctx, e.js, merged)
		if err == nil {
			// Retry commit: write through the cells that actually committed.
			e.trackPing(t0, seq, len(merged))
			e.applyPublishedCells(mergedKeys, func(k game.CellPos) game.Cell { return mergedCells[k] }, seq, locked)
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

// refetchAndMerge fetches the latest stream message for every cell in keys (one
// batched round trip) and rebuilds the publish batch with refreshed per-subject
// CAS expectations. Used by the merge-retry path.
//
// The merge is per cell and therefore much simpler than the old per-row
// overlay: a cell's new content is wholly ours, EXCEPT where the latest stream
// state holds the OTHER player's mid-flight (active) cell — those cells are
// skipped entirely (we neither overwrite nor vacate their piece; this matters
// for line clears, whose shift would otherwise drop a locked cell onto their
// piece). A cell with no stream message yet is an empty cell with CAS
// expectation 0. The returned key order preserves the caller's category order
// minus the skipped cells.
func (e *Engine) refetchAndMerge(ctx context.Context, keys []game.CellPos, cells map[game.CellPos]game.Cell) ([]natspkg.CellUpdate, map[game.CellPos]game.Cell, []game.CellPos, bool) {
	subjects := make([]string, len(keys))
	for i, k := range keys {
		subjects[i] = e.cellSubject(k.Row, k.Col)
	}
	msgs, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, subjects)
	if err != nil {
		return nil, nil, nil, false
	}
	latest := make(map[game.CellPos]game.Cell, len(msgs))
	latestSeq := make(map[game.CellPos]uint64, len(msgs))
	for _, m := range msgs {
		c, uErr := game.UnmarshalCell(m.Payload)
		if uErr != nil {
			return nil, nil, nil, false
		}
		pos := game.CellPos{Row: m.Row, Col: m.Col}
		latest[pos] = c
		latestSeq[pos] = m.Seq
	}

	merged := make([]natspkg.CellUpdate, 0, len(keys))
	mergedCells := make(map[game.CellPos]game.Cell, len(keys))
	mergedKeys := make([]game.CellPos, 0, len(keys))
	for _, k := range keys {
		if lc := latest[k]; lc.Active && lc.PlayerIdx != e.playerIdx {
			continue // never overwrite (or vacate) the other player's mid-flight piece
		}
		data, mErr := cells[k].Marshal()
		if mErr != nil {
			return nil, nil, nil, false
		}
		merged = append(merged, natspkg.CellUpdate{
			Subject:       e.cellSubject(k.Row, k.Col),
			Payload:       data,
			ExpectLastSeq: latestSeq[k],
		})
		mergedCells[k] = cells[k]
		mergedKeys = append(mergedKeys, k)
	}
	return merged, mergedCells, mergedKeys, true
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
	if e.getMode() != ModePlayer || len(flashCells) == 0 {
		return
	}
	e.emitUpdate(EngineUpdate{
		Kind:           UpdateCASFlash,
		FlashCells:     flashCells,
		FlashPlayerIdx: e.playerIdx,
	})
}

func (e *Engine) publishPieceIdxUpdate(pieceIdx uint64) {
	// On shared boards (coop, teams), each player has independent piece tracking
	if e.sharedBoard() {
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
