package main

import "testing"

// benchBoard is a 14-wide crew's board with a ragged stack, the kind the
// planner sees mid-game.
func benchBoard() *grid {
	g := newGrid(24, 14)
	heights := []int{3, 4, 4, 2, 5, 3, 3, 6, 4, 2, 3, 5, 4, 3}
	for c, h := range heights {
		for r := 24 - h; r < 24; r++ {
			g.set(r, c, 1)
		}
	}
	return g
}

func benchPlan(b *testing.B, upcoming []int, proj *projection) {
	g := benchBoard()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		planPlacements(g, 2, spawnRow, 3, 3, upcoming, proj)
	}
}

func BenchmarkPlanNoLookahead(b *testing.B) { benchPlan(b, nil, nil) }
func BenchmarkPlanLookahead1(b *testing.B)  { benchPlan(b, []int{0}, nil) }
func BenchmarkPlanLookahead3(b *testing.B)  { benchPlan(b, []int{0, 1, 2}, nil) }
func BenchmarkPlanLookahead6(b *testing.B)  { benchPlan(b, []int{0, 1, 2, 3, 4, 5}, nil) }
func BenchmarkPlanLookahead6Projected(b *testing.B) {
	proj := &projection{claimed: map[cell]int{}, soft: map[cell]int{}, depETA: map[int]int{}, pieces: map[int]int{}, ownSpawnC: 3,
		boxes: []spawnBox{{seat: 1, c0: 7, c1: 10, imminent: true}}}
	benchPlan(b, []int{0, 1, 2, 3, 4, 5}, proj)
}
