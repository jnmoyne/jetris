package rng

import (
	"math/rand/v2"
	"slices"

	"jetris/internal/game"
)

// Sequence provides a deterministic, seekable piece sequence.
type Sequence struct {
	seed uint64
	set  []game.PieceType // the types this sequence draws from: all seven, or one seat's ration in a split-pieces game
}

// New creates a Sequence from the given seed: the standard 7-bag every peer
// of an ordinary game runs.
func New(seed uint64) *Sequence {
	return &Sequence{seed: seed, set: slices.Clone(allPieces[:])}
}

// NewSet creates a Sequence that draws only from set — one seat's ration in a
// game that splits the pieces between teammates (PieceSets). The bag is the
// ration itself: every set of |set| pieces is a shuffle of it, so a player
// holding three types sees those three in a random order, forever, and a
// player holding one type sees only that one. An empty set falls back to the
// full bag.
func NewSet(seed uint64, set []game.PieceType) *Sequence {
	s := slices.Clone(set)
	slices.Sort(s)
	s = slices.Compact(s)
	if len(s) == 0 {
		return New(seed)
	}
	return &Sequence{seed: rationSeed(seed, s), set: s}
}

var allPieces = [7]game.PieceType{
	game.PieceI, game.PieceO, game.PieceT,
	game.PieceS, game.PieceZ, game.PieceJ, game.PieceL,
}

// Piece returns the piece type at the given index using a bag randomiser over
// the sequence's set — the seven pieces of the standard bag, or the seat's
// ration in a split-pieces game.
// This is seekable: any index can be computed independently.
func (s *Sequence) Piece(index uint64) game.PieceType {
	n := uint64(len(s.set))
	bag := index / n
	pos := index % n
	src := rand.NewPCG(s.seed, bag)
	r := rand.New(src)
	pieces := slices.Clone(s.set)
	r.Shuffle(len(pieces), func(i, j int) { pieces[i], pieces[j] = pieces[j], pieces[i] })
	return pieces[pos]
}

// Set returns the piece types this sequence draws from, in piece order.
func (s *Sequence) Set() []game.PieceType { return slices.Clone(s.set) }

// dealStream is the PCG stream the piece SPLIT is dealt from. Bag streams are
// numbered upwards from 0 (Piece), so the deal takes the one number no bag
// will ever reach: the deal and the bags never draw from the same numbers.
const dealStream = ^uint64(0)

// PieceSets deals the seven piece types out among seats seats — the teams
// mode split-pieces rule (GameMeta.SplitPieces). Every one of the seven goes
// to somebody and every seat gets at least one, so between them the team
// still has the whole bag: with two seats one holds four types and the other
// three, with seven they hold one each, and past seven the pieces start
// doubling up (nobody is left empty-handed). Which seat draws which — and
// which of them gets the bigger ration — is the seed's to decide, so the deal
// is the same on every peer that computes it: both teams' boards (a team's
// slot N holds what the other team's slot N holds, exactly as both teams have
// always seen one identical piece sequence), a spectator's engine, an agent's
// own port of this function. Returns one set per seat, each in piece order.
func PieceSets(seed uint64, seats int) [][]game.PieceType {
	if seats <= 0 {
		return nil
	}
	if seats == 1 {
		return [][]game.PieceType{slices.Clone(allPieces[:])}
	}
	r := rand.New(rand.NewPCG(seed, dealStream))

	// Deal to the seats in a shuffled order, so a seat is never favoured by
	// its slot — the bigger ration falls where the seed drops it.
	order := make([]int, seats)
	for i := range order {
		order[i] = i
	}
	r.Shuffle(seats, func(i, j int) { order[i], order[j] = order[j], order[i] })

	// One card at a time off a shuffled deck of the seven, reshuffled
	// whenever it runs out: the first seven deals cover every piece type, and
	// dealing at least `seats` cards leaves no seat empty-handed.
	sets := make([][]game.PieceType, seats)
	deck := allPieces
	for i := 0; i < max(seats, len(allPieces)); i++ {
		if i%len(allPieces) == 0 {
			deck = allPieces
			r.Shuffle(len(deck), func(a, b int) { deck[a], deck[b] = deck[b], deck[a] })
		}
		seat, pt := order[i%seats], deck[i%len(allPieces)]
		if !slices.Contains(sets[seat], pt) {
			sets[seat] = append(sets[seat], pt)
		}
	}
	for i := range sets {
		slices.Sort(sets[i])
	}
	return sets
}

// PieceSetFor returns the ration seat slot holds in a game of seats seats
// (PieceSets). A slot outside the deal — or a game of one seat — holds the
// whole bag.
func PieceSetFor(seed uint64, seats, slot int) []game.PieceType {
	sets := PieceSets(seed, seats)
	if slot < 0 || slot >= len(sets) {
		return slices.Clone(allPieces[:])
	}
	return sets[slot]
}

// rationSeed derives the stream a ration draws its bags from: the game's seed
// mixed (splitmix64) with the ration itself, so seats holding different
// rations shuffle independently while one ration draws the same order
// everywhere it is held — on both teams' boards, in a spectator's engine, in
// an agent's port. The full bag keeps the game's raw seed (New), so an
// ordinary game's sequence is bit-for-bit what it always was.
func rationSeed(seed uint64, set []game.PieceType) uint64 {
	var mask uint64
	for _, pt := range set {
		mask |= 1 << uint(pt)
	}
	x := seed + 0x9E3779B97F4A7C15*(mask+1)
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}
