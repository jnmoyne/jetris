package nativeui

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// replayStartGuard widens the "game has started" threshold the pacer uses:
// messages recorded before StartedAt−guard (game creation, roster joins, the
// lobby wait) fast-forward instead of replaying their arbitrarily long real
// gaps. StartedAt is stamped by the client that ran the countdown while the
// copied timestamps come from the server, so the guard also keeps a fast
// client clock from fast-forwarding through the game's first cell writes.
// It costs at most a couple of paced seconds at the start of an
// original-speed replay — and the countdown itself no longer depends on the
// guard's width: its first number opens the schedule where it was recorded
// (replayPacer.open), so the whole 5-4-3-2-1-GO replays at its own pace.
const replayStartGuard = 2 * time.Second

// replayPacer schedules an original-speed replay from the copied messages'
// ORIGINAL timestamps (the config.ReplayTsHeader header — the shared replay
// stream stamps copy-time timestamps, so the recorded pace lives in the
// header). The first paced message anchors recorded time to the wall clock;
// every later message is due at anchor + (its recorded time − the first's),
// an absolute schedule that cannot drift.
type replayPacer struct {
	thresh time.Time // pre-game cutoff: messages recorded before it fast-forward
	base   time.Time // recorded time of the first paced message
	wall0  time.Time // wall-clock moment the first paced message was applied
}

// delay returns how long to wait before applying a message recorded at ts,
// given the current wall clock.
func (p *replayPacer) delay(ts, now time.Time) time.Duration {
	if ts.IsZero() || ts.Before(p.thresh) {
		return 0
	}
	if p.base.IsZero() {
		p.base, p.wall0 = ts, now
		return 0
	}
	if d := p.wall0.Add(ts.Sub(p.base)).Sub(now); d > 0 {
		return d
	}
	return 0
}

// open lowers the pre-game cutoff to ts so that message — and everything
// recorded after it — is paced rather than fast-forwarded. The countdown's
// first number calls it: the countdown IS the game starting as the stream
// recorded it, so its own server timestamp is a truer — and skew-free — start
// of the paced window than StartedAt−guard, which falls in the middle of the
// count and would fast-forward everything before it.
func (p *replayPacer) open(ts time.Time) {
	if !ts.IsZero() && ts.Before(p.thresh) {
		p.thresh = ts
	}
}

// shift moves the schedule's anchor forward by d — the wall-clock span the
// replay stood paused — so the next message is due as far after the resume
// as it was after the pause, instead of everything recorded meanwhile being
// past due at once. A schedule not yet anchored has nothing to shift.
func (p *replayPacer) shift(d time.Duration) {
	if !p.base.IsZero() && d > 0 {
		p.wall0 = p.wall0.Add(d)
	}
}

// replayGate is the pause control shared by the replay screen, which flips
// it, and the session goroutine, which waits on it between messages
// (replayWait). A resume banks the paused span for the pacer (take).
type replayGate struct {
	mu       sync.Mutex
	paused   bool
	pausedAt time.Time
	held     time.Duration // paused span not yet folded into the pacer's anchor
	changed  chan struct{} // closed and replaced whenever paused flips, waking waiters
}

func newReplayGate() *replayGate { return &replayGate{changed: make(chan struct{})} }

// set pauses (true) or resumes (false) as of now; a no-op when already so.
func (g *replayGate) set(paused bool, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused == paused {
		return
	}
	g.paused = paused
	if paused {
		g.pausedAt = now
	} else {
		g.held += now.Sub(g.pausedAt)
	}
	close(g.changed)
	g.changed = make(chan struct{})
}

// state reports whether the replay is paused, with a channel that closes on
// the next flip either way.
func (g *replayGate) state() (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused, g.changed
}

// take returns the paused span banked since the last take, and resets it.
func (g *replayGate) take() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.held
	g.held = 0
	return d
}

// replayMsgTime parses a copied message's original timestamp header
// (nanoseconds since the Unix epoch); zero when absent or malformed — the
// pacer applies such a message immediately.
func replayMsgTime(msg jetstream.Msg) time.Time {
	ns, err := strconv.ParseInt(msg.Headers().Get(config.ReplayTsHeader), 10, 64)
	if err != nil || ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// replayBoard is one board's live state during a replay, rebuilt cell by cell
// from the replay stream. Written by the replay consumer goroutine and read by
// the layout — both under App.mu.
type replayBoard struct {
	label        string
	idx          int // player/team index for coloring; -1 if not applicable
	width        int
	height       int // total rows (headroom + visible)
	visibleStart int
	rows         []game.Row
}

func newReplayBoard(label string, idx, width, height, visibleStart int) *replayBoard {
	rows := make([]game.Row, height)
	for r := range rows {
		rows[r] = game.Row{Cells: make([]game.Cell, width)}
	}
	return &replayBoard{label: label, idx: idx, width: width, height: height, visibleStart: visibleStart, rows: rows}
}

// snapshot deep-copies the board's visible region into a renderable
// BoardSnapshot. Caller holds App.mu.
func (b *replayBoard) snapshot() engine.BoardSnapshot {
	h := b.height - b.visibleStart
	rows := make([]game.Row, h)
	for r := 0; r < h; r++ {
		cells := make([]game.Cell, b.width)
		copy(cells, b.rows[b.visibleStart+r].Cells)
		rows[r] = game.Row{Cells: cells}
	}
	return engine.BoardSnapshot{Width: b.width, Height: h, VisibleStart: 0, Rows: rows}
}

// replayView is one replay session: the archived game being replayed, the
// chosen speed, and the boards being rebuilt from its replay stream. done/err
// (and doneAt, the winner show's clock — see replay_winner.go) are written by
// the consumer goroutine under App.mu. rank/of place the game in its replay
// bucket's all-time ranking (config.ReplayRank), which grades the trophy the
// ending floats over the winning board; fixed at start.
type replayView struct {
	rec      config.ArchiveRecord
	fast     bool // as fast as possible vs. paced at the recorded rate (replayPacer)
	boards   []*replayBoard
	byPlayer map[string]int // competitive: playerID → boards index
	gate     *replayGate    // Pause / Resume (its own lock; see replayWait)
	cancel   context.CancelFunc
	rank, of int
	// The replayed pre-game countdown, folded from the countdown and meta
	// messages as they come by (applyReplayCountdown) and drawn over the
	// boards exactly as the live screen draws it: countdown is -1 until the
	// first number, then 5..1 and 0 for GO!; countdownAt is the wall-clock
	// moment that number was applied, which the pop animation runs from; and
	// started is set by the first replayed meta whose status has moved past
	// the pre-game ones, ending the count.
	countdown   int
	countdownAt time.Time
	started     bool
	done        bool
	doneAt      time.Time
	err         string
}

// newReplayView builds the mode-appropriate board set: one shared board for
// cooperative, one per player (sorted by ID, matching the archive viewer's
// coloring) for competitive, one per team for teams.
func newReplayView(rec config.ArchiveRecord, fast bool) *replayView {
	rv := &replayView{rec: rec, fast: fast, byPlayer: map[string]int{}, gate: newReplayGate(), rank: 1, of: 1, countdown: -1}
	switch rec.Mode {
	case config.ModeCooperative:
		rv.boards = []*replayBoard{newReplayBoard("", -1,
			config.SharedBoardWidth(rec.PlayerCount, rec.ExtraColumns),
			config.HeadroomRows+config.VisibleRows, config.VisibleRowStart)}
	case config.ModeTeams:
		for t := 0; t < config.TeamCount; t++ {
			rv.boards = append(rv.boards, newReplayBoard("Team "+teamName(t), t,
				config.TeamBoardWidth(rec.TeamSize, rec.ExtraColumns),
				config.TeamTotalRows(rec.TeamSize), config.TeamVisibleRowStart(rec.TeamSize)))
		}
	default: // competitive
		ids := make([]string, 0, len(rec.Players))
		for _, p := range rec.Players {
			ids = append(ids, p.PlayerID)
		}
		sort.Strings(ids)
		for i, id := range ids {
			rv.byPlayer[id] = i
			rv.boards = append(rv.boards, newReplayBoard(id, i,
				config.StandardWidth,
				config.CompetitiveTotalRows(rec.PlayerCount), config.CompetitiveVisibleRowStart(rec.PlayerCount)))
		}
	}
	return rv
}

// handleReplayChoice dispatches the speed-choice dialog's buttons and reports
// whether the dialog is (still) open.
func (a *App) handleReplayChoice(gtx C) bool {
	if a.replayChoice == nil {
		return false
	}
	rec := *a.replayChoice
	switch {
	case a.replayNormalBtn.Clicked(gtx):
		a.replayChoice = nil
		a.startReplay(rec, false)
	case a.replayFastBtn.Clicked(gtx):
		a.replayChoice = nil
		a.startReplay(rec, true)
	case a.replayCancelBtn.Clicked(gtx):
		a.replayChoice = nil
	default:
		return true
	}
	return false
}

// replayChoiceOverlay is the modal asking how to replay the chosen game:
// at the recorded pace, or as fast as the messages can be delivered.
func (a *App) replayChoiceOverlay(gtx C, rec config.ArchiveRecord) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 430)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "REPLAY GAME", colAccent).Layout),
							layout.Rigid(spacer(12)),
							layout.Rigid(a.body(archiveLine(rec), colFg)),
							layout.Rigid(spacer(6)),
							layout.Rigid(a.body("The whole game was recorded — watch it play back at the speed it actually happened, or skip straight through.", colMuted)),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return a.primaryButton(gtx, &a.replayNormalBtn, "Replay at original speed")
							}),
							layout.Rigid(spacer(10)),
							layout.Rigid(func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return a.secondaryButton(gtx, &a.replayFastBtn, "Replay as fast as possible")
							}),
							layout.Rigid(spacer(10)),
							layout.Rigid(func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return a.secondaryButton(gtx, &a.replayCancelBtn, "Cancel")
							}),
						)
					})
				})
			})
		})
	})
}

// startReplay opens the replay screen and starts the consumer session. The
// game's rank — which grades the trophy the ending floats — is taken against
// the lobby's copy of the whole history, so it is the same standing the
// history's TOP 10 mark reflects.
func (a *App) startReplay(rec config.ArchiveRecord, fast bool) {
	rv := newReplayView(rec, fast)
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
	go a.runReplaySession(ctx, rv)
	a.invalidate()
}

// closeReplay stops the replay session and returns to the lobby.
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

// runReplaySession consumes the game's slice of the shared replay stream
// (one ordered consumer on "jetris.replay.<id>.>") and folds every cell
// message into the session's boards. At original speed each message is
// applied on the recorded schedule via replayPacer (the original timestamps
// ride the config.ReplayTsHeader header); fast mode applies them as delivered.
// Either way every message passes the pause gate first (replayWait).
// Runs on its own goroutine; ends when the game's copy-complete marker — the
// last message under its prefix — arrives. If the channel goes quiet without
// the marker, the marker is re-checked: gone means the game was displaced and
// its replay purged mid-watch.
func (a *App) runReplaySession(ctx context.Context, rv *replayView) {
	defer rv.cancel()
	gameID := rv.rec.GameID

	a.mu.Lock()
	js := a.js
	a.mu.Unlock()
	if js == nil {
		a.setReplayErr(rv, "Not connected.")
		return
	}
	markerSeq, err := natspkg.GetReplayMarker(ctx, js, gameID)
	if err != nil {
		a.setReplayErr(rv, "This game's replay is no longer available.")
		return
	}

	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.ReplayStream,
		FilterSubject: config.ReplayFilter(gameID),
	})
	if err != nil {
		a.setReplayErr(rv, "Replay failed to start: "+err.Error())
		return
	}
	defer cancel()

	pacer := replayPacer{thresh: rv.rec.StartedAt.Add(-replayStartGuard)}
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
			// Quiet channel without the marker: purged mid-watch, or just a
			// slow server — the marker lookup tells them apart.
			if _, err := natspkg.GetReplayMarker(ctx, js, gameID); err != nil {
				a.setReplayErr(rv, "Replay ended early — this game's replay was just removed.")
				return
			}
		case msg, ok := <-ch:
			if !ok {
				a.setReplayErr(rv, "Replay ended early — the connection to the replay stream was lost.")
				return
			}
			doneSeq := false
			if md, err := msg.Metadata(); err == nil && md.Sequence.Stream >= markerSeq {
				doneSeq = true
			}
			if doneSeq || msg.Subject() == config.ReplayMarkerSubject(gameID) {
				a.mu.Lock()
				rv.done, rv.doneAt = true, time.Now()
				a.mu.Unlock()
				a.invalidate()
				return
			}
			ts := replayMsgTime(msg)
			if msg.Subject() == replayCountdownSubject(gameID) {
				// Open the paced window at the countdown rather than at
				// the guarded StartedAt, which falls in the middle of it:
				// the numbers were published a second apart and are
				// replayed a second apart, the whole count and not just
				// its tail.
				pacer.open(ts)
			}
			if !replayWait(ctx, rv, &pacer, ts) {
				return
			}
			if a.applyReplayCountdown(rv, msg.Subject(), msg.Data()) ||
				a.applyReplayCell(rv, msg.Subject(), msg.Data()) {
				a.invalidate()
			}
		}
	}
}

// replayWait blocks until the message recorded at ts is due — after its
// recorded gap at original speed, at once in fast mode — honoring the pause
// gate throughout: a pause holds the message (mid-gap, the rest of its gap)
// until the resume, and the pacer's anchor then shifts by the paused span so
// the schedule continues where it stopped. Reports false when ctx ends.
func replayWait(ctx context.Context, rv *replayView, pacer *replayPacer, ts time.Time) bool {
	for {
		paused, changed := rv.gate.state()
		if paused {
			select {
			case <-ctx.Done():
				return false
			case <-changed:
			}
			pacer.shift(rv.gate.take())
			continue
		}
		if rv.fast {
			return true
		}
		d := pacer.delay(ts, time.Now())
		if d <= 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		case <-changed: // paused mid-gap: hold, then re-time from the shifted anchor
		}
	}
}

// replayIdleCheck is how long the session waits on a silent message channel
// before re-checking that the replay still exists (a displacement purge stops
// deliveries without an error).
const replayIdleCheck = 10 * time.Second

// setReplayErr records a session failure for the replay screen's status line.
func (a *App) setReplayErr(rv *replayView, msg string) {
	a.mu.Lock()
	rv.err = msg
	rv.done = true
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

// applyReplayCountdown folds a replayed pre-game message into the countdown
// overlay and reports whether the screen must repaint. A countdown message is
// the next number, and applying it restarts the pop animation from now — so at
// original speed the count lands over the empty boards a second at a time,
// exactly as the players saw it. A meta message ends the count once its
// recorded status has moved past the pre-game ones (preStartStatus), which is
// the live screen's own rule: the number goes when the game goes, and no stale
// GO! is left standing over the replayed gameplay.
func (a *App) applyReplayCountdown(rv *replayView, subject string, data []byte) bool {
	switch subject {
	case replayCountdownSubject(rv.rec.GameID):
		var cd struct {
			Seconds int `json:"seconds"`
		}
		if err := json.Unmarshal(data, &cd); err != nil || cd.Seconds < 0 {
			return false
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if rv.started {
			return false // a number recorded after the start: never resurrect the count
		}
		rv.countdown, rv.countdownAt = cd.Seconds, time.Now()
		return true
	case replayMetaSubject(rv.rec.GameID):
		var meta config.GameMeta
		if err := json.Unmarshal(data, &meta); err != nil || preStartStatus(string(meta.Status)) {
			return false
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		started := rv.started
		rv.started = true
		return !started // the count coming down is a repaint; every later meta is not
	}
	return false
}

// replayCountdownVisible reports whether the replayed countdown number belongs
// over the boards: a number has arrived, the recorded game has not started
// yet, and the replay is still running (a finished one shows its ending, and a
// failed one its error).
func replayCountdownVisible(countdown int, started, done bool) bool {
	return countdown >= 0 && !started && !done
}

// applyReplayCell folds one replayed message into the session's boards and
// reports whether anything changed. Non-cell subjects (roster, events, the
// board registers) are simply skipped — the replay redraws the playfields
// only, the countdown and meta having been read by applyReplayCountdown.
func (a *App) applyReplayCell(rv *replayView, subject string, data []byte) bool {
	if config.IsGarbageSubject(subject) || config.IsTxnSubject(subject) {
		return false
	}
	row, col := natspkg.ParseCellFromSubject(subject)
	if row < 0 {
		return false
	}
	// The copied subjects keep the game-stream tail under the replay prefix:
	// jetris.replay.<id>.playfield.cell.<r>.<c>              (cooperative)
	// jetris.replay.<id>.player.<pid>.playfield.cell.<r>.<c> (competitive)
	// jetris.replay.<id>.team.<t>.playfield.cell.<r>.<c>     (teams)
	tokens := strings.Split(subject, ".")
	if len(tokens) < 7 {
		return false
	}
	boardIdx := -1
	switch tokens[3] {
	case "playfield":
		if rv.rec.Mode == config.ModeCooperative {
			boardIdx = 0
		}
	case "player":
		if i, ok := rv.byPlayer[tokens[4]]; ok {
			boardIdx = i
		}
	case "team":
		if t, err := strconv.Atoi(tokens[4]); err == nil && t >= 0 && t < len(rv.boards) {
			boardIdx = t
		}
	}
	if boardIdx < 0 || boardIdx >= len(rv.boards) {
		return false
	}
	cell, err := game.UnmarshalCell(data)
	if err != nil {
		return false
	}
	b := rv.boards[boardIdx]
	if row >= b.height || col < 0 || col >= b.width {
		return false
	}
	a.mu.Lock()
	b.rows[row].Cells[col] = cell
	a.mu.Unlock()
	return row >= b.visibleStart // headroom-only changes need no repaint
}

// layoutReplay is the replay screen: the boards being replayed center-stage
// over a status line, with the game's summary up top. The replay opens on the
// game's own countdown — the recorded numbers pop over the still-empty boards
// (applyReplayCountdown), the status line counting down with them — and the
// first cell lands when the recorded game started. The summary names the
// players in their board colors but keeps scores and winners back until the
// replay completes; then the ending is revealed — the winners' names go gold
// in bold italic, the status line says who won, the beaten boards read OUT
// and the winning board gets the winner show (replay_winner.go), which
// animates for as long as the screen is up.
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
	if a.replayBackBtn.Clicked(gtx) {
		a.closeReplay()
		return D{}
	}
	paused, _ := rv.gate.state()
	if a.replayPauseBtn.Clicked(gtx) {
		paused = !paused
		rv.gate.set(paused, time.Now())
	}

	a.mu.Lock()
	done, errMsg, doneAt := rv.done, rv.err, rv.doneAt
	countdown, countdownAt, started := rv.countdown, rv.countdownAt, rv.started
	boards := make([]labeledBoard, len(rv.boards))
	for i, b := range rv.boards {
		boards[i] = labeledBoard{label: b.label, idx: b.idx, snap: b.snapshot()}
	}
	a.mu.Unlock()

	reveal := done && errMsg == ""
	if reveal {
		animate(gtx) // keep the winner show animating while the screen is up
		a.crownWinners(boards, rv, doneAt, gtx.Now)
	}

	speed := "ORIGINAL SPEED"
	if rv.fast {
		speed = "FAST"
	}
	counting := replayCountdownVisible(countdown, started, done)
	status, statusCol := "REPLAYING · "+speed, colNATSGreen
	switch {
	case errMsg != "":
		status, statusCol = strings.ToUpper(errMsg), colErr
	case done:
		status, statusCol = "REPLAY COMPLETE", colGold
	case paused:
		status, statusCol = "PAUSED · "+speed, colWarn
	case counting:
		status, statusCol = "COUNTING DOWN · "+speed, colGold
	}
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
	pauseLabel := "Pause"
	if paused {
		pauseLabel = "Resume"
	}

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
				if gtx.Now.Sub(countdownAt) < countdownAnimDur {
					animate(gtx) // keep animating the countdown pop until it settles
				}
				return layout.Stack{Alignment: layout.Center}.Layout(gtx,
					layout.Expanded(content),
					layout.Stacked(func(gtx C) D { return a.countdownOverlay(gtx, countdown, countdownAt) }),
				)
			}),
			layout.Rigid(spacer(14)),
			layout.Rigid(func(gtx C) D {
				// Pause / Resume leads while the replay runs; a finished (or
				// failed) replay has nothing to pause and shows only the exit.
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.replayBackBtn, "Back to Lobby") }),
				}
				if !done {
					children = append([]layout.FlexChild{
						layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.replayPauseBtn, pauseLabel) }),
						layout.Rigid(hSpacer(12)),
					}, children...)
				}
				// Centered under the boards: the column hands this row the
				// full width, and a bare Flex would pack the buttons left.
				return layout.Center.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
				})
			}),
		)
	})
}
