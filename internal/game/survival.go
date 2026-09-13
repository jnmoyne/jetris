package game

import (
	"math/rand/v2"
	"sort"
	"time"

	"jetris/internal/config"
)

// The rising floor of a survival game (config.Survival): garbage rows rise
// from the bottom of the crew's board on a clock of their own, whatever the
// players do, and the game ends at the first top-out — the time survived is
// the result. Three tiers set the pace; every one of them quickens with the
// level (Level: one more every ten lines, the same level gravity follows —
// a raised row clears like any line once its holes are filled, so digging
// out is what speeds the floor up), the interval shrinking in a straight
// line from the tier's start at level 1 to its end at MaxLevel:
//
//	Easy    a row every 2.5 s → 1.0 s, one row at a time
//	Normal  every 3.0 s → 1.5 s, one to four rows at a time (a seeded draw)
//	Hard    every 5.0 s → 1.0 s, four rows at a time
//
// Everything about a raise is a pure function of the game's seed, so every
// engine, every agent and a replay agree on it without a word on the wire
// beyond the count of raises: how many rows the k-th raise brings
// (SurvivalRaiseRows), and where the holes of every row go (SurvivalHoles).
// The engine's clock, and how the raise is committed exactly once when
// several engines share the board, are the engine's business
// (engine/ledger.go, engine/solo.go).

// SurvivalWellSpan is how many rows the floor raises through one hole column
// set before a new one is drawn: the well moves every four rows, so an I
// stood in it clears a raise's worth as a quad, and a crew that keeps up
// keeps its well — until the floor moves it.
const SurvivalWellSpan = 4

// The seed streams the survival draws run on: mixed into the game's seed so
// neither the rows-per-raise draw nor the hole draw ever shares a PCG state
// with the piece sequence (rng.Sequence) or with each other.
const (
	survivalRowsStream uint64 = 0x5375727620726f77 // "Surv row"
	survivalHoleStream uint64 = 0x53757276206f6c65 // "Surv ole"
)

// SurvivalPace is what a tier sets: the time between raises at level 1
// (Start) and at MaxLevel (End), and how many rows a raise brings —
// MinRows..MaxRows, the same number when the tier fixes it.
type SurvivalPace struct {
	Start, End       time.Duration
	MinRows, MaxRows int
}

// SurvivalPaceOf is the pace of a tier; the zero pace for no rising floor.
func SurvivalPaceOf(tier config.Survival) SurvivalPace {
	switch tier.Normalized() {
	case config.SurvivalEasy:
		return SurvivalPace{Start: 2500 * time.Millisecond, End: time.Second, MinRows: 1, MaxRows: 1}
	case config.SurvivalNormal:
		return SurvivalPace{Start: 3 * time.Second, End: 1500 * time.Millisecond, MinRows: 1, MaxRows: 4}
	case config.SurvivalHard:
		return SurvivalPace{Start: 5 * time.Second, End: time.Second, MinRows: 4, MaxRows: 4}
	}
	return SurvivalPace{}
}

// SurvivalInterval is the time between two raises of a tier's floor at the
// given level: the tier's start at MinLevel, its end at MaxLevel, and a
// straight line between them — in whole nanoseconds, so every peer that
// computes it (the agents port the expression) lands on the same duration.
// A level outside MinLevel..MaxLevel reads as the nearer bound; zero for no
// rising floor.
func SurvivalInterval(tier config.Survival, level int) time.Duration {
	p := SurvivalPaceOf(tier)
	if p.Start <= 0 {
		return 0
	}
	level = min(max(level, MinLevel), MaxLevel)
	return p.Start - (p.Start-p.End)*time.Duration(level-MinLevel)/time.Duration(MaxLevel-MinLevel)
}

// SurvivalRaiseRows is how many rows the raise-th raise (1-based) of a
// tier's floor brings: the tier's fixed count, or — where the tier spans a
// range — a draw seeded by the game's seed and the raise's number, so every
// peer sees the same rows come up whichever engine's clock won the raise.
// Zero for no rising floor.
func SurvivalRaiseRows(tier config.Survival, seed uint64, raise int) int {
	p := SurvivalPaceOf(tier)
	if p.MaxRows <= 0 {
		return 0
	}
	if p.MaxRows == p.MinRows {
		return p.MinRows
	}
	r := rand.New(rand.NewPCG(seed^survivalRowsStream, uint64(raise)))
	return p.MinRows + r.IntN(p.MaxRows-p.MinRows+1)
}

// SurvivalHoles returns the hole columns of the rows a raise lands, top to
// bottom, for ProjectShrinkCascade — the survival counterpart of RaiseHoles,
// drawn from the game's seed instead of at random. Every raised row has an
// ordinal: firstRow is the ordinal of the raise's first row (the board's
// count of rows raised so far), and the raise's rows are the rows ordinals
// firstRow..firstRow+rows-1. By default the rows of one well — the
// SurvivalWellSpan consecutive ordinals sharing ordinal/SurvivalWellSpan —
// share one draw of holes columns, whether they came up in one raise or
// several, so the holes line up into a well that moves every four rows;
// with random set, every row draws its own columns, and no well forms at
// all. The count is floored at config.MinSurvivalHoles — a raised row must
// be clearable — and capped like every garbage row's (RandomGarbageHoles).
// Nil for no rows.
func SurvivalHoles(width, holes, firstRow, rows int, random bool, seed uint64) [][]int {
	holes = min(max(holes, config.MinSurvivalHoles), config.MaxGarbageHoles, width-1)
	if rows <= 0 || holes <= 0 {
		return nil
	}
	out := make([][]int, rows)
	for k := range out {
		n := firstRow + k
		stream := n / SurvivalWellSpan
		if random {
			stream = n
		}
		out[k] = survivalHoleDraw(seed, uint64(stream), width, holes)
	}
	return out
}

// survivalHoleDraw draws one row's hole columns: holes distinct columns of a
// width-wide board, sorted, off PCG(seed ^ the hole stream, stream).
func survivalHoleDraw(seed, stream uint64, width, holes int) []int {
	r := rand.New(rand.NewPCG(seed^survivalHoleStream, stream))
	cols := r.Perm(width)[:holes]
	sort.Ints(cols)
	return cols
}
