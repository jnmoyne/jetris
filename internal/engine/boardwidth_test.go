package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// The create wizard's board-width setting (GameMeta.ExtraColumns) decides how
// wide a SHARED board is: the standard 10 columns for its first seat and
// ExtraColumns more for every seat after it, the seats' spawn points one such
// step apart. A meta without the field — every game created before the
// setting existed — keeps the historical board of a full 10-column section
// per seat, so old games and their replays reconstruct unchanged.
//
// These tests assert the geometry an engine actually builds from a meta, for
// both shared modes and both ends of the setting.
func TestSharedBoardWidthFromMeta(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mode          config.GameMode
		players       int
		teamSize      int
		extraCols     int
		wantWidth     int
		wantSpawnCols []int // one per seat, the piece's own Col at spawn
	}{
		{"coop 2 at the default 4", config.ModeCooperative, 2, 0, 4, 14, []int{3, 7}},
		{"coop 3 at the default 4", config.ModeCooperative, 3, 0, 4, 18, []int{3, 7, 11}},
		{"coop 2 at the maximum 10", config.ModeCooperative, 2, 0, 10, 20, []int{3, 13}},
		{"coop 2 with no setting", config.ModeCooperative, 2, 0, 0, 20, []int{3, 13}},
		{"teams 2v2 at the default 4", config.ModeTeams, 4, 2, 4, 14, []int{3, 7}},
		{"teams 3v3 at the default 4", config.ModeTeams, 6, 3, 4, 18, []int{3, 7, 11}},
		{"teams 2v2 with no setting", config.ModeTeams, 4, 2, 0, 20, []int{3, 13}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, _ := testutil.StartServer(t)
			nc, err := nats.Connect(url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(nc.Close)
			js, err := jetstream.New(nc)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			gameID := "board-width-test-game"
			if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
				t.Fatal(err)
			}
			meta := config.GameMeta{
				GameID: gameID, Mode: tc.mode, PlayerCount: tc.players, TeamSize: tc.teamSize,
				ExtraColumns: tc.extraCols,
				Seed:         5, Status: config.GameStatusInProgress,
				CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
			}
			data, _ := json.Marshal(meta)
			if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
				t.Fatal(err)
			}

			// One engine per seat of ONE board: coop seats are laid out by
			// playerIdx, a team's by the slot within the team.
			engines := make([]*Engine, len(tc.wantSpawnCols))
			for i := range engines {
				idx, team, slot := i, 0, 0
				if tc.mode == config.ModeTeams {
					slot = i
				}
				e := New(js, gameID, "p"+string(rune('0'+i)), "", tc.mode, ModePlayer, idx, team, slot)
				if err := e.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(e.Stop)
				engines[i] = e
			}

			for i, e := range engines {
				if got := e.Playfield().Width; got != tc.wantWidth {
					t.Errorf("seat %d board width = %d, want %d", i, got, tc.wantWidth)
				}
			}

			// Every seat spawns in its own section, one extra-columns step
			// from its neighbour — and wholly on the board.
			for i, e := range engines {
				e := e
				i := i
				waitUntil(t, 5*time.Second, func() bool {
					return e.Playfield().ActivePieceForPlayer(e.PlayerIdx()) != nil
				}, "seat's first piece to spawn")
				p := e.Playfield().ActivePieceForPlayer(e.PlayerIdx())
				if p.Col != tc.wantSpawnCols[i] {
					t.Errorf("seat %d spawned at col %d, want %d", i, p.Col, tc.wantSpawnCols[i])
				}
				for _, c := range p.Cells() {
					if c[1] < 0 || c[1] >= tc.wantWidth {
						t.Errorf("seat %d spawn cell at col %d is off a %d-wide board", i, c[1], tc.wantWidth)
					}
				}
			}
		})
	}
}
