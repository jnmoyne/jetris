package engine

import (
	"jetris/internal/config"
	"jetris/internal/game"
	"jetris/internal/rng"
)

// OfflineGame is what Offline builds an engine from: a game that is not
// being played, handed over whole.
type OfflineGame struct {
	GameID, PlayerID string
	Mode             config.GameMode
	// Board is the playfield as it is to show, the active piece included,
	// at the height every live board has (config.TotalRows): the headroom
	// over the visible rows.
	Board *game.Playfield
	// Rules are the play rules a screen reads — the NEXT well's depth, the
	// ghost, the hold queue, the bag — normalized for the mode as a live
	// game's are.
	Rules config.GameRules
	// Seed is the piece sequence the NEXT well reads (dealt by Rules.Bag).
	Seed uint64
	// Held is the piece in the hold slot, nil for an empty one.
	Held *game.PieceType
}

// Offline builds a transport-less engine standing for a game that is not
// being played — the lobby's How to play tour (nativeui/tutorial.go), which
// shows the game screen with nothing behind it. Start is never called: the
// engine holds the board it is handed and the rules a screen reads, and
// nothing on it moves. Every accessor a screen calls answers as it would for
// a live one-seat game — the visible region below the headroom, the NEXT
// well off the sequence, the hold slot — while the accessors the pipeline
// backs (the intent, the acks) find the piece where the board has it.
func Offline(g OfflineGame) *Engine {
	e := New(nil, g.GameID, g.PlayerID, "", g.Mode, ModePlayer, 0, 0, 0)
	if g.Board != nil {
		e.playfield = g.Board
		e.ackedField = e.playfield
	}
	e.visibleRowStart = config.VisibleRowStart
	e.playerCount = 1
	rules := g.Rules.Normalized(g.Mode)
	e.nextCount = rules.NextCount
	e.noGhost = !rules.Ghost
	e.showHeadroom = rules.ShowHeadroom
	e.hold = rules.Hold
	e.bag = rules.Bag
	e.seq = rng.NewBag(g.Seed, nil, e.bag)
	if g.Held != nil {
		e.heldPiece, e.hasHeld = *g.Held, true
	}
	return e
}
