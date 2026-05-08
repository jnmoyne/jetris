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

		game.ClearRows(e.playfield, completed)

		// After ClearRows shifts rows down, update AnchorRow in all remaining
		// active cells (other player's pieces in coop mode) to reflect the shift.
		// Without this, ActivePieceForPlayer returns a piece at the stale position.
		if e.gameMode == config.ModeCooperative {
			for i := range e.playfield.Rows {
				for j := range e.playfield.Rows[i].Cells {
					c := &e.playfield.Rows[i].Cells[j]
					if c.Active {
						c.AnchorRow += len(completed)
					}
				}
			}
		}

		// Publish only visible rows (not empty headroom) — reduces NATS round trips.
		visibleRows := make([]int, 0, e.playfield.Height-e.visibleRowStart)
		for r := e.visibleRowStart; r < e.playfield.Height; r++ {
			visibleRows = append(visibleRows, r)
		}
		e.publishPlayfieldRowsNoCAS(ctx, visibleRows)

		// Update level in cooperative mode
		if e.gameMode == config.ModeCooperative {
			newLevel := game.Level(e.totalLines)
			if newLevel != e.level {
				e.level = newLevel
				e.emitUpdate(EngineUpdate{Kind: UpdateLevel, Level: e.level})
			}
		}

		// Emit a single update with ALL visible rows
		allVisibleRows := make([]int, 0, e.playfield.Height-e.visibleRowStart)
		for r := e.visibleRowStart; r < e.playfield.Height; r++ {
			allVisibleRows = append(allVisibleRows, r)
		}
		e.emitUpdate(EngineUpdate{
			Kind:        UpdateLineClear,
			ChangedRows: allVisibleRows,
		})
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
		// In cooperative mode, add the other player's score delta to our shared score
		if ev.PlayerID != e.playerID && e.gameMode == config.ModeCooperative {
			e.score += ev.Score
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: e.score})
		}
	case EventCASFlash:
		e.emitUpdate(EngineUpdate{
			Kind:           UpdateCASFlash,
			FlashCells:     ev.FlashCells,
			FlashPlayerIdx: ev.FlashPlayerIdx,
		})
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

	// Shift playfield up by rowsToAdd (existing pieces move up)
	for i := 0; i < e.playfield.Height-rowsToAdd; i++ {
		e.playfield.Rows[i] = e.playfield.Rows[i+rowsToAdd]
	}
	// Add fully occupied permanent adversarial rows at the bottom, tagged with
	// the causer's player index so the UI can render them in that player's color.
	for i := e.playfield.Height - rowsToAdd; i < e.playfield.Height; i++ {
		e.playfield.Rows[i] = game.NewRow(e.playfield.Width)
		for c := 0; c < e.playfield.Width; c++ {
			e.playfield.Rows[i].Cells[c] = game.Cell{Occupied: true, PieceType: game.PieceO, Adversarial: true, PlayerIdx: causerIdx}
		}
	}

	// Update AnchorRow in all active cells to reflect the upward shift.
	// Without this, ActivePiece() reconstructs the piece at the stale
	// pre-shift position, causing false top-out detection.
	for i := range e.playfield.Rows {
		for j := range e.playfield.Rows[i].Cells {
			c := &e.playfield.Rows[i].Cells[j]
			if c.Active {
				c.AnchorRow -= rowsToAdd
			}
		}
	}

	// Check if the shift caused a top-out (active piece pushed out of bounds)
	topOut := false
	if p := e.playfield.ActivePieceForPlayer(e.playerIdx); p != nil {
		if p.Row < 0 {
			topOut = true
		} else if e.gameMode == config.ModeCooperative {
			if !game.CanPlaceCoop(*p, e.playfield, e.playerIdx) {
				topOut = true
			}
		} else {
			if !game.CanPlace(*p, e.playfield) {
				topOut = true
			}
		}
	}
	// Snapshot row data WHILE holding the mutex to avoid racing with
	// move operations that temporarily clear active cells.
	type rowSnapshot struct {
		data []byte
		row  int
	}
	// Only snapshot visible rows (not empty headroom) to reduce NATS round trips
	snapshots := make([]rowSnapshot, 0, e.playfield.Height-e.visibleRowStart)
	for r := e.visibleRowStart; r < e.playfield.Height; r++ {
		data, err := e.playfield.Rows[r].Marshal()
		if err != nil {
			continue
		}
		snapshots = append(snapshots, rowSnapshot{data: data, row: r})
	}
	e.mu.Unlock()

	// Publish snapshots with NoCAS — the shrink is authoritative.
	playerID := e.effectivePlayerID()
	for _, s := range snapshots {
		seq, err := natspkg.PublishSingleRowNoCAS(ctx, e.js, e.gameID, natspkg.RowUpdate{
			Row:      s.row,
			PlayerID: playerID,
			Payload:  s.data,
		})
		if err == nil {
			e.playfield.LastSeq[s.row] = seq
		}
	}

	if topOut {
		e.handleTopOut(ctx)
	}
}
