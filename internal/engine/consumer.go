package engine

import (
	"context"
	"encoding/json"
	"log"

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
			}

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
				// very message may be the blocker moving away. The gravity
				// tick's retry remains as the backstop, but at agent speeds a
				// blocking piece slides across the spawn cells in milliseconds
				// and waiting a full gravity tick (a second at level 0) per
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
	// Detect completed rows on the live replica. Cooperative publishes the
	// collapse with merge-retry (no garbage in coop, no gate); competitive
	// and teams run it as a GATED transform, whose recompute re-detects the
	// completed rows from converged state if the gate is lost (e.g. a shrink
	// landed first and shifted them, or a teammate's clear already took them).
	// Score, events, and the attack are derived from the rows the committed
	// transform ACTUALLY cleared, not the pre-race detection.
	clearedLines := 0
	var clearedRows []int // the rows the committed transform actually cleared (pre-collapse indices)
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
			e.publishProjectedCellsWithMergeRetry(ctx, changed, nil, true)
			clearedLines = len(completed)
			clearedRows = completed
		} else {
			shiftAnchors := e.sharedBoard()
			var cleared []int
			committed := e.publishGatedTransform(ctx, txnOpClear, true, func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) ([]game.Row, TxnRegister, bool) {
				rows := game.CompletedRows(pf)
				if len(rows) == 0 {
					return nil, TxnRegister{}, false
				}
				cleared = rows
				return pf.ProjectClearRows(rows, shiftAnchors), TxnRegister{Applied: txn.Applied}, true
			})
			if committed {
				clearedLines = len(cleared)
				clearedRows = cleared
			}
		}
	}

	if clearedLines > 0 {
		e.totalLines.Add(int64(clearedLines))

		var scoreDelta int
		switch e.gameMode {
		case config.ModeCooperative:
			// Cooperative: score = number of players per line cleared
			scoreDelta = e.playerCount * clearedLines
		case config.ModeTeams:
			// Teams: coop scoring within the team — players per line cleared
			scoreDelta = e.teamSize * clearedLines
		default:
			// Competitive: score = number of lines cleared (simple count)
			scoreDelta = clearedLines
		}
		e.score.Add(int64(scoreDelta))
		e.ownClearScore.Add(int64(scoreDelta))
		e.ownClearLines.Add(int64(clearedLines))

		// Update level on shared boards (level is driven by the shared line total)
		if e.sharedBoard() {
			e.refreshLevel()
		}

		// Re-render the whole board: a clear shifts every row, and the UI
		// re-renders from e.playfield as the published rows echo back. A single
		// full-board update is robust against dropped per-row triggers.
		e.emitFullBoardRerender()
		// Arcade feedback: tell the UI WHICH rows this player's lock completed
		// (their pre-collapse positions) so it can strobe them. Teammates on a
		// shared board strobe the same rows off the line-clear event below,
		// which carries them as cleared_rows.
		e.emitUpdate(EngineUpdate{Kind: UpdateRowsCleared, ChangedRows: clearedRows})
		e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
		if e.gameMode == config.ModeTeams {
			// Fold our own clear into the per-team scoreboard; everyone else
			// folds it off the line-clear event below.
			e.teamScores[e.teamIdx].Add(int64(scoreDelta))
			e.teamLines[e.teamIdx].Add(int64(clearedLines))
			e.emitTeamStats()
		}

		// Cooperative: notify other players of the score change. Teams: notify
		// everyone of the score AND line-count change (lines keep every
		// teammate's level/gravity in sync). The event goes to the sender's
		// per-kind subject and carries the sender's cumulative own-clears
		// totals — receivers fold deltas, so retention trimming an older
		// event of ours is harmless.
		if e.gameMode == config.ModeCooperative || e.gameMode == config.ModeTeams {
			ev := GameEvent{
				Kind:         EventLineClear,
				PlayerID:     e.playerID,
				Team:         e.teamIdx,
				Score:        scoreDelta,
				LinesCleared: clearedLines,
				ClearedRows:  clearedRows,
				TotalScore:   int(e.ownClearScore.Load()),
				TotalLines:   int(e.ownClearLines.Load()),
			}
			data, _ := json.Marshal(ev)
			_, _ = e.js.Publish(ctx, config.EventKindSubject(e.gameID, string(EventLineClear), e.playerID), data)
		}

		// The attack: advance every victim board's garbage register (teams:
		// the opposing board; competitive: all surviving opponents). The
		// durable CAS-add ledger replaces the old fire-and-forget shrink
		// event — simultaneous attacks sum instead of trimming each other,
		// and victims reconcile the register whenever they catch up. On a
		// goroutine: handleLockIn holds e.mu, which the bump needs briefly,
		// and the bump's publishes must not extend the lock-in critical
		// section anyway. The attack is one row per line, or the Guideline
		// table (a single sends nothing) when the game was created with
		// guideline garbage; a zero attack is no bump at all.
		if e.gameMode == config.ModeTeams || e.gameMode == config.ModeCompetitive {
			go e.bumpVictimLedgers(ctx, game.AttackRows(clearedLines, e.guidelineGarbage))
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

func (e *Engine) handleGameEvent(ctx context.Context, ev GameEvent) {
	switch ev.Kind {
	case EventLineClear:
		// Fold the DELTA between the sender's cumulative totals and the last
		// totals we saw from them. Deltas make the fold replay-proof: an
		// engine replaying the full event history (a mid-game spectator)
		// converges to the same totals, and anything missed is absorbed by
		// the next event's cumulative numbers at once.
		e.mu.Lock()
		seen := e.eventTotals[ev.PlayerID]
		deltaScore := ev.TotalScore - seen.score
		deltaLines := ev.TotalLines - seen.lines
		if deltaScore < 0 || deltaLines < 0 {
			e.mu.Unlock()
			return // stale replay of an older total: already folded
		}
		e.eventTotals[ev.PlayerID] = struct{ score, lines int }{ev.TotalScore, ev.TotalLines}
		e.mu.Unlock()

		// In cooperative mode the board is shared: when ANOTHER player clears
		// lines, our playfield consumer applies the same cleared rows (the
		// authoritative state always converges), but the per-row render
		// triggers can be dropped by the lossy Updates fan-out during the
		// clear's full-visible-range republish — leaving stale, un-cleared rows
		// on our board. Force a full-board re-render from the converged
		// e.playfield (the same thing the clearing player does) so every player
		// sees the cleared board. Also fold in the shared score delta, and
		// strobe the cleared rows: the crew's clear is as much ours as theirs.
		if ev.PlayerID != e.playerID && e.gameMode == config.ModeCooperative {
			e.score.Add(int64(deltaScore))
			e.totalLines.Add(int64(deltaLines))
			e.refreshLevel()
			e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
			e.emitFullBoardRerender()
			e.emitTeammateClear(ev)
		}
		// Teams: EVERY engine — teammates, the opposing team's players, and
		// spectators — folds the clear into the per-team scoreboard (the
		// clearing player already did in handleLockIn), so the TEAM A / TEAM B
		// scores stay live on every screen.
		if e.gameMode == config.ModeTeams && ev.PlayerID != e.playerID {
			e.teamScores[ev.Team].Add(int64(deltaScore))
			e.teamLines[ev.Team].Add(int64(deltaLines))
			e.emitTeamStats()
			// Our own team's clear: same shared-board reasoning as cooperative
			// above. Also fold the line count so every teammate's level/gravity
			// stays in sync with the team's total. The opposing team's board
			// repaints via its own consumer.
			if ev.Team == e.teamIdx {
				e.score.Add(int64(deltaScore))
				e.totalLines.Add(int64(deltaLines))
				e.refreshLevel()
				e.emitUpdate(EngineUpdate{Kind: UpdateScore, Score: int(e.score.Load())})
				e.emitFullBoardRerender()
				e.emitTeammateClear(ev)
			}
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
