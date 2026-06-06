package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

func lowestCellRow(p *game.Piece) int {
	low := -1
	for _, c := range p.Cells() {
		if c[0] > low {
			low = c[0]
		}
	}
	return low
}

// TestCoopLineClearKeepsOtherPlayersPiece reproduces the bug where a line clear by
// one player wiped the OTHER player's falling piece and respawned it for them. A
// clear shifts every piece DOWN; published top-to-bottom, the other player's piece
// is erased from its old rows before its new (shifted) rows arrive, so its
// active-cell count momentarily hits zero and that player's lock-in detector fires
// a spurious lock + respawn. Publishing the clear bottom-first keeps the shifted
// piece always overlapping itself, so only the clearing player gets a new piece.
//
// Seed 5 makes the first piece a horizontal I (1 row tall) for both players, so a
// single line clear is enough to vanish the other player's piece under the bug.
func TestCoopLineClearKeepsOtherPlayersPiece(t *testing.T) {
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
	gameID := "coop-lineclear-keep-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	a := New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0)
	b := New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 1)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	defer b.Stop()

	// Wait for both first pieces (seed 5 piece 0 = horizontal I; a at cols 3-6,
	// b at cols 13-16, both spawning in the headroom at row 3).
	waitUntil(t, 3*time.Second, func() bool {
		return a.Playfield().ActivePieceForPlayer(0) != nil &&
			b.Playfield().ActivePieceForPlayer(1) != nil
	}, "both first pieces to spawn")

	// Move b's I down out of the headroom into the visible area (the clear only
	// republishes visible rows, so a headroom piece wouldn't be shifted/exercised).
	// Keep it well above the bottom so it can't lock on its own during the test.
	waitUntil(t, 3*time.Second, func() bool {
		p := b.Playfield().ActivePieceForPlayer(1)
		if p == nil {
			return false
		}
		if lowestCellRow(p) >= 9 {
			return true
		}
		b.MoveDown()
		return false
	}, "b's piece to descend into the visible area")

	// b is still on its first piece (pieceIdx 0); a spurious respawn would bump it.
	idx0 := b.PieceIdx()

	pf := a.Playfield()
	width := pf.Width       // 20 (2 players * 10)
	bottom := pf.Height - 1 // 27

	// Pre-fill the bottom row everywhere except cols 3,4,5,6 so a's horizontal I
	// hard drop completes exactly that row (a single clear).
	cells := make([]game.Cell, width)
	gap := map[int]bool{3: true, 4: true, 5: true, 6: true}
	for c := 0; c < width; c++ {
		if !gap[c] {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}
		}
	}
	rd, _ := (game.Row{Cells: cells}).Marshal()
	if _, err := js.Publish(ctx, config.CoopRowSubject(gameID, bottom), rd); err != nil {
		t.Fatal(err)
	}

	// Wait for a's consumer to apply the pre-filled bottom row.
	waitUntil(t, 3*time.Second, func() bool {
		n := 0
		for _, c := range a.Playfield().Rows[bottom].Cells {
			if c.Occupied {
				n++
			}
		}
		return n == width-len(gap)
	}, "pre-filled bottom row to apply")

	if a.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("player 0 has no active piece before hard drop")
	}

	// a hard-drops its I, completing the bottom row (a single line clear).
	a.HardDrop()

	// The clear must register for a.
	waitUntil(t, 3*time.Second, func() bool {
		return a.score > 0 && len(game.CompletedRows(a.Playfield())) == 0
	}, "a's line clear to register")

	// THE ASSERTION: b keeps its piece (shifted down) and does NOT respawn. Allow a
	// beat for any erroneous respawn to surface before checking.
	time.Sleep(300 * time.Millisecond)
	if got := b.PieceIdx(); got != idx0 {
		t.Fatalf("player 1 respawned on the other player's line clear: pieceIdx %d -> %d (want unchanged)", idx0, got)
	}
	if b.Playfield().ActivePieceForPlayer(1) == nil {
		t.Fatal("player 1's active piece was lost after the other player's line clear")
	}
}
