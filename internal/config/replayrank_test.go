package config

import (
	"testing"
	"time"
)

// ReplayRank places a game in its own bucket only — same mode, same
// with/without-agents class — by the shared RankBefore order, counting the
// game itself whether or not the list already holds it.
func TestReplayRank(t *testing.T) {
	at := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	comp := func(id string, score int, agent bool) ArchiveRecord {
		return ArchiveRecord{GameID: id, Mode: ModeCompetitive, StartedAt: at, FinishedAt: at.Add(time.Minute),
			Players: []PlayerResult{{PlayerID: "p", Score: score, Agent: agent}}}
	}
	recs := []ArchiveRecord{
		comp("c1", 900, false), comp("c2", 500, false), comp("c3", 100, false),
		comp("a1", 9000, true), // agents-only: a different bucket, however high it scores
		{GameID: "t1", Mode: ModeTeams, TeamScores: []int{9000, 1}, StartedAt: at, FinishedAt: at.Add(time.Minute)},
		comp("c2", 500, false), // a duplicate record counts once
	}
	if rank, of := ReplayRank(recs, comp("c2", 500, false)); rank != 2 || of != 3 {
		t.Errorf("c2: rank %d of %d, want 2 of 3", rank, of)
	}
	if rank, of := ReplayRank(recs, comp("c1", 900, false)); rank != 1 || of != 3 {
		t.Errorf("c1: rank %d of %d, want 1 of 3", rank, of)
	}
	if rank, of := ReplayRank(recs, comp("a1", 9000, true)); rank != 1 || of != 1 {
		t.Errorf("a1: rank %d of %d, want 1 of 1 (its own bucket)", rank, of)
	}
	// A game not yet in the list is ranked against the list plus itself.
	if rank, of := ReplayRank(recs, comp("c4", 700, false)); rank != 2 || of != 4 {
		t.Errorf("c4 (unlisted): rank %d of %d, want 2 of 4", rank, of)
	}
	if rank, of := ReplayRank(nil, comp("c9", 1, false)); rank != 1 || of != 1 {
		t.Errorf("empty history: rank %d of %d, want 1 of 1", rank, of)
	}
}
