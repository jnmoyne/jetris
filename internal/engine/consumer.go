package engine

import (
	"context"
	"encoding/json"
	"log"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
)

// runConsumer drives an ordered consumer over filterSubject, applying every row
// it delivers to pf. opponentID is set only for an opponent's playfield consumer
// (competitive mode) and tags the emitted UpdateOpponentField events; it is
// empty for this engine's own playfield.
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
			subject := msg.Subject()
			rowIdx := natspkg.ParseRowFromSubject(subject)
			if rowIdx < 0 {
				continue
			}

			row, err := game.UnmarshalRow(msg.Data())
			if err != nil {
				continue
			}

			e.mu.Lock()
			md, _ := msg.Metadata()
			var seq uint64
			if md != nil {
				seq = md.Sequence.Stream
			}
			pf.Apply(rowIdx, row, seq)

			if isOpponent {
				e.mu.Unlock()
				e.emitUpdate(EngineUpdate{
					Kind:        UpdateOpponentField,
					ChangedRows: []int{rowIdx},
					OpponentID:  opponentID,
				})
			} else {
				// Lock-in detection: had active → no active. handleLockIn spawns the
				// next piece, and the publish write-through makes it active in pf
				// immediately (still under e.mu here), so RE-READ hasActive after it
				// — otherwise hadActivePiece is left false while a piece is active,
				// and if that piece locks before the next echo (runInput races ahead
				// of the consumer on fast drops) its lock-in is missed and the player
				// stops spawning entirely.
				hasActive := pf.ActivePieceForPlayer(e.playerIdx) != nil
				if e.hadActivePiece && !hasActive {
					e.handleLockIn(ctx)
					hasActive = pf.ActivePieceForPlayer(e.playerIdx) != nil
				}
				e.hadActivePiece = hasActive
				e.mu.Unlock()

				// Signal CAS notification
				select {
				case e.rowUpdated <- struct{}{}:
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
	// Check for completed rows
	completed := game.CompletedRows(e.playfield)
	if len(completed) > 0 {
		e.totalLines.Add(int64(len(completed)))

		var scoreDelta int
		if e.gameMode == config.ModeCooperative {
			// Cooperative: score = number of players per line cleared
			scoreDelta = e.playerCount * len(completed)
		} else {
			// Competitive: score = number of lines cleared (simple count)
			scoreDelta = len(completed)
		}
		e.score.Add(int64(scoreDelta))

		// Compute the cleared/shifted projection without mutating e.playfield. In
		// coop, remaining active cells get their AnchorRow shifted by len(completed)
		// so other players' pieces land in the right anchor position.
		shiftAnchors := e.gameMode == config.ModeCooperative
		projected := e.playfield.ProjectClearRows(completed, shiftAnchors)

		// Republish ONLY the rows the clear actually changed (a low stack changes
		// just a few), not the whole visible range — this slashes the per-subject
		// CAS contention on the shared coop board that was making the merge-retry
		// exhaust and drop the clear. handleLockIn holds e.mu, so reading
		// e.playfield.Rows for the diff is safe.
		changed := changedRows(e.playfield.Rows, projected, e.visibleRowStart, e.playfield.Height)
		if e.gameMode == config.ModeCooperative {
			// Shared board: CAS+merge-retry so the shift can't clobber the other
			// player's mid-flight piece with our snapshot. bottomFirst=true: a clear
			// shifts pieces DOWN, so consumers must apply the highest (lowest-on-
			// screen) rows first — applied top-to-bottom, another player's mid-flight
			// piece is briefly erased from its old rows before its shifted rows
			// arrive, its active-cell count hits zero, and it fires a spurious lock +
			// respawn. Bottom-first keeps the shifted piece overlapping itself.
			e.publishProjectedRowsWithMergeRetry(ctx, changed, nil, true, true)
		} else {
			// Competitive: own board, single writer — authoritative NoCAS.
			e.publishProjectedRowsNoCAS(ctx, changed, true, true)
		}

		// Update level in cooperative mode
		if e.gameMode == config.ModeCooperative {
			newLevel := game.Level(int(e.totalLines.Load()))
			if int64(newLevel) != e.level.Load() {
				e.level.Store(int64(newLevel))
				e.emitUpdate(EngineUpdate{Kind: UpdateLevel, Level: newLevel})
			}
		}

		// Re-render the whole board: a clear shifts every row, and the UI
		// re-renders from e.playfield as the published rows echo back. A single
		// full-board update is robust against dropped per-row triggers.
		e.emitFullBoardRerender()
		e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})

		// Cooperative: notify other players of the score change
		if e.gameMode == config.ModeCooperative {
			ev := GameEvent{
				Kind:     EventLineClear,
				PlayerID: e.playerID,
				Score:    scoreDelta,
			}
			data, _ := json.Marshal(ev)
			_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)
		}

		// Competitive: send shrink event to ALL opponents (every line clear adds rows)
		if e.gameMode == config.ModeCompetitive {
			ev := GameEvent{
				Kind:        EventShrink,
				PlayerID:    e.playerID,
				PlayerIdx:   e.playerIdx,
				RowsRemoved: len(completed),
			}
			data, _ := json.Marshal(ev)
			_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)
		}
	}

	e.emitUpdate(EngineUpdate{Kind: UpdatePieceLocked})

	// Increment piece index and spawn next
	e.pieceIdx.Add(1)
	go e.publishPieceIdxUpdate(e.pieceIdx.Load())

	// Spawn next piece if we're the player. handleLockIn runs under e.mu (held
	// by runConsumer), so locked=true.
	if e.getMode() == ModePlayer {
		e.spawnPiece(ctx, true)
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
			var cd struct {
				Seconds int `json:"seconds"`
			}
			if err := json.Unmarshal(msg.Data(), &cd); err != nil {
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
			var meta config.GameMeta
			if err := json.Unmarshal(msg.Data(), &meta); err != nil {
				continue
			}
			if meta.Status == config.GameStatusInProgress && e.getMode() == ModePlayer {
				e.mu.Lock()
				if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil {
					e.spawnPiece(ctx, true) // under e.mu
				}
				e.mu.Unlock()
				e.emitUpdate(EngineUpdate{
					Kind:       UpdateGameStatus,
					GameStatus: "in_progress",
				})
			}
		}
	}
}

func (e *Engine) runEventsConsumer(ctx context.Context) {
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, e.js, natspkg.OrderedConsumerConfig{
		Stream:        config.GameStream(e.gameID),
		FilterSubject: config.EventsSubject(e.gameID),
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
			var ev GameEvent
			if err := json.Unmarshal(msg.Data(), &ev); err != nil {
				continue
			}
			e.handleGameEvent(ctx, ev)
		}
	}
}

func (e *Engine) handleGameEvent(ctx context.Context, ev GameEvent) {
	switch ev.Kind {
	case EventLineClear:
		// In cooperative mode the board is shared: when ANOTHER player clears
		// lines, our playfield consumer applies the same cleared rows (the
		// authoritative state always converges), but the per-row render
		// triggers can be dropped by the lossy Updates fan-out during the
		// clear's full-visible-range republish — leaving stale, un-cleared rows
		// on our board. Force a full-board re-render from the converged
		// e.playfield (the same thing the clearing player does) so every player
		// sees the cleared board. Also fold in the shared score delta.
		if ev.PlayerID != e.playerID && e.gameMode == config.ModeCooperative {
			e.score.Add(int64(ev.Score))
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
			e.emitFullBoardRerender()
		}
	case EventShrink:
		// Apply shrink from any OTHER player (not ourselves)
		if ev.PlayerID != e.playerID {
			go e.applyOpponentShrink(ctx, ev.RowsRemoved, ev.PlayerIdx)
		}
	case EventGameOver:
		if ev.PlayerID != e.playerID {
			if e.gameMode == config.ModeCooperative {
				// Cooperative: any player's game over ends the game for all
				e.transitionToSpectator(false)
			} else {
				// Competitive: track eliminated player
				e.mu.Lock()
				e.eliminatedPlayers[ev.PlayerID] = true
				eliminated := len(e.eliminatedPlayers)
				e.mu.Unlock()

				e.emitUpdate(EngineUpdate{
					Kind:               UpdatePlayerEliminated,
					EliminatedPlayerID: ev.PlayerID,
				})

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
			e.mu.Unlock()
			e.emitUpdate(EngineUpdate{
				Kind:               UpdatePlayerEliminated,
				EliminatedPlayerID: e.playerID,
			})
			// Draw case: our own event completes the set of eliminations.
			if eliminated >= e.playerCount {
				go e.transitionGameToFinished(ctx)
			}
		}
	}
}

func (e *Engine) applyOpponentShrink(ctx context.Context, rowsToAdd int, causerIdx int) {
	e.mu.Lock()

	// Compute the post-shrink projection without mutating e.playfield here; the
	// publish below write-throughs the committed rows into e.playfield (and the
	// consumer echo reconciles via pf.Apply's strictly-higher-sequence rule).
	// ProjectShrink holds our falling piece in place, pushes it up only as far
	// as the rising stack/garbage forces it, and reports topOut when that push
	// would run it off the top of the board.
	projected, topOut := e.playfield.ProjectShrink(rowsToAdd, causerIdx, e.playerIdx)

	// Republish only the rows the shift actually changed (diffed under the lock,
	// reading the live rows), not the whole visible range. Competitive boards are
	// per-player single-writer, so the shrink is authoritative NoCAS.
	changed := changedRows(e.playfield.Rows, projected, e.visibleRowStart, e.playfield.Height)

	e.mu.Unlock()

	// locked=false: applyOpponentShrink released e.mu above before publishing.
	e.publishProjectedRowsNoCAS(ctx, changed, false, false)

	// Shrink republishes the whole visible range; force a full-board re-render so
	// no row is left stale if a per-row trigger is dropped (see emitFullBoardRerender).
	e.emitFullBoardRerender()

	if topOut {
		e.handleTopOut(ctx)
	}
}
