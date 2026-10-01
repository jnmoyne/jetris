package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// coordStats is one game's account of the agent's traffic on its board —
// the collisions the blackboard is there to spare it, and what the
// coordination itself cost — printed once as the log's `stats:` line
// (statsLine) at the end of the game, which is what scripts/bench-mk2.sh
// reads to compare crews. Guarded by g.mu, except casLosses: a dropped
// write is counted where it flashes (flash), and a pipelined batch flashes
// from its ack goroutine.
type coordStats struct {
	casLosses      atomic.Int64
	walkWaits      int   // episodes spent waiting on a teammate's falling piece (a walk, a fall or a drop blocked by it)
	walkWaitMs     int64 // their total length
	deferredSpawns int   // spawns deferred by a foreign piece on our spawn box
	deferredMs     int64
	spawnUnblocks  int            // stale spawn blockers vacated (spawnBlockedVacateAfter)
	idleVacates    int            // idle or seatless foreign pieces vacated (idlePieceVacateAfter)
	lockRetries    int            // CAS locks retried on a shared board
	locksDropped   int            // CAS locks given up on (the piece re-planned)
	claimsYielded  int            // claims re-planned because an earlier claim overlapped them
	depWaits       int            // drops held for an earlier claim to land
	depWaitMs      int64          // their total length
	echoTimeouts   int            // claims played uncoordinated because their echo never came
	replans        map[string]int // re-plans by reason (replanReason.String)
	started        time.Time      // when the game started for us
}

// noteReplan counts a re-plan by its reason. Caller holds mu.
func (s *coordStats) noteReplan(reason string) {
	if s.replans == nil {
		s.replans = map[string]int{}
	}
	s.replans[reason]++
}

// statsLine is the game's account in one greppable line:
//
//	stats: game=<id> mode=<coop|competitive|teams> seat=N coordinate=<bool> pieces=N lines=N score=N shared_score=N cas_losses=N walk_waits=N/<ms>ms deferred_spawns=N/<ms>ms spawn_unblocks=N idle_vacates=N lock_retries=N locks_dropped=N replans=<reason:count,…|none> claims_yielded=N dep_waits=N/<ms>ms echo_timeouts=N duration=<s>s
//
// Caller holds mu.
func (g *Game) statsLine() string {
	mode := map[int]string{modeCooperative: "coop", modeCompetitive: "competitive", modeTeams: "teams"}[g.mode]
	shared := g.score
	switch g.mode {
	case modeCooperative:
		shared = g.sharedScore
	case modeTeams:
		if g.team >= 0 && g.team < len(g.teamScores) {
			shared = g.teamScores[g.team]
		}
	}
	replans := "none"
	if len(g.st.replans) > 0 {
		keys := make([]string, 0, len(g.st.replans))
		for k := range g.st.replans {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s:%d", k, g.st.replans[k]))
		}
		replans = strings.Join(parts, ",")
	}
	duration := 0.0
	if !g.st.started.IsZero() {
		duration = time.Since(g.st.started).Seconds()
	}
	return fmt.Sprintf("stats: game=%s mode=%s seat=%d coordinate=%v pieces=%d lines=%d score=%d shared_score=%d cas_losses=%d walk_waits=%d/%dms deferred_spawns=%d/%dms spawn_unblocks=%d idle_vacates=%d lock_retries=%d locks_dropped=%d replans=%s claims_yielded=%d dep_waits=%d/%dms echo_timeouts=%d duration=%.1fs",
		g.id, mode, g.idx, g.coordinating(), g.pieceIdx, g.lines, g.score, shared, g.st.casLosses.Load(),
		g.st.walkWaits, g.st.walkWaitMs, g.st.deferredSpawns, g.st.deferredMs, g.st.spawnUnblocks, g.st.idleVacates,
		g.st.lockRetries, g.st.locksDropped, replans, g.st.claimsYielded, g.st.depWaits, g.st.depWaitMs, g.st.echoTimeouts, duration)
}

// trace logs one step of a piece's lifecycle when --trace is on: the
// tuning aid behind every number in the stats line.
func (g *Game) trace(format string, args ...any) {
	if g.a == nil || !g.a.trace {
		return
	}
	log.Printf("%s seat %d piece %d: "+format, append([]any{time.Now().Format("15:04:05.000000"), g.idx, g.pieceIdx}, args...)...)
}
