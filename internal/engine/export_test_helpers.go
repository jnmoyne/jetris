package engine

import (
	"context"

	"jetris/internal/config"
)

// Test hooks for other packages' tests (the UI's reveal, the archiver): the
// settings and the verdict an engine only ever takes from its game — a meta
// at Start, the ordered event stream — set directly.

// SetIndividualScoringForTest flips the single shared playfield's scoring
// rule as a meta would have at Start.
func (e *Engine) SetIndividualScoringForTest(on bool) { e.individual = on }

// DecideOutcomeForTest records a verdict as the event stream would have.
func (e *Engine) DecideOutcomeForTest(winners map[string]bool, winTeam int) {
	e.decideOutcome(winners, winTeam, false)
}

// SetLineGoalForTest sets the game's length in lines as a meta would have
// at Start.
func (e *Engine) SetLineGoalForTest(goal int) { e.lineGoal = goal }

// SetSurvivalForTest sets the rising floor's tier as a meta would have at
// Start.
func (e *Engine) SetSurvivalForTest(tier config.Survival) { e.survival = tier }

// HandleGameEventForTest folds one event as the ordered event stream would
// have delivered it.
func (e *Engine) HandleGameEventForTest(ev GameEvent) { e.handleGameEvent(e.ctxForTest(), ev) }

func (e *Engine) ctxForTest() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

// SetOwnTotalsForTest seeds this player's own cumulative totals — the points
// and lines of their own locks — as those locks would have.
func (e *Engine) SetOwnTotalsForTest(score, lines int) {
	e.ownScore.Store(int64(score))
	e.ownClearLines.Store(int64(lines))
}
