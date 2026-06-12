package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
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
			subject := msg.Subject()
			rowIdx, colIdx := natspkg.ParseCellFromSubject(subject)
			if rowIdx < 0 {
				continue
			}

			cell, err := game.UnmarshalCell(msg.Data())
			if err != nil {
				continue
			}

			e.mu.Lock()
			md, _ := msg.Metadata()
			var seq uint64
			if md != nil {
				seq = md.Sequence.Stream
			}
			pf.Apply(rowIdx, colIdx, cell, seq)

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
				//
				// Gated on ModePlayer: an engine that is no longer playing (a
				// spectator, or a teams-mode player whose elimination vacated
				// their cells) must never run lock-in side effects — publishing
				// clears or spawning — off its own echoes.
				hasActive := pf.ActivePieceForPlayer(e.playerIdx) != nil
				if e.hadActivePiece && !hasActive && e.getMode() == ModePlayer {
					e.handleLockIn(ctx)
					hasActive = pf.ActivePieceForPlayer(e.playerIdx) != nil
				}
				e.hadActivePiece = hasActive
				e.mu.Unlock()

				// Complete a pending ping measurement if this is the first
				// message of a batch this engine published (see ping.go).
				e.notePingEcho(seq)

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
	// Check for completed rows
	completed := game.CompletedRows(e.playfield)
	if len(completed) > 0 {
		e.totalLines.Add(int64(len(completed)))

		var scoreDelta int
		switch e.gameMode {
		case config.ModeCooperative:
			// Cooperative: score = number of players per line cleared
			scoreDelta = e.playerCount * len(completed)
		case config.ModeTeams:
			// Teams: coop scoring within the team — players per line cleared
			scoreDelta = e.teamSize * len(completed)
		default:
			// Competitive: score = number of lines cleared (simple count)
			scoreDelta = len(completed)
		}
		e.score.Add(int64(scoreDelta))

		// Compute the cleared/shifted projection without mutating e.playfield. On
		// shared boards, remaining active cells get their AnchorRow shifted by
		// len(completed) so other players' pieces land in the right anchor position.
		shiftAnchors := e.sharedBoard()
		projected := e.playfield.ProjectClearRows(completed, shiftAnchors)

		// Republish ONLY the cells the clear actually changed (a low stack
		// changes just a few), not the whole visible range — this slashes the
		// per-subject CAS contention on the shared board that was making
		// the merge-retry exhaust and drop the clear. handleLockIn holds e.mu,
		// so reading e.playfield.Rows for the diff is safe.
		changed := changedCells(e.playfield.Rows, projected, e.visibleRowStart, e.playfield.Height)
		if e.sharedBoard() {
			// Shared board: CAS+merge-retry so the shift can't clobber another
			// player's mid-flight piece with our snapshot. orderedCellKeys applies
			// another player's shifted (active) piece cells before its old
			// positions are vacated, so its active-cell count never hits zero and
			// no spurious lock + respawn fires on their engine.
			e.publishProjectedCellsWithMergeRetry(ctx, changed, nil, true)
		} else {
			// Competitive: own board, single writer — authoritative NoCAS.
			e.publishProjectedCellsNoCAS(ctx, changed, true)
		}

		// Update level on shared boards (level is driven by the shared line total)
		if e.sharedBoard() {
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

		// Teams: notify teammates of the score AND line-count change (lines keep
		// every teammate's level/gravity in sync), then send the garbage attack
		// to the opposing team.
		if e.gameMode == config.ModeTeams {
			clearEv := GameEvent{
				Kind:         EventLineClear,
				PlayerID:     e.playerID,
				Team:         e.teamIdx,
				Score:        scoreDelta,
				LinesCleared: len(completed),
			}
			data, _ := json.Marshal(clearEv)
			_, _ = e.js.Publish(ctx, config.EventsSubject(e.gameID), data)

			shrinkEv := GameEvent{
				Kind:        EventShrink,
				PlayerID:    e.playerID,
				PlayerIdx:   e.playerIdx,
				Team:        e.teamIdx,
				TargetTeam:  1 - e.teamIdx,
				RowsRemoved: len(completed),
			}
			data, _ = json.Marshal(shrinkEv)
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
		// Teams: same shared-board reasoning, scoped to OUR team's clears.
		// Also fold the line count so every teammate's level/gravity stays in
		// sync with the team's total. The opposing team's clears reach us as
		// an EventShrink instead, and their board repaints via its consumer.
		if e.gameMode == config.ModeTeams && ev.Team == e.teamIdx && ev.PlayerID != e.playerID {
			e.score.Add(int64(ev.Score))
			e.totalLines.Add(int64(ev.LinesCleared))
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
			e.emitFullBoardRerender()
		}
	case EventShrink:
		if e.gameMode == config.ModeTeams {
			// Garbage lands on the TARGET team's shared board. Every alive
			// member of that team races to apply it; the deficit guard in
			// applyTeamShrink makes the application idempotent. Eliminated
			// players and spectators never apply (their alive teammates do).
			if ev.TargetTeam == e.teamIdx && e.getMode() == ModePlayer {
				go e.applyTeamShrink(ctx, ev)
			}
			return
		}
		// Apply shrink from any OTHER player (not ourselves)
		if ev.PlayerID != e.playerID {
			go e.applyOpponentShrink(ctx, ev.RowsRemoved, ev.PlayerIdx)
		}
	case EventGameOver:
		if e.gameMode == config.ModeTeams {
			e.handleTeamGameOverEvent(ctx, ev)
			return
		}
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

	// Republish only the cells the shift actually changed (diffed under the
	// lock, reading the live rows), not the whole visible range. Competitive
	// boards are per-player single-writer, so the shrink is authoritative NoCAS.
	changed := changedCells(e.playfield.Rows, projected, e.visibleRowStart, e.playfield.Height)

	e.mu.Unlock()

	// locked=false: applyOpponentShrink released e.mu above before publishing.
	e.publishProjectedCellsNoCAS(ctx, changed, false)

	// Shrink republishes the whole visible range; force a full-board re-render so
	// no row is left stale if a per-row trigger is dropped (see emitFullBoardRerender).
	e.emitFullBoardRerender()

	if topOut {
		e.handleTopOut(ctx, false)
	}
}

// handleTeamGameOverEvent processes a teams-mode elimination event (any
// player's, including the echo of our own). It tracks per-team elimination
// counts and decides the game outcome exactly once: a team loses when ALL its
// members have topped out, at which point every member of the other team —
// alive or already eliminated — has won.
//
// The events subject is a single ordered stream, so all engines see the same
// elimination order and reach the same verdict. transitionGameToFinished is
// CAS-protected and idempotent, so every winning engine may safely call it.
func (e *Engine) handleTeamGameOverEvent(ctx context.Context, ev GameEvent) {
	e.mu.Lock()
	e.eliminatedPlayers[ev.PlayerID] = true
	e.eliminatedTeam[ev.PlayerID] = ev.Team
	elimMine, elimOther := 0, 0
	for pid := range e.eliminatedPlayers {
		if e.eliminatedTeam[pid] == e.teamIdx {
			elimMine++
		} else {
			elimOther++
		}
	}
	myTeamDead := elimMine >= e.teamSize
	otherTeamDead := elimOther >= e.teamSize
	decide := (myTeamDead || otherTeamDead) && !e.teamOutcomeDone
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
	case otherTeamDead && !myTeamDead:
		// Our team won. Alive members stop playing; already-eliminated members
		// flip their "you lost" to the team win. Spectator engines (initialMode
		// ModeSpectator, teamIdx 0 default) just keep watching.
		if e.initialMode == ModePlayer {
			e.transitionToSpectator(true)
			go e.transitionGameToFinished(ctx)
		}
	case myTeamDead && !otherTeamDead:
		// Our team lost. Each member already transitioned individually on their
		// own top-out; the winning team's engines transition the game meta.
	default:
		// Defensive: both teams read as dead (shouldn't happen with an ordered
		// event stream, where one team completes strictly first). Treat as a
		// draw and make sure SOMEONE finishes the game so it archives.
		if e.initialMode == ModePlayer {
			go e.transitionGameToFinished(ctx)
		}
	}
}

// applyTeamShrink applies a garbage attack to this engine's shared team board.
//
// Unlike competitive's applyOpponentShrink (single writer, authoritative
// NoCAS), several alive teammates receive the same event and race to apply
// the identical transform to the same subjects. Two mechanisms make that safe:
//
//   - Idempotency guard: expectedGarbage accumulates the rows owed to this
//     board across all shrink events; AdversarialRowCount() is what the
//     converged board actually shows. Garbage rows are permanent and
//     bottom-anchored, so the count grows monotonically toward the target and
//     "deficit <= 0" means the shift (ours or a teammate's) already landed.
//   - Recompute-on-CAS-failure: the batch publishes WITH per-subject CAS, and
//     a failure throws away the projection entirely — waiting for the local
//     consumer to converge, re-checking the guard, and re-projecting from
//     fresh state. A blind merge-retry would republish a stale shift after a
//     teammate's shift committed and double-shift the stack; recomputing
//     cannot. Any batch computed from a torn/stale board necessarily carries
//     a stale expectation on at least one garbage-row cell (the winning batch
//     wrote the full board width), so CAS rejects it.
//
// Exactly one teammate's batch commits per deficit; the rest converge and
// stop. Active pieces are overlaid in place by the projection (no lift — see
// ProjectShrinkShared), so the batch never touches piece cells and no
// spurious lock-in can fire on any teammate.
func (e *Engine) applyTeamShrink(ctx context.Context, ev GameEvent) {
	e.mu.Lock()
	e.expectedGarbage += ev.RowsRemoved
	e.mu.Unlock()

	const maxAttempts = 16
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Wait for the local board to take new writes (the winning batch
			// echoing back, or our own write-throughs), capped so a quiet
			// stretch still retries; the per-player offset desynchronizes
			// teammates' retry loops like the merge-retry backoff does.
			wait := time.Duration(attempt+e.playerIdx) * time.Millisecond
			if wait > 10*time.Millisecond {
				wait = 10 * time.Millisecond
			}
			select {
			case <-e.cellUpdated:
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}

		e.mu.Lock()
		deficit := e.expectedGarbage - e.playfield.AdversarialRowCount()
		if deficit <= 0 {
			e.mu.Unlock()
			break // already applied by us or a teammate
		}
		projected := e.playfield.ProjectShrinkShared(deficit, ev.PlayerIdx)
		changed := changedCells(e.playfield.Rows, projected, e.visibleRowStart, e.playfield.Height)
		keys := orderedCellKeys(changed)
		updates, err := e.buildBatchUpdates(keys, changed, true)
		e.mu.Unlock()
		if err != nil {
			log.Printf("team shrink: build batch: %v", err)
			return
		}
		if len(updates) == 0 {
			break
		}

		seq, pubErr := natspkg.PublishMoveAtomically(ctx, e.js, updates)
		if pubErr == nil {
			e.applyPublishedCells(keys, func(k game.CellPos) game.Cell { return changed[k] }, seq, false)
			break
		}
		if !errors.Is(pubErr, natspkg.ErrCASFailure) {
			log.Printf("team shrink: publish batch: %v", pubErr)
			return
		}
		// CAS conflict: a teammate's shift (or move) landed first — loop,
		// converge, re-check the guard, recompute.
	}

	// The shift touches most of the board; force a full re-render so no row is
	// left stale if per-row triggers were dropped.
	e.emitFullBoardRerender()
}
