package main

import (
	"reflect"
	"testing"
	"time"
)

// The rising floor's pace is the GUI's exactly (internal/game/survival.go):
// the straight line from each tier's level-1 start to its level-15 end.
func TestSurvivalCurveParity(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for _, tc := range []struct {
		tier  string
		level int
		want  time.Duration
	}{
		{"too_easy", 1, ms(20000)}, {"too_easy", 8, ms(14000)}, {"too_easy", 15, ms(8000)},
		{"super_easy", 1, ms(10000)}, {"super_easy", 8, ms(7000)}, {"super_easy", 15, ms(4000)},
		{"very_easy", 1, ms(5000)}, {"very_easy", 8, ms(3500)}, {"very_easy", 15, ms(2000)},
		{"easy", 1, ms(2500)}, {"easy", 8, ms(1750)}, {"easy", 15, ms(1000)},
		{"normal", 1, ms(3000)}, {"normal", 8, ms(2250)}, {"normal", 15, ms(1500)},
		{"hard", 1, ms(5000)}, {"hard", 8, ms(3000)}, {"hard", 15, ms(1000)},
		{"easy", 0, ms(2500)}, {"easy", 99, ms(1000)}, {"", 5, 0}, {"lunatic", 5, 0},
	} {
		if got := survivalInterval(tc.tier, tc.level); got != tc.want {
			t.Errorf("survivalInterval(%q, %d) = %v, want %v", tc.tier, tc.level, got, tc.want)
		}
	}
	if normalizeSurvival("hard") != "hard" || normalizeSurvival("too_easy") != "too_easy" ||
		normalizeSurvival("Hard") != "" || normalizeSurvival("very easy") != "" || normalizeSurvival("") != "" {
		t.Error("normalizeSurvival misreads a tier")
	}
}

// The rows a raise brings: Easy and below one, Hard four, Normal the GUI's seeded draw
// — the twenty raises of seed 42 as internal/game deals them.
func TestSurvivalRowsParity(t *testing.T) {
	want := []int{4, 4, 3, 4, 4, 3, 4, 1, 1, 2, 2, 1, 1, 3, 1, 1, 3, 2, 2, 4}
	for raise, n := range want {
		if got := survivalRaiseRows("normal", 42, raise+1); got != n {
			t.Errorf("normal raise %d (seed 42) brings %d rows, want %d", raise+1, got, n)
		}
		for _, tier := range []string{"too_easy", "super_easy", "very_easy", "easy"} {
			if survivalRaiseRows(tier, 42, raise+1) != 1 {
				t.Errorf("raise %d: %s rows are not 1", raise+1, tier)
			}
		}
		if survivalRaiseRows("hard", 42, raise+1) != 4 {
			t.Errorf("raise %d: hard rows are not 4", raise+1)
		}
	}
	if survivalRaiseRows("", 42, 1) != 0 {
		t.Error("no floor, but rows")
	}
}

// The holes: the GUI's seeded well — rows 0-3 share a column, the next four
// another — and its random draw, from the same seed and ordinals.
func TestSurvivalHolesParity(t *testing.T) {
	var well []int
	for _, h := range survivalHoles(10, 1, 0, 12, false, 42) {
		if len(h) != 1 {
			t.Fatalf("one hole drew %v", h)
		}
		well = append(well, h[0])
	}
	if want := []int{7, 7, 7, 7, 3, 3, 3, 3, 1, 1, 1, 1}; !reflect.DeepEqual(well, want) {
		t.Errorf("the well of seed 42 = %v, want %v", well, want)
	}
	messy := survivalHoles(14, 2, 5, 6, true, 42)
	if want := [][]int{{7, 8}, {8, 11}, {1, 2}, {3, 5}, {1, 12}, {5, 12}}; !reflect.DeepEqual(messy, want) {
		t.Errorf("random holes of seed 42 = %v, want %v", messy, want)
	}
	// Chunked as the raises would come, the same draws.
	var chunked [][]int
	for _, r := range [][2]int{{0, 3}, {3, 1}, {4, 4}, {8, 4}} {
		chunked = append(chunked, survivalHoles(10, 1, r[0], r[1], false, 42)...)
	}
	if !reflect.DeepEqual(chunked, survivalHoles(10, 1, 0, 12, false, 42)) {
		t.Error("chunked raises draw differently from one")
	}
	if got := survivalHoles(10, 0, 0, 1, false, 42); len(got[0]) != minSurvivalHoles {
		t.Errorf("zero holes drew %v", got)
	}
	if got := survivalHoles(3, 4, 0, 1, false, 42); len(got[0]) != 2 {
		t.Errorf("four holes on a 3-wide board drew %v", got)
	}
	if survivalHoles(10, 1, 0, 0, false, 42) != nil {
		t.Error("no rows, but holes")
	}
}
