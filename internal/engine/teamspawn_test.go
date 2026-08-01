package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// These tests cover the shared-board spawn rule: a spawn whose cells are
// covered ONLY by another player's ACTIVE (falling) piece must be DEFERRED
// (retried on the gravity tick), not treated as a top-out — the same
// distinction gravity and hard drop already make (attemptMoveCoop). Spawn
// cells held by LOCKED cells remain a genuine top-out. The deferral lives in
// the single sharedBoard() branch of spawnPiece that cooperative and teams
// modes share, so a teams test covers both; coop end-to-end coverage stays
// with TestCoopConcurrentPlayNoPieceCorruption and the agent suite.

// setupPartialTeamsGame creates an in-progress 2v2 teams game but starts
// engines only for p1 (team 0, slot 1 — the subject) and p2 (team 1 — the
// elimination witness). p0's engine never starts, so cells fabricated in its
// name (playerIdx 0) sit wherever the test puts them — a deterministic
// stand-in for "a teammate's falling piece crossing the spawn area" with no
// gravity race.
func setupPartialTeamsGame(t *testing.T) (js jetstream.JetStream, gameID string, p1, p2 *Engine) {
	t.Helper()
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err = jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gameID = "teams-spawn-test-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeTeams, PlayerCount: 4, TeamSize: 2,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	p1 = New(js, gameID, "p1", "", config.ModeTeams, ModePlayer, 1, 0, 1)
	if err := p1.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p1.Stop)
	p2 = New(js, gameID, "p2", "", config.ModeTeams, ModePlayer, 2, 1, 0)
	if err := p2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p2.Stop)

	waitUntil(t, 5*time.Second, func() bool {
		return p1.Playfield().ActivePieceForPlayer(1) != nil &&
			p2.Playfield().ActivePieceForPlayer(2) != nil
	}, "p1 and p2 first pieces to spawn")
	return js, gameID, p1, p2
}

// publishTeamAreaCells overwrites every cell of the given rows/column range
// on a team board with the same payload — INCLUDING empty cells (unlike
// publishTeamRowCells, which skips them), so it can also vacate an area.
func publishTeamAreaCells(t *testing.T, js jetstream.JetStream, gameID string, team int, rows []int, colFrom, colTo int, cell game.Cell) {
	t.Helper()
	ctx := context.Background()
	data, err := cell.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for col := colFrom; col < colTo; col++ {
			if _, err := js.Publish(ctx, config.TeamCellSubject(gameID, team, row, col), data); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// blockP1Spawn walks p1's piece below the spawn area, covers p1's whole
// section's spawn rows with cells fabricated as playerIdx-0 material of the
// given kind, and hard-drops p1 so its next spawn hits the blocker.
func blockP1Spawn(t *testing.T, js jetstream.JetStream, gameID string, p1 *Engine, blocker game.Cell) {
	t.Helper()
	waitUntil(t, 5*time.Second, func() bool {
		p := p1.Playfield().ActivePieceForPlayer(1)
		if p == nil {
			return false
		}
		if lowestCellRow(p) >= 8 {
			return true
		}
		p1.MoveDown()
		return false
	}, "p1's piece to descend below the spawn area")

	// Cover rows 2-3 (anchor row 2; every spawn orientation's lowest cell is
	// row 3) across p1's whole section (slot 1 → columns 10-19).
	publishTeamAreaCells(t, js, gameID, 0, []int{2, 3}, 10, 20, blocker)
	waitUntil(t, 3*time.Second, func() bool {
		c := p1.Playfield().Rows[3].Cells[13]
		return c == blocker
	}, "the spawn blocker to apply on p1's replica")

	p1.HardDrop()
}

// A spawn blocked only by another player's ACTIVE piece must defer — the
// player stays in the game, keeps their piece index, and spawns as soon as
// the blocker moves away — instead of being spuriously eliminated (the "only
// one piece per team board" bug).
func TestTeamsSpawnBlockedByTeammatePieceDefersNotEliminates(t *testing.T) {
	js, gameID, p1, p2 := setupPartialTeamsGame(t)

	fakeActive := game.Cell{
		Active: true, PieceType: game.PieceT, Orientation: 0,
		AnchorRow: 2, AnchorCol: 13, PlayerIdx: 0,
	}
	blockP1Spawn(t, js, gameID, p1, fakeActive)

	// The first piece locked (pieceIdx advanced) and the next spawn is
	// genuinely deferred: for ~2.5 gravity ticks p1 must stay a player with
	// no active piece and no elimination anywhere.
	waitUntil(t, 3*time.Second, func() bool { return p1.PieceIdx() == 1 }, "p1's first lock-in")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := p1.Mode(); got != ModePlayer {
			t.Fatalf("p1 mode = %v while spawn blocked by an active piece; want ModePlayer (spurious top-out)", got)
		}
		if p1.Playfield().ActivePieceForPlayer(1) != nil {
			t.Fatal("p1 has an active piece while the spawn area is covered by a teammate's piece")
		}
		if p2.IsEliminated("p1") {
			t.Fatal("p2 recorded p1 as eliminated during a deferred spawn")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The blocking piece "falls away": vacate its cells. The gravity-tick
	// retry must spawn p1's next piece — exactly one.
	publishTeamAreaCells(t, js, gameID, 0, []int{2, 3}, 10, 20, game.Cell{})
	waitUntil(t, 3*time.Second, func() bool {
		return p1.Playfield().ActivePieceForPlayer(1) != nil
	}, "the deferred spawn to complete after the blocker cleared")

	active := 0
	for _, row := range p1.Playfield().Rows {
		for _, c := range row.Cells {
			if c.Active && c.PlayerIdx == 1 {
				active++
			}
		}
	}
	if active != 4 {
		t.Fatalf("p1 has %d active cells after the deferred spawn, want exactly 4 (double spawn?)", active)
	}
	if got := p1.PieceIdx(); got != 1 {
		t.Fatalf("p1 pieceIdx = %d after the deferred spawn, want 1 (deferral must not consume pieces)", got)
	}
	if p1.Mode() != ModePlayer {
		t.Fatal("p1 is not playing after the deferred spawn completed")
	}
}

// A deferred spawn whose cells become LOCKED material (the blocker locked in
// place, or garbage rose into the spawn rows) must still be a genuine
// top-out on the next retry.
func TestTeamsSpawnPendingThenLockedCellsTopsOut(t *testing.T) {
	js, gameID, p1, p2 := setupPartialTeamsGame(t)

	fakeActive := game.Cell{
		Active: true, PieceType: game.PieceT, Orientation: 0,
		AnchorRow: 2, AnchorCol: 13, PlayerIdx: 0,
	}
	blockP1Spawn(t, js, gameID, p1, fakeActive)
	waitUntil(t, 3*time.Second, func() bool { return p1.PieceIdx() == 1 }, "p1's first lock-in")

	// Give the deferral a beat to engage, then turn the blocker into locked
	// stack — as if the teammate's piece locked right there.
	time.Sleep(500 * time.Millisecond)
	if got := p1.Mode(); got != ModePlayer {
		t.Fatalf("p1 mode = %v before the blocker locked; want ModePlayer", got)
	}
	locked := game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}
	publishTeamAreaCells(t, js, gameID, 0, []int{2, 3}, 10, 20, locked)

	waitUntil(t, 5*time.Second, func() bool { return p1.Mode() == ModeGameOver }, "p1 to top out on locked spawn cells")
	waitUntil(t, 3*time.Second, func() bool { return p2.IsEliminated("p1") }, "p2 to record p1's elimination")
}
