package game

import "testing"

// TestSpawnLowestCellRowConsistent guards against the bug where the I-piece
// spawned one row higher than every other piece, so it entered the visible area
// one gravity tick later and a player hard-dropping each piece on sight would
// drop the I before seeing it ("never get an I"). Every piece type must spawn
// with its lowest cell at the same playfield row.
func TestSpawnLowestCellRowConsistent(t *testing.T) {
	want := -1
	for pt := PieceI; pt <= PieceL; pt++ {
		p := SpawnPiece(pt, 10)
		lowest := -1
		for _, c := range p.Cells() {
			if c[0] > lowest {
				lowest = c[0]
			}
		}
		if want == -1 {
			want = lowest
		} else if lowest != want {
			t.Errorf("piece %d spawns with lowest cell at row %d, want %d (all pieces must align)", pt, lowest, want)
		}
	}
}
