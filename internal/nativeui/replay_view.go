package nativeui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// replayBoard is one board of a replay, rebuilt cell by cell as the playhead
// moves over the timeline (replay_timeline.go). Written only by the seek the
// layout runs, under App.mu.
type replayBoard struct {
	label        string
	idx          int // player/team index for coloring; -1 if not applicable
	width        int
	height       int // total rows (headroom + visible)
	visibleStart int
	rows         []game.Row
}

func newReplayBoard(label string, idx, width, height, visibleStart int) *replayBoard {
	b := &replayBoard{label: label, idx: idx, width: width, height: height, visibleStart: visibleStart}
	b.reset()
	return b
}

// reset empties the board — where every backwards seek starts from.
func (b *replayBoard) reset() {
	if b.rows == nil {
		b.rows = make([]game.Row, b.height)
	}
	for r := range b.rows {
		if b.rows[r].Cells == nil {
			b.rows[r].Cells = make([]game.Cell, b.width)
			continue
		}
		clear(b.rows[r].Cells)
	}
}

// snapshot deep-copies the board's visible region into a renderable
// BoardSnapshot — or, with headroom, the whole board with its visible region
// marked, for the strip to draw the hidden rows behind smoked glass
// (boardRows, drawBoard). Caller holds App.mu.
func (b *replayBoard) snapshot(headroom bool) engine.BoardSnapshot {
	top := b.visibleStart
	if headroom {
		top = 0
	}
	h := b.height - top
	rows := make([]game.Row, h)
	for r := 0; r < h; r++ {
		cells := make([]game.Cell, b.width)
		copy(cells, b.rows[top+r].Cells)
		rows[r] = game.Row{Cells: cells}
	}
	return engine.BoardSnapshot{Width: b.width, Height: h, VisibleStart: b.visibleStart - top, Rows: rows}
}

// newReplayBoards builds the mode-appropriate board set for an archived game —
// one shared board for cooperative, one per player (sorted by ID, matching the
// archive viewer's coloring) for competitive, one per team for teams (however
// many teams the game was played between) — with
// the competitive playerID → board index map beside it. height is the total
// rows (headroom + visible) the game was PLAYED at, not today's: the loader
// measures it off the replay stream (replayBoardHeight), and a game played on
// a taller board than today's gets that board back, or its bottom rows would
// be lost.
func newReplayBoards(rec config.ArchiveRecord, height int) ([]*replayBoard, map[string]int) {
	byPlayer := map[string]int{}
	switch rec.Mode {
	case config.ModeCooperative:
		return []*replayBoard{newReplayBoard("", -1,
			config.SharedBoardWidth(rec.PlayerCount, rec.ExtraColumns),
			height, config.VisibleRowStart)}, byPlayer
	case config.ModeTeams:
		var boards []*replayBoard
		for t := 0; t < rec.Teams(); t++ {
			boards = append(boards, newReplayBoard("Team "+teamName(t), t,
				config.TeamBoardWidth(rec.TeamSize, rec.ExtraColumns),
				height, config.VisibleRowStart))
		}
		return boards, byPlayer
	default: // competitive
		ids := make([]string, 0, len(rec.Players))
		for _, p := range rec.Players {
			ids = append(ids, p.PlayerID)
		}
		sort.Strings(ids)
		var boards []*replayBoard
		for i, id := range ids {
			byPlayer[id] = i
			boards = append(boards, newReplayBoard(id, i,
				config.StandardWidth,
				height, config.VisibleRowStart))
		}
		return boards, byPlayer
	}
}

// replaySpeeds are the playback rates the transport offers, and replaySkip is
// how far the two skip buttons jump — both in RECORDED time, so 2× plays a
// recorded second in half a second and a skip is always ten seconds of game.
var replaySpeeds = []float64{0.5, 1, 2, 4, 8}

const (
	replaySkip       = 10 * time.Second
	replayNormalRate = 1.0
)

// replayView is one replay session: the archived game, the timeline loaded off
// the replay stream, the boards the playhead paints, and the transport's
// state. The load fields (tl, loaded, total, err) are written by the loader
// goroutine and read by the layout, both under App.mu; the playback fields are
// the layout's own — every one of them is set from a click, a drag or the
// frame clock, all of which happen on the UI goroutine.
//
// rank/of place the game in its replay bucket's all-time ranking
// (config.ReplayRank), which grades the trophy the ending floats over the
// winning board; fixed at start.
type replayView struct {
	rec      config.ArchiveRecord
	boards   []*replayBoard
	byPlayer map[string]int // competitive: playerID → boards index
	cancel   context.CancelFunc
	rank, of int

	tl     *replayTimeline // nil until the whole replay is loaded
	loaded int             // messages read so far (the loading progress)
	total  int             // messages the copy holds, from its marker (0 = unknown)
	err    string

	head    time.Duration // the playhead: everything about the picture on screen
	cursor  int           // cells of the timeline applied to the boards
	playing bool
	speed   float64
	anchor  time.Time // frame clock the playhead was last advanced from
	shownN  int       // countdown number currently on screen (-1 = none)
	shownAt time.Time // when it appeared, for the pop animation
	done    bool      // the playhead has reached the end: the ending is revealed
	doneAt  time.Time

	// The scrub track as it was last laid out (replayScrubber), in pixels
	// from the scrubber's own left edge: what a drag's position is measured
	// against a frame later.
	trackX0, trackW int
}

// newReplayView opens a replay session on the record's own word for its
// boards' height — the placeholder the loading screen stands on. The loader
// replaces the boards with ones measured off the replay stream before the
// timeline lands (runReplayLoad).
func newReplayView(rec config.ArchiveRecord) *replayView {
	boards, byPlayer := newReplayBoards(rec, rec.BoardHeight())
	return &replayView{rec: rec, boards: boards, byPlayer: byPlayer,
		rank: 1, of: 1, speed: replayNormalRate, shownN: -1}
}

// seek paints the boards as the recording had them at the playhead: cells
// recorded at or before it applied, everything after it undone. Forwards is a
// walk from where the last frame stopped; backwards empties the boards and
// walks up from the start, which is a few hundred thousand slice writes for a
// whole game — nothing next to a frame, and the reason scrubbing back is as
// cheap as scrubbing on.
func (rv *replayView) seek(head time.Duration) {
	cells := rv.tl.cells
	n := sort.Search(len(cells), func(i int) bool { return cells[i].off > head })
	if n < rv.cursor {
		for _, b := range rv.boards {
			b.reset()
		}
		rv.cursor = 0
	}
	for ; rv.cursor < n; rv.cursor++ {
		c := cells[rv.cursor]
		rv.boards[c.board].rows[c.row].Cells[c.col] = c.cell
	}
}

// replayMaxFrameStep caps how much wall time a single frame may carry the
// playhead. Playback runs on the frame clock, so a window that stopped
// painting — occluded, suspended, a laptop lid closed mid-replay — would come
// back to find the whole game had gone by in one frame. Past this a stall
// simply holds the tape where it stands.
const replayMaxFrameStep = time.Second

// advance moves the playhead for this frame: while playing, by the wall time
// since the last frame times the chosen speed, stopping at the end.
func (rv *replayView) advance(now time.Time) {
	if !rv.playing || rv.anchor.IsZero() {
		rv.anchor = now
		return
	}
	d := now.Sub(rv.anchor)
	rv.anchor = now
	if d > 0 {
		rv.head += time.Duration(float64(min(d, replayMaxFrameStep)) * rv.speed)
	}
	if rv.head >= rv.tl.dur {
		rv.head, rv.playing = rv.tl.dur, false
	}
}

// cue jumps the playhead, clamped to the recording, without changing whether
// the replay is playing — the slider and the skip buttons both land here.
func (rv *replayView) cue(head time.Duration, now time.Time) {
	rv.head = min(max(head, 0), rv.tl.dur)
	rv.anchor = now
}

// startReplay opens the replay screen and starts the load. The game's rank —
// which grades the trophy the ending floats — is taken against the lobby's
// copy of the whole history, so it is the same standing the history's TOP 10
// mark reflects.
func (a *App) startReplay(rec config.ArchiveRecord) {
	rv := newReplayView(rec)
	if lb := a.getLobby(); lb != nil {
		rv.rank, rv.of = config.ReplayRank(lb.Archives(), rec)
	}
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	rv.cancel = cancel
	a.mu.Lock()
	a.replayView = rv
	a.screen = screenReplay
	a.mu.Unlock()
	go a.runReplayLoad(ctx, rv)
	a.invalidate()
}

// closeReplay stops the replay and returns to the lobby.
func (a *App) closeReplay() {
	a.mu.Lock()
	rv := a.replayView
	a.replayView = nil
	a.screen = screenLobby
	a.mu.Unlock()
	if rv != nil && rv.cancel != nil {
		rv.cancel()
	}
	a.invalidate()
}

// replayIdleCheck is how long the load waits on a silent message channel
// before re-checking that the replay still exists (a displacement purge stops
// deliveries without an error).
const replayIdleCheck = 10 * time.Second

// replayProgressEvery is how many decoded messages pass between repaints of
// the loading bar — often enough to look live, rare enough that the load is
// not throttled by the window.
const replayProgressEvery = 200

// runReplayLoad reads the game's whole slice of the shared replay stream (one
// ordered consumer on "jetris.replay.<id>.>") as fast as the server will send
// it, decoding every message into the timeline the transport then plays. It
// ends at the game's copy-complete marker — the last message under its prefix.
// If the channel goes quiet without the marker, the marker is re-checked: gone
// means the game was displaced and its replay purged mid-load.
//
// Runs on its own goroutine and touches the view only under App.mu; the
// playhead never moves here — the load's only job is to hand the layout a
// finished timeline.
func (a *App) runReplayLoad(ctx context.Context, rv *replayView) {
	defer rv.cancel()
	gameID := rv.rec.GameID

	a.mu.Lock()
	js := a.js
	a.mu.Unlock()
	if js == nil {
		a.setReplayErr(rv, "Not connected.")
		return
	}
	markerSeq, total, err := natspkg.GetReplayMarker(ctx, js, gameID)
	if err != nil {
		a.setReplayErr(rv, "This game's replay is no longer available.")
		return
	}
	// The boards' height, measured off the recording itself before a single
	// message of it is decoded: the game's subjects say how tall its boards
	// were, and every cell the load then reads has a row to land on.
	height := replayBoardHeight(ctx, js, rv.rec)
	boards, byPlayer := newReplayBoards(rv.rec, height)
	a.mu.Lock()
	rv.total = int(total)
	rv.boards, rv.byPlayer = boards, byPlayer
	a.mu.Unlock()

	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.ReplayStream,
		FilterSubject: config.ReplayFilter(gameID),
	})
	if err != nil {
		a.setReplayErr(rv, "Replay failed to load: "+err.Error())
		return
	}
	defer cancel()

	b := newReplayBuilder(rv.rec, height)
	idle := time.NewTimer(replayIdleCheck)
	defer idle.Stop()
	for {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(replayIdleCheck)
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			// Quiet channel without the marker: purged mid-load, or just a
			// slow server — the marker lookup tells them apart.
			if _, _, err := natspkg.GetReplayMarker(ctx, js, gameID); err != nil {
				a.setReplayErr(rv, "Replay ended early — this game's replay was just removed.")
				return
			}
		case msg, ok := <-ch:
			if !ok {
				a.setReplayErr(rv, "Replay failed to load — the connection to the replay stream was lost.")
				return
			}
			doneSeq := false
			if md, err := msg.Metadata(); err == nil && md.Sequence.Stream >= markerSeq {
				doneSeq = true
			}
			if doneSeq || msg.Subject() == config.ReplayMarkerSubject(gameID) {
				tl := b.finish()
				a.mu.Lock()
				rv.loaded, rv.tl = b.n, tl
				rv.playing = true
				rv.anchor = time.Now()
				a.mu.Unlock()
				a.invalidate()
				return
			}
			b.add(msg.Subject(), msg.Data(), replayMsgTime(msg))
			if b.n%replayProgressEvery == 0 {
				a.mu.Lock()
				rv.loaded = b.n
				a.mu.Unlock()
				a.invalidate()
			}
		}
	}
}

// replayBoardHeight is the height (headroom + visible) the replay rebuilds a
// game's boards at: what the game's replay subjects say
// (natspkg.ReplayBoardHeight — the tallest cell row ever written, plus one),
// never less than today's board, so a recording that ended before its first
// piece reached the floor still stands a whole board tall. A recording with
// no cells at all, or a stream that cannot be asked, falls back to the
// archive record's own word (config.ArchiveRecord.BoardHeight).
func replayBoardHeight(ctx context.Context, js jetstream.JetStream, rec config.ArchiveRecord) int {
	h, err := natspkg.ReplayBoardHeight(ctx, js, rec.GameID)
	if err != nil || h <= 0 {
		return rec.BoardHeight()
	}
	return max(h, config.TotalRows)
}

// setReplayErr records a load failure for the replay screen's status line.
func (a *App) setReplayErr(rv *replayView, msg string) {
	a.mu.Lock()
	rv.err = msg
	a.mu.Unlock()
	a.invalidate()
}

// replayCountdownSubject / replayMetaSubject are the copied homes of a game's
// countdown and meta messages in the shared replay stream.
func replayCountdownSubject(gameID string) string {
	return config.ReplayCopySubject(gameID, config.CountdownSubject(gameID))
}

func replayMetaSubject(gameID string) string {
	return config.ReplayCopySubject(gameID, config.MetaSubject(gameID))
}

// replayClock formats a playhead as m:ss (h:mm:ss past the hour).
func replayClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second) / time.Second)
	if h := s / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, (s/60)%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// speedLabel prints a playback rate the way the transport's buttons do.
func speedLabel(sp float64) string {
	if sp == float64(int(sp)) {
		return fmt.Sprintf("%dx", int(sp))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", sp), "0"), ".") + "x"
}

// layoutReplay is the replay screen. While the recording loads it is a
// progress bar; once loaded it is a player: the boards center-stage with the
// game's own countdown counting them in, the transport bar under them — the
// clear timeline, the scrub slider, the buttons — and the game's summary up
// top. The summary names the players in their board colors but keeps scores
// and winners back until the playhead reaches the end; then the ending is
// revealed — the winners' names go gold in bold italic, the status line says
// who won, the beaten boards read OUT and the winning board gets the winner
// show (replay_winner.go), which animates for as long as the screen is up.
// Scrub back into the game and the reveal packs itself away again.
func (a *App) layoutReplay(gtx C) D {
	a.mu.Lock()
	rv := a.replayView
	a.mu.Unlock()
	if rv == nil {
		a.mu.Lock()
		a.screen = screenLobby
		a.mu.Unlock()
		return D{}
	}
	// The way out, by button or by ESC, is taken before the lock: closing the
	// replay takes it too.
	if a.replayBackBtn.Clicked(gtx) || a.replayKeys(gtx, rv) {
		a.closeReplay()
		return D{}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if rv.tl == nil {
		return pointerArea(gtx, &a.replayTag, func(gtx C) D { return a.layoutReplayLoading(gtx, rv) })
	}
	a.replayTransportEvents(gtx, rv)
	rv.advance(gtx.Now)
	rv.seek(rv.head)
	if rv.playing {
		animate(gtx) // the playhead moves with the frame clock
	}

	// The ending is a property of where the playhead stands, so scrubbing
	// away from the end takes it back off the screen.
	if end := rv.head >= rv.tl.dur; end != rv.done {
		rv.done, rv.doneAt = end, gtx.Now
	}
	boards := make([]labeledBoard, len(rv.boards))
	for i, b := range rv.boards {
		boards[i] = labeledBoard{label: b.label, idx: b.idx, snap: b.snapshot(rv.tl.headroom), headroom: rv.tl.headroom}
	}
	reveal := rv.done
	if reveal {
		animate(gtx) // keep the winner show animating while the screen is up
		a.crownWinners(boards, rv, rv.doneAt, gtx.Now)
	}

	// The countdown is read off the timeline like everything else, and pops
	// in wall time whenever the number on screen changes — scrubbed into or
	// played into, at any speed.
	count, counting := rv.tl.countdownAt(rv.head)
	if !counting {
		count = -1
	}
	if count != rv.shownN {
		rv.shownN, rv.shownAt = count, gtx.Now
	}
	if counting && gtx.Now.Sub(rv.shownAt) < countdownAnimDur {
		animate(gtx) // keep animating the countdown pop until it settles
	}

	status, statusCol := "PLAYING · "+speedLabel(rv.speed), colNATSGreen
	switch {
	case rv.err != "":
		status, statusCol = strings.ToUpper(rv.err), colErr
	case reveal:
		status, statusCol = "REPLAY COMPLETE", colGold
	case counting:
		status, statusCol = "COUNTING DOWN", colGold
	case !rv.playing:
		status, statusCol = "PAUSED", colWarn
	}
	status += " · " + replayClock(rv.head) + " / " + replayClock(rv.tl.dur)
	statusLine := a.pixel(unit.Sp(9), status, statusCol).Layout
	if reveal {
		// The verdict rides the status line in bold italic — synthesized,
		// the pixel face having none — so who won is the one thing on the
		// line set differently from everything else.
		statusLine = func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(a.pixel(unit.Sp(9), status+" · ", statusCol).Layout),
				layout.Rigid(a.pixelEmph(unit.Sp(9), replayVerdict(rv.rec), colGold)),
			)
		}
	}

	// The whole screen is the transport's key-input area: the tag has to be
	// in the frame's op tree for its filters to live (pointerArea), and a
	// click anywhere on the screen hands the keys back to it.
	return pointerArea(gtx, &a.replayTag, func(gtx C) D {
		return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.brandBanner("")),
				layout.Rigid(spacer(8)),
				layout.Rigid(a.header("GAME REPLAY")),
				layout.Rigid(spacer(4)),
				layout.Rigid(a.spansLine(replaySummary(rv.rec, reveal))),
				layout.Rigid(spacer(8)),
				layout.Rigid(statusLine),
				layout.Rigid(spacer(14)),
				layout.Flexed(1, func(gtx C) D {
					// The boards stay centered with or without the countdown
					// Stack — the same reason the spectator's boards go through
					// a Center of their own (gameBoardArea): the Flexed slot
					// hands down tight constraints that would pin the strip to
					// the top-left the moment the overlay goes away.
					content := func(gtx C) D {
						return layout.Center.Layout(gtx, func(gtx C) D {
							return a.boardsStrip(gtx, &a.replayBoardsList, boards)
						})
					}
					if !counting {
						return content(gtx)
					}
					return layout.Stack{Alignment: layout.Center}.Layout(gtx,
						layout.Expanded(content),
						layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, count, rv.shownAt) }),
					)
				}),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D { return a.replayTransport(gtx, rv) }),
				layout.Rigid(spacer(8)),
				layout.Rigid(a.replayKeyHint),
			)
		})
	})
}

// layoutReplayLoading is the screen while the recording is being read off the
// replay stream: the game's summary, a progress bar counting off the copy's
// own message total, and the way out.
func (a *App) layoutReplayLoading(gtx C, rv *replayView) D {
	if rv.err == "" {
		animate(gtx)
	}
	msg, col := fmt.Sprintf("LOADING REPLAY · %d MESSAGES", rv.loaded), colNATSGreen
	if rv.total > 0 {
		msg = fmt.Sprintf("LOADING REPLAY · %d / %d MESSAGES", min(rv.loaded, rv.total), rv.total)
	}
	if rv.err != "" {
		msg, col = strings.ToUpper(rv.err), colErr
	}
	frac := 0.0
	if rv.total > 0 {
		frac = clampF(float64(rv.loaded)/float64(rv.total), 0, 1)
	}
	return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.brandBanner("")),
			layout.Rigid(spacer(8)),
			layout.Rigid(a.header("GAME REPLAY")),
			layout.Rigid(spacer(4)),
			layout.Rigid(a.spansLine(replaySummary(rv.rec, false))),
			layout.Flexed(1, func(gtx C) D {
				return layout.Center.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.pixel(unit.Sp(9), msg, col).Layout),
						layout.Rigid(spacer(12)),
						layout.Rigid(func(gtx C) D {
							if rv.err != "" {
								return D{}
							}
							gtx.Constraints.Min.X = min(gtx.Dp(420), gtx.Constraints.Max.X)
							return a.replayProgressBar(gtx, frac)
						}),
					)
				})
			}),
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D {
				return layout.Center.Layout(gtx, func(gtx C) D {
					return a.secondaryButton(gtx, &a.replayBackBtn, "Back to Lobby")
				})
			}),
			layout.Rigid(spacer(8)),
			layout.Rigid(func(gtx C) D { return a.keyHintLine(gtx, "ESC LOBBY") }),
		)
	})
}
