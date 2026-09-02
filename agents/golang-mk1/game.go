package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"
)

// errCAS signals a rejected CAS/gated write (wrong expected sequence): the
// caller flashes and resynchronizes rather than treating it as a hard error.
var errCAS = errors.New("cas conflict")

// active is the single falling piece: type, orientation, and anchor position.
type active struct{ pt, orient, row, col int }

// Game is this agent's authoritative engine for its OWN board in one
// competitive game, plus the consumers that watch meta, events, and the garbage
// register. All board mutation is serialized by mu.
type Game struct {
	a   *Agent
	id  string
	idx int

	mode      int // modeCooperative / modeCompetitive / modeTeams
	team      int // teams: 0 = A, 1 = B, …
	teamCount int // teams: how many teams the game is played between (meta team_count; 2 unless the creator asked for more)
	teamSlot  int // teams: section index on the team board
	w        int // board width (competitive 10; a shared board 10 + extra_columns per seat beyond the first — sharedWidth)
	spawnC   int // our spawn column (section-centered on shared boards)
	runCtx   context.Context

	mu          sync.Mutex
	locked      map[cell]wireCell // settled cells (stack + garbage)
	seqs        map[cell]uint64   // per-cell CAS expectation (last stream seq)
	piece       *active
	othersAct   map[cell]int   // shared boards: other players' active cells → owner idx
	othersPiece map[int]active // shared boards: other players' pieces by owner idx
	pieceIdx    int
	score       int // OWN cumulative score (line_clear events carry it)
	lines       int // OWN cumulative cleared lines

	sharedScore  int               // coop: the one shared score (all senders folded)
	totalLines   int               // coop: all cleared lines (level source)
	teamScores   []int             // teams: per-team scoreboard, one entry per team
	teamLines    []int             // teams: per-team line totals (level source), one entry per team
	senderTotals map[string][2]int // last cumulative {score,lines} folded per sender

	eliminated map[string]bool
	results    map[string]event
	roster     []playerSummary

	// attackRotor picks which opposing team our next attack lands on: past
	// two teams a clear cannot go to "the other" team, so targets rotate
	// (nextGarbageTarget). Guarded by mu.
	attackRotor int

	garbageOwed int
	garbageBy   int
	txnApplied  int
	txnSeq      uint64

	metaSeed    uint64
	playerCount int
	nextCount   int
	holes       int   // holes punched in every garbage row this board raises (meta garbage_holes, 0..maxGarbageHoles; 0 = solid, permanent rows)
	randomHoles bool  // every garbage row draws its own hole columns (meta random_garbage_holes; off = one draw per raise)
	guideline   bool  // attacks follow the Guideline table, 0/1/2/4 rows for 1/2/3/4 lines (meta guideline_garbage; off = one row per line)
	ration      []int // teams with meta split_pieces: the piece types THIS seat draws from, ascending (rng.go pieceSetFor); nil = the full 7-bag every other game runs
	dead        bool

	// The batch pipeline (pipeline.go, guide §4.3). inflight counts the move
	// batches sent and not yet acked; inflightCells the in-flight writes per
	// cell (such a cell's expectation can only be none, or optimistic-mode's
	// prediction in inflightSeq); pipePredictedEnd is the predicted stream
	// sequence of the last in-flight batch's commit; pipeBroken marks a lost
	// batch until the repair. All guarded by mu; pipeCond (on mu) wakes
	// settlers and slot-waiters when a batch resolves. streamSeqSeen is the
	// highest stream sequence observed anywhere (consumers, acks, fetches) —
	// the stream's end as the agent knows it, what predictions start from.
	inflight         int
	inflightCells    map[cell]int
	inflightSeq      map[cell]uint64
	pipePredictedEnd uint64
	pipeBroken       bool
	pipeCond         *sync.Cond
	streamSeqSeen    atomic.Uint64

	stream       jetstream.Stream
	started      chan struct{}
	ended        chan struct{}
	startedOnce  sync.Once
	endedOnce    sync.Once
	consumeCtxts []jetstream.ConsumeContext
}

func newGame(a *Agent, id string, idx int) *Game {
	g := &Game{
		a: a, id: id, idx: idx,
		locked: map[cell]wireCell{}, seqs: map[cell]uint64{},
		othersAct: map[cell]int{}, othersPiece: map[int]active{}, senderTotals: map[string][2]int{},
		eliminated: map[string]bool{}, results: map[string]event{},
		inflightCells: map[cell]int{}, inflightSeq: map[cell]uint64{},
		started: make(chan struct{}), ended: make(chan struct{}),
	}
	g.pipeCond = sync.NewCond(&g.mu)
	return g
}

func (g *Game) markStarted() { g.startedOnce.Do(func() { close(g.started) }) }
func (g *Game) markEnded()   { g.endedOnce.Do(func() { close(g.ended) }) }
func (g *Game) isEnded() bool {
	select {
	case <-g.ended:
		return true
	default:
		return false
	}
}

// height: 4 headroom + 24 visible + garbage room (one row per player feeding
// the board: playerCount in competitive, every seat on every OTHER team in
// teams; none in coop).
func (g *Game) height() int {
	switch g.mode {
	case modeCooperative:
		return 28
	case modeTeams:
		return 28 + g.playerCount - g.teamSize()
	default:
		return 28 + g.playerCount
	}
}

// teams is how many teams this game is played between (2 unless the meta says
// otherwise), and teamSize how many seats each of them holds.
func (g *Game) teams() int {
	if g.teamCount < minTeamCount {
		return defaultTeamCount
	}
	return min(g.teamCount, maxTeamCount)
}

func (g *Game) teamSize() int { return g.playerCount / g.teams() }

// ---- subjects ------------------------------------------------------------

func (g *Game) cellSubject(c cell) string {
	switch g.mode {
	case modeCooperative:
		return fmt.Sprintf("jetris.game.%s.playfield.cell.%d.%d", g.id, c.r, c.c)
	case modeTeams:
		return fmt.Sprintf("jetris.game.%s.team.%d.playfield.cell.%d.%d", g.id, g.team, c.r, c.c)
	default:
		return fmt.Sprintf("jetris.game.%s.player.%s.playfield.cell.%d.%d", g.id, g.a.name, c.r, c.c)
	}
}

// garbageSubject is a victim board's garbage register: per-player in
// competitive, per-team in teams (pid ignored there; t is the team).
func (g *Game) garbageSubject(pid string) string {
	return fmt.Sprintf("jetris.game.%s.player.%s.playfield.garbage", g.id, pid)
}
func (g *Game) teamGarbageSubject(t int) string {
	return fmt.Sprintf("jetris.game.%s.team.%d.playfield.garbage", g.id, t)
}
func (g *Game) txnSubject() string {
	if g.mode == modeTeams {
		return fmt.Sprintf("jetris.game.%s.team.%d.playfield.txn", g.id, g.team)
	}
	return fmt.Sprintf("jetris.game.%s.player.%s.playfield.txn", g.id, g.a.name)
}

// ---- cell payloads -------------------------------------------------------

func (g *Game) activePayload(a active) *wireCell {
	c := &wireCell{T: a.pt, A: true, R: a.orient, Ar: a.row, Ac: a.col}
	if g.idx != 0 {
		c.Pi = g.idx
	}
	return c
}
func (g *Game) lockedPayload(pt int) wireCell {
	c := wireCell{O: true, T: pt}
	if g.idx != 0 {
		c.Pi = g.idx
	}
	return c
}

// maxGarbageHoles is the game's cap on meta garbage_holes (jetris-gameplays.md
// §1b): the empty cells punched in every garbage row a raise lands.
const maxGarbageHoles = 4

// garbageHoleColumns draws one set of hole columns: g.holes distinct random
// columns, always leaving at least one adversarial cell in the row. Empty
// when the game raises solid rows. A raise draws once for all its rows (the
// holes line up into a well) unless the game's random_garbage_holes is set,
// in which case every row draws its own.
func (g *Game) garbageHoleColumns() map[int]bool {
	n := min(g.holes, g.w-1)
	holes := make(map[int]bool, n)
	for _, c := range mrand.Perm(g.w)[:max(n, 0)] {
		holes[c] = true
	}
	return holes
}

func garbagePayload(causer int) wireCell {
	c := wireCell{O: true, T: 1, G: true}
	if causer != 0 {
		c.Pi = causer
	}
	return c
}
func payloadBytes(c *wireCell) []byte {
	if c == nil {
		return []byte("{}")
	}
	return c.bytes()
}

// activeCellSet returns the four cells of piece a as a set.
func cellSet(cs [4]cell) map[cell]bool {
	m := make(map[cell]bool, 4)
	for _, c := range cs {
		m[c] = true
	}
	return m
}

func (g *Game) canPlace(cs [4]cell) bool {
	h := g.height()
	for _, c := range cs {
		if c.r < 0 || c.r >= h || c.c < 0 || c.c >= g.w {
			return false
		}
		if _, ok := g.locked[c]; ok {
			return false
		}
	}
	return true
}

func (g *Game) dropRowLocked(a active) int {
	row := a.row
	for g.canPlace(pieceCells(a.pt, a.orient, row+1, a.col)) {
		row++
	}
	return row
}

// ---- atomic CAS batch publishing (guide §4.3) ----------------------------

type cellUpd struct {
	at    cell
	c     *wireCell // nil vacates
	guard bool      // gated batches: carry this cell's CAS expectation anyway (teams teammate-piece guard)
}

func category(c *wireCell) int {
	switch {
	case c != nil && c.A:
		return 0
	case c != nil && c.O:
		return 1
	default:
		return 2
	}
}

func orderCells(cells []cellUpd) {
	sort.SliceStable(cells, func(i, j int) bool {
		ci, cj := category(cells[i].c), category(cells[j].c)
		if ci != cj {
			return ci < cj
		}
		if cells[i].at.r != cells[j].at.r {
			return cells[i].at.r < cells[j].at.r
		}
		return cells[i].at.c < cells[j].at.c
	})
}

// publishBatch publishes cell updates as ONE atomic batch, active→locked→empty
// ordered so a relocating piece never transiently vanishes on ordered
// consumers. With cas, each message carries its per-subject expected-last
// sequence; a mismatch drops the whole batch (errCAS). On success per-cell
// sequences advance by write-through.
func (g *Game) publishBatch(ctx context.Context, cells []cellUpd, cas bool) error {
	orderCells(cells)
	n := len(cells)
	if n == 0 {
		return nil
	}
	if n == 1 {
		u := cells[0]
		var h nats.Header
		if cas {
			h = nats.Header{hExpectLast: []string{fmt.Sprint(g.seqs[u.at])}}
		}
		seq, conflict, err := g.a.casRequest(ctx, g.cellSubject(u.at), payloadBytes(u.c), h)
		if err != nil {
			return err
		}
		if conflict {
			return errCAS
		}
		g.noteStreamSeq(seq)
		g.seqs[u.at] = seq
		return nil
	}
	batchID := randID(22)
	var commitSeq uint64
	for i, u := range cells {
		subj := g.cellSubject(u.at)
		h := nats.Header{hBatchID: []string{batchID}, hBatchSeq: []string{fmt.Sprint(i + 1)}}
		if cas {
			h.Set(hExpectLast, fmt.Sprint(g.seqs[u.at]))
		}
		last := i == n-1
		if last {
			h.Set(hBatchCommit, "1")
		}
		if i == 0 || last { // first (flow control) and commit are requests
			seq, conflict, err := g.a.casRequest(ctx, subj, payloadBytes(u.c), h)
			if err != nil {
				return err
			}
			if conflict {
				return errCAS
			}
			if last {
				commitSeq = seq
			}
		} else if err := g.a.nc.PublishMsg(&nats.Msg{Subject: subj, Data: payloadBytes(u.c), Header: h}); err != nil {
			return err
		}
	}
	g.noteStreamSeq(commitSeq)
	for i, u := range cells { // write-through
		g.seqs[u.at] = commitSeq - uint64(n-1-i)
	}
	return nil
}

// txnReg is the per-board transaction register that gates bulk transforms.
type txnReg struct {
	Applied int    `json:"applied"`
	Op      string `json:"op,omitempty"`
	By      int    `json:"by,omitempty"`
	Topped  []int  `json:"topped,omitempty"`
	Full    bool   `json:"full,omitempty"`
}

// publishGatedBatch publishes one bulk transform (garbage application or
// line-clear collapse) as a single gated atomic batch: message 1 is the txn
// register carrying the per-subject CAS gate; every cell after it is NoCAS. A
// stale gate atomically rejects the WHOLE batch (errCAS) and nothing is stored.
func (g *Game) publishGatedBatch(ctx context.Context, txn txnReg, cells []cellUpd) error {
	orderCells(cells)
	n := len(cells) + 1
	batchID := randID(22)
	txnPayload, _ := json.Marshal(txn)
	h := nats.Header{hBatchID: []string{batchID}, hBatchSeq: []string{"1"}, hExpectLast: []string{fmt.Sprint(g.txnSeq)}}
	if n == 1 {
		h.Set(hBatchCommit, "1")
	}
	seq, conflict, err := g.a.casRequest(ctx, g.txnSubject(), txnPayload, h)
	if err != nil {
		return err
	}
	if conflict {
		return errCAS
	}
	commitSeq := seq
	for i, u := range cells {
		hh := nats.Header{hBatchID: []string{batchID}, hBatchSeq: []string{fmt.Sprint(i + 2)}}
		if u.guard {
			hh.Set(hExpectLast, fmt.Sprint(g.seqs[u.at]))
		}
		last := i == len(cells)-1
		if last {
			hh.Set(hBatchCommit, "1")
			s, conflict, err := g.a.casRequest(ctx, g.cellSubject(u.at), payloadBytes(u.c), hh)
			if err != nil {
				return err
			}
			if conflict {
				return errCAS
			}
			commitSeq = s
		} else if err := g.a.nc.PublishMsg(&nats.Msg{Subject: g.cellSubject(u.at), Data: payloadBytes(u.c), Header: hh}); err != nil {
			return err
		}
	}
	g.noteStreamSeq(commitSeq)
	g.txnSeq = commitSeq - uint64(n-1)
	g.txnApplied = txn.Applied
	for i, u := range cells {
		g.seqs[u.at] = commitSeq - uint64(n-1-(i+1))
	}
	return nil
}

// ---- board writes --------------------------------------------------------

func (g *Game) flash(cells []cell) {
	pts := make([][2]int, len(cells))
	for i, c := range cells {
		pts[i] = [2]int{c.r, c.c}
	}
	b, _ := json.Marshal(map[string]any{"pi": g.idx, "c": pts})
	_ = g.a.nc.Publish("jetris.flash."+g.id+"."+g.a.name, b)
}

// fetchChunk bounds the subjects per multi-subject direct get: the server
// answers at most 1024 results per request (413 Too Many Results, no
// pagination), so larger boards go in chunks.
const fetchChunk = 512

// boardMsg is one cell's last stream message: its sequence and decoded payload.
type boardMsg struct {
	seq uint64
	wc  wireCell
}

// fetchBoard fetches the last message of every cell of OUR board with orbit's
// multi-subject direct get (guide §4.5): one round trip per fetchChunk cells
// instead of one per cell. That matters on a real link — a 2v2 teams board is
// 600 cells, and the per-cell form froze the agent for ~20 s at 33 ms RTT on
// every dropped write. A fetch that needs several chunks is bound to the
// stream's current last sequence so the whole snapshot is consistent at one
// point in the stream; the board consumer's strictly-higher-sequence rule
// folds anything newer. Cells never written are absent from the result.
func (g *Game) fetchBoard(ctx context.Context) (map[cell]boardMsg, error) {
	h := g.height()
	subjects := make([]string, 0, h*g.w)
	for r := 0; r < h; r++ {
		for c := 0; c < g.w; c++ {
			subjects = append(subjects, g.cellSubject(cell{r, c}))
		}
	}
	var opts []jetstreamext.GetLastForOpt
	if len(subjects) > fetchChunk {
		info, err := g.stream.Info(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, jetstreamext.GetLastMsgsUpToSeq(info.State.LastSeq))
	}
	out := make(map[cell]boardMsg, len(subjects))
	for start := 0; start < len(subjects); start += fetchChunk {
		end := min(start+fetchChunk, len(subjects))
		msgs, err := jetstreamext.GetLastMsgsFor(ctx, g.a.js, gameStreamName(g.id), subjects[start:end], opts...)
		if err != nil {
			if errors.Is(err, jetstreamext.ErrNoMessages) {
				continue // nothing written under these subjects yet
			}
			return nil, err
		}
		for m, err := range msgs {
			if err != nil {
				if errors.Is(err, jetstreamext.ErrNoMessages) {
					continue
				}
				return nil, err
			}
			r, c, ok := parseCellSubject(m.Subject)
			if !ok {
				continue
			}
			var wc wireCell
			if len(m.Data) > 0 {
				_ = json.Unmarshal(m.Data, &wc)
			}
			g.noteStreamSeq(m.Sequence)
			out[cell{r, c}] = boardMsg{seq: m.Sequence, wc: wc}
		}
	}
	return out, nil
}

// resync refetches our own board from the stream after a dropped write. A
// fetch that fails outright leaves the current state in place (and logs)
// rather than wiping the board to empty.
func (g *Game) resync(ctx context.Context) {
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		log.Printf("resync: %v", err)
		return
	}
	g.foldSnapshot(snap)
}

// foldSnapshot replaces the local board state — settled cells, our own piece,
// on shared boards the other players' pieces, and every per-cell sequence —
// with one consistent stream snapshot. Caller holds mu.
func (g *Game) foldSnapshot(snap map[cell]boardMsg) {
	g.locked = map[cell]wireCell{}
	g.seqs = map[cell]uint64{}
	g.piece = nil
	if g.shared() {
		g.othersAct = map[cell]int{}
		g.othersPiece = map[int]active{}
	}
	for at, m := range snap {
		g.seqs[at] = m.seq
		wc := m.wc
		switch {
		case wc.A && (!g.shared() || wc.Pi == g.idx):
			g.piece = &active{wc.T, wc.R, wc.Ar, wc.Ac}
		case wc.A:
			g.othersAct[at] = wc.Pi
			g.othersPiece[wc.Pi] = active{wc.T, wc.R, wc.Ar, wc.Ac}
		case wc.O:
			g.locked[at] = wc
		}
	}
}

func (g *Game) refreshRegisters(ctx context.Context) {
	if g.mode == modeCooperative {
		return
	}
	if raw, err := g.stream.GetLastMsgForSubject(ctx, g.txnSubject()); err == nil {
		g.txnSeq = raw.Sequence
		var t txnReg
		if len(raw.Data) > 0 {
			_ = json.Unmarshal(raw.Data, &t)
		}
		g.txnApplied = t.Applied
	} else {
		g.txnSeq, g.txnApplied = 0, 0
	}
	ownGarbage := g.garbageSubject(g.a.name)
	if g.mode == modeTeams {
		ownGarbage = g.teamGarbageSubject(g.team)
	}
	if raw, err := g.stream.GetLastMsgForSubject(ctx, ownGarbage); err == nil {
		var reg garbageReg
		if len(raw.Data) > 0 {
			_ = json.Unmarshal(raw.Data, &reg)
		}
		if reg.Total > g.garbageOwed {
			g.garbageOwed = reg.Total
		}
		g.garbageBy = reg.By
	}
}

type garbageReg struct {
	Total int `json:"total"`
	By    int `json:"by"`
}

// publishPieceMove CAS-batches the diff between the current active cells and
// `to`. Pipelined modes (--publish async/optimistic) send the batch and move
// on — a loss surfaces later and settles the pipeline (pipeline.go). Sync
// mode blocks on the ack; a dropped write flashes and resyncs.
func (g *Game) publishPieceMove(ctx context.Context, to active) bool {
	old := g.activeCells()
	newCells := pieceCells(to.pt, to.orient, to.row, to.col)
	newSet := cellSet(newCells)
	var cells []cellUpd
	for _, c := range newCells {
		cells = append(cells, cellUpd{at: c, c: g.activePayload(to)})
	}
	for _, c := range old {
		if !newSet[c] {
			cells = append(cells, cellUpd{at: c})
		}
	}
	if g.a.pub != pubSync {
		if !g.publishBatchAsync(ctx, cells, old) {
			g.settlePipeline(ctx)
			return false
		}
		p := to
		g.piece = &p // the optimistic projection; the ack reconciles sequences
		return true
	}
	if err := g.publishBatch(ctx, cells, true); err != nil {
		if errors.Is(err, errCAS) {
			g.flash(old)
		} else {
			log.Printf("move: %v", err)
		}
		g.resync(ctx)
		return false
	}
	p := to
	g.piece = &p
	return true
}

func (g *Game) activeCells() []cell {
	if g.piece == nil {
		return nil
	}
	cs := pieceCells(g.piece.pt, g.piece.orient, g.piece.row, g.piece.col)
	return cs[:]
}

// pieceAt returns the piece type at the given index of THIS seat's sequence:
// the game's 7-bag, or — when the game deals its pieces out between teammates
// (meta split_pieces) — a bag of this seat's ration alone.
func (g *Game) pieceAt(index int) int {
	if len(g.ration) == 0 {
		return pieceAt(g.metaSeed, index)
	}
	return pieceAtIn(g.metaSeed, g.ration, index)
}

// rationNames spells a ration out in piece letters, for the log line that
// tells the operator which hand this agent was dealt.
func rationNames(set []int) string {
	out := ""
	for _, pt := range set {
		if out != "" {
			out += " "
		}
		out += string("IOTSZJL"[pt])
	}
	return out
}

// spawn publishes a fresh piece. Returns the spawn time, whether the piece is
// on the board, and whether we topped out. A spawn covered by LOCKED cells is
// the top-out; covered only by another player's falling piece it is DEFERRED —
// placed=false, topped=false — and the caller retries (gameplays §3).
func (g *Game) spawn(ctx context.Context) (spawnT time.Time, placed, topped bool) {
	g.settlePipeline(ctx) // barrier: exact expectations, converged board
	if g.piece != nil {
		// We already own a live piece on the board — adopted from a committed
		// transform that moved it (a garbage lift racing our NoCAS lock), or
		// re-adopted by a resync (execute's parked-piece escape). Spawning
		// over it would orphan its cells as a frozen ghost nothing ever
		// vacates (our own vacates follow g.piece). Resume it instead: the
		// caller re-plans from its actual position, same as the native
		// engine's rule of only spawning on the zero-active-cells edge.
		return time.Now(), true, false
	}
	pt := g.pieceAt(g.pieceIdx)
	n := active{pt, 0, spawnRow, g.spawnC}
	cs := pieceCells(pt, 0, spawnRow, g.spawnC)
	if !g.canPlace(cs) {
		return time.Time{}, false, true
	}
	if g.sharedBlocked(cs) {
		return time.Time{}, false, false // a transient piece is crossing our spawn
	}
	var cells []cellUpd
	for _, c := range cs {
		cells = append(cells, cellUpd{at: c, c: g.activePayload(n)})
	}
	if err := g.publishBatch(ctx, cells, true); err != nil {
		if errors.Is(err, errCAS) {
			g.flash(cs[:])
		} else {
			log.Printf("spawn: %v", err)
		}
		g.resync(ctx)
		return time.Now(), g.piece != nil, false
	}
	p := n
	g.piece = &p
	return time.Now(), true, false
}

// lockPiece settles the piece at dest (authoritative NoCAS), runs line clears,
// bumps the piece index.
func (g *Game) lockPiece(ctx context.Context, dest active) {
	old := g.activeCells()
	destCells := pieceCells(dest.pt, dest.orient, dest.row, dest.col)
	destSet := cellSet(destCells)
	lp := g.lockedPayload(dest.pt)
	var cells []cellUpd
	for _, c := range destCells {
		cc := lp
		cells = append(cells, cellUpd{at: c, c: &cc})
	}
	for _, c := range old {
		if !destSet[c] {
			cells = append(cells, cellUpd{at: c})
		}
	}
	if err := g.publishBatch(ctx, cells, false); err != nil {
		log.Printf("lock: %v", err)
	}
	for _, c := range destCells {
		g.locked[c] = lp
	}
	g.piece = nil
	done := g.completedRows()
	if g.shared() {
		done = g.completedRowsShared()
	}
	if len(done) > 0 {
		g.clearRows(ctx, done)
	}
	g.pieceIdx++
	if g.mode == modeCompetitive {
		g.bumpMetaPieceIdx(ctx) // shared modes: every seat has its own index; the meta field stays the creator's
	}
}

// completedRows: every cell settled and at least one of them ours/the stack's
// — a solid garbage row is permanent, a garbage row whose holes the stack
// filled clears like any other (gameplays §4).
func (g *Game) completedRows() []int {
	var out []int
	h := g.height()
	for r := 0; r < h; r++ {
		full, stack := true, false
		for c := 0; c < g.w; c++ {
			cc, ok := g.locked[cell{r, c}]
			if !ok {
				full = false
				break
			}
			if !cc.G {
				stack = true
			}
		}
		if full && stack {
			out = append(out, r)
		}
	}
	return out
}

// clearRows collapses completed rows: competitive and teams as a GATED
// transform (teams also shifting teammates' pieces down with the stack), coop
// as a CAS merge-retry batch; then scores the clear, announces it, and routes
// the attack (competitive: every surviving opponent; teams: the opposing
// board; coop: nobody).
func (g *Game) clearRows(ctx context.Context, rows []int) {
	clearedRows := append([]int(nil), rows...)
	cleared := 0
	if g.mode == modeCooperative {
		cleared = g.clearRowsCoop(ctx, rows)
	} else {
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				g.refreshRegisters(ctx)
				g.resync(ctx)
				rows = g.completedRows()
				if g.shared() {
					rows = g.completedRowsShared()
				}
				if len(rows) == 0 {
					return
				}
			}
			var diff []cellUpd
			if g.mode == modeTeams {
				newLocked, moved := g.clearProjection(rows)
				diff = g.diffCells(newLocked)
				for pi, np := range moved {
					op := g.othersPiece[pi]
					oldCells := cellSet(pieceCells(op.pt, op.orient, op.row, op.col))
					newSet := cellSet(pieceCells(np.pt, np.orient, np.row, np.col))
					pl := wireCell{T: np.pt, A: true, R: np.orient, Ar: np.row, Ac: np.col, Pi: pi}
					// guard: a teammate locking or moving mid-shift must
					// atomically reject this batch (the recompute sees their
					// new reality) — never strand a ghost at the shifted spot.
					for c := range newSet {
						cc := pl
						diff = append(diff, cellUpd{at: c, c: &cc, guard: true})
					}
					for c := range oldCells {
						if !newSet[c] {
							if _, isLocked := newLocked[c]; !isLocked {
								diff = append(diff, cellUpd{at: c, guard: true})
							}
						}
					}
				}
				txn := txnReg{Applied: g.txnApplied, Op: "clear", By: g.idx}
				if err := g.publishGatedBatch(ctx, txn, diff); err != nil {
					if errors.Is(err, errCAS) {
						continue
					}
					log.Printf("clear: %v", err)
					return
				}
				g.locked = newLocked
				for pi, np := range moved {
					g.othersPiece[pi] = np
				}
				g.reindexOthers()
			} else {
				removed := make(map[int]bool, len(rows))
				for _, r := range rows {
					removed[r] = true
				}
				newLocked := map[cell]wireCell{}
				shift := 0
				for r := g.height() - 1; r >= 0; r-- {
					if removed[r] {
						shift++
						continue
					}
					for c := 0; c < g.w; c++ {
						if cc, ok := g.locked[cell{r, c}]; ok {
							newLocked[cell{r + shift, c}] = cc
						}
					}
				}
				diff = g.diffCells(newLocked)
				txn := txnReg{Applied: g.txnApplied, Op: "clear", By: g.idx}
				if err := g.publishGatedBatch(ctx, txn, diff); err != nil {
					if errors.Is(err, errCAS) {
						continue
					}
					log.Printf("clear: %v", err)
					return
				}
				g.locked = newLocked
			}
			cleared = len(rows)
			break
		}
	}
	if cleared == 0 {
		return
	}
	scoreDelta := cleared // competitive: one point per line
	switch g.mode {
	case modeCooperative:
		scoreDelta = g.playerCount * cleared
		g.sharedScore += scoreDelta
		g.totalLines += cleared
	case modeTeams:
		scoreDelta = (g.playerCount / 2) * cleared
		g.teamScores[g.team] += scoreDelta
		g.teamLines[g.team] += cleared
	}
	g.score += scoreDelta
	g.lines += cleared
	g.publishLineClear(ctx, cleared, scoreDelta, clearedRows)
	log.Printf("cleared %d line(s), score %d", cleared, g.score)
	if rows := g.attackRows(cleared); rows > 0 {
		switch g.mode {
		case modeCompetitive:
			g.bumpVictimLedgers(ctx, rows)
		case modeTeams:
			g.bumpTeamLedger(ctx, rows)
		}
	}
}

// attackRows converts a clear into the garbage it owes: one row per line, or
// under the game's guideline_garbage rule the Guideline table — a single
// sends nothing, a double 1 row, a triple 2, a Tetris 4 (gameplays §4).
func (g *Game) attackRows(lines int) int {
	if lines <= 0 {
		return 0
	}
	if !g.guideline {
		return lines
	}
	switch lines {
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return 2
	}
	return 4
}

// bumpTeamLedger CAS-adds `lines` to ONE opposing team's garbage register:
// between two teams the other one, and past that the rotation's next, so an
// attack weighs what it always did rather than hitting every opponent at once
// (the same rule the GUI engine plays by — internal/engine/ledger.go).
func (g *Game) bumpTeamLedger(ctx context.Context, lines int) {
	subject := g.teamGarbageSubject(g.nextGarbageTarget())
	for i := 0; i < 20; i++ {
		var total int
		var seq uint64
		if raw, err := g.stream.GetLastMsgForSubject(ctx, subject); err == nil {
			var reg garbageReg
			if len(raw.Data) > 0 {
				_ = json.Unmarshal(raw.Data, &reg)
			}
			total, seq = reg.Total, raw.Sequence
		}
		payload, _ := json.Marshal(garbageReg{Total: total + lines, By: g.idx})
		_, conflict, err := g.a.casRequest(ctx, subject, payload, nats.Header{hExpectLast: []string{fmt.Sprint(seq)}})
		if err != nil {
			log.Printf("team ledger bump: %v", err)
			return
		}
		if !conflict {
			return
		}
	}
}

// diffCells returns the cell updates that turn g.locked into newLocked.
func (g *Game) diffCells(newLocked map[cell]wireCell) []cellUpd {
	var diff []cellUpd
	h := g.height()
	for r := 0; r < h; r++ {
		for c := 0; c < g.w; c++ {
			at := cell{r, c}
			nv, nok := newLocked[at]
			ov, ook := g.locked[at]
			if nok != ook || nv != ov {
				if nok {
					cc := nv
					diff = append(diff, cellUpd{at: at, c: &cc})
				} else {
					diff = append(diff, cellUpd{at: at})
				}
			}
		}
	}
	return diff
}

// bumpVictimLedgers CAS-adds `lines` to every surviving opponent's garbage
// register; the register is a cumulative total so concurrent attackers converge.
func (g *Game) bumpVictimLedgers(ctx context.Context, lines int) {
	for _, p := range g.roster {
		pid := p.PlayerID
		if pid == g.a.name || g.eliminated[pid] {
			continue
		}
		subject := g.garbageSubject(pid)
		for i := 0; i < 20; i++ {
			var total int
			var seq uint64
			if raw, err := g.stream.GetLastMsgForSubject(ctx, subject); err == nil {
				var reg garbageReg
				if len(raw.Data) > 0 {
					_ = json.Unmarshal(raw.Data, &reg)
				}
				total, seq = reg.Total, raw.Sequence
			}
			payload, _ := json.Marshal(garbageReg{Total: total + lines, By: g.idx})
			_, conflict, err := g.a.casRequest(ctx, subject, payload, nats.Header{hExpectLast: []string{fmt.Sprint(seq)}})
			if err != nil {
				log.Printf("ledger bump %s: %v", pid, err)
				break
			}
			if !conflict {
				break
			}
			// lost the add race to another attacker: refresh and re-add
		}
	}
}

func (g *Game) bumpMetaPieceIdx(ctx context.Context) {
	meta, seq, err := g.a.fetchMeta(ctx, g.id)
	if err != nil {
		return
	}
	meta.set("piece_idx", g.pieceIdx)
	_, _, _ = g.a.metaPublish(ctx, g.id, meta.bytes(), seq)
}

// applyOwedGarbage applies the outstanding deficit as one gated cascade: shift
// the stack up, fill the bottom with adversarial rows (punched with the
// game's garbage_holes, the same columns on every row of the raise), lift the
// falling piece the minimum needed to clear the risen stack (top out if it is
// pushed off), and top out if locked cells are shoved past row 0.
func (g *Game) applyOwedGarbage(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settlePipeline(ctx) // barrier: the gated batch needs exact expectations
	for attempt := 0; attempt < 3; attempt++ {
		n := g.garbageOwed - g.txnApplied
		if n <= 0 || g.dead {
			return
		}
		causer := g.garbageBy
		gp := garbagePayload(causer)
		h := g.height()
		boardFull := false
		for c := range g.locked {
			if c.r < n {
				boardFull = true
				break
			}
		}
		newLocked := map[cell]wireCell{}
		for c, v := range g.locked {
			if c.r-n >= 0 {
				newLocked[cell{c.r - n, c.c}] = v
			}
		}
		var holes map[int]bool
		for r := h - n; r < h; r++ {
			if holes == nil || g.randomHoles {
				holes = g.garbageHoleColumns()
			}
			for c := 0; c < g.w; c++ {
				if holes[c] {
					continue
				}
				newLocked[cell{r, c}] = gp
			}
		}
		// Lift EVERY falling piece — ours and, in teams, every teammate's —
		// the minimum rows that clear the risen stack. Lifts cascade: a
		// lifted piece is an obstacle for pieces above it (gameplays §5). A
		// piece pushed off the top eliminates its owner (txn.topped).
		type lifted struct {
			owner    int
			from, to active
			squeezed bool
		}
		var pieces []lifted
		if g.piece != nil {
			pieces = append(pieces, lifted{owner: g.idx, from: *g.piece})
		}
		for pi, p := range g.othersPiece {
			pieces = append(pieces, lifted{owner: pi, from: p})
		}
		// Lowest piece first, so a rising column of pieces cascades upward.
		sort.Slice(pieces, func(i, j int) bool { return pieces[i].from.row > pieces[j].from.row })
		obstacles := func(cs [4]cell, settled map[cell]wireCell, placedSoFar map[cell]bool) bool {
			for _, c := range cs {
				if c.c < 0 || c.c >= g.w || c.r >= g.height() {
					return false
				}
				if _, ok := settled[c]; ok {
					return false
				}
				if placedSoFar[c] {
					return false
				}
			}
			return true
		}
		placedCells := map[cell]bool{}
		var toppedOwners []int
		for i := range pieces {
			pc := &pieces[i]
			pc.squeezed = true
			for k := 0; ; k++ {
				cand := active{pc.from.pt, pc.from.orient, pc.from.row - k, pc.from.col}
				cs := pieceCells(cand.pt, cand.orient, cand.row, cand.col)
				minR := cs[0].r
				for _, c := range cs[1:] {
					if c.r < minR {
						minR = c.r
					}
				}
				if minR < 0 {
					break // off the top: squeezed out
				}
				if obstacles(cs, newLocked, placedCells) {
					pc.to, pc.squeezed = cand, false
					for _, c := range cs {
						placedCells[c] = true
					}
					break
				}
			}
			if pc.squeezed {
				toppedOwners = append(toppedOwners, pc.owner)
			}
		}
		diff := g.diffCells(newLocked)
		for _, pc := range pieces {
			oldCells := cellSet(pieceCells(pc.from.pt, pc.from.orient, pc.from.row, pc.from.col))
			if pc.squeezed {
				for c := range oldCells {
					if _, in := newLocked[c]; !in {
						diff = append(diff, cellUpd{at: c, guard: pc.owner != g.idx})
					}
				}
				continue
			}
			if pc.to == pc.from {
				// Untouched teammate piece: guard it with expectation
				// carriers so a concurrent move rejects this whole batch
				// instead of being buried under a stale projection.
				if pc.owner != g.idx {
					for c := range oldCells {
						pl := wireCell{T: pc.from.pt, A: true, R: pc.from.orient, Ar: pc.from.row, Ac: pc.from.col, Pi: pc.owner}
						diff = append(diff, cellUpd{at: c, c: &pl, guard: true})
					}
				}
				continue
			}
			newSet := cellSet(pieceCells(pc.to.pt, pc.to.orient, pc.to.row, pc.to.col))
			pl := wireCell{T: pc.to.pt, A: true, R: pc.to.orient, Ar: pc.to.row, Ac: pc.to.col}
			if pc.owner != 0 {
				pl.Pi = pc.owner
			}
			for c := range newSet {
				cc := pl
				diff = append(diff, cellUpd{at: c, c: &cc, guard: pc.owner != g.idx})
			}
			for c := range oldCells {
				if !newSet[c] {
					if _, in := newLocked[c]; !in {
						diff = append(diff, cellUpd{at: c, guard: pc.owner != g.idx})
					}
				}
			}
		}
		txn := txnReg{Applied: g.garbageOwed, Op: "shrink", By: g.idx}
		txn.Topped = toppedOwners
		if boardFull {
			txn.Full = true
		}
		if err := g.publishGatedBatch(ctx, txn, diff); err != nil {
			if errors.Is(err, errCAS) {
				g.refreshRegisters(ctx)
				g.resync(ctx)
				continue
			}
			log.Printf("garbage: %v", err)
			return
		}
		g.locked = newLocked
		mySqueezed := false
		for _, pc := range pieces {
			if pc.owner == g.idx {
				if pc.squeezed {
					g.piece, mySqueezed = nil, true
				} else if pc.to != pc.from {
					p := pc.to
					g.piece = &p
				}
			} else {
				if pc.squeezed {
					delete(g.othersPiece, pc.owner)
				} else if pc.to != pc.from {
					g.othersPiece[pc.owner] = pc.to
				}
			}
		}
		g.reindexOthers()
		log.Printf("garbage: +%d row(s) from player %d", n, causer)
		if mySqueezed || boardFull {
			g.dead = true
		}
		return
	}
}

// canPlaceIn is canPlace against an explicit locked map (used mid-transform).
func (g *Game) canPlaceIn(locked map[cell]wireCell, cs [4]cell) bool {
	h := g.height()
	for _, c := range cs {
		if c.r < 0 || c.r >= h || c.c < 0 || c.c >= g.w {
			return false
		}
		if _, ok := locked[c]; ok {
			return false
		}
	}
	return true
}

// ---- consumers -----------------------------------------------------------

func (g *Game) startConsumers(ctx context.Context) error {
	metaCons, err := g.consume(ctx, metaSubject(g.id), func(m jetstream.Msg) {
		o := toObj(m.Data())
		st := o.str("status")
		if st == "in_progress" {
			g.markStarted()
		}
		if st == "finished" || st == "archived" || st == "cancelled" {
			g.markEnded()
		}
	}, true)
	if err != nil {
		return err
	}
	eventsCons, err := g.consume(ctx, "jetris.game."+g.id+".events.>", func(m jetstream.Msg) {
		var ev event
		if json.Unmarshal(m.Data(), &ev) != nil {
			return
		}
		switch ev.Kind {
		case "line_clear":
			g.foldLineClear(ev)
		case "game_over":
			g.mu.Lock()
			g.eliminated[ev.PlayerID] = true
			if ev.PlayerID != g.a.name {
				g.results[ev.PlayerID] = ev
			}
			enemyDead := g.mode == modeTeams && g.othersDead()
			g.mu.Unlock()
			if g.mode == modeCooperative || enemyDead {
				// Coop: anyone's top-out ends the game for everyone. Teams:
				// the verdict is EVENT-driven, not piece-cadence-driven — the
				// enemy team's last elimination must end our play loop even
				// if our current piece never reaches a lock-in.
				g.markEnded()
			}
		}
	}, true)
	if err != nil {
		return err
	}
	g.consumeCtxts = []jetstream.ConsumeContext{metaCons, eventsCons}
	if g.shared() {
		// The shared board's cells (and, in teams, its garbage/txn registers)
		// arrive on one board consumer.
		boardCons, err := g.consume(ctx, g.boardFilter(), g.handleBoardMsg, true)
		if err != nil {
			return err
		}
		g.consumeCtxts = append(g.consumeCtxts, boardCons)
	} else {
		garbageCons, err := g.consume(ctx, g.garbageSubject(g.a.name), func(m jetstream.Msg) {
			var reg garbageReg
			if len(m.Data()) > 0 {
				_ = json.Unmarshal(m.Data(), &reg)
			}
			g.mu.Lock()
			if reg.Total > g.garbageOwed {
				g.garbageOwed = reg.Total
			}
			g.garbageBy = reg.By
			need := g.garbageOwed > g.txnApplied && !g.dead
			g.mu.Unlock()
			if need {
				g.applyOwedGarbage(ctx)
			}
		}, false)
		if err != nil {
			return err
		}
		g.consumeCtxts = append(g.consumeCtxts, garbageCons)
	}
	return nil
}

// consume attaches an ordered consumer filtered to one subject. endOnErr marks
// the game ended if the consumer dies (its stream was deleted).
func (g *Game) consume(ctx context.Context, filter string, handler jetstream.MessageHandler, endOnErr bool) (jetstream.ConsumeContext, error) {
	cons, err := g.stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}})
	if err != nil {
		return nil, err
	}
	return cons.Consume(handler, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		if endOnErr {
			g.markEnded()
		}
	}))
}

func (g *Game) stopConsumers() {
	for _, c := range g.consumeCtxts {
		if c != nil {
			c.Stop()
		}
	}
}

// ---- the game ------------------------------------------------------------

func (g *Game) run(ctx context.Context) bool {
	meta, _, err := g.a.fetchMeta(ctx, g.id)
	if err != nil {
		log.Printf("fetch meta: %v", err)
		return false
	}
	g.metaSeed = meta.u64("seed")
	g.mode = meta.int("mode")
	g.playerCount = meta.int("player_count")
	g.teamCount = normalizeTeamCount(meta.int("team_count")) // absent (pre-field meta) = the historical two
	g.nextCount = meta.int("next_count")
	g.holes = min(max(meta.int("garbage_holes"), 0), maxGarbageHoles) // absent (pre-field meta) = 0: solid rows
	g.randomHoles = meta.boolv("random_garbage_holes") && g.holes > 0
	g.guideline = meta.boolv("guideline_garbage")
	g.a.mu.Lock()
	g.roster = g.a.listings[g.id].players()
	g.a.mu.Unlock()
	// Board geometry and our spawn section (gameplays §2/§3/§5). Every seat
	// tracks its own piece index on shared boards; competitive keeps the
	// legacy meta counter.
	extra := extraColumns(meta.int("extra_columns"))
	g.w, g.spawnC = width, spawnCol
	switch g.mode {
	case modeCooperative:
		g.w = sharedWidth(g.playerCount, extra)
		g.spawnC = g.idx*extra + spawnCol
	case modeTeams:
		g.teamScores, g.teamLines = make([]int, g.teams()), make([]int, g.teams())
		for _, p := range g.roster {
			if p.PlayerID == g.a.name {
				g.team, g.teamSlot = p.Team, p.TeamSlot
			}
		}
		teamSize := g.teamSize()
		g.w = sharedWidth(teamSize, extra)
		g.spawnC = g.teamSlot*extra + spawnCol
		// The piece split (gameplays §5): the seven types dealt out between
		// the teammates off the game's seed, this seat playing only its own
		// ration. A team of one has nobody to split with and is dealt the
		// whole bag, so the deal is only read past that.
		if meta.boolv("split_pieces") && teamSize > 1 {
			g.ration = pieceSetFor(g.metaSeed, teamSize, g.teamSlot)
			log.Printf("split pieces: slot %d holds %s", g.teamSlot, rationNames(g.ration))
		}
	default:
		g.pieceIdx = meta.int("piece_idx")
	}
	g.runCtx = ctx
	if g.stream, err = g.a.stream(ctx, g.id); err != nil {
		log.Printf("stream: %v", err)
		return false
	}
	if err := g.startConsumers(ctx); err != nil {
		log.Printf("consumers: %v", err)
		return false
	}
	defer g.stopConsumers()

	if g.a.toggleReady(ctx, g.id) {
		log.Print("all ready — running the countdown")
		g.a.runCountdown(ctx, g.id)
	}
	// A game that never fills or readies must not wedge the agent forever:
	// after the wait timeout we walk away cleanly (guide §5 step 6) and return to
	// the lobby.
	start := time.NewTimer(g.a.wait)
	defer start.Stop()
	select {
	case <-g.started:
	case <-start.C:
		log.Printf("game %s did not start within %s — un-joining", g.id, g.a.wait)
		g.a.unjoinGame(ctx, g.id)
		return false
	case <-g.ended:
		return false
	case <-ctx.Done():
		return false
	case <-g.a.stopCh:
		return false
	}
	g.a.mu.Lock()
	if pl := g.a.listings[g.id].players(); len(pl) > 0 {
		g.roster = pl
	}
	g.a.mu.Unlock()
	log.Print("game started")

	won := g.playPieces(ctx)
	if won {
		g.transitionFinishedAndArchive(ctx)
	} else {
		g.waitForEnd(ctx)
	}
	return won
}

func (g *Game) playPieces(ctx context.Context) bool {
	deferrals := 0
	for !g.isEnded() {
		g.mu.Lock()
		dead := g.dead
		g.mu.Unlock()
		if dead {
			return g.topOut(ctx)
		}
		g.mu.Lock()
		spawnT, placed, over := g.spawn(ctx)
		g.mu.Unlock()
		if over {
			return g.topOut(ctx)
		}
		if !placed {
			// Deferred spawn: a teammate's falling piece is crossing our spawn
			// cells (or our spawn batch lost its CAS). Retry shortly — the
			// blocker falls away within a tick (gameplays §3). A blocker that
			// never falls away is a stale ghost (a lock we mis-tracked):
			// rebuild from the stream rather than wedge forever.
			deferrals++
			if deferrals%30 == 0 {
				g.mu.Lock()
				g.settlePipeline(ctx)
				g.resyncShared(ctx)
				g.mu.Unlock()
			}
			if !g.wait(ctx, 100*time.Millisecond) {
				break
			}
			continue
		}
		deferrals = 0
		if !g.wait(ctx, g.a.tn.pieceDelay) {
			break
		}
		g.mu.Lock()
		piece := g.piece
		g.mu.Unlock()
		if piece == nil {
			continue // spawn was dropped by CAS; resynced, try again
		}
		plan, ok := g.plan(*piece)
		if !ok {
			return g.topOut(ctx)
		}
		g.execute(ctx, plan, spawnT)
		g.mu.Lock()
		dead = g.dead
		g.mu.Unlock()
		if dead {
			return g.topOut(ctx)
		}
		if g.winCheck() {
			return true
		}
	}
	return !g.dead && g.winCheck()
}

// plan builds a grid from the settled board and returns the chosen placement.
func (g *Game) plan(p active) (placement, bool) {
	g.mu.Lock()
	gr := g.toGrid()
	pieceIdx := g.pieceIdx
	g.mu.Unlock()
	upcoming := revealedPieces(g.metaSeed, g.ration, pieceIdx, g.nextCount, g.a.tn.lookahead)
	ranked := planPlacements(gr, p.pt, p.row, p.col, g.spawnC, upcoming)
	return choose(ranked, g.a.tn, g.a.rng)
}

// toGrid snapshots the settled board for the planner (caller holds mu). Other
// players' falling pieces are marked as obstacles (3): solid for placement and
// every feature, but never completing a row — the planner routes around the
// transient obstacle without fantasizing a clear through it.
func (g *Game) toGrid() *grid {
	gr := newGrid(g.height(), g.w)
	for c, v := range g.locked {
		if c.r < 0 || c.r >= gr.h || c.c < 0 || c.c >= gr.w {
			continue
		}
		if v.G {
			gr.set(c.r, c.c, 2)
		} else {
			gr.set(c.r, c.c, 1)
		}
	}
	for c := range g.othersAct {
		if c.r >= 0 && c.r < gr.h && c.c >= 0 && c.c < gr.w {
			gr.set(c.r, c.c, 3)
		}
	}
	return gr
}

// execute drives the piece to (orient, col) honoring gravity, then hard-drops.
// On shared boards another player's falling piece is a TRANSIENT obstacle:
// blocked by it we wait (it falls away), never lock against it, and never top
// out on it (gameplays §3).
func (g *Game) execute(ctx context.Context, plan placement, spawnT time.Time) {
	g.mu.Lock()
	step := gravity
	if g.shared() {
		step = g.gravityNow()
	}
	g.mu.Unlock()
	nextGravity := spawnT.Add(step)
	lastProgress := time.Now()
	var lastPos active
	for {
		g.mu.Lock()
		if g.dead || g.isEnded() {
			g.mu.Unlock()
			return
		}
		if g.pipeBroken {
			// A pipelined batch was lost: drain, repair, re-plan.
			g.settlePipeline(ctx)
			g.mu.Unlock()
			return
		}
		if g.piece == nil {
			g.mu.Unlock()
			return // a shrink squeezed the piece away
		}
		p := *g.piece
		if p != lastPos {
			lastPos, lastProgress = p, time.Now()
		} else if g.shared() && time.Since(lastProgress) > 3*time.Second {
			// Parked behind an obstacle that never moves: a live teammate
			// piece falls away in well under a second, so this is a stale
			// ghost. Rebuild from the stream and re-plan.
			g.settlePipeline(ctx)
			g.resyncShared(ctx)
			g.mu.Unlock()
			return
		}
		locked := false
		if !time.Now().Before(nextGravity) {
			down := active{p.pt, p.orient, p.row + 1, p.col}
			dcs := pieceCells(down.pt, down.orient, down.row, down.col)
			switch {
			case g.canMove(dcs):
				g.publishPieceMove(ctx, down)
			case g.canPlace(dcs):
				// blocked only by another falling piece: wait, don't lock
			default:
				if g.settleForLock(ctx, p) {
					g.mu.Unlock()
					return
				}
				g.lockPiece(ctx, p)
				locked = true
			}
			if g.shared() {
				step = g.gravityNow()
			}
			nextGravity = nextGravity.Add(step)
		} else if p.orient != plan.orient || p.col != plan.col {
			// The whole walk to the plan — the rotations, then the shifts —
			// as ONE batch: every step is validated on the local board, and
			// the piece goes as far along the path as it can. A step blocked
			// by a transient piece ends the walk there (the rest resumes once
			// it falls away); one blocked by the stack, before any step could
			// be made, locks the piece where it stands.
			to, transient := g.walk(p, plan)
			switch {
			case to != p:
				g.publishPieceMove(ctx, to)
			case transient:
				// a crossing piece is in the way: wait it out
			default:
				if g.settleForLock(ctx, p) {
					g.mu.Unlock()
					return
				}
				g.lockPiece(ctx, active{p.pt, p.orient, g.dropRowShared(p), p.col})
				locked = true
			}
		} else {
			// The piece is at its planned column: settle before deciding its
			// landing, so the drop row comes from converged state.
			if g.settleForLock(ctx, p) {
				g.mu.Unlock()
				return
			}
			dr := g.dropRowShared(p)
			below := pieceCells(p.pt, p.orient, dr+1, p.col)
			if g.canPlace(below) && g.sharedBlocked(below) {
				// The drop rests on another FALLING piece: move there but
				// stay active — gravity resumes once the obstacle falls.
				if dr != p.row {
					g.publishPieceMove(ctx, active{p.pt, p.orient, dr, p.col})
				}
			} else {
				g.lockPiece(ctx, active{p.pt, p.orient, dr, p.col})
				locked = true
			}
		}
		g.mu.Unlock()
		if locked {
			return
		}
		if !g.wait(ctx, g.a.tn.moveDelay) {
			return
		}
	}
}

// walk is the path from p to the plan's orientation and column played out
// on the local board — the rotations first, one at a time in place, then
// the shifts, one column at a time — and returns the furthest position it
// reaches: the plan itself when every step is free, or the position before
// the first blocked step, with transient reporting that the blocker is
// another player's falling piece (worth waiting for) rather than the stack.
// The caller publishes the whole walk as one batch. Call with g.mu held.
func (g *Game) walk(p active, plan placement) (to active, transient bool) {
	to = p
	for to.orient != plan.orient {
		st := active{to.pt, (to.orient + 1) & 3, to.row, to.col}
		scs := pieceCells(st.pt, st.orient, st.row, st.col)
		if !g.canMove(scs) {
			return to, g.canPlace(scs)
		}
		to = st
	}
	for to.col != plan.col {
		d := 1
		if plan.col < to.col {
			d = -1
		}
		st := active{to.pt, to.orient, to.row, to.col + d}
		scs := pieceCells(st.pt, st.orient, st.row, st.col)
		if !g.canMove(scs) {
			return to, g.canPlace(scs)
		}
		to = st
	}
	return to, false
}

func (g *Game) winCheck() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.mode {
	case modeCooperative:
		return false // no winner: the game ends when anyone tops out
	case modeTeams:
		return g.othersDead() && !g.dead
	}
	others := 0
	for _, p := range g.roster {
		if p.PlayerID == g.a.name {
			continue
		}
		others++
		if !g.eliminated[p.PlayerID] {
			return false
		}
	}
	return others > 0 && !g.dead
}

func (g *Game) topOut(ctx context.Context) bool {
	switch g.mode {
	case modeCooperative:
		// Any top-out ends the game for everyone; the topper finishes and
		// archives it (gameplays §3).
		g.mu.Lock()
		g.dead = true
		g.eliminated[g.a.name] = true
		g.mu.Unlock()
		g.publishGameOver(ctx)
		log.Printf("topped out — cooperative game over (shared score %d)", g.sharedScore)
		g.transitionFinishedAndArchive(ctx)
		return false
	case modeTeams:
		// A player out is not a team out: vacate our dead piece (gated, so a
		// racing garbage cascade can't resurrect it), announce, and stay for
		// the verdict — our team plays on (gameplays §5).
		g.mu.Lock()
		g.dead = true
		g.vacateOwnPiece(ctx)
		g.eliminated[g.a.name] = true
		g.mu.Unlock()
		g.publishGameOver(ctx)
		log.Printf("topped out — team %s plays on", teamLetter(g.team))
		return g.waitForVerdict(ctx)
	}
	g.mu.Lock()
	g.dead = true
	elim := len(g.eliminated)
	if !g.eliminated[g.a.name] {
		elim++
	}
	pc := g.playerCount
	g.mu.Unlock()
	g.publishGameOver(ctx)
	log.Printf("topped out (score %d)", g.score)
	if elim >= pc { // simultaneous draw: we finish it
		g.transitionFinishedAndArchive(ctx)
	}
	return false
}

func (g *Game) publishGameOver(ctx context.Context) {
	ev := event{Kind: "game_over", PlayerID: g.a.name, PlayerIdx: g.idx, Team: g.team,
		Score: g.score, Level: min(g.lines/10, 19), PieceCount: g.pieceIdx}
	b, _ := json.Marshal(ev)
	_, _ = g.a.js.Publish(ctx, "jetris.game."+g.id+".events.game_over."+g.a.name, b)
}

func (g *Game) transitionFinishedAndArchive(ctx context.Context) {
	if g.a.transitionMeta(ctx, g.id, "finished") {
		g.archive(ctx)
	}
	g.markEnded()
}

func (g *Game) waitForEnd(ctx context.Context) {
	select {
	case <-g.ended:
	case <-ctx.Done():
	case <-g.a.stopCh:
	case <-time.After(120 * time.Second):
	}
}

// wait sleeps d, returning false if the game/agent is stopping.
func (g *Game) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	select {
	case <-ctx.Done():
		return false
	case <-g.a.stopCh:
		return false
	case <-g.ended:
		return false
	case <-time.After(d):
		return true
	}
}

// archiveGrace is how long after the finish the game stream and listing
// survive, so every peer receives the final events before they are deleted.
// Only the destructive steps wait: the record is published right away so
// every lobby shows the finished game immediately.
const archiveGrace = 5 * time.Second

// archive: we triggered the finish, so transition finished→archived (CAS elects
// one archiver), publish the record, archive the replay, then — once the
// grace has passed — delete the game's resources (guide §5.5–5.6).
func (g *Game) archive(ctx context.Context) {
	finished := time.Now()
	meta, seq, err := g.a.fetchMeta(ctx, g.id)
	if err != nil || meta.str("status") != "finished" {
		return
	}
	meta.set("status", "archived")
	if _, conflict, err := g.a.metaPublish(ctx, g.id, meta.bytes(), seq); err != nil || conflict {
		return // someone else won the archive CAS
	}
	record := map[string]any{
		"game_id": g.id, "mode": g.mode, "player_count": meta.int("player_count"),
		"players":      g.playerResults(),
		"started_at":   firstNonEmpty(meta.str("started_at"), meta.str("created_at")),
		"finished_at":  firstNonEmpty(meta.str("finished_at"), nowRFC()),
		"winning_team": -1,
		"boards":       g.boardPictures(ctx),
	}
	// The shared board's width setting travels with the record: without it
	// a replay would rebuild the board a full section per seat wide and lay
	// the recorded cells out on the wrong geometry.
	if extra := meta.int("extra_columns"); extra > 0 && g.mode != modeCompetitive {
		record["extra_columns"] = extra
	}
	switch g.mode {
	case modeCooperative:
		g.mu.Lock()
		record["total_score"] = g.sharedScore
		record["final_level"] = min(g.totalLines/10, 19)
		g.mu.Unlock()
	case modeTeams:
		g.mu.Lock()
		levels := make([]int, len(g.teamLines))
		for t, l := range g.teamLines {
			levels[t] = min(l/10, 19)
		}
		record["team_count"] = g.teams()
		record["team_size"] = g.teamSize()
		record["winning_team"] = g.winningTeam()
		record["team_scores"] = append([]int(nil), g.teamScores...)
		record["team_levels"] = levels
		g.mu.Unlock()
	}
	b, _ := json.Marshal(record)
	// The record goes out FIRST — it is what every lobby's history shows.
	if _, err := g.a.js.Publish(ctx, archiveSubject, b); err != nil {
		log.Printf("archive publish: %v", err)
		// An unlisted game could never be ranked or displaced: no replay,
		// and its stream is left for the startup cleanup pass.
		return
	}
	// Replay archive (guide §5 step 6): copy the game stream to the replay
	// stream (purging the replays this game displaces) — after the record,
	// before the game stream is deleted below.
	g.a.maybeArchiveReplay(ctx, b)
	// Let every peer see the final events before the stream goes away.
	if wait := archiveGrace - time.Since(finished); wait > 0 {
		time.Sleep(wait)
	}
	_ = g.a.js.DeleteStream(ctx, gameStreamName(g.id))
	_ = g.a.kv.Delete(ctx, "games."+g.id)
	if s, err := g.a.js.Stream(ctx, chatStream); err == nil {
		_ = s.Purge(ctx, jetstream.WithPurgeSubject("jetris.chat."+g.id))
	}
	log.Printf("archived game %s", g.id)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (g *Game) playerResults() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	winningTeam := -1
	if g.mode == modeTeams {
		winningTeam = g.winningTeam()
	}
	out := make([]map[string]any, 0, len(g.roster))
	for _, p := range g.roster {
		var score, level, pieces int
		if p.PlayerID == g.a.name {
			score, level, pieces = g.score, min(g.lines/10, 19), g.pieceIdx
		} else if ev, ok := g.results[p.PlayerID]; ok {
			score, level, pieces = ev.Score, ev.Level, ev.PieceCount
		} else if tot, ok := g.senderTotals[p.PlayerID]; ok {
			// Never topped out (a coop survivor, an alive teams winner):
			// their cumulative line-clear totals are the best record we have.
			score, level = tot[0], min(tot[1]/10, 19)
		}
		r := map[string]any{"player_id": p.PlayerID, "score": score, "piece_count": pieces}
		if level != 0 {
			r["level"] = level
		}
		switch g.mode {
		case modeCooperative:
			// no winners in coop
		case modeTeams:
			r["team"] = p.Team
			if p.Team == winningTeam {
				r["winner"] = true
			}
		default:
			if !g.eliminated[p.PlayerID] {
				r["winner"] = true
			}
		}
		if p.Agent {
			r["agent"] = true
		}
		out = append(out, r)
	}
	return out
}

// boardPictures fetches the finished boards' visible regions from the stream
// before it is deleted (row 0 = first visible row, headroom stripped): one
// picture per player in competitive, ONE shared board in coop, one per team
// in teams.
func (g *Game) boardPictures(ctx context.Context) []map[string]any {
	h := g.height()
	snap := func(label string, idx int, subj func(r, c int) string) map[string]any {
		var cells []map[string]any
		for r := headroom; r < h; r++ {
			for c := 0; c < g.w; c++ {
				raw, err := g.stream.GetLastMsgForSubject(ctx, subj(r, c))
				if err != nil || len(raw.Data) == 0 || string(raw.Data) == "{}" {
					continue
				}
				var d map[string]any
				_ = json.Unmarshal(raw.Data, &d)
				cells = append(cells, map[string]any{"r": r - headroom, "c": c, "d": d})
			}
		}
		return map[string]any{"label": label, "idx": idx, "w": g.w, "h": h - headroom, "cells": cells}
	}
	switch g.mode {
	case modeCooperative:
		return []map[string]any{snap("", -1, func(r, c int) string {
			return fmt.Sprintf("jetris.game.%s.playfield.cell.%d.%d", g.id, r, c)
		})}
	case modeTeams:
		pics := make([]map[string]any, 0, g.teams())
		for t := 0; t < g.teams(); t++ {
			t := t
			pics = append(pics, snap("Team "+teamLetter(t), t, func(r, c int) string {
				return fmt.Sprintf("jetris.game.%s.team.%d.playfield.cell.%d.%d", g.id, t, r, c)
			}))
		}
		return pics
	}
	g.mu.Lock()
	ids := make([]string, 0, len(g.roster))
	for _, p := range g.roster {
		ids = append(ids, p.PlayerID)
	}
	g.mu.Unlock()
	sort.Strings(ids)
	pics := make([]map[string]any, 0, len(ids))
	for i, pid := range ids {
		pid := pid
		pics = append(pics, snap(pid, i, func(r, c int) string {
			return fmt.Sprintf("jetris.game.%s.player.%s.playfield.cell.%d.%d", g.id, pid, r, c)
		}))
	}
	return pics
}
