package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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

	mu       sync.Mutex
	locked   map[cell]wireCell // settled cells (stack + garbage)
	seqs     map[cell]uint64   // per-cell CAS expectation (last stream seq)
	piece    *active
	pieceIdx int
	score    int
	lines    int

	eliminated map[string]bool
	results    map[string]event
	roster     []playerSummary

	garbageOwed int
	garbageBy   int
	txnApplied  int
	txnSeq      uint64

	metaSeed    uint64
	playerCount int
	nextCount   int
	dead        bool

	stream       jetstream.Stream
	started      chan struct{}
	ended        chan struct{}
	startedOnce  sync.Once
	endedOnce    sync.Once
	consumeCtxts []jetstream.ConsumeContext
}

func newGame(a *Agent, id string, idx int) *Game {
	return &Game{
		a: a, id: id, idx: idx,
		locked: map[cell]wireCell{}, seqs: map[cell]uint64{},
		eliminated: map[string]bool{}, results: map[string]event{},
		started: make(chan struct{}), ended: make(chan struct{}),
	}
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

func (g *Game) height() int { return 28 + g.playerCount } // 4 headroom + 24 + P

// ---- subjects ------------------------------------------------------------

func (g *Game) cellSubject(c cell) string {
	return fmt.Sprintf("jetris.game.%s.player.%s.playfield.cell.%d.%d", g.id, g.a.name, c.r, c.c)
}
func (g *Game) garbageSubject(pid string) string {
	return fmt.Sprintf("jetris.game.%s.player.%s.playfield.garbage", g.id, pid)
}
func (g *Game) txnSubject() string {
	return fmt.Sprintf("jetris.game.%s.player.%s.playfield.txn", g.id, g.a.name)
}

// ---- cell payloads -------------------------------------------------------

func activePayload(a active) *wireCell {
	return &wireCell{T: a.pt, A: true, R: a.orient, Ar: a.row, Ac: a.col}
}
func (g *Game) lockedPayload(pt int) wireCell {
	c := wireCell{O: true, T: pt}
	if g.idx != 0 {
		c.Pi = g.idx
	}
	return c
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
		if c.r < 0 || c.r >= h || c.c < 0 || c.c >= width {
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
	at cell
	c  *wireCell // nil vacates
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

// resync refetches our own board from the stream after a dropped write.
func (g *Game) resync(ctx context.Context) {
	g.locked = map[cell]wireCell{}
	g.seqs = map[cell]uint64{}
	g.piece = nil
	h := g.height()
	for r := 0; r < h; r++ {
		for c := 0; c < width; c++ {
			at := cell{r, c}
			raw, err := g.stream.GetLastMsgForSubject(ctx, g.cellSubject(at))
			if err != nil {
				continue
			}
			g.seqs[at] = raw.Sequence
			var wc wireCell
			if len(raw.Data) > 0 {
				_ = json.Unmarshal(raw.Data, &wc)
			}
			switch {
			case wc.A:
				g.piece = &active{wc.T, wc.R, wc.Ar, wc.Ac}
			case wc.O:
				g.locked[at] = wc
			}
		}
	}
}

func (g *Game) refreshRegisters(ctx context.Context) {
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
	if raw, err := g.stream.GetLastMsgForSubject(ctx, g.garbageSubject(g.a.name)); err == nil {
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
// `to`. On a dropped write it flashes and resyncs.
func (g *Game) publishPieceMove(ctx context.Context, to active) bool {
	old := g.activeCells()
	newCells := pieceCells(to.pt, to.orient, to.row, to.col)
	newSet := cellSet(newCells)
	var cells []cellUpd
	for _, c := range newCells {
		cells = append(cells, cellUpd{c, activePayload(to)})
	}
	for _, c := range old {
		if !newSet[c] {
			cells = append(cells, cellUpd{c, nil})
		}
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

// spawn publishes a fresh piece. Returns the spawn time and whether it topped
// out (spawn blocked by settled cells).
func (g *Game) spawn(ctx context.Context) (time.Time, bool) {
	pt := pieceAt(g.metaSeed, g.pieceIdx)
	n := active{pt, 0, spawnRow, spawnCol}
	cs := pieceCells(pt, 0, spawnRow, spawnCol)
	if !g.canPlace(cs) {
		return time.Time{}, true
	}
	var cells []cellUpd
	for _, c := range cs {
		cells = append(cells, cellUpd{c, activePayload(n)})
	}
	if err := g.publishBatch(ctx, cells, true); err != nil {
		if errors.Is(err, errCAS) {
			g.flash(cs[:])
		} else {
			log.Printf("spawn: %v", err)
		}
		g.resync(ctx)
	} else {
		p := n
		g.piece = &p
	}
	return time.Now(), false
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
		cells = append(cells, cellUpd{c, &cc})
	}
	for _, c := range old {
		if !destSet[c] {
			cells = append(cells, cellUpd{c, nil})
		}
	}
	if err := g.publishBatch(ctx, cells, false); err != nil {
		log.Printf("lock: %v", err)
	}
	for _, c := range destCells {
		g.locked[c] = lp
	}
	g.piece = nil
	if done := g.completedRows(); len(done) > 0 {
		g.clearRows(ctx, done)
	}
	g.pieceIdx++
	g.bumpMetaPieceIdx(ctx)
}

func (g *Game) completedRows() []int {
	var out []int
	h := g.height()
	for r := 0; r < h; r++ {
		full, garbage := true, false
		for c := 0; c < width; c++ {
			cc, ok := g.locked[cell{r, c}]
			if !ok {
				full = false
				break
			}
			if cc.G {
				garbage = true
			}
		}
		if full && !garbage {
			out = append(out, r)
		}
	}
	return out
}

// clearRows collapses completed rows as a GATED transform, then attacks every
// surviving opponent's garbage register.
func (g *Game) clearRows(ctx context.Context, rows []int) {
	cleared := 0
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			g.refreshRegisters(ctx)
			g.resync(ctx)
			rows = g.completedRows()
			if len(rows) == 0 {
				return
			}
		}
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
			for c := 0; c < width; c++ {
				if cc, ok := g.locked[cell{r, c}]; ok {
					newLocked[cell{r + shift, c}] = cc
				}
			}
		}
		diff := g.diffCells(newLocked)
		txn := txnReg{Applied: g.txnApplied, Op: "clear", By: g.idx}
		if err := g.publishGatedBatch(ctx, txn, diff); err != nil {
			if errors.Is(err, errCAS) {
				continue
			}
			log.Printf("clear: %v", err)
			return
		}
		g.locked = newLocked
		cleared = len(rows)
		break
	}
	if cleared == 0 {
		return
	}
	g.score += cleared
	g.lines += cleared
	log.Printf("cleared %d line(s), score %d", cleared, g.score)
	g.bumpVictimLedgers(ctx, cleared)
}

// diffCells returns the cell updates that turn g.locked into newLocked.
func (g *Game) diffCells(newLocked map[cell]wireCell) []cellUpd {
	var diff []cellUpd
	h := g.height()
	for r := 0; r < h; r++ {
		for c := 0; c < width; c++ {
			at := cell{r, c}
			nv, nok := newLocked[at]
			ov, ook := g.locked[at]
			if nok != ook || nv != ov {
				if nok {
					cc := nv
					diff = append(diff, cellUpd{at, &cc})
				} else {
					diff = append(diff, cellUpd{at, nil})
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
// the stack up, fill the bottom with permanent adversarial rows, lift the
// falling piece the minimum needed to clear the risen stack (top out if it is
// pushed off), and top out if locked cells are shoved past row 0.
func (g *Game) applyOwedGarbage(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
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
		for r := h - n; r < h; r++ {
			for c := 0; c < width; c++ {
				newLocked[cell{r, c}] = gp
			}
		}
		// Lift the falling piece the minimum amount that clears the risen stack.
		var newPiece *active
		squeezed := false
		if g.piece != nil {
			squeezed = true
			for k := 0; ; k++ {
				cand := active{g.piece.pt, g.piece.orient, g.piece.row - k, g.piece.col}
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
				if g.canPlaceIn(newLocked, cs) {
					p := cand
					newPiece, squeezed = &p, false
					break
				}
			}
		}
		diff := g.diffCells(newLocked)
		if g.piece != nil {
			oldActive := g.activeCells()
			if squeezed {
				for _, c := range oldActive {
					if _, in := newLocked[c]; !in {
						diff = append(diff, cellUpd{c, nil})
					}
				}
			} else if newPiece != nil && *newPiece != *g.piece {
				newCells := cellSet(pieceCells(newPiece.pt, newPiece.orient, newPiece.row, newPiece.col))
				for c := range newCells {
					diff = append(diff, cellUpd{c, activePayload(*newPiece)})
				}
				for _, c := range oldActive {
					if !newCells[c] {
						if _, in := newLocked[c]; !in {
							diff = append(diff, cellUpd{c, nil})
						}
					}
				}
			}
		}
		txn := txnReg{Applied: g.garbageOwed, Op: "shrink", By: g.idx}
		if squeezed {
			txn.Topped = []int{g.idx}
		}
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
		if squeezed {
			g.piece = nil
		} else if newPiece != nil {
			g.piece = newPiece
		}
		log.Printf("garbage: +%d row(s) from player %d", n, causer)
		if squeezed || boardFull {
			g.dead = true
		}
		return
	}
}

// canPlaceIn is canPlace against an explicit locked map (used mid-transform).
func (g *Game) canPlaceIn(locked map[cell]wireCell, cs [4]cell) bool {
	h := g.height()
	for _, c := range cs {
		if c.r < 0 || c.r >= h || c.c < 0 || c.c >= width {
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
		if ev.Kind != "game_over" {
			return
		}
		g.mu.Lock()
		g.eliminated[ev.PlayerID] = true
		if ev.PlayerID != g.a.name {
			g.results[ev.PlayerID] = ev
		}
		g.mu.Unlock()
	}, true)
	if err != nil {
		return err
	}
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
	g.consumeCtxts = []jetstream.ConsumeContext{metaCons, eventsCons, garbageCons}
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
	g.playerCount = meta.int("player_count")
	g.nextCount = meta.int("next_count")
	g.pieceIdx = meta.int("piece_idx")
	g.a.mu.Lock()
	g.roster = g.a.listings[g.id].players()
	g.a.mu.Unlock()
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
	// after the wait timeout we walk away cleanly (guide §5.6) and return to
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
	for !g.isEnded() {
		g.mu.Lock()
		dead := g.dead
		g.mu.Unlock()
		if dead {
			return g.topOut(ctx)
		}
		g.mu.Lock()
		spawnT, over := g.spawn(ctx)
		g.mu.Unlock()
		if over {
			return g.topOut(ctx)
		}
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
	k := g.a.tn.lookahead
	if k > g.nextCount {
		k = g.nextCount
	}
	var upcoming []int
	for i := 1; i <= k; i++ {
		upcoming = append(upcoming, pieceAt(g.metaSeed, pieceIdx+i))
	}
	ranked := planPlacements(gr, p.pt, p.row, p.col, upcoming)
	return choose(ranked, g.a.tn, g.a.rng)
}

// toGrid snapshots the settled board for the planner (caller holds mu).
func (g *Game) toGrid() *grid {
	gr := newGrid(g.height())
	for c, v := range g.locked {
		if c.r < 0 || c.r >= gr.h || c.c < 0 || c.c >= width {
			continue
		}
		if v.G {
			gr.set(c.r, c.c, 2)
		} else {
			gr.set(c.r, c.c, 1)
		}
	}
	return gr
}

// execute drives the piece to (orient, col) honoring gravity, then hard-drops.
func (g *Game) execute(ctx context.Context, plan placement, spawnT time.Time) {
	nextGravity := spawnT.Add(gravity)
	for {
		g.mu.Lock()
		if g.dead || g.isEnded() {
			g.mu.Unlock()
			return
		}
		if g.piece == nil {
			g.mu.Unlock()
			return // a shrink squeezed the piece away
		}
		p := *g.piece
		locked := false
		if !time.Now().Before(nextGravity) {
			down := active{p.pt, p.orient, p.row + 1, p.col}
			if g.canPlace(pieceCells(down.pt, down.orient, down.row, down.col)) {
				g.publishPieceMove(ctx, down)
				nextGravity = nextGravity.Add(gravity)
			} else {
				g.lockPiece(ctx, p)
				locked = true
			}
		} else if p.orient != plan.orient {
			step := active{p.pt, (p.orient + 1) & 3, p.row, p.col}
			if !g.canPlace(pieceCells(step.pt, step.orient, step.row, step.col)) {
				g.lockPiece(ctx, active{p.pt, p.orient, g.dropRowLocked(p), p.col})
				locked = true
			} else {
				g.publishPieceMove(ctx, step)
			}
		} else if p.col != plan.col {
			d := 1
			if plan.col < p.col {
				d = -1
			}
			step := active{p.pt, p.orient, p.row, p.col + d}
			if !g.canPlace(pieceCells(step.pt, step.orient, step.row, step.col)) {
				g.lockPiece(ctx, active{p.pt, p.orient, g.dropRowLocked(p), p.col})
				locked = true
			} else {
				g.publishPieceMove(ctx, step)
			}
		} else {
			g.lockPiece(ctx, active{p.pt, p.orient, g.dropRowLocked(p), p.col})
			locked = true
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

func (g *Game) winCheck() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
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
	ev := event{Kind: "game_over", PlayerID: g.a.name, Score: g.score, Level: min(g.lines/10, 19), PieceCount: g.pieceIdx}
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

// archive: we triggered the finish, so transition finished→archived (CAS elects
// one archiver), publish the record, delete the game's resources (guide §5.5).
func (g *Game) archive(ctx context.Context) {
	time.Sleep(5 * time.Second) // let every peer see the final events
	meta, seq, err := g.a.fetchMeta(ctx, g.id)
	if err != nil || meta.str("status") != "finished" {
		return
	}
	meta.set("status", "archived")
	if _, conflict, err := g.a.metaPublish(ctx, g.id, meta.bytes(), seq); err != nil || conflict {
		return // someone else won the archive CAS
	}
	record := map[string]any{
		"game_id": g.id, "mode": modeCompetitive, "player_count": meta.int("player_count"),
		"players":      g.playerResults(),
		"started_at":   firstNonEmpty(meta.str("started_at"), meta.str("created_at")),
		"finished_at":  firstNonEmpty(meta.str("finished_at"), nowRFC()),
		"winning_team": -1,
		"boards":       g.boardPictures(ctx),
	}
	b, _ := json.Marshal(record)
	if _, err := g.a.js.Publish(ctx, archiveSubject, b); err != nil {
		log.Printf("archive publish: %v", err)
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
	out := make([]map[string]any, 0, len(g.roster))
	for _, p := range g.roster {
		var score, level, pieces int
		if p.PlayerID == g.a.name {
			score, level, pieces = g.score, min(g.lines/10, 19), g.pieceIdx
		} else if ev, ok := g.results[p.PlayerID]; ok {
			score, level, pieces = ev.Score, ev.Level, ev.PieceCount
		}
		r := map[string]any{"player_id": p.PlayerID, "score": score, "piece_count": pieces}
		if level != 0 {
			r["level"] = level
		}
		if !g.eliminated[p.PlayerID] {
			r["winner"] = true
		}
		if p.Agent {
			r["agent"] = true
		}
		out = append(out, r)
	}
	return out
}

// boardPictures fetches each player's visible region from the stream before it
// is deleted (row 0 = first visible row, headroom stripped).
func (g *Game) boardPictures(ctx context.Context) []map[string]any {
	g.mu.Lock()
	ids := make([]string, 0, len(g.roster))
	for _, p := range g.roster {
		ids = append(ids, p.PlayerID)
	}
	g.mu.Unlock()
	sort.Strings(ids)
	h := g.height()
	pics := make([]map[string]any, 0, len(ids))
	for i, pid := range ids {
		var cells []map[string]any
		for r := headroom; r < h; r++ {
			for c := 0; c < width; c++ {
				subj := fmt.Sprintf("jetris.game.%s.player.%s.playfield.cell.%d.%d", g.id, pid, r, c)
				raw, err := g.stream.GetLastMsgForSubject(ctx, subj)
				if err != nil || len(raw.Data) == 0 || string(raw.Data) == "{}" {
					continue
				}
				var d map[string]any
				_ = json.Unmarshal(raw.Data, &d)
				cells = append(cells, map[string]any{"r": r - headroom, "c": c, "d": d})
			}
		}
		pics = append(pics, map[string]any{"label": pid, "idx": i, "w": width, "h": h - headroom, "cells": cells})
	}
	return pics
}
