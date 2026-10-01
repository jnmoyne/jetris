package main

import (
	"fmt"
	"testing"
	"time"
)

// The keep set with pins: a pinned game stays in the set out of both the
// top N and the recent N, and the top set is unchanged by it.
func TestReplayKeepSetPins(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	var recs []replayRecord
	total := replayTopN + replayRecentN + 1
	for i := 1; i <= total; i++ {
		recs = append(recs, replayRecord{GameID: fmt.Sprintf("g-%02d", i), Mode: modeCooperative, TotalScore: i * 100,
			StartedAt: t0.Add(time.Duration(i) * time.Minute), FinishedAt: t0.Add(time.Duration(i)*time.Minute + 30*time.Second)})
	}
	keep, top := replayKeepSet(recs, nil)
	if keep["g-01"] || top["g-01"] {
		t.Fatal("the oldest, lowest game is out of both sets")
	}
	keep, top = replayKeepSet(recs, map[string]bool{"g-01": true})
	if !keep["g-01"] {
		t.Fatal("a pinned game must be kept")
	}
	if top["g-01"] {
		t.Fatal("a pin must not rank a game")
	}
	if len(keep) != replayTopN+replayRecentN+1 && len(keep) != replayRecentN+1 {
		t.Fatalf("keep set of %d games", len(keep))
	}
}
