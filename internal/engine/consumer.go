package engine

import (
	"context"
	"encoding/json"
	"log"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
)

func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, playerID string, startSeq uint64, isOpponent bool) {
	filterSubject := "jetricks.game." + e.gameID + ".player." + playerID + ".playfield.row.>"

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
					OpponentID:  playerID,
				})
			} else {
				// Lock-in detection: had active → no active
				hasActive := pf.ActivePieceForPlayer(e.playerIdx) != nil
				if e.hadActivePiece && !hasActive {
					e.handleLockIn(ctx)
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
		e.totalLines += len(completed)

		var scoreDelta int
		if e.gameMode == config.ModeCooperative {
			// Cooperative: score = number of players per line cleared
			scoreDelta = e.playerCount * len(completed)
		} else {
			// Competitive: score = number of lines cleared (simple count)
			scoreDelta = len(completed)
		}
		e.score += scoreDelta

		// Compute the cleared/shifted projection without mutating e.playfield.
		// The consumer will apply the published rows on echo. In coop mode,
		// remaining active cells get their AnchorRow shifted by len(completed)
		// so other players' pieces land in the right anchor position.
		shiftAnchors := e.gameMode == config.ModeCooperative
		projected := e.playfield.ProjectClearRows(completed, shiftAnchors)

		// Publish only visible rows (not empty headroom) — reduces NATS round trips.
		if e.gameMode == config.ModeCooperative {
			// Shared board: use CAS+merge-retry so the clear's whole-board shift
			// can't clobber the other player's mid-flight piece with our snapshot.
			rowsMap := make(map[int]game.Row, e.playfield.Height-e.visibleRowStart)
			for r := e.visibleRowStart; r < e.playfield.Height && r < len(projected); r++ {
				rowsMap[r] = projected[r]
			}
			e.publishProjectedRowsWithMergeRetry(ctx, rowsMap, nil, false)
		} else {
			// Competitive: per-player subjects, no other writer to preserve.
			e.publishProjectedRowsSliceNoCAS(ctx, projected, e.visibleRowStart, e.playfield.Height)
		}

		// Update level in cooperative mode
		if e.gameMode == config.ModeCooperative {
			newLevel := game.Level(e.totalLines)
			if newLevel != e.level {
				e.level = newLevel
				e.emitUpdate(EngineUpdate{Kind: UpdateLevel, Level: e.level})
			}
		}

		// Re-render the whole board: a clear shifts every row, and the UI
		// re-renders from e.playfield as the published rows echo back. A single
		// full-board update is robust against dropped per-row triggers.
		e.emitFullBoardRerender()
		e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: e.score})

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

		log.Printf("cleared %d lines, score now %d (delta %d)", len(completed), e.score, scoreDelta)
	}

	e.emitUpdate(EngineUpdate{Kind: UpdatePieceLocked})

	// Increment piece index and spawn next
	e.pieceIdx++
	go e.publishPieceIdxUpdate(e.pieceIdx)

	// Spawn next piece if we're the player
	if e.mode == ModePlayer {
		e.spawnPiece(ctx)
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
			if meta.Status == config.GameStatusInProgress && e.mode == ModePlayer {
				e.mu.Lock()
				if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil {
					e.spawnPiece(ctx)
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
			e.score += ev.Score
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: e.score})
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
				if eliminated >= e.playerCount-1 && e.mode == ModePlayer {
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

	// Compute the post-shrink projection without mutating e.playfield. The
	// consumer will update e.playfield when our published rows echo back.
	projected := e.playfield.ProjectShrink(rowsToAdd, causerIdx)

	// Top-out check uses the projection, not the in-memory playfield. Build a
	// temporary Playfield wrapping the projected rows for collision checks.
	tmpPF := &game.Playfield{
		Width:  e.playfield.Width,
		Height: e.playfield.Height,
		Rows:   projected,
	}
	topOut := false
	if p := tmpPF.ActivePieceForPlayer(e.playerIdx); p != nil {
		if p.Row < 0 {
			topOut = true
		} else if e.gameMode == config.ModeCooperative {
			if !game.CanPlaceCoop(*p, tmpPF, e.playerIdx) {
				topOut = true
			}
		} else {
			if !game.CanPlace(*p, tmpPF) {
				topOut = true
			}
		}
	}
	e.mu.Unlock()

	// Publish only visible rows (not empty headroom) NoCAS — shrink is authoritative.
	e.publishProjectedRowsSliceNoCAS(ctx, projected, e.visibleRowStart, e.playfield.Height)

	// Shrink republishes the whole visible range; force a full-board re-render so
	// no row is left stale if a per-row trigger is dropped (see emitFullBoardRerender).
	e.emitFullBoardRerender()

	if topOut {
		e.handleTopOut(ctx)
	}
}
