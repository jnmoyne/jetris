package rng

import (
	"math/rand/v2"

	"jetris/internal/game"
)

// Sequence provides a deterministic, seekable piece sequence.
type Sequence struct {
	seed uint64
}

// New creates a Sequence from the given seed.
func New(seed uint64) *Sequence {
	return &Sequence{seed: seed}
}

var allPieces = [7]game.PieceType{
	game.PieceI, game.PieceO, game.PieceT,
	game.PieceS, game.PieceZ, game.PieceJ, game.PieceL,
}

// Piece returns the piece type at the given index using a 7-bag randomiser.
// This is seekable: any index can be computed independently.
func (s *Sequence) Piece(index uint64) game.PieceType {
	bag := index / 7
	pos := index % 7
	src := rand.NewPCG(s.seed, bag)
	r := rand.New(src)
	pieces := allPieces
	r.Shuffle(7, func(i, j int) { pieces[i], pieces[j] = pieces[j], pieces[i] })
	return pieces[pos]
}
