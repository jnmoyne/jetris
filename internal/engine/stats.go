package engine

import (
	"time"

	"jetris/internal/config"
)

// Every seat's stats — what its locks did over the game, by the Guideline's
// names — are folded from the same line_clear events the scoreboards are
// (foldTotals): each event names its clear (lines, T-spin, Back-to-Back,
// combo, perfect), so an engine that folds every seat's events holds every
// seat's tally, its own included once its echoes come back, and the archiver
// writes what it folded into the record (config.PlayerStats). The events are
// cumulative in their totals and a replay of them is dropped by the fold's
// delta rule, so a tally counts each lock once however many times the
// history is read.

// TallyEvent folds one line_clear event into a seat's tally: the clear by
// its size, the spin that made it, whether it extended a Back-to-Back chain,
// its place in the combo (the tally keeps the longest), whether it left the
// board empty, and — where the game attacks (attacks: competitive and teams)
// — the garbage rows it sent, by the game's attack rule (guideline:
// game.Clear.AttackRows). The seat's piece count is kept at the highest the
// events announced, a game_over's too. Any other kind of event tallies
// nothing but the pieces.
func TallyEvent(st *config.PlayerStats, ev GameEvent, attacks, guideline bool) {
	st.Pieces = max(st.Pieces, int(ev.PieceCount))
	if ev.Kind != EventLineClear {
		return
	}
	clear := awardFromEvent(ev)
	switch {
	case clear.Lines >= 4:
		st.Quads++
	case clear.Lines == 3:
		st.Triples++
	case clear.Lines == 2:
		st.Doubles++
	case clear.Lines == 1:
		st.Singles++
	}
	switch ev.TSpin {
	case 1: // game.TSpinMini
		st.MiniTSpins++
	case 2, 3: // game.TSpinFull, game.TSpin180
		st.TSpins++
	}
	if clear.BackToBack && clear.Difficult() {
		st.BackToBacks++
	}
	if clear.Lines > 0 {
		st.MaxCombo = max(st.MaxCombo, clear.Combo)
		if clear.Perfect {
			st.PerfectClears++
		}
		if attacks {
			st.Attack += clear.AttackRows(guideline)
		}
	}
}

// tallyLocked folds an event this engine just accepted as news into its
// sender's tally. e.mu held.
func (e *Engine) tallyLocked(ev GameEvent) {
	st := e.eventStats[ev.PlayerID]
	TallyEvent(&st, ev, e.gameMode != config.ModeCooperative, e.guidelineGarbage)
	e.eventStats[ev.PlayerID] = st
}

// PlayerStats reports every seat's tally as this engine folded it from the
// seats' line_clear events — the stats beside every scoreboard row, and what
// the archiver writes for every player. A seat's own row is a round trip
// behind its play, like its scoreboard row (its last lock's event still on
// its way back).
func (e *Engine) PlayerStats() map[string]config.PlayerStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]config.PlayerStats, len(e.eventStats))
	for id, st := range e.eventStats {
		out[id] = st
	}
	return out
}

// OwnStats is this player's own tally (PlayerStats' row for them) with what
// the engine knows first-hand filled in: the pieces it drew, and the time it
// played — from the game's start to its own game over, or to now while it
// plays (Played).
func (e *Engine) OwnStats() config.PlayerStats {
	e.mu.Lock()
	st := e.eventStats[e.playerID]
	e.mu.Unlock()
	st.Pieces = max(st.Pieces, int(e.pieceIdx.Load()))
	st.PlayedMs = e.Played().Milliseconds()
	return st
}

// Played is how long this engine's player has been in the game: from the
// moment it went in progress (the meta's StartedAt) to their game over —
// their top-out, or the finish that ended the game for everyone — and to now
// while they play. Zero before the game started. The same clock the rising
// floor's result reads (Survived); every game has the figure.
func (e *Engine) Played() time.Duration { return e.Survived() }
