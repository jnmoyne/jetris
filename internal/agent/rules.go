package agent

import (
	"jetris/internal/game"
)

// Rules captures the board variant the planner and simulator must respect —
// the exact split the engine itself makes between competitive private boards
// and shared (cooperative/teams) boards.
//
// On a private competitive board only locked cells block (CanPlace/Rotate/
// HardDropDestination). On a shared board another player's mid-flight piece
// is a temporary obstacle too, so the *Coop variants apply, keyed by our own
// global roster index.
type Rules struct {
	Shared     bool // shared board: other players' active pieces block moves
	PlayerIdx  int  // own global roster index (tags our cells; keys coop collision)
	SectionIdx int  // own 10-wide section on a shared board (0 on private boards)
}

func (r Rules) canPlace(p game.Piece, pf *game.Playfield) bool {
	if r.Shared {
		return game.CanPlaceCoop(p, pf, r.PlayerIdx)
	}
	return game.CanPlace(p, pf)
}

func (r Rules) rotate(p game.Piece, clockwise bool, pf *game.Playfield) (game.Piece, bool) {
	if r.Shared {
		return game.RotateCoop(p, clockwise, pf, r.PlayerIdx)
	}
	return game.Rotate(p, clockwise, pf)
}

func (r Rules) dropDest(p game.Piece, pf *game.Playfield) game.Piece {
	if r.Shared {
		return game.HardDropDestinationCoop(p, pf, r.PlayerIdx)
	}
	return game.HardDropDestination(p, pf)
}
