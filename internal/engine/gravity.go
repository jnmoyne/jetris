package engine

import (
	"context"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/game"
)

func (e *Engine) runGravity(ctx context.Context) {
	level := 0
	timer := time.NewTimer(game.GravityInterval(level))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if e.mode != ModePlayer {
				return
			}
			_ = e.attemptMove(ctx, MoveDown)

			if e.gameMode == config.ModeCooperative {
				newLevel := game.Level(e.totalLines)
				if newLevel != level {
					level = newLevel
				}
			}
			timer.Reset(game.GravityInterval(level))
		}
	}
}
