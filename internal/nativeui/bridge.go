package nativeui

import (
	"context"
	"time"

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
			case engine.UpdateCASFlash:
				now := time.Now()
				for _, rc := range u.FlashCells {
					a.flash[[2]int{rc[0], rc[1]}] = now
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
			a.mu.Lock()
			switch u.Kind {
			case lobby.LobbyUpdateChat:
				if u.ChatMsg != nil {
					a.chatLog = append(a.chatLog, *u.ChatMsg)
					if len(a.chatLog) > 200 {
						a.chatLog = a.chatLog[len(a.chatLog)-200:]
					}
				}
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
