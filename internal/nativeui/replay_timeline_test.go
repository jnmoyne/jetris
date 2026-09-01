package nativeui

import (
	"encoding/json"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

// replayFeed builds a timeline the way the loader does: messages in stream
// order, each with the original time it was recorded at.
type replayFeed struct {
	b  *replayBuilder
	t0 time.Time
}

func newReplayFeed(rec config.ArchiveRecord, t0 time.Time) *replayFeed {
	return &replayFeed{b: newReplayBuilder(rec), t0: t0}
}

func (f *replayFeed) at(off time.Duration, subject string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	f.b.add(subject, data, f.t0.Add(off))
}

func lockedCell() game.Cell { return game.Cell{Occupied: true, PieceType: game.PieceL} }

// The timeline's clock starts at the countdown — the game as the stream
// recorded it — so the lobby wait before it never reaches the playhead, the
// numbers land a second apart, and the meta that starts the game is where the
// countdown stops being shown.
func TestReplayTimelineBuild(t *testing.T) {
	const id = "g1"
	rec := config.ArchiveRecord{GameID: id, Mode: config.ModeCompetitive, PlayerCount: 2,
		Players: []config.PlayerResult{{PlayerID: "alice"}, {PlayerID: "bob"}}}
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	f := newReplayFeed(rec, t0)

	// Four minutes of lobby before anything: created, a join, a readying up.
	f.at(-4*time.Minute, replayMetaSubject(id), config.GameMeta{GameID: id, Status: config.GameStatusCreated})
	f.at(-3*time.Minute, config.ReplayCopySubject(id, config.RosterSubject(id, "alice")), map[string]string{"player_id": "alice"})
	f.at(-time.Minute, replayMetaSubject(id), config.GameMeta{GameID: id, Status: config.GameStatusStarting})
	// The countdown, then the start, then two cells a second apart.
	for i, n := range []int{3, 2, 1, 0} {
		f.at(time.Duration(i)*time.Second, replayCountdownSubject(id), map[string]int{"seconds": n})
	}
	f.at(4700*time.Millisecond, replayMetaSubject(id), config.GameMeta{GameID: id, Status: config.GameStatusInProgress})
	f.at(5*time.Second, config.ReplayCopySubject(id, config.CompetitiveCellSubject(id, "alice", 6, 2)), lockedCell())
	f.at(6*time.Second, config.ReplayCopySubject(id, config.CompetitiveCellSubject(id, "bob", 7, 3)), lockedCell())
	// A garbage register write is not a cell and never reaches a board.
	f.at(7*time.Second, config.ReplayCopySubject(id, config.CompetitiveGarbageSubject(id, "alice")), map[string]int{"rows": 2})
	tl := f.b.finish()

	if len(tl.counts) != 4 || tl.counts[0].off != 0 || tl.counts[0].n != 3 {
		t.Fatalf("counts = %+v, want 3-2-1-GO from zero", tl.counts)
	}
	if got, want := tl.counts[3].off, 3*time.Second; got != want {
		t.Errorf("GO! at %v, want %v", got, want)
	}
	if got, want := tl.startOff, 4700*time.Millisecond; got != want {
		t.Errorf("startOff = %v, want %v", got, want)
	}
	if len(tl.cells) != 2 {
		t.Fatalf("cells = %d, want the two playfield writes (the roster, metas and garbage are not cells)", len(tl.cells))
	}
	if got, want := tl.cells[0].off, 5*time.Second; got != want {
		t.Errorf("first cell at %v, want %v", got, want)
	}
	if tl.cells[0].board != 0 || tl.cells[1].board != 1 {
		t.Errorf("cells landed on boards %d and %d, want alice's then bob's", tl.cells[0].board, tl.cells[1].board)
	}
	if got, want := tl.dur, 6*time.Second; got != want {
		t.Errorf("duration = %v, want %v (the last cell)", got, want)
	}

	// The countdown as a function of the playhead: the last number recorded
	// at or before it, and nothing at all once the game has started.
	for _, tc := range []struct {
		head time.Duration
		n    int
		ok   bool
	}{
		{-time.Second, -1, false},
		{0, 3, true},
		{1500 * time.Millisecond, 2, true},
		{3 * time.Second, 0, true},
		{4500 * time.Millisecond, 0, true},
		{tl.startOff, -1, false},
		{5 * time.Second, -1, false},
	} {
		n, ok := tl.countdownAt(tc.head)
		if n != tc.n || ok != tc.ok {
			t.Errorf("countdownAt(%v) = (%d, %v), want (%d, %v)", tc.head, n, ok, tc.n, tc.ok)
		}
	}
}

// A recording with no in_progress meta (an older copy, or one whose start was
// trimmed) still stops counting down: the first cell write is the game.
func TestReplayTimelineStartsAtFirstCell(t *testing.T) {
	const id = "g2"
	rec := config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative, PlayerCount: 2}
	f := newReplayFeed(rec, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	f.at(0, replayCountdownSubject(id), map[string]int{"seconds": 1})
	f.at(time.Second, replayCountdownSubject(id), map[string]int{"seconds": 0})
	f.at(2*time.Second, config.ReplayCopySubject(id, config.CoopCellSubject(id, 8, 1)), lockedCell())
	tl := f.b.finish()
	if got, want := tl.startOff, 2*time.Second; got != want {
		t.Fatalf("startOff = %v, want the first cell at %v", got, want)
	}
	if _, ok := tl.countdownAt(2 * time.Second); ok {
		t.Error("the countdown outlived the first cell of the game")
	}
}

// Seeking is the whole player: forwards walks the cells on, backwards rebuilds
// from empty, and either way the boards hold exactly what the recording had at
// the playhead.
func TestReplaySeek(t *testing.T) {
	const id = "g3"
	rec := config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative, PlayerCount: 2}
	f := newReplayFeed(rec, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	f.at(0, replayCountdownSubject(id), map[string]int{"seconds": 0})
	cellSubj := func(r, c int) string { return config.ReplayCopySubject(id, config.CoopCellSubject(id, r, c)) }
	// A cell written at 1s, vacated at 3s; a second cell written at 2s.
	f.at(time.Second, cellSubj(10, 4), lockedCell())
	f.at(2*time.Second, cellSubj(11, 5), lockedCell())
	f.at(3*time.Second, cellSubj(10, 4), game.Cell{})

	rv := newReplayView(rec)
	rv.tl = f.b.finish()
	occupied := func(r, c int) bool { return rv.boards[0].rows[r].Cells[c].Occupied }

	for _, tc := range []struct {
		head       time.Duration
		a, b       bool
		wantCursor int
	}{
		{0, false, false, 0},
		{1500 * time.Millisecond, true, false, 1},
		{2500 * time.Millisecond, true, true, 2},
		{4 * time.Second, false, true, 3},         // forwards over the vacate
		{1500 * time.Millisecond, true, false, 1}, // backwards: rebuilt from empty
		{0, false, false, 0},
		{time.Hour, false, true, 3}, // past the end: everything applied
	} {
		rv.seek(tc.head)
		if occupied(10, 4) != tc.a || occupied(11, 5) != tc.b || rv.cursor != tc.wantCursor {
			t.Errorf("seek(%v): cells (%v,%v) cursor %d, want (%v,%v) cursor %d",
				tc.head, occupied(10, 4), occupied(11, 5), rv.cursor, tc.a, tc.b, tc.wantCursor)
		}
	}
}

// The shared boards publish a line_clear event per clear, so their markers are
// read straight off it: when, how many lines, and whose color.
func TestReplayClearMarksFromEvents(t *testing.T) {
	const id = "g4"
	rec := config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative, PlayerCount: 2}
	f := newReplayFeed(rec, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	f.at(0, replayCountdownSubject(id), map[string]int{"seconds": 0})
	ev := func(pid string, idx, lines int) any {
		return engine.GameEvent{Kind: engine.EventLineClear, PlayerID: pid, PlayerIdx: idx, LinesCleared: lines}
	}
	subj := func(pid string) string {
		return config.ReplayCopySubject(id, config.EventKindSubject(id, string(engine.EventLineClear), pid))
	}
	f.at(10*time.Second, subj("alice"), ev("alice", 0, 2))
	f.at(20*time.Second, subj("bob"), ev("bob", 1, 4))
	// A game_over event on the same events subtree is not a clear.
	f.at(30*time.Second, config.ReplayCopySubject(id, config.EventKindSubject(id, string(engine.EventGameOver), "bob")),
		engine.GameEvent{Kind: engine.EventGameOver, PlayerID: "bob"})
	tl := f.b.finish()

	want := []clearMark{{off: 10 * time.Second, color: 0, lines: 2}, {off: 20 * time.Second, color: 1, lines: 4}}
	if len(tl.marks) != len(want) {
		t.Fatalf("marks = %+v, want %+v", tl.marks, want)
	}
	for i, m := range tl.marks {
		if m != want[i] {
			t.Errorf("mark %d = %+v, want %+v", i, m, want[i])
		}
	}
}

// Competitive games publish no line_clear event, so their markers are read off
// the boards: locked cells only ever vanish a row at a time, and only a clear
// can take them.
func TestDetectClearsFromBoards(t *testing.T) {
	board := newReplayBoard("p1", 0, config.StandardWidth, 8, 0)
	var cells []replayCell
	at := func(off time.Duration, row, col int, c game.Cell) {
		cells = append(cells, replayCell{off: off, board: 0, row: int32(row), col: int32(col), cell: c})
	}
	locked := lockedCell()
	active := game.Cell{Occupied: true, Active: true, PieceType: game.PieceI}
	garbage := game.Cell{Occupied: true, Adversarial: true}

	// t=1s: the row fills, nine cells locked — no clear yet.
	for c := 0; c < 9; c++ {
		at(time.Second, 7, c, locked)
	}
	// t=5s: a piece is falling (active cells move about, locking nothing).
	at(5*time.Second, 3, 4, active)
	at(5100*time.Millisecond, 3, 4, game.Cell{})
	at(5200*time.Millisecond, 3, 5, active)
	// t=10s: the piece locks into the last hole and the row goes.
	at(10*time.Second, 7, 9, locked)
	at(10010*time.Millisecond, 3, 5, game.Cell{})
	for c := 0; c < config.StandardWidth; c++ {
		at(10020*time.Millisecond, 7, c, game.Cell{})
	}
	// t=20s: the row fills again and clears again — and a garbage raise lands
	// on the board a batch later, adding a solid row and pushing the cell at
	// the top off the board. Cells lost, but not to a clear: the raise must
	// not swallow the clear it arrives beside, nor pass for one itself.
	for c := 0; c < config.StandardWidth; c++ {
		at(20*time.Second, 7, c, locked)
	}
	at(20500*time.Millisecond, 0, 3, locked) // something at the ceiling to lose
	for c := 0; c < config.StandardWidth; c++ {
		at(21*time.Second, 7, c, game.Cell{}) // the clear
	}
	at(21002*time.Millisecond, 0, 3, game.Cell{}) // the raise: pushed off the top
	for c := 0; c < config.StandardWidth; c++ {
		at(21002*time.Millisecond+time.Duration(c)*time.Microsecond, 7, c, garbage)
	}

	marks := detectClears(cells, []*replayBoard{board})
	if len(marks) != 2 {
		t.Fatalf("marks = %+v, want the clears at 10s and 21s", marks)
	}
	for _, m := range marks {
		if m.lines != 1 || m.color != 0 {
			t.Errorf("mark = %+v, want one line in board 0's color", m)
		}
	}
	if got := marks[0].off; got < 10*time.Second || got > 10100*time.Millisecond {
		t.Errorf("first mark at %v, want the batch that cleared (≈10s)", got)
	}
	if got := marks[1].off; got < 21*time.Second || got > 21001*time.Millisecond {
		t.Errorf("second mark at %v, want the batch that cleared (≈21s)", got)
	}
}
