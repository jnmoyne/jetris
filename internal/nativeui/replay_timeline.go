package nativeui

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// A replay is loaded WHOLE before it plays. One ordered consumer reads the
// game's slice of the replay stream end to end, and every message is decoded
// once into a replayTimeline: the cell writes, each stamped with how long
// after the game's start it was recorded; the countdown numbers; and the line
// clears, which the scrubber draws as its markers. Playback is then a pure
// function of a single number — the playhead — so play, pause, scrub, skip
// and speed are all just ways of moving it, and seeking BACKWARDS costs no
// more than seeking forwards. Nothing waits on the network once the load is
// done; nothing depends on the messages arriving at the pace they were
// recorded at, which is what the old pacer had to reconstruct message by
// message from the same timestamps this reads once.

// replayCell is one decoded cell write: where it lands, what the cell becomes,
// and when it was recorded, relative to the timeline's origin.
type replayCell struct {
	off      time.Duration
	board    int32
	row, col int32
	cell     game.Cell
}

// countMark is one pre-game countdown number (0 = GO!) at its recorded time.
type countMark struct {
	off time.Duration
	n   int
}

// clearMark is one line clear: when it happened, how many lines went, and the
// color index of the player who cleared them — the timeline's markers, drawn
// in that player's board color above the scrub slider.
type clearMark struct {
	off   time.Duration
	color int
	lines int
}

// replayTimeline is a whole recorded game in memory. cells and counts are
// sorted by off (stream order, which is timestamp order); startOff is when the
// recorded game left its pre-game statuses, which is when the countdown stops
// being drawn; dur is the length of the recording.
type replayTimeline struct {
	cells    []replayCell
	counts   []countMark
	marks    []clearMark
	startOff time.Duration
	dur      time.Duration
}

// countdownAt returns the countdown number to show at the playhead — the last
// number recorded at or before it, while the recorded game had still not
// started. ok is false once the game starts, so a replay scrubbed into the
// game itself never wears a stale GO!.
func (t *replayTimeline) countdownAt(head time.Duration) (n int, ok bool) {
	if len(t.counts) == 0 || head >= t.startOff {
		return -1, false
	}
	i := sort.Search(len(t.counts), func(i int) bool { return t.counts[i].off > head })
	if i == 0 {
		return -1, false
	}
	return t.counts[i-1].n, true
}

// replayMsgTime parses a copied message's ORIGINAL timestamp header
// (nanoseconds since the Unix epoch — the replay stream itself stamps
// copy-time timestamps, so the recorded pace lives in the header); zero when
// absent or malformed.
func replayMsgTime(msg jetstream.Msg) time.Time {
	ns, err := strconv.ParseInt(msg.Headers().Get(config.ReplayTsHeader), 10, 64)
	if err != nil || ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// replayBuilder decodes one game's replay messages into a replayTimeline as
// they are delivered. It carries the board geometry (the same set the screen
// draws) so a cell subject can be resolved to a board slot once, at load, and
// never again while the replay plays.
type replayBuilder struct {
	rec      config.ArchiveRecord
	boards   []*replayBoard // geometry only — never written
	byPlayer map[string]int
	tl       replayTimeline
	origin   time.Time // recorded time the timeline's clock starts at
	started  bool      // an in_progress (or later) meta has been seen
	n        int       // messages taken in
}

// newReplayBuilder starts a builder for one game's recording; height is the
// boards' total rows, measured off the replay stream by the loader (the
// builder's geometry and the view's must agree, or a cell would land on the
// wrong board or off it).
func newReplayBuilder(rec config.ArchiveRecord, height int) *replayBuilder {
	boards, byPlayer := newReplayBoards(rec, height)
	return &replayBuilder{rec: rec, boards: boards, byPlayer: byPlayer, tl: replayTimeline{startOff: -1}}
}

// add takes one copied message. ts is its ORIGINAL recorded time; the message
// that starts the game — the countdown's first number, or failing that the
// first cell write — sets the timeline's clock, so the lobby wait before it
// (the game's creation, the roster joins, the readying up) is simply prelude
// that never reaches the timeline, and anything recorded during it that DOES
// matter (a board written before the count) sits at zero, where a replay
// opens.
func (b *replayBuilder) add(subject string, data []byte, ts time.Time) {
	b.n++
	gameID := b.rec.GameID
	switch subject {
	case replayCountdownSubject(gameID):
		var cd struct {
			Seconds int `json:"seconds"`
		}
		if err := json.Unmarshal(data, &cd); err != nil || cd.Seconds < 0 || b.started {
			return
		}
		if len(b.tl.counts) == 0 && !ts.IsZero() {
			// The countdown IS the game starting, so it takes the clock even
			// from cells recorded before it: a board written during the lobby
			// (a rejoin's catch-up, a seeded board) is the board the game
			// opens on, and it belongs at zero, not at a negative time.
			b.origin = ts
			for i := range b.tl.cells {
				b.tl.cells[i].off = 0
			}
		}
		b.tl.counts = append(b.tl.counts, countMark{off: b.offset(ts), n: cd.Seconds})
		return
	case replayMetaSubject(gameID):
		var meta config.GameMeta
		if err := json.Unmarshal(data, &meta); err != nil || preStartStatus(string(meta.Status)) || b.started {
			return
		}
		// The status that ends the countdown, at the moment it was recorded:
		// the same rule the live screen uses (countdownVisible), read off the
		// recording instead of off a running game.
		b.started = true
		b.tl.startOff = b.offset(ts)
		return
	}
	if ev, ok := b.decodeClearEvent(subject, data, ts); ok {
		b.tl.marks = append(b.tl.marks, ev)
		return
	}
	if c, ok := b.decodeCell(subject, data); ok {
		if b.origin.IsZero() && !ts.IsZero() {
			b.origin = ts
		}
		c.off = b.offset(ts)
		b.tl.cells = append(b.tl.cells, c)
	}
}

// offset places a recorded time on the timeline's clock. A message recorded
// before the origin — or with no recorded time at all — sits at zero.
func (b *replayBuilder) offset(ts time.Time) time.Duration {
	if ts.IsZero() || b.origin.IsZero() || ts.Before(b.origin) {
		return 0
	}
	return ts.Sub(b.origin)
}

// finish closes the timeline: its length, the clears the modes that publish no
// line_clear event have to be read off the boards themselves, and the
// countdown's end for a recording that never carried the meta that started it.
func (b *replayBuilder) finish() *replayTimeline {
	t := &b.tl
	if n := len(t.cells); n > 0 {
		t.dur = t.cells[n-1].off
	}
	if n := len(t.counts); n > 0 && t.counts[n-1].off > t.dur {
		t.dur = t.counts[n-1].off
	}
	if t.startOff < 0 {
		// No recorded in_progress meta: the countdown ends where the game's
		// first cell write begins.
		t.startOff = t.dur
		if len(t.cells) > 0 {
			t.startOff = t.cells[0].off
		}
	}
	if b.rec.Mode == config.ModeCompetitive {
		t.marks = detectClears(t.cells, b.boards)
	}
	sort.SliceStable(t.marks, func(i, j int) bool { return t.marks[i].off < t.marks[j].off })
	return t
}

// decodeCell resolves one copied cell subject to a board slot and unmarshals
// its payload. The copied subjects keep the game-stream tail under the replay
// prefix:
//
//	jetris.replay.<id>.playfield.cell.<r>.<c>              (cooperative)
//	jetris.replay.<id>.player.<pid>.playfield.cell.<r>.<c> (competitive)
//	jetris.replay.<id>.team.<t>.playfield.cell.<r>.<c>     (teams)
//
// Everything else — the roster, the events, the garbage and txn registers —
// is not a cell and never reaches a board.
func (b *replayBuilder) decodeCell(subject string, data []byte) (replayCell, bool) {
	if config.IsGarbageSubject(subject) || config.IsTxnSubject(subject) {
		return replayCell{}, false
	}
	row, col := natspkg.ParseCellFromSubject(subject)
	if row < 0 {
		return replayCell{}, false
	}
	tokens := strings.Split(subject, ".")
	if len(tokens) < 7 {
		return replayCell{}, false
	}
	boardIdx := -1
	switch tokens[3] {
	case "playfield":
		if b.rec.Mode == config.ModeCooperative {
			boardIdx = 0
		}
	case "player":
		if i, ok := b.byPlayer[tokens[4]]; ok {
			boardIdx = i
		}
	case "team":
		if t, err := strconv.Atoi(tokens[4]); err == nil && t >= 0 && t < len(b.boards) {
			boardIdx = t
		}
	}
	if boardIdx < 0 || boardIdx >= len(b.boards) {
		return replayCell{}, false
	}
	brd := b.boards[boardIdx]
	if row >= brd.height || col < 0 || col >= brd.width {
		return replayCell{}, false
	}
	cell, err := game.UnmarshalCell(data)
	if err != nil {
		return replayCell{}, false
	}
	return replayCell{board: int32(boardIdx), row: int32(row), col: int32(col), cell: cell}, true
}

// decodeClearEvent reads a line_clear event — published on the SHARED boards
// only (cooperative and teams, where a clear is everyone's business and moves
// a shared scoreboard); competitive boards publish none, and their clears are
// read off the boards by detectClears instead. The marker takes the clearing
// player's own color, the one the cells they lock wear on the shared board.
func (b *replayBuilder) decodeClearEvent(subject string, data []byte, ts time.Time) (clearMark, bool) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < 6 || tokens[3] != "events" || tokens[4] != string(engine.EventLineClear) {
		return clearMark{}, false
	}
	var ev engine.GameEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.LinesCleared <= 0 {
		return clearMark{}, false
	}
	return clearMark{off: b.offset(ts), color: ev.PlayerIdx, lines: ev.LinesCleared}, true
}

// replayBatchGap separates one PUBLISHED BATCH from the next when clears are
// read off the boards. A batch — a lock, a clear's collapse, a garbage raise —
// is written to the stream in one go, its messages microseconds apart, while
// consecutive batches are the better part of a millisecond apart or more. This
// sits an order of magnitude above the first and below the second.
const replayBatchGap = 300 * time.Microsecond

// replayLockCells is the most cells one lock can add, and so the most a batch
// that carries BOTH a lock and the clear it triggered can hide from the
// measurement below.
const replayLockCells = 4

// detectClears finds the line clears of a competitive recording, which
// publishes no line_clear event — the boards are the only record of them.
//
// It replays the cells onto scratch boards and watches ONE number per board,
// batch by batch: how many cells the board holds. A falling piece never moves
// that number (its cells are Active and hold nothing); locking a piece adds
// four; a garbage raise adds whole rows at the bottom and can only push as
// many cells off the top as it added, so it can never end a batch DOWN. Only a
// clear takes cells away, and it takes exactly one row's worth per line — the
// four a lock in the same batch had just added included, which is why the
// measurement runs from the batch's peak to where it settles, and why the
// distance is a row's width per line whatever the piece was or wherever it
// landed. A batch that ends up holding more adversarial cells than it started
// with is a raise, and is left alone: on a board stacked to the ceiling a
// raise does lose cells off the top, and those are not a clear.
func detectClears(cells []replayCell, boards []*replayBoard) []clearMark {
	if len(boards) == 0 {
		return nil
	}
	scratch := make([]*replayBoard, len(boards))
	for i, b := range boards {
		scratch[i] = newReplayBoard(b.label, b.idx, b.width, b.height, b.visibleStart)
	}
	type batch struct {
		open    bool
		startAt time.Duration
		lastAt  time.Duration
		dropAt  time.Duration // the last write that took a cell away: when the row went
		peak    int           // the most cells the board held during the batch
		adv0    int           // adversarial cells it started with
	}
	batches := make([]batch, len(boards))
	held := make([]int, len(boards))
	adv := make([]int, len(boards))
	var out []clearMark

	endBatch := func(i int) {
		b := &batches[i]
		if !b.open {
			return
		}
		b.open = false
		if adv[i] > b.adv0 {
			return // a raise: what it took off the top it had just added at the bottom
		}
		w := boards[i].width
		drop := b.peak - held[i]
		if drop < w/2 {
			return
		}
		lines := int(math.Round(float64(drop) / float64(w)))
		if lines < 1 || lines > 4 || abs(drop-lines*w) > replayLockCells {
			return // neither a clear nor a clear plus its lock: leave it alone
		}
		out = append(out, clearMark{off: max(b.dropAt, b.startAt), color: boards[i].idx, lines: lines})
	}
	for _, c := range cells {
		i := int(c.board)
		if i < 0 || i >= len(scratch) {
			continue
		}
		if batches[i].open && c.off-batches[i].lastAt > replayBatchGap {
			endBatch(i)
		}
		if !batches[i].open {
			batches[i] = batch{open: true, startAt: c.off, peak: held[i], adv0: adv[i]}
		}
		batches[i].lastAt = c.off
		old := scratch[i].rows[c.row].Cells[c.col]
		scratch[i].rows[c.row].Cells[c.col] = c.cell
		switch {
		case !old.Occupied && c.cell.Occupied:
			held[i]++
		case old.Occupied && !c.cell.Occupied:
			held[i]--
			batches[i].dropAt = c.off // the collapse: where the marker goes
		}
		if old.Occupied && old.Adversarial {
			adv[i]--
		}
		if c.cell.Occupied && c.cell.Adversarial {
			adv[i]++
		}
		batches[i].peak = max(batches[i].peak, held[i])
	}
	for i := range batches {
		endBatch(i)
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
