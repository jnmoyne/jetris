package engine

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// runConsumer drives an ordered consumer over filterSubject, applying every
// cell it delivers to pf. opponentID is set only for an opponent's playfield
// consumer (competitive mode) and tags the emitted UpdateOpponentField events;
// it is empty for this engine's own playfield.
func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, filterSubject, opponentID string, startSeq uint64, isOpponent bool) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: filterSubject,
		StartSeq:      startSeq,
	})
	if err != nil {
		log.Printf("consumer start error: %v", err)
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
			e.tapMsg(msg)
			subject := msg.Subject()
			md, _ := msg.Metadata()
			var seq uint64
			if md != nil {
				seq = md.Sequence.Stream
			}

			// Complete a pending RTT measurement BEFORE the register/cell
			// branch: a gated transform's batch puts the txn register first,
			// so the batch's first-message echo — the one the RTT tracker
			// keys on — can be a register, not a cell (see rtt.go).
			if !isOpponent {
				e.noteRTTEcho(seq)
			}

			// Board registers (the widened competitive/teams filters also
			// deliver the garbage/txn registers) are folded before the cell
			// parse; they carry no cell payload.
			if config.IsGarbageSubject(subject) {
				e.handleGarbageRegisterEcho(opponentID, isOpponent, msg.Data(), seq)
				continue
			}
			if config.IsTxnSubject(subject) {
				e.handleTxnRegisterEcho(ctx, isOpponent, msg.Data(), seq)
				continue
			}

			rowIdx, colIdx := natspkg.ParseCellFromSubject(subject)
			if rowIdx < 0 {
				continue
			}

			cell, err := game.UnmarshalCell(msg.Data())
			if err != nil {
				continue
			}

			e.mu.Lock()
			pf.Apply(rowIdx, colIdx, cell, seq)
			if !isOpponent && e.echoField != nil {
				// The echo-only replica (EchoSnapshot): what the stream has
				// delivered, and nothing the engine wrote through ahead of it.
				e.echoField.Apply(rowIdx, colIdx, cell, seq)
				// A peer's piece seen somewhere new restarts its idle clock
				// (roster.go: a piece left behind is vacated a threshold on).
				e.notePeerCellLocked(cell, time.Now())
			}

			if isOpponent {
				e.mu.Unlock()
				e.emitUpdate(EngineUpdate{
					Kind:        UpdateOpponentField,
					ChangedRows: []int{rowIdx},
					OpponentID:  opponentID,
				})
			} else if e.solo() {
				// A solo game's stream is a journal of the game played on
				// the local board (solo.go): its echo keeps the acked and
				// echo replicas, applied above, and drives nothing — the
				// lock-in fired as the lock was journaled. The echo display
				// still repaints on it.
				e.mu.Unlock()
				e.emitUpdate(EngineUpdate{
					Kind:        UpdatePlayfield,
					ChangedRows: []int{rowIdx},
				})
			} else {
				// Lock-in detection: had active → no active. handleLockIn spawns the
				// next piece, and the publish write-through makes it active in pf
				// immediately (still under e.mu here), so RE-READ hasActive after it
				// — otherwise hadActivePiece is left false while a piece is active,
				// and if that piece locks before the next echo (runInput races ahead
				// of the consumer on fast drops) its lock-in is missed and the player
				// stops spawning entirely.
				//
				// Gated on ModePlayer: an engine that is no longer playing (a
				// spectator, or a teams-mode player whose elimination vacated
				// their cells) must never run lock-in side effects — publishing
				// clears or spawning — off its own echoes.
				hasActive := pf.ActivePieceForPlayer(e.playerIdx) != nil
				if e.hadActivePiece && !hasActive && e.getMode() == ModePlayer {
					if e.toppedByShrink {
						// The zero-active edge was caused by a remote gated
						// shrink whose txn register (delivered before these
						// vacates — it's the batch's first message) listed us
						// as pushed off the top: this is an elimination, not
						// a lock-in.
						e.toppedByShrink = false
						e.handleTopOut(ctx, true)
					} else {
						e.handleLockIn(ctx)
						hasActive = pf.ActivePieceForPlayer(e.playerIdx) != nil
					}
				}
				e.hadActivePiece = hasActive
				// A deferred spawn (blocked only by another player's active
				// piece) retries the moment the shared board CHANGES — this
				// very message may be the blocker moving away. The housekeeping
				// tick's retry remains as the backstop, but at agent speeds a
				// blocking piece slides across the spawn cells in milliseconds
				// and waiting a full housekeeping tick (a second) per
				// attempt starves the deferred player down to a piece every
				// few seconds.
				if e.spawnPending && !hasActive && e.getMode() == ModePlayer && e.gameStarted.Load() {
					e.spawnPiece(ctx, true)
					e.hadActivePiece = pf.ActivePieceForPlayer(e.playerIdx) != nil
				}
				e.mu.Unlock()

				// Signal CAS notification
				select {
				case e.cellUpdated <- struct{}{}:
				default:
				}

				e.emitUpdate(EngineUpdate{
					Kind:        UpdatePlayfield,
					ChangedRows: []int{rowIdx},
				})
			}
		}
	}
}

func (e *Engine) handleLockIn(ctx context.Context) {
	// What the lock is worth beyond its lines — the T-spin it made and the
	// drop points the piece earned on the way down — was judged by the
	// publish that locked it (award.go); a lock-in nothing armed (a lock
	// this engine did not publish) is a plain one. The Guideline scores a
	// clear at the level BEFORE it: game.Level is 0-based, the table's
	// multiplier one more.
	award := e.lockAward
	e.lockAward = lockAward{}
	// The lock-in is here: the watchdog's wait for the lock is over, and
	// from here to the spawn's commit it stands down for the lock-in
	// itself (spawning) — which releases the engine's lock around its
	// network round trips below, the clear's publish and the spawn's.
	// Held across them, a far server's latency (a beta player's 189 ms
	// batch round trip) stalled every UI frame and the input loop for two
	// round trips per piece and one more per clear, and the next piece's
	// inputs pressed in that time raced the spawn for the lock and were
	// lost when they won. The deferred relock runs first at return, so
	// the flag clears under the lock; the caller (runConsumer, the
	// journal's publishLocal) holds the lock again when this returns and
	// re-reads the board's piece before trusting it.
	e.lockInFlight = false
	e.spawning = true
	defer func() { e.spawning = false }()
	level := game.Level(int(e.totalLines.Load()))

	// Detect completed rows on the live replica. Cooperative publishes the
	// collapse with merge-retry (no garbage in coop, no gate); competitive
	// and teams run it as a GATED transform, whose recompute re-detects the
	// completed rows from converged state if the gate is lost (e.g. a shrink
	// landed first and shifted them, or a teammate's clear already took them).
	// Score, events, and the attack are derived from the rows the committed
	// transform ACTUALLY cleared, not the pre-race detection.
	clearedLines := 0
	var clearedRows []int // the rows the committed transform actually cleared (pre-collapse indices)
	var after []game.Row  // the board as the committed collapse leaves it — a perfect clear leaves nothing locked on it
	if completed := game.CompletedRows(e.playfield); len(completed) > 0 {
		if e.gameMode == config.ModeCooperative {
			// Compute the cleared/shifted projection without mutating
			// e.playfield; remaining active cells get their AnchorRow shifted
			// by len(completed) so other players' pieces land in the right
			// anchor position. The diff covers the FULL row range including
			// the headroom — a truncated diff used to strand duplicated or
			// orphaned cells in rows 0-3.
			projected := e.playfield.ProjectClearRows(completed, true)
			changed := changedCells(e.playfield.Rows, projected, 0, e.playfield.Height)
			// Shared board: CAS+merge-retry so the shift can't clobber another
			// player's mid-flight piece with our snapshot. orderedCellKeys
			// applies another player's shifted (active) piece cells before its
			// old positions are vacated, so its active-cell count never hits
			// zero and no spurious lock + respawn fires on their engine.
			// Off the lock for the round trip (above); the merge-retry
			// refetches on a lost race either way.
			e.mu.Unlock()
			e.publishProjectedCellsWithMergeRetry(ctx, changed, nil, false)
			e.mu.Lock()
			clearedLines = len(completed)
			clearedRows = completed
			after = projected
		} else {
			shiftAnchors := e.sharedBoard()
			var cleared []int
			// Off the lock for the round trip (above): the transform clones
			// the board under the lock itself for its first attempt, and a
			// lost gate recomputes from the server's snapshot regardless.
			e.mu.Unlock()
			committed := e.publishGatedTransform(ctx, txnOpClear, false, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
				rows := game.CompletedRows(pf)
				if len(rows) == 0 {
					return nil, TxnRegister{}, false
				}
				cleared = rows
				after = pf.ProjectClearRows(rows, shiftAnchors)
				return after, TxnRegister{Applied: txn.Applied}, true
			})
			e.mu.Lock()
			if committed {
				clearedLines = len(cleared)
				clearedRows = cleared
			}
		}
	}

	// The Guideline's account of the lock — the combo, the Back-to-Back
	// chain, the points (award.go).
	clear, points := e.accountLock(clearedLines, award, after, level)

	if clearedLines > 0 {
		e.totalLines.Add(int64(clearedLines))
		e.ownClearLines.Add(int64(clearedLines))
	}
	if points > 0 {
		e.score.Add(int64(points))
		e.ownScore.Add(int64(points))
	}
	// The level follows the line total in every mode — the crew's on a
	// shared board, this player's own in competitive: the multiplier the
	// HUD's LEVEL names, and the speed runInput's gravity clock falls at.
	e.refreshLevel()

	if clearedLines > 0 {
		// Re-render the whole board: a clear shifts every row, and the UI
		// re-renders from e.playfield as the published rows echo back. A single
		// full-board update is robust against dropped per-row triggers.
		e.emitFullBoardRerender()
		// Arcade feedback: tell the UI WHICH rows this player's lock completed
		// (their pre-collapse positions) so it can strobe them. Teammates on a
		// shared board strobe the same rows off the line-clear event below,
		// which carries them as cleared_rows.
		e.emitUpdate(EngineUpdate{Kind: UpdateRowsCleared, ChangedRows: clearedRows})
	}
	if points > 0 {
		e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
	}
	if e.gameMode == config.ModeTeams && (clearedLines > 0 || points > 0) {
		// Fold our own lock into the per-team scoreboard; everyone else
		// folds it off the line-clear event below.
		e.teamScores[e.teamIdx].Add(int64(points))
		e.teamLines[e.teamIdx].Add(int64(clearedLines))
		e.emitTeamStats()
	}
	// The banner names a clear or a T-spin; drop points alone are silent.
	if clearedLines > 0 || clear.Spin != game.TSpinNone {
		e.emitUpdate(EngineUpdate{Kind: UpdateAward, PlayerID: e.playerID, Clear: clear, Score: points})
	}

	// Off the lock from here to the end (see the top): nothing below needs
	// the board to stand still — the event carries totals already folded,
	// the piece index is atomic, and spawnPiece takes the lock itself
	// exactly as it does from Start.
	e.mu.Unlock()
	defer e.mu.Lock()

	// Cooperative: notify other players of the score change. Teams: notify
	// everyone of the score AND line-count change (lines keep every
	// teammate's level/gravity in sync). Competitive: the board is private,
	// but the totals still tell every peer where the player stands — the
	// line goal (goal.go) and the archive read them. Every lock that scored
	// is announced — one that cleared nothing but earned drop points or a
	// T-spin too, with lines_cleared 0 — so the shared score converges on
	// every point. The event goes to the sender's per-kind subject and
	// carries the sender's cumulative own totals — receivers fold deltas, so
	// retention trimming an older event of ours is harmless.
	if clearedLines > 0 || points > 0 {
		ev := GameEvent{
			Kind:         EventLineClear,
			PlayerID:     e.playerID,
			Team:         e.teamIdx,
			Score:        points,
			LinesCleared: clearedLines,
			ClearedRows:  clearedRows,
			TSpin:        int(clear.Spin),
			BackToBack:   clear.BackToBack,
			Combo:        clear.Combo,
			Perfect:      clear.Perfect,
			TotalScore:   int(e.ownScore.Load()),
			TotalLines:   int(e.ownClearLines.Load()),
		}
		data, _ := json.Marshal(ev)
		e.publishEvent(ctx, config.EventKindSubject(e.gameID, string(EventLineClear), e.playerID), data)
	}

	// The attack: advance every victim board's garbage register (teams:
	// the opposing board; competitive: all surviving opponents). The
	// durable CAS-add ledger replaces the old fire-and-forget shrink
	// event — simultaneous attacks sum instead of trimming each other,
	// and victims reconcile the register whenever they catch up. On a
	// goroutine: the bump's publishes must not delay the spawn behind
	// them (the lock-in is off e.mu here, which the bump takes briefly
	// itself). The attack is one row per line, or the Guideline
	// table (a single sends nothing, a T-spin more, a Back-to-Back or a
	// perfect clear a bonus — game.Clear.AttackRows) when the game was
	// created with guideline garbage; a zero attack is no bump at all.
	if clearedLines > 0 && (e.gameMode == config.ModeTeams || e.gameMode == config.ModeCompetitive) {
		go e.bumpVictimLedgers(ctx, clear.AttackRows(e.guidelineGarbage))
	}

	e.emitUpdate(EngineUpdate{Kind: UpdatePieceLocked})

	// Increment piece index and spawn next
	e.pieceIdx.Add(1)
	go e.publishPieceIdxUpdate(e.pieceIdx.Load())

	// Spawn next piece if we're the player — off the lock (above), the
	// spawn's publish being a round trip.
	if e.getMode() == ModePlayer {
		e.spawnPiece(ctx, false)
	}
}

func (e *Engine) runCountdownConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: config.CountdownSubject(e.gameID),
	})
	if err != nil {
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
			e.tapMsg(msg)
			var cd struct {
				Seconds int `json:"seconds"`
			}
			if err := json.Unmarshal(msg.Data(), &cd); err != nil {
				continue
			}
			// The stream retains full history, so an engine joining or
			// spectating mid-game replays the pre-game countdown here — drop
			// it rather than flash 3-2-1-GO over a running game. gameStarted
			// is set from the meta fetch before any consumer delivers, so the
			// guard is deterministic for mid-game joins; during a live
			// countdown it is still false and the numbers pass through.
			if e.gameStarted.Load() {
				continue
			}
			e.emitUpdate(EngineUpdate{Kind: UpdateCountdown, Countdown: cd.Seconds})
		}
	}
}

func (e *Engine) runMetaConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: config.MetaSubject(e.gameID),
	})
	if err != nil {
		log.Printf("meta consumer error: %v", err)
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
			e.tapMsg(msg)
			var meta config.GameMeta
			if err := json.Unmarshal(msg.Data(), &meta); err != nil {
				continue
			}
			if meta.Status == config.GameStatusInProgress {
				e.gameStarted.Store(true)
			}
			if (meta.Status == config.GameStatusFinished || meta.Status == config.GameStatusArchived) && e.getMode() == ModePlayer {
				// The game is over and this engine has not heard why yet — a
				// peer decided it (a line goal reached, a roster that emptied
				// every other board) before our events consumer caught up.
				// Play stops now; the events still to come flip the verdict
				// to won if we won (decideOutcome), like a teams win does.
				e.transitionToSpectator(false)
			}
			// Status reaches EVERY engine — spectators included. Gating this
			// on ModePlayer left a spectator's view stuck pre-start ("GO!"
			// never cleared: countdownVisible waits for in_progress), and the
			// meta consumer's replay of the current meta is also what tells a
			// mid-game spectator the game is already running.
			e.emitUpdate(EngineUpdate{
				Kind:       UpdateGameStatus,
				GameStatus: string(meta.Status),
			})
			if meta.Status == config.GameStatusInProgress && e.getMode() == ModePlayer {
				e.mu.Lock()
				// Spawn only when no piece is on the board AND none is pending
				// lock-in: a piece-less replica with hadActivePiece still true
				// means a lock/hard-drop write-through happened and its echo —
				// which fires the lock-in edge and spawns the next piece — is
				// in flight. Spawning here would re-stamp the same piece over
				// the vacated cells and MASK that edge (the replica never
				// reads zero active cells), so the locked piece's line clear
				// would never run. The piece-less watchdog still covers any
				// genuinely lost spawn.
				if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil && !e.hadActivePiece {
					e.spawnPiece(ctx, true) // under e.mu
				}
				e.mu.Unlock()
			}
		}
	}
}

func (e *Engine) runEventsConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: config.EventsSubjectFilter(e.gameID),
	})
	if err != nil {
		log.Printf("events consumer error: %v", err)
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
			e.tapMsg(msg)
			// The join draws a line through the stream (joinSeq): events
			// at or before it are the game's history, replayed to converge
			// the totals; the first one past it ends the replay, and it and
			// everything after are news.
			if e.eventReplay.Load() {
				if md, err := msg.Metadata(); err == nil && md.Sequence.Stream > e.joinSeq {
					e.eventReplay.Store(false)
				}
			}
			var ev GameEvent
			if err := json.Unmarshal(msg.Data(), &ev); err != nil {
				continue
			}
			e.handleGameEvent(ctx, ev)
		}
	}
}

// emitTeammateClear strobes the rows a teammate's clear just completed on the
// shared board (the event's cleared_rows, pre-collapse indices) — the same
// UpdateRowsCleared the clearer's own engine raised for itself, so the whole
// crew sees the flash. A peer that omits the rows simply doesn't strobe.
func (e *Engine) emitTeammateClear(ev GameEvent) {
	if len(ev.ClearedRows) == 0 {
		return
	}
	e.emitUpdate(EngineUpdate{Kind: UpdateRowsCleared, ChangedRows: ev.ClearedRows})
}

// restoreOwnTotals seeds this player's cumulative totals from the last
// line_clear they published to this game — the totals they had when they
// last played it, before leaving and coming back. A player's ID is their
// name, so the rejoined player is the same sender to every other engine,
// and those fold the DELTA between the totals they last saw from a sender
// and the ones its next event carries (foldTotals): the totals the next
// clear announces must continue from where they were, or the whole crew
// drops it as a stale replay. The points and lines are folded into the
// shown totals as their locks folded them (handleLockIn): the shared score
// and level on a shared board, this player's own on a private one, and the
// team's scoreboard. The events consumer's replay of the same history then
// finds these totals already seen. Runs from Start before any consumer or
// the input loop, so nothing else touches the totals: no lock.
func (e *Engine) restoreOwnTotals(payload []byte) {
	var ev GameEvent
	if err := json.Unmarshal(payload, &ev); err != nil || ev.PlayerID != e.playerID {
		return
	}
	e.ownScore.Store(int64(ev.TotalScore))
	e.ownClearLines.Store(int64(ev.TotalLines))
	e.eventTotals[e.playerID] = struct{ score, lines, team int }{ev.TotalScore, ev.TotalLines, e.teamIdx}
	e.score.Add(int64(ev.TotalScore))
	e.totalLines.Add(int64(ev.TotalLines))
	e.refreshLevel()
	if ev.TotalScore > 0 {
		e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
	}
	if e.gameMode == config.ModeTeams {
		e.teamScores[e.teamIdx].Add(int64(ev.TotalScore))
		e.teamLines[e.teamIdx].Add(int64(ev.TotalLines))
		e.emitTeamStats()
	}
}

// foldTotals folds another player's cumulative own totals — a line_clear
// event's, or the ones its game_over carries — into the scoreboards: the
// DELTA between the sender's totals and the last totals we saw from them.
// Deltas make the fold replay-proof: an engine replaying the full event
// history (a mid-game spectator) converges to the same totals, and anything
// missed is absorbed by the next event's cumulative numbers at once. In
// cooperative mode the crew's shared score and line total move (and the
// level with them); in teams EVERY engine — teammates, every other team's
// players, and spectators — folds the sender's team's scoreboard, and its
// teammates their own shared score too, so every team's score stays live on
// every screen. Reports whether the event was news: not a stale replay of
// an older total, and not our own (folded at lock time).
func (e *Engine) foldTotals(ev GameEvent) bool {
	e.mu.Lock()
	seen := e.eventTotals[ev.PlayerID]
	deltaScore := ev.TotalScore - seen.score
	deltaLines := ev.TotalLines - seen.lines
	if deltaScore < 0 || deltaLines < 0 {
		e.mu.Unlock()
		return false // stale replay of an older total: already folded
	}
	e.eventTotals[ev.PlayerID] = struct{ score, lines, team int }{ev.TotalScore, ev.TotalLines, ev.Team}
	e.mu.Unlock()
	if ev.PlayerID == e.playerID {
		return false
	}
	own := e.gameMode == config.ModeCooperative
	if e.gameMode == config.ModeTeams && ev.Team >= 0 && ev.Team < e.TeamCount() {
		e.teamScores[ev.Team].Add(int64(deltaScore))
		e.teamLines[ev.Team].Add(int64(deltaLines))
		if deltaScore > 0 || deltaLines > 0 {
			e.emitTeamStats()
		}
		own = ev.Team == e.teamIdx
	}
	if own {
		// A shared board whose seats are scored on their own folds the
		// crew's LINES (the shared level and gravity) but never their points:
		// the shown score stays this player's alone.
		if !e.individual {
			e.score.Add(int64(deltaScore))
		}
		e.totalLines.Add(int64(deltaLines))
		e.refreshLevel()
		if deltaScore > 0 && !e.individual {
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
		}
	}
	return true
}

func (e *Engine) handleGameEvent(ctx context.Context, ev GameEvent) {
	switch ev.Kind {
	case EventLineClear:
		// The line goal is checked on every line_clear consumed — before the
		// fold's verdict on whether the event was news, since our own echo
		// is the one that carries our own crossing.
		defer e.checkLineGoal(ctx, ev)
		if !e.foldTotals(ev) {
			return
		}
		// In cooperative mode the board is shared: when ANOTHER player clears
		// lines, our playfield consumer applies the same cleared rows (the
		// authoritative state always converges), but the per-row render
		// triggers can be dropped by the lossy Updates fan-out during the
		// clear's full-visible-range republish — leaving stale, un-cleared rows
		// on our board. Force a full-board re-render from the converged
		// e.playfield (the same thing the clearing player does) so every player
		// sees the cleared board, and strobe the cleared rows: the crew's
		// clear is as much ours as theirs. Same for a teammate's clear on
		// our team board; every other team's board repaints via its own
		// consumer. A lock that only scored (lines_cleared 0) moved nothing.
		if e.gameMode == config.ModeCooperative || (e.gameMode == config.ModeTeams && ev.Team == e.teamIdx) {
			if ev.LinesCleared > 0 {
				e.emitFullBoardRerender()
			}
			// A clear from before this engine joined is history, not news:
			// its rows are long gone from the board and its points are
			// folded above. Flashing it, or naming it on the banner, would
			// tell a player who just (re)joined that a teammate scored
			// this instant.
			if e.eventReplay.Load() {
				return
			}
			if ev.LinesCleared > 0 {
				e.emitTeammateClear(ev)
			}
			if ev.LinesCleared > 0 || ev.TSpin != 0 {
				e.emitUpdate(EngineUpdate{Kind: UpdateAward, PlayerID: ev.PlayerID, Clear: awardFromEvent(ev), Score: ev.Score})
			}
		}
	case EventGameOver:
		// The ending player's last points: its game_over carries its own
		// totals like a line_clear would, so the shared score converges on
		// them everywhere before anyone archives.
		e.foldTotals(ev)
		if e.gameMode == config.ModeTeams {
			e.handleTeamGameOverEvent(ctx, ev)
			return
		}
		if e.gameMode == config.ModeCooperative && e.individual {
			// A shared board scored per seat: the top-out ends the game for
			// everyone, and the ranking is decided here — from the totals the
			// ordered stream carried up to this very event, the topper's own
			// echo included, so every engine crowns the same top scorer(s).
			e.decideOutcome(e.topScorers(), -1, false)
			if ev.PlayerID != e.playerID {
				e.finishCoopGame(ctx)
			}
			return
		}
		if ev.PlayerID != e.playerID {
			if e.gameMode == config.ModeCooperative {
				// Cooperative: any player's game over ends the game for all —
				// a loss when the crew had a goal to reach (nobody wins),
				// the end of the crew's run otherwise.
				if e.lineGoal > 0 {
					e.decideOutcome(map[string]bool{}, -1, false)
				} else {
					e.transitionToSpectator(false)
				}
				e.finishCoopGame(ctx)
			} else {
				// Competitive: track eliminated player
				e.mu.Lock()
				e.eliminatedPlayers[ev.PlayerID] = true
				eliminated := len(e.eliminatedPlayers)
				rostered := e.roster != nil
				e.mu.Unlock()

				e.emitUpdate(EngineUpdate{
					Kind:               UpdatePlayerEliminated,
					EliminatedPlayerID: ev.PlayerID,
				})

				if rostered {
					// An open game's boards come and go: the count follows
					// the seats held (roster.go).
					e.evaluateRosterOutcome(ctx)
					return
				}
				// If we're the last player standing, we win
				if eliminated >= e.playerCount-1 && e.getMode() == ModePlayer {
					e.transitionToSpectator(true) // we won!
					go e.transitionGameToFinished(ctx)
				} else if eliminated >= e.playerCount && e.initialMode == ModePlayer {
					// Draw: all players eliminated (simultaneous top-out).
					// Nobody became "last standing", so no one triggered the
					// finish transition. Do it now. CAS in
					// transitionGameToFinished ensures only one caller advances.
					go e.transitionGameToFinished(ctx)
				}
			}
		} else if e.gameMode == config.ModeCompetitive {
			// Our own game over event — mark ourselves eliminated
			e.mu.Lock()
			e.eliminatedPlayers[e.playerID] = true
			eliminated := len(e.eliminatedPlayers)
			rostered := e.roster != nil
			e.mu.Unlock()
			e.emitUpdate(EngineUpdate{
				Kind:               UpdatePlayerEliminated,
				EliminatedPlayerID: e.playerID,
			})
			if rostered {
				e.evaluateRosterOutcome(ctx)
				return
			}
			// Draw case: our own event completes the set of eliminations.
			if eliminated >= e.playerCount {
				go e.transitionGameToFinished(ctx)
			}
		}
	}
}

// finishCoopGame moves the meta to finished on a seated engine that just
// consumed a peer's game_over on the crew's board: the topper finishes the
// game too (handleTopOut), but its client may be gone — the crew's top-out
// once left an open game running for hours because the topper's finish
// never landed — and the CAS makes the finish idempotent, so every seated
// engine may make it. The one whose transition lands archives the game.
func (e *Engine) finishCoopGame(ctx context.Context) {
	if e.initialMode == ModePlayer && e.js != nil {
		go e.transitionGameToFinished(ctx)
	}
}

// handleTeamGameOverEvent processes a teams-mode elimination event (any
// player's, including the echo of our own). It tracks per-team elimination
// counts and decides the game outcome exactly once: a team is out when ALL
// its members have topped out, and the game is over when at most one team is
// still standing — that last team, alive members and already-eliminated ones
// alike, has won. With the usual two teams that is the moment either side
// falls; in a three- or six-way game the survivors play on until only one
// team is left.
//
// The events subject is a single ordered stream, so all engines see the same
// elimination order and reach the same verdict. transitionGameToFinished is
// CAS-protected and idempotent, so every winning engine may safely call it.
func (e *Engine) handleTeamGameOverEvent(ctx context.Context, ev GameEvent) {
	n := e.TeamCount()
	e.mu.Lock()
	e.eliminatedPlayers[ev.PlayerID] = true
	e.eliminatedTeam[ev.PlayerID] = ev.Team
	// The teams still standing: with a roster pushed, the ones with a seated
	// member not eliminated (an open game's teams come and go); without one,
	// the ones with fewer eliminations than seats.
	alive, lastAlive := e.alivePlayfieldsLocked()
	myTeamDead := e.teamIdx >= 0 && e.teamIdx < n
	if myTeamDead {
		if e.roster == nil {
			dead := 0
			for pid := range e.eliminatedPlayers {
				if e.eliminatedTeam[pid] == e.teamIdx {
					dead++
				}
			}
			myTeamDead = dead >= e.teamSize
		} else {
			standing := 0
			for _, s := range e.roster {
				if s.Team == e.teamIdx && !e.eliminatedPlayers[s.PlayerID] {
					standing++
				}
			}
			myTeamDead = standing == 0
		}
	}
	winTeam := -1
	if alive == 1 {
		winTeam = lastAlive
	}
	decide := alive <= 1 && !e.teamOutcomeDone && !e.outcomeDecided
	if decide {
		e.teamOutcomeDone = true
	}
	e.mu.Unlock()

	if ev.PlayerID != e.playerID {
		e.emitUpdate(EngineUpdate{
			Kind:               UpdatePlayerEliminated,
			EliminatedPlayerID: ev.PlayerID,
			Team:               ev.Team,
		})
	}

	if !decide {
		return
	}

	// Tell the UI the game is over regardless of which side we're on: an
	// eliminated player on the losing team is showing "your team plays on"
	// until this flips the status.
	e.emitUpdate(EngineUpdate{Kind: UpdateGameStatus, GameStatus: string(config.GameStatusFinished)})

	switch {
	case winTeam >= 0 && winTeam == e.teamIdx && !myTeamDead:
		// Our team won. Alive members stop playing; already-eliminated members
		// flip their "you lost" to the team win. Spectator engines (initialMode
		// ModeSpectator, teamIdx 0 default) just keep watching.
		if e.initialMode == ModePlayer {
			e.transitionToSpectator(true)
			go e.transitionGameToFinished(ctx)
		}
	case winTeam >= 0:
		// Another team won. Each of our members already transitioned
		// individually on their own top-out; the winning team's engines
		// transition the game meta.
	default:
		// Every team read as dead (the last two fell together — with an
		// ordered event stream one normally completes strictly first). Treat
		// it as a draw and make sure SOMEONE finishes the game so it archives.
		if e.initialMode == ModePlayer {
			go e.transitionGameToFinished(ctx)
		}
	}
}
