package nativeui

import (
	"context"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// pumpEngine drains an engine's Updates channel, folds scalar state into the
// App snapshot, and requests a redraw. Board/opponent updates carry no scalar
// payload — the layout re-reads e.Snapshot() each frame — so they only need an
// Invalidate. Must be cancelled via ctx: engine.Stop() does NOT close Updates.
func (a *App) pumpEngine(ctx context.Context, e *engine.Engine) {
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-e.Updates:
			if !ok {
				return
			}
			// A cooperative game-over can still earn fireworks: beating the
			// best archived co-op score for this seat count. Resolved BEFORE
			// taking a.mu — beatsCoopBest reads the lobby via getLobby, which
			// locks a.mu itself.
			coopRecord := u.Kind == engine.UpdateGameOver &&
				e.GameMode() == config.ModeCooperative && !e.IndividualScoring() && a.beatsCoopBest(e)
			a.mu.Lock()
			switch u.Kind {
			case engine.UpdateScore:
				a.score = u.Score
			case engine.UpdateLevel:
				a.level = u.Level
			case engine.UpdateAward:
				a.award = awardBanner{clear: u.Clear, points: u.Score, player: u.PlayerID, own: u.PlayerID == e.PlayerID(), at: time.Now()}

			case engine.UpdateTeamStats:
				a.teamScores = u.TeamScores
				a.teamLevels = u.TeamLevels
			case engine.UpdateGameStatus:
				a.gameStatus = u.GameStatus
			case engine.UpdateCountdown:
				if u.Countdown != a.countdown {
					a.countdownAt = time.Now() // restart the pop animation for each new number
				}
				a.countdown = u.Countdown
			case engine.UpdateGameOver:
				a.gameOver = true
				a.won = u.Won
				// A competitive or teams win earns a fireworks show. Teams
				// re-emits Won:true to already-eliminated members of the
				// winning team, so their screens celebrate too. A cooperative
				// crew celebrates a high score instead: every member's engine
				// emits the shared game over, so all their screens light up.
				if u.Won {
					if gm := e.GameMode(); gm == config.ModeCompetitive || gm == config.ModeTeams || e.IndividualScoring() {
						a.fireworks = newFireworksShow(time.Now())
					}
				}
				if coopRecord {
					a.fireworks = newFireworksShow(time.Now())
				}
			case engine.UpdateCASFlash:
				now := time.Now()
				a.casFlashes.Add(1) // the touch diagnostic's count of rejected moves
				if e.Mode() == engine.ModeSpectator {
					// Spectator: a broadcast flash from some player. Key its
					// board — the player's index (competitive) or team (teams).
					board := u.FlashPlayerIdx
					if e.GameMode() == config.ModeTeams {
						board = u.Team
					}
					m := a.specFlash[board]
					if m == nil {
						m = make(map[[2]int]time.Time)
						a.specFlash[board] = m
					}
					for _, rc := range u.FlashCells {
						m[[2]int{rc[0], rc[1]}] = now
					}
				} else {
					// Player: our own dropped write, told in two halves. The
					// piece FLIES BACK to where it stood and BUZZES there
					// (casKickAt/casKickFrom — the layout runs the recoil,
					// trackRecoil), while the
					// outline BLINKS where the lost step wanted it: the move
					// that was taken away, drawn where it would have gone. A rejection
					// with no target — a lost spawn, lock or gravity step,
					// nothing that was headed anywhere — keeps the plain
					// rainbow border on the piece's own cells instead.
					a.casKickAt = now
					a.casKickFrom = [2]float64{}
					if len(u.FlashTargetCells) > 0 {
						stood, wanted := map[[2]int]bool{}, map[[2]int]bool{}
						for _, rc := range u.FlashCells {
							stood[[2]int{rc[0], rc[1]}] = true
						}
						for _, rc := range u.FlashTargetCells {
							wanted[[2]int{rc[0], rc[1]}] = true
							a.casWant[[2]int{rc[0], rc[1]}] = now
						}
						a.casKickFrom = cellsDisplacement(wanted, stood)
					} else {
						for _, rc := range u.FlashCells {
							a.flash[[2]int{rc[0], rc[1]}] = now
						}
					}
				}
			case engine.UpdateRowsCleared:
				// Arcade feedback: strobe the rows just cleared on this
				// player's board, at their pre-collapse positions — their own
				// clear, or a teammate's on a shared board (the engine raises
				// both). Players only: a spectator's boards get no clear
				// strobe in any mode.
				if e.Mode() == engine.ModePlayer {
					now := time.Now()
					for _, r := range u.ChangedRows {
						a.rowStrobes[r] = rowStrobe{start: now, col: colStrobe}
					}
				}
			case engine.UpdateRTT:
				a.rtt = u.RTT
			}
			a.mu.Unlock()
			a.invalidate()
		}
	}
}

// beatsCoopBest reports whether the finished cooperative game's shared score
// is a new record against the lobby's archived history.
func (a *App) beatsCoopBest(e *engine.Engine) bool {
	lb := a.getLobby()
	if lb == nil || e.IndividualScoring() {
		return false // a board scored per seat has no shared score to beat
	}
	return coopScoreIsRecord(lb.Archives(), e.Score(), e.PlayerCount(), e.GameID())
}

// coopScoreIsRecord reports whether score strictly beats the best archived
// co-op TotalScore for the same seat count. The game's own record is excluded
// by gameID — depending on how fast the archive round-trips, it may or may
// not already be in the list when the game-over update arrives, and a game
// must never compete against itself. A zero score never counts as a record
// (an instant top-out with an empty history is nothing to celebrate).
func coopScoreIsRecord(recs []config.ArchiveRecord, score, playerCount int, gameID string) bool {
	if score <= 0 {
		return false
	}
	best := 0
	for _, rec := range recs {
		if rec.Mode == config.ModeCooperative && rec.PlayerCount == playerCount &&
			rec.GameID != gameID && rec.TotalScore > best {
			best = rec.TotalScore
		}
	}
	return score > best
}

// pumpLobby drains the lobby Updates channel. Player/game/archive lists are read
// live from the lobby snapshots at draw time, so most updates only Invalidate;
// chat messages are appended to the local log, and game updates refresh the
// in-game roster so the legend/ready list track late joiners.
func (a *App) pumpLobby(ctx context.Context, lb *lobby.Lobby) {
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-lb.Updates:
			if !ok {
				return
			}
			var games map[string]lobby.GameListing
			if u.Kind == lobby.LobbyUpdateGames {
				games = lb.Games()
			}
			var chat []lobby.ChatMessage
			if u.Kind == lobby.LobbyUpdateChat {
				// Re-read the full log rather than appending u.ChatMsg: the
				// Updates channel is lossy (see Lobby.emitUpdate), so this
				// ping may stand for several messages.
				chat = lb.ChatLog()
			}
			a.mu.Lock()
			switch u.Kind {
			case lobby.LobbyUpdateChat:
				a.chatLog = chat
			case lobby.LobbyUpdateGames:
				if a.eng != nil {
					if g, ok := games[a.eng.GameID()]; ok {
						// The roster as it stands — seats and all — for the
						// legend, the ready list, and the engine (the deal
						// and the game-end counting follow the seats present).
						seats := g.NormalizedSeats()
						a.gamePlayers = seats
						a.readyPlayers = seats
						a.readyNote = g.ReadyBlocker()
						a.eng.SetRoster(engineSeats(seats))
					}
				}
			}
			a.mu.Unlock()
			a.invalidate()
		}
	}
}
