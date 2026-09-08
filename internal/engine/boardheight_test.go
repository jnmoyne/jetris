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

// The create wizard's extra-rows setting (GameMeta.ExtraRows) decides how
// tall a SHARED board is: the standard 24 rows (4 of headroom over the 20
// visible) for its first seat and ExtraRows more for every seat after it.
// The board grows downwards — the headroom, and so the spawn row, stays at
// the top — and a competitive board, one seat's, ignores the setting. A meta
// without the field (every game created before it) keeps the standard board.
func TestSharedBoardHeightFromMeta(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       config.GameMode
		players    int
		teamSize   int
		extraRows  int
		wantHeight int
	}{
		{"coop 3 at 5 extra rows", config.ModeCooperative, 3, 0, 5, 34},
		{"coop 2 with no setting", config.ModeCooperative, 2, 0, 0, config.TotalRows},
		{"teams 2v2 at the maximum 10", config.ModeTeams, 4, 2, 10, 34},
		{"competitive ignores the setting", config.ModeCompetitive, 2, 0, 10, config.TotalRows},
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
			gameID := "board-height-test-game"
			if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
				t.Fatal(err)
			}
			meta := config.GameMeta{
				GameID: gameID, Mode: tc.mode, PlayerCount: tc.players, TeamSize: tc.teamSize,
				ExtraColumns: 4, ExtraRows: tc.extraRows,
				Seed: 5, Status: config.GameStatusInProgress,
				CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
			}
			data, _ := json.Marshal(meta)
			if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
				t.Fatal(err)
			}

			e := New(js, gameID, "p0", "", tc.mode, ModePlayer, 0, 0, 0)
			if err := e.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(e.Stop)

			if got := e.Playfield().Height; got != tc.wantHeight {
				t.Errorf("board height = %d, want %d", got, tc.wantHeight)
			}
			if got := e.PlayfieldHeight(); got != tc.wantHeight {
				t.Errorf("PlayfieldHeight() = %d, want %d", got, tc.wantHeight)
			}
			if got := e.VisibleRowStart(); got != config.VisibleRowStart {
				t.Errorf("visible rows start at %d, want the headroom's %d", got, config.VisibleRowStart)
			}
			if got, want := e.ExtraRows(), meta.ExtraRows; tc.mode != config.ModeCompetitive && got != want {
				t.Errorf("ExtraRows() = %d, want %d", got, want)
			}

			// The spawn row is the headroom's whatever the height.
			waitUntil(t, 5*time.Second, func() bool {
				return e.Playfield().ActivePieceForPlayer(e.PlayerIdx()) != nil
			}, "the first piece to spawn")
			p := e.Playfield().ActivePieceForPlayer(e.PlayerIdx())
			for _, c := range p.Cells() {
				if c[0] < 0 || c[0] >= config.HeadroomRows+1 {
					t.Errorf("spawn cell at row %d is outside the headroom of a %d-row board", c[0], tc.wantHeight)
				}
			}
			// The grown rows are real rows of the board.
			if len(e.Playfield().Rows) != tc.wantHeight {
				t.Fatalf("board has %d rows, want %d", len(e.Playfield().Rows), tc.wantHeight)
			}
		})
	}
}
