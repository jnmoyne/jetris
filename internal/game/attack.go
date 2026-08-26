package game

// AttackRows converts a clear of lines rows into the garbage it owes the
// opponents. By default every cleared line sends one row. Under a game's
// guideline-garbage rule (GameMeta.GuidelineGarbage) the attack follows the
// Tetris Guideline table instead: a single sends nothing, a double 1 row, a
// triple 2, a Tetris 4 — no tetromino clears more than four rows at once.
func AttackRows(lines int, guideline bool) int {
	if lines <= 0 {
		return 0
	}
	if !guideline {
		return lines
	}
	switch lines {
	case 1:
		return 0
	case 2:
		return 1
	case 3:
		return 2
	}
	return 4
}
