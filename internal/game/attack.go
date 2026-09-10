package game

// AttackRows converts a plain clear of lines rows — no T-spin, no
// Back-to-Back, no perfect clear — into the garbage it owes the opponents:
// every cleared line sends one row, or under a game's guideline-garbage rule
// (GameMeta.GuidelineGarbage) the Guideline table's 0, 1, 2 or 4 rows for a
// single, double, triple or quad. The full rule, with the T-spin rows and
// the bonuses, is Clear.AttackRows.
func AttackRows(lines int, guideline bool) int {
	return Clear{Lines: lines}.AttackRows(guideline)
}
