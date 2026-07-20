package nativeui

import (
	"context"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
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
			a.mu.Lock()
			switch u.Kind {
			case engine.UpdateScore:
				a.score = u.Score
			case engine.UpdateLevel:
				a.level = u.Level
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
				// winning team, so their screens celebrate too.
				if u.Won {
					if gm := e.GameMode(); gm == config.ModeCompetitive || gm == config.ModeTeams {
						a.fireworks = newFireworksShow(time.Now())
					}
				}
			case engine.UpdateCASFlash:
				now := time.Now()
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
					// Player: our own dropped-write flash on our own board.
					for _, rc := range u.FlashCells {
						a.flash[[2]int{rc[0], rc[1]}] = now
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
						a.gamePlayers = g.Players
						a.readyPlayers = g.Players
					}
				}
			}
			a.mu.Unlock()
			a.invalidate()
		}
	}
}
