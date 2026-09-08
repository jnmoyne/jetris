package engine

import (
	"context"

	"jetris/internal/config"
)

// The line goal (GameMeta.LineGoal): the game's length in lines. The game
// ends the moment a playfield has cleared that many lines in total — on a
// single playfield the game is over (the crew is done; a board scored per
// seat crowns its top scorer), across several the first playfield there
// wins. The goal is detected where every engine sees the same thing: the
// ordered events stream, when the line_clear that crosses it is consumed —
// never at the clearer's own lock, which peers would see later — so every
// engine names the same playfield first. Every mode publishes line_clear
// for it (competitive included, since a private board's lines are its
// player's alone).

// playfieldLinesLocked is the lines the playfield ev's sender plays on has
// cleared, as the stream told this engine so far: every seat's total on the
// crew's board (a departed seat's lines were cleared here too), the team's
// seats' on a team's board, the sender's own on a competitive board. e.mu
// held.
func (e *Engine) playfieldLinesLocked(ev GameEvent) int {
	switch e.gameMode {
	case config.ModeCooperative:
		n := 0
		for _, t := range e.eventTotals {
			n += t.lines
		}
		return n
	case config.ModeTeams:
		n := 0
		for _, t := range e.eventTotals {
			if t.team == ev.Team {
				n += t.lines
			}
		}
		return n
	default:
		return e.eventTotals[ev.PlayerID].lines
	}
}

// checkLineGoal runs on every line_clear consumed (own echo included, a
// stale replay too — the totals are cumulative and the verdict is made
// once): when the sender's playfield has reached the goal, the game is
// decided — the crew wins together, a board scored per seat by its top
// scorer(s), a team by its seated members, a competitive board by its
// player — and every player's engine moves the meta to finished (the CAS
// makes the finish idempotent, so it never depends on the winner being a
// peer that knows the rule).
func (e *Engine) checkLineGoal(ctx context.Context, ev GameEvent) {
	if e.lineGoal <= 0 {
		return
	}
	e.mu.Lock()
	decided := e.outcomeDecided
	lines := e.playfieldLinesLocked(ev)
	e.mu.Unlock()
	if decided || lines < e.lineGoal {
		return
	}
	winners, winTeam := map[string]bool{}, -1
	switch e.gameMode {
	case config.ModeCooperative:
		if e.individual {
			winners = e.topScorers()
		} else {
			e.mu.Lock()
			for _, id := range e.rosterIDsLocked() {
				winners[id] = true
			}
			e.mu.Unlock()
		}
	case config.ModeTeams:
		winTeam = ev.Team
		e.mu.Lock()
		for _, s := range e.roster {
			if s.Team == winTeam {
				winners[s.PlayerID] = true
			}
		}
		if len(e.roster) == 0 {
			// No roster pushed: the team's senders, and ourselves if we are on it.
			for id, t := range e.eventTotals {
				if t.team == winTeam {
					winners[id] = true
				}
			}
			if e.initialMode == ModePlayer && e.teamIdx == winTeam {
				winners[e.playerID] = true
			}
		}
		e.mu.Unlock()
	default:
		winners[ev.PlayerID] = true
	}
	e.mu.Lock()
	e.goalReached = true
	e.mu.Unlock()
	if !e.decideOutcome(winners, winTeam) {
		return
	}
	if e.initialMode == ModePlayer && e.js != nil {
		go e.transitionGameToFinished(ctx)
	}
}

// GoalReached reports whether the game ended by reaching its line goal.
func (e *Engine) GoalReached() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.goalReached
}

// GoalProgress is this playfield's lines against the goal — the crew's
// total on the shared board, the team's on a team's, our own on a
// competitive board — and the goal itself (0 = the game has none).
func (e *Engine) GoalProgress() (lines, goal int) {
	switch e.gameMode {
	case config.ModeCooperative:
		lines = int(e.totalLines.Load())
	case config.ModeTeams:
		if e.teamIdx >= 0 && e.teamIdx < config.MaxTeamCount {
			lines = int(e.teamLines[e.teamIdx].Load())
		}
	default:
		lines = int(e.ownClearLines.Load())
	}
	return lines, e.lineGoal
}

// TeamGoalProgress is one team's lines against the goal.
func (e *Engine) TeamGoalProgress(team int) (lines, goal int) {
	if team >= 0 && team < config.MaxTeamCount {
		lines = int(e.teamLines[team].Load())
	}
	return lines, e.lineGoal
}
