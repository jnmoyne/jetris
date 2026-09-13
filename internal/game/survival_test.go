package game

import (
	"reflect"
	"testing"
	"time"

	"jetris/internal/config"
)

// The rising floor's pace: every tier's interval is a straight line from
// its level-1 start to its level-15 end, in whole nanoseconds, clamped to the
// level range — the numbers the agents' port has to match bit for bit.
func TestSurvivalInterval(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for _, tc := range []struct {
		tier  config.Survival
		level int
		want  time.Duration
	}{
		{config.SurvivalEasy, 1, ms(2500)}, {config.SurvivalEasy, 8, ms(1750)}, {config.SurvivalEasy, 15, ms(1000)},
		{config.SurvivalNormal, 1, ms(3000)}, {config.SurvivalNormal, 8, ms(2250)}, {config.SurvivalNormal, 15, ms(1500)},
		{config.SurvivalHard, 1, ms(5000)}, {config.SurvivalHard, 8, ms(3000)}, {config.SurvivalHard, 15, ms(1000)},
		{config.SurvivalEasy, 0, ms(2500)}, {config.SurvivalEasy, 99, ms(1000)},
		{config.SurvivalNone, 5, 0}, {"bogus", 5, 0},
	} {
		if got := SurvivalInterval(tc.tier, tc.level); got != tc.want {
			t.Errorf("SurvivalInterval(%q, %d) = %v, want %v", tc.tier, tc.level, got, tc.want)
		}
	}
	// Monotonic: the floor never slows down as the level rises.
	for _, tier := range config.SurvivalTiers() {
		for l := MinLevel; l < MaxLevel; l++ {
			if SurvivalInterval(tier, l+1) > SurvivalInterval(tier, l) {
				t.Errorf("%s: the interval grows from level %d to %d", tier, l, l+1)
			}
		}
	}
}

// Rows per raise: Easy one, Hard four, Normal a seeded draw of one to four
// that every peer repeats and that does vary from raise to raise.
func TestSurvivalRaiseRows(t *testing.T) {
	for raise := 1; raise <= 50; raise++ {
		if got := SurvivalRaiseRows(config.SurvivalEasy, 42, raise); got != 1 {
			t.Fatalf("easy raise %d brings %d rows", raise, got)
		}
		if got := SurvivalRaiseRows(config.SurvivalHard, 42, raise); got != 4 {
			t.Fatalf("hard raise %d brings %d rows", raise, got)
		}
	}
	if got := SurvivalRaiseRows(config.SurvivalNone, 42, 1); got != 0 {
		t.Errorf("no floor, but %d rows", got)
	}
	seen := map[int]bool{}
	for raise := 1; raise <= 50; raise++ {
		n := SurvivalRaiseRows(config.SurvivalNormal, 42, raise)
		if n < 1 || n > 4 {
			t.Fatalf("normal raise %d brings %d rows", raise, n)
		}
		if again := SurvivalRaiseRows(config.SurvivalNormal, 42, raise); again != n {
			t.Fatalf("normal raise %d: %d rows, then %d", raise, n, again)
		}
		seen[n] = true
	}
	if len(seen) < 3 {
		t.Errorf("fifty normal raises only ever brought %v rows", seen)
	}
	if SurvivalRaiseRows(config.SurvivalNormal, 42, 1) == SurvivalRaiseRows(config.SurvivalNormal, 43, 1) &&
		SurvivalRaiseRows(config.SurvivalNormal, 42, 2) == SurvivalRaiseRows(config.SurvivalNormal, 43, 2) &&
		SurvivalRaiseRows(config.SurvivalNormal, 42, 3) == SurvivalRaiseRows(config.SurvivalNormal, 43, 3) {
		t.Error("two seeds deal the same first three raises")
	}
}

// The holes: a well that moves every four rows — rows 0-3 share a draw, row
// 4 starts another — however the rows are chunked into raises; random holes
// give every row its own; the count is floored at one and capped below the
// width; and the same seed and ordinals always draw the same columns.
func TestSurvivalHoles(t *testing.T) {
	const width = 10
	whole := SurvivalHoles(width, 1, 0, 12, false, 7)
	if len(whole) != 12 {
		t.Fatalf("%d rows drawn, want 12", len(whole))
	}
	for n := 1; n < 12; n++ {
		same := n/SurvivalWellSpan == (n-1)/SurvivalWellSpan
		if got := reflect.DeepEqual(whole[n], whole[n-1]); got != same {
			t.Errorf("rows %d and %d: same well %v, want %v (%v / %v)", n-1, n, got, same, whole[n-1], whole[n])
		}
	}
	// Chunked as the raises would come (3 + 1 + 4 + 4 rows): the same draws.
	var chunked [][]int
	for _, r := range [][2]int{{0, 3}, {3, 1}, {4, 4}, {8, 4}} {
		chunked = append(chunked, SurvivalHoles(width, 1, r[0], r[1], false, 7)...)
	}
	if !reflect.DeepEqual(chunked, whole) {
		t.Errorf("chunked raises drew %v, one raise %v", chunked, whole)
	}
	// Some seed moves the well from row 3 to row 4 (they are independent
	// draws, so any seed where they differ proves the boundary).
	moved := false
	for seed := uint64(1); seed < 20; seed++ {
		h := SurvivalHoles(width, 1, 0, 5, false, seed)
		if !reflect.DeepEqual(h[3], h[4]) {
			moved = true
		}
		if !reflect.DeepEqual(h[0], h[3]) {
			t.Errorf("seed %d: rows 0 and 3 differ", seed)
		}
	}
	if !moved {
		t.Error("the well never moves at row 4")
	}
	// Random holes: every row its own draw.
	messy := SurvivalHoles(width, 1, 0, 8, true, 7)
	distinct := 0
	for n := 1; n < 8; n++ {
		if !reflect.DeepEqual(messy[n], messy[n-1]) {
			distinct++
		}
	}
	if distinct < 4 {
		t.Errorf("random holes drew only %d changes over 8 rows: %v", distinct, messy)
	}
	// The count: floored at one, capped at MaxGarbageHoles and below the width.
	if got := SurvivalHoles(width, 0, 0, 1, false, 7); len(got) != 1 || len(got[0]) != config.MinSurvivalHoles {
		t.Errorf("zero holes drew %v", got)
	}
	if got := SurvivalHoles(width, 9, 0, 1, false, 7); len(got[0]) != config.MaxGarbageHoles {
		t.Errorf("nine holes drew %v", got)
	}
	if got := SurvivalHoles(3, 4, 0, 1, false, 7); len(got[0]) != 2 {
		t.Errorf("four holes on a 3-wide board drew %v", got)
	}
	for _, row := range SurvivalHoles(width, 3, 0, 4, false, 7) {
		if len(row) != 3 || row[0] >= row[1] || row[1] >= row[2] || row[2] >= width {
			t.Errorf("three holes drew %v", row)
		}
	}
	if SurvivalHoles(width, 1, 0, 0, false, 7) != nil {
		t.Error("no rows, but holes")
	}
	// The raise lands them: the bottom rows of a shrink carry exactly the holes.
	pf := NewPlayfieldWithHeight(width, 24)
	holes := SurvivalHoles(width, 1, 0, 2, false, 7)
	rows, topped, full := pf.ProjectShrinkCascade(2, 0, holes)
	if topped != nil || full {
		t.Fatalf("an empty board topped %v / full %v", topped, full)
	}
	for k, row := range rows[22:] {
		for c, cell := range row.Cells {
			want := c != holes[k][0]
			if cell.Occupied != want || (want && !cell.Adversarial) {
				t.Errorf("raised row %d col %d: %+v", k, c, cell)
			}
		}
	}
}
