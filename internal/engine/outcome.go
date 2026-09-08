package engine

import (
	"slices"
	"sort"

	"jetris/internal/config"
	"jetris/internal/game"
	"jetris/internal/rng"
)

// The game's outcome, decided once. Every way a game can end — the last
// player or team standing, the cooperative crew's top-out, a shared board
// whose seats are ranked by their own scores, a playfield reaching the line
// goal, a roster that emptied every board but one — comes here to record
// the winners (decideOutcome), and every screen and the archiver read them
// back (Winners). The decision is made from the ordered event stream on
// every engine alike, so every engine names the same winners.

// Seat is one roster entry as the engine tracks it: the player and the seat
// they hold (the index their cells carry), and in teams their team and slot.
// Pushed in by the lobby (SetRoster) whenever the listing changes, so an
// open game's engine knows who is seated right now.
type Seat struct {
	PlayerID string
	Seat     int
	Team     int
	TeamSlot int
}

// SetRoster tells the engine who is seated: the listing's roster, whenever
// it changes. Until the first call the engine knows only what the stream
// tells it (the senders of events), which is every invite game's whole
// story; an open game's roster changes, and the ranking, the deal and the
// game-end counting follow the seats present.
func (e *Engine) SetRoster(seats []Seat) {
	e.mu.Lock()
	e.roster = append([]Seat(nil), seats...)
	if e.splitPieces && e.seatsPerBoard > 1 {
		// The deal follows the seats present: re-deal when they change.
		present := e.presentSlotsLocked()
		sort.Ints(present)
		if !slices.Equal(present, e.presentSlots) {
			e.redealLocked(present)
		}
	}
	e.mu.Unlock()
	// A roster that left every board but one without a player decides a
	// multi-playfield game (roster.go).
	if e.ctx != nil {
		e.evaluateRosterOutcome(e.ctx)
	}
}

// rosterIDsLocked is every seated player's ID (the roster if the lobby
// pushed one, else every sender seen plus ourselves). e.mu held.
func (e *Engine) rosterIDsLocked() []string {
	if len(e.roster) > 0 {
		ids := make([]string, 0, len(e.roster))
		for _, s := range e.roster {
			ids = append(ids, s.PlayerID)
		}
		return ids
	}
	ids := make([]string, 0, len(e.eventTotals)+1)
	seen := false
	for id := range e.eventTotals {
		ids = append(ids, id)
		seen = seen || id == e.playerID
	}
	if !seen && e.initialMode == ModePlayer {
		ids = append(ids, e.playerID)
	}
	return ids
}

// topScorers names the seated players with the highest published score —
// every one of them on a tie. Scores are the cumulative totals the ordered
// event stream carried (eventTotals, our own included), so every engine
// deciding at the same point of the stream names the same winners; nobody's
// unannounced last points count.
func (e *Engine) topScorers() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	winners := map[string]bool{}
	best, found := 0, false
	for _, id := range e.rosterIDsLocked() {
		s := e.eventTotals[id].score
		switch {
		case !found || s > best:
			best, found = s, true
			winners = map[string]bool{id: true}
		case s == best:
			winners[id] = true
		}
	}
	return winners
}

// decideOutcome records the game's winners — once; a later call is a no-op
// and reports false. Every player's engine turns to game over with its own
// verdict (won if it is among the winners), a spectator's simply ends, and
// the UI learns the game is finished. The meta is not touched here: the
// caller that owns the finish (the topper, the winners) transitions it.
func (e *Engine) decideOutcome(winners map[string]bool, winTeam int) bool {
	e.mu.Lock()
	if e.outcomeDecided {
		e.mu.Unlock()
		return false
	}
	e.outcomeDecided = true
	e.winners = make(map[string]bool, len(winners))
	for id, w := range winners {
		if w {
			e.winners[id] = true
		}
	}
	e.winTeam = winTeam
	e.mu.Unlock()
	e.transitionToSpectator(winners[e.playerID])
	e.emitUpdate(EngineUpdate{Kind: UpdateGameStatus, GameStatus: string(config.GameStatusFinished)})
	return true
}

// Winners reports the decided game's winners — the player IDs on the
// winning side and, in teams, the winning team (-1 otherwise, and on a
// draw) — and whether the game is decided at all. Undecided until the
// outcome lands (decideOutcome); the last player standing and the last team
// standing are decided here too, so every screen asks one place.
func (e *Engine) Winners() (winners map[string]bool, winTeam int, decided bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.outcomeDecided {
		return nil, -1, false
	}
	winners = make(map[string]bool, len(e.winners))
	for id := range e.winners {
		winners[id] = true
	}
	return winners, e.winTeam, true
}

// IndividualScoring reports whether the seats of this game's single shared
// playfield are scored on their own (GameMeta.IndividualScoring): the shown
// score is the player's own, the crew's line total still drives the shared
// level, and the top score at the end wins.
func (e *Engine) IndividualScoring() bool { return e.individual }

// PlayerLines reports every player's cumulative own line clears as this
// engine folded them from their line_clear events — the record a player who
// never topped out (a goal-ended game has no game_over at all) archives with.
func (e *Engine) PlayerLines() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.eventTotals))
	for id, t := range e.eventTotals {
		out[id] = t.lines
	}
	return out
}

// PlayerLevel is the level a player's own line total reached (game.Level),
// for the archive's record of a player who never announced a game over.
func PlayerLevel(lines int) int { return game.Level(lines) }

// redealLocked deals this engine's playfield: the full bag to every seat in
// a game that does not split its pieces, else the seven types dealt out
// (rng.PieceSets, off the game's seed) between the slots PRESENT — every
// slot when present is nil (the deal at creation, every invite game's deal
// for good), the seated ones of an open game otherwise, ranked in slot
// order, so a seat's ration follows its rank among the company it has. Only
// a seat's own sequence depends on the deal, so peers need no agreement —
// they all compute the same deal from the same roster anyway. The piece
// index is untouched: the next piece is drawn from the new ration at the
// same position. e.mu held (or the engine not yet started).
func (e *Engine) redealLocked(present []int) {
	e.presentSlots = nil
	if !e.splitPieces || e.seatsPerBoard <= 1 {
		e.pieceSets = nil
		e.seq.Store(rng.NewBag(e.seed, nil, e.bag))
		return
	}
	slots := make([]int, 0, e.seatsPerBoard)
	if len(present) == 0 {
		for i := 0; i < e.seatsPerBoard; i++ {
			slots = append(slots, i)
		}
	} else {
		seen := make(map[int]bool, len(present))
		for _, s := range present {
			if s >= 0 && s < e.seatsPerBoard && !seen[s] {
				seen[s] = true
				slots = append(slots, s)
			}
		}
		sort.Ints(slots)
		if len(slots) == 0 {
			for i := 0; i < e.seatsPerBoard; i++ {
				slots = append(slots, i)
			}
		} else {
			e.presentSlots = slots
		}
	}
	sets := rng.PieceSets(e.seed, len(slots))
	perSlot := make([][]game.PieceType, e.seatsPerBoard)
	for rank, slot := range slots {
		perSlot[slot] = sets[rank]
	}
	e.pieceSets = perSlot
	mine := e.pieceSetForSlotLocked(e.seatSlot())
	if mine == nil {
		mine = sets[0] // a spectator (or a seat not in the deal): the first ration, never spawned from
	}
	e.seq.Store(rng.NewBag(e.seed, mine, e.bag))
}

// presentSlotsLocked is the slots seated on this engine's playfield per the
// roster the lobby pushed: every seat on the crew's board, the team's slots
// on a team's. Nil until a roster was pushed. e.mu held.
func (e *Engine) presentSlotsLocked() []int {
	if e.roster == nil {
		return nil
	}
	slots := make([]int, 0, len(e.roster))
	for _, s := range e.roster {
		switch e.gameMode {
		case config.ModeCooperative:
			slots = append(slots, s.Seat)
		case config.ModeTeams:
			if s.Team == e.teamIdx {
				slots = append(slots, s.TeamSlot)
			}
		}
	}
	return slots
}
