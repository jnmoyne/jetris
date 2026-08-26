package game

import "testing"

func TestAttackRows(t *testing.T) {
	for lines := 0; lines <= 4; lines++ {
		if got := AttackRows(lines, false); got != lines {
			t.Errorf("AttackRows(%d, plain) = %d, want %d", lines, got, lines)
		}
	}
	want := map[int]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4}
	for lines, rows := range want {
		if got := AttackRows(lines, true); got != rows {
			t.Errorf("AttackRows(%d, guideline) = %d, want %d", lines, got, rows)
		}
	}
	if got := AttackRows(-1, true); got != 0 {
		t.Errorf("negative lines should owe nothing, got %d", got)
	}
}
