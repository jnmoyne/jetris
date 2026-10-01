package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"sort"
	"time"

	"github.com/nats-io/nats.go"
)

// The rising floor of a survival game (gameplays §2 "Survival", guide §4.4):
// garbage rows rise from the bottom of the crew's board on a clock of their
// own, whatever the players do, and the game ends at the first top-out — the
// time survived is the result. Six tiers set the pace, every one of them
// quickening with the level (levelOf: the board's lines, a raised row
// clearing like any line once its holes are filled): the interval shrinks in
// a straight line from the tier's start at level 1 to its end at level 15.
//
//	too_easy    a row every 20 s → 8 s, one row at a time
//	super_easy  every 10 s → 4 s, one row at a time
//	very_easy   every 5 s → 2 s, one row at a time
//	easy        every 2.5 s → 1.0 s, one row at a time
//	normal      every 3.0 s → 1.5 s, one to four rows at a time (a seeded draw)
//	hard        every 5.0 s → 1.0 s, four rows at a time
//
// Everything about a raise is a function of the game's seed, so every engine
// on the board — this agent, the GUI's, a replay — agrees on it with nothing
// on the wire beyond the register's count of raises: how many rows the k-th
// raise brings (survivalRaiseRows) and where every row's holes go
// (survivalHoles). These are bit-for-bit ports of the GUI's
// internal/game/survival.go, pinned by survival_test.go.

// minSurvivalHoles floors the game's garbage_holes in a survival game: a row
// the floor raises must be clearable, or the board would only ever fill.
const minSurvivalHoles = 1

// survivalWellSpan is how many rows the floor raises through one hole column
// set before a new one is drawn: the well moves every four rows.
const survivalWellSpan = 4

// The seed streams the survival draws run on, mixed into the game's seed
// (the GUI's constants, exactly).
const (
	survivalRowsStream uint64 = 0x5375727620726f77
	survivalHoleStream uint64 = 0x53757276206f6c65
)

// normalizeSurvival reads a recorded tier: the six tiers as themselves,
// anything else — absent, an unknown word — as no rising floor.
func normalizeSurvival(s string) string {
	switch s {
	case "too_easy", "super_easy", "very_easy", "easy", "normal", "hard":
		return s
	}
	return ""
}

// survivalPace is what a tier sets: the time between raises at level 1 and
// at level 15, and how many rows a raise brings (min..max, equal when the
// tier fixes it). The zero pace is no rising floor.
type survivalPace struct {
	start, end       time.Duration
	minRows, maxRows int
}

func survivalPaceOf(tier string) survivalPace {
	switch normalizeSurvival(tier) {
	case "too_easy":
		return survivalPace{20 * time.Second, 8 * time.Second, 1, 1}
	case "super_easy":
		return survivalPace{10 * time.Second, 4 * time.Second, 1, 1}
	case "very_easy":
		return survivalPace{5 * time.Second, 2 * time.Second, 1, 1}
	case "easy":
		return survivalPace{2500 * time.Millisecond, time.Second, 1, 1}
	case "normal":
		return survivalPace{3 * time.Second, 1500 * time.Millisecond, 1, 4}
	case "hard":
		return survivalPace{5 * time.Second, time.Second, 4, 4}
	}
	return survivalPace{}
}

// survivalInterval is the time between two raises at the given level: the
// tier's start at level 1, its end at level 15, a straight line between — in
// whole nanoseconds, the GUI's expression exactly. A level outside the range
// reads as the nearer bound; zero for no rising floor.
func survivalInterval(tier string, level int) time.Duration {
	p := survivalPaceOf(tier)
	if p.start <= 0 {
		return 0
	}
	level = min(max(level, minLevel), maxLevel)
	return p.start - (p.start-p.end)*time.Duration(level-minLevel)/time.Duration(maxLevel-minLevel)
}

// survivalRaiseRows is how many rows the raise-th raise (1-based) brings: the
// tier's fixed count, or a draw off the seed and the raise's number.
func survivalRaiseRows(tier string, seed uint64, raise int) int {
	p := survivalPaceOf(tier)
	if p.maxRows <= 0 {
		return 0
	}
	if p.maxRows == p.minRows {
		return p.minRows
	}
	r := mrand.New(mrand.NewPCG(seed^survivalRowsStream, uint64(raise)))
	return p.minRows + r.IntN(p.maxRows-p.minRows+1)
}

// survivalHoles returns the hole columns of the rows a raise lands, top to
// bottom. Every raised row has an ordinal — firstRow is the board's count of
// rows applied so far, the raise's rows the ordinals after it — and the rows
// of one well (survivalWellSpan consecutive ordinals) share one draw, so the
// holes line up into a well that moves every four rows whichever raises the
// rows came in; with random set, every row draws its own. The count is
// floored at minSurvivalHoles and capped like any garbage row's. Nil for no
// rows.
func survivalHoles(width, holes, firstRow, rows int, random bool, seed uint64) [][]int {
	holes = min(max(holes, minSurvivalHoles), maxGarbageHoles, width-1)
	if rows <= 0 || holes <= 0 {
		return nil
	}
	out := make([][]int, rows)
	for k := range out {
		n := firstRow + k
		stream := n / survivalWellSpan
		if random {
			stream = n
		}
		r := mrand.New(mrand.NewPCG(seed^survivalHoleStream, uint64(stream)))
		cols := r.Perm(width)[:holes]
		sort.Ints(cols)
		out[k] = cols
	}
	return out
}

// runSurvivalClock is the rising floor's clock, run beside the piece loop
// from the game's start until it ends (guide §4.4): a raise falls due
// survivalInterval of the level after the last one landed. The raise is ONE
// write to the crew's garbage register — the next raise's rows added to the
// total owed, the count of raises advanced — under a per-subject CAS at the
// register's last-seen sequence (0 for a register never written). Every
// engine on the board keeps the same clock and fires within a round trip of
// the others; the CAS lets exactly one raise land per tick, and a lost race
// is NOT retried — the winner's echo (handleBoardMsg's kick) is what every
// clock re-arms on, this one's included. The rows are then applied by
// whichever engine's gated shrink wins (applyOwedGarbage), like an attack's.
// A tick that finds the register moved since the clock was armed (a
// crewmate's raise folded a moment before it fired, its kick still queued)
// is a no-op, not a second raise on top of theirs. The level is re-read as
// the clock waits, so the floor quickens the moment the level does.
func (g *Game) runSurvivalClock(ctx context.Context) {
	tier := g.survival
	subject := g.ownGarbageSubject()
	anchor := time.Now()
	g.mu.Lock()
	armedSeq := g.garbageSeq
	g.mu.Unlock()
	for {
		g.mu.Lock()
		level, seq, owed, raises, dead := g.level(), g.garbageSeq, g.garbageOwed, g.survivalRaises, g.dead
		g.mu.Unlock()
		if dead || g.isEnded() {
			return
		}
		due := anchor.Add(survivalInterval(tier, level))
		if wait := time.Until(due); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-g.ended:
				return
			case <-g.a.stopCh:
				return
			case <-g.survivalKick:
				anchor = time.Now()
				g.mu.Lock()
				armedSeq = g.garbageSeq
				g.mu.Unlock()
			case <-time.After(min(wait, 250*time.Millisecond)):
			}
			continue
		}
		if seq != armedSeq {
			// A raise landed since the clock was armed: it re-arms from it.
			anchor, armedSeq = time.Now(), seq
			continue
		}
		rows := survivalRaiseRows(tier, g.metaSeed, raises+1)
		if rows <= 0 {
			return
		}
		payload, _ := json.Marshal(garbageReg{Total: owed + rows, Raises: raises + 1, By: g.idx})
		if _, conflict, err := g.a.casRequest(ctx, subject, payload, nats.Header{hExpectLast: []string{fmt.Sprint(seq)}}); err != nil {
			log.Printf("survival raise: %v", err)
		} else if !conflict {
			log.Printf("survival: the floor rises %d row(s) (raise %d)", rows, raises+1)
		}
		// Whether the raise was ours or a crewmate's beat it, its echo
		// re-arms the clock; a raise lost to the wire is tried again an
		// interval on.
		anchor = time.Now()
	}
}
