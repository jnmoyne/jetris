package engine

// Test hooks for other packages' tests (the UI's reveal, the archiver): the
// settings and the verdict an engine only ever takes from its game — a meta
// at Start, the ordered event stream — set directly.

// SetIndividualScoringForTest flips the single shared playfield's scoring
// rule as a meta would have at Start.
func (e *Engine) SetIndividualScoringForTest(on bool) { e.individual = on }

// DecideOutcomeForTest records a verdict as the event stream would have.
func (e *Engine) DecideOutcomeForTest(winners map[string]bool, winTeam int) {
	e.decideOutcome(winners, winTeam)
}
