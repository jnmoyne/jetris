package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// setupTeamsGame starts an embedded server, creates an in-progress 2v2 teams
// game (seed 42 → first piece is a T for everyone), and returns the four
// engines: p0/p1 on team 0 (slots 0/1), p2/p3 on team 1 (slots 0/1).
func setupTeamsGame(t *testing.T) (js jetstream.JetStream, gameID string, engines [4]*Engine) {
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
	gameID = "teams-test-game"
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

	specs := []struct {
		id              string
		idx, team, slot int
	}{
		{"p0", 0, 0, 0},
		{"p1", 1, 0, 1},
		{"p2", 2, 1, 0},
		{"p3", 3, 1, 1},
	}
	for i, s := range specs {
		e := New(js, gameID, s.id, "", config.ModeTeams, ModePlayer, s.idx, s.team, s.slot)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		engines[i] = e
	}

	// Wait for every player's first piece on their team board.
	waitUntil(t, 5*time.Second, func() bool {
		for i, s := range specs {
			if engines[i].Playfield().ActivePieceForPlayer(s.idx) == nil {
				return false
			}
		}
		return true
	}, "all four first pieces to spawn")
	return js, gameID, engines
}

// publishTeamRowCells pre-fills one row of a team board by publishing one
// message per non-empty cell.
func publishTeamRowCells(t *testing.T, js jetstream.JetStream, gameID string, team, row int, cells []game.Cell) {
	t.Helper()
	ctx := context.Background()
	for col, c := range cells {
		if c == (game.Cell{}) {
			continue
		}
		data, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.Publish(ctx, config.TeamCellSubject(gameID, team, row, col), data); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTeamsGarbageHitsOpposingBoardExactlyOnce: a team-0 line clear must add
// exactly RowsRemoved adversarial rows to the OPPOSING team's shared board —
// not zero (the event was dropped) and not double (both alive receivers
// applied their own shift) — and must leave the clearing team's board alone.
// Teammates of the clearer fold the score without re-publishing anything.
func TestTeamsGarbageHitsOpposingBoardExactlyOnce(t *testing.T) {
	js, gameID, engines := setupTeamsGame(t)
	p0, p1, p2, p3 := engines[0], engines[1], engines[2], engines[3]

	// Pre-fill team 0's bottom row except p0's T columns (3,4,5) so p0's hard
	// drop completes exactly that row. Width 20: this also fills under p1's
	// section, but p1's piece is still high in the headroom.
	pf := p0.Playfield()
	bottom := pf.Height - 1
	gap := map[int]bool{3: true, 4: true, 5: true}
	cells := make([]game.Cell, pf.Width)
	for c := 0; c < pf.Width; c++ {
		if !gap[c] {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}
		}
	}
	publishTeamRowCells(t, js, gameID, 0, bottom, cells)
	waitUntil(t, 3*time.Second, func() bool {
		n := 0
		for _, c := range p0.Playfield().Rows[bottom].Cells {
			if c.Occupied {
				n++
			}
		}
		return n == pf.Width-len(gap)
	}, "pre-filled bottom row to apply")

	p0.HardDrop()

	// The clear scores teamSize × lines = 2 for the clearer, and the teammate
	// folds the same delta off the line-clear event.
	waitUntil(t, 3*time.Second, func() bool { return p0.Score() == 2 }, "p0's clear to score")
	waitUntil(t, 3*time.Second, func() bool { return p1.Score() == 2 }, "p1 to fold the team score")

	// EVERY engine — the clearer, their teammate, AND the opposing team's
	// players — converges on the per-team scoreboard (the opposing team folds
	// it off the line-clear event even though their own Score() is untouched).
	waitUntil(t, 3*time.Second, func() bool {
		want := [config.TeamCount]int{2, 0}
		for _, e := range engines {
			if e.TeamScores() != want {
				return false
			}
		}
		return true
	}, "all four engines to converge on team scores A=2 B=0")
	if got := p2.Score(); got != 0 {
		t.Fatalf("p2 (opposing team) own score = %d, want 0", got)
	}

	// Both alive members of team 1 raced to apply the shrink; the deficit
	// guard must leave exactly ONE adversarial row on their shared board.
	waitUntil(t, 3*time.Second, func() bool {
		return p2.Playfield().AdversarialRowCount() == 1 && p3.Playfield().AdversarialRowCount() == 1
	}, "team 1's board to gain one garbage row")

	// Give a racing double-apply time to surface, then re-assert.
	time.Sleep(500 * time.Millisecond)
	if got := p2.Playfield().AdversarialRowCount(); got != 1 {
		t.Fatalf("team 1 board has %d adversarial rows, want exactly 1 (double-applied shrink?)", got)
	}

	// The clearing team's own board must NOT receive garbage.
	if got := p0.Playfield().AdversarialRowCount(); got != 0 {
		t.Fatalf("team 0 board has %d adversarial rows, want 0", got)
	}

	// Team 1's players keep their falling pieces (no spurious lock-in from the
	// shift batch — it never touches active piece cells).
	if p2.Playfield().ActivePieceForPlayer(2) == nil {
		t.Fatal("p2 lost its active piece during the shrink")
	}
	if p3.Playfield().ActivePieceForPlayer(3) == nil {
		t.Fatal("p3 lost its active piece during the shrink")
	}
}

// topOutTeamPlayer forces one team-1 player to top out: walk their piece out
// of the headroom, wall off their spawn area, then hard-drop so the next
// spawn has nowhere to go.
func topOutTeamPlayer(t *testing.T, js jetstream.JetStream, gameID string, e *Engine, playerIdx, colOffset int) {
	t.Helper()
	waitUntil(t, 5*time.Second, func() bool {
		p := e.Playfield().ActivePieceForPlayer(playerIdx)
		if p == nil {
			return false
		}
		if lowestCellRow(p) >= 8 {
			return true
		}
		e.MoveDown()
		return false
	}, "piece to descend below the spawn area")

	// Wall off the player's spawn rows (anchor row 2, lowest cells row 3)
	// across their whole 10-column section.
	for _, row := range []int{2, 3} {
		cells := make([]game.Cell, e.Playfield().Width)
		for c := colOffset; c < colOffset+10; c++ {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: playerIdx}
		}
		publishTeamRowCells(t, js, gameID, 1, row, cells)
	}
	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().Rows[3].Cells[colOffset+3].Occupied
	}, "spawn wall to apply")

	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return e.Mode() == ModeGameOver }, "player to top out")
}

// TestTeamsEliminationTeamPlaysOnThenTeamWin drives the full teams lifecycle:
// one team-1 player tops out → their team plays on (game stays in progress,
// the teammate keeps playing); the second team-1 player tops out → team 0
// wins (every member gets Won=true, including via the meta transition to
// finished).
func TestTeamsEliminationTeamPlaysOnThenTeamWin(t *testing.T) {
	js, gameID, engines := setupTeamsGame(t)
	p0, p1, p2, p3 := engines[0], engines[1], engines[2], engines[3]
	ctx := context.Background()

	// Record game-over updates from team 0's engines (the lossy Updates
	// channel must be drained continuously, as the UI pump would).
	var mu sync.Mutex
	wonByID := map[string]bool{}
	for _, e := range []*Engine{p0, p1} {
		e := e
		go func() {
			for {
				select {
				case u := <-e.Updates:
					if u.Kind == UpdateGameOver {
						mu.Lock()
						wonByID[e.PlayerID()] = u.Won
						mu.Unlock()
					}
				case <-time.After(30 * time.Second):
					return
				}
			}
		}()
	}

	// First elimination: p2 (team 1, slot 0, columns 0-9).
	topOutTeamPlayer(t, js, gameID, p2, 2, 0)

	// The team plays on: the game must still be in progress, p3 still playing.
	waitUntil(t, 3*time.Second, func() bool { return p0.IsEliminated("p2") }, "p0 to record p2's elimination")
	meta, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != config.GameStatusInProgress {
		t.Fatalf("game status after first elimination = %s, want in_progress (team plays on)", meta.Status)
	}
	if p3.Mode() != ModePlayer {
		t.Fatal("p3 stopped playing after their teammate topped out")
	}
	if p0.Mode() != ModePlayer || p1.Mode() != ModePlayer {
		t.Fatal("team 0 stopped playing after an opposing elimination")
	}
	// p2's piece was vacated from the shared board (seen from p3's replica).
	waitUntil(t, 3*time.Second, func() bool {
		return p3.Playfield().ActivePieceForPlayer(2) == nil
	}, "p2's piece to be vacated from the team board")

	// Second elimination: p3 (team 1, slot 1, columns 10-19) → team 1 is out.
	topOutTeamPlayer(t, js, gameID, p3, 3, 10)

	// Team 0 wins: both members flip to game over with Won=true.
	waitUntil(t, 5*time.Second, func() bool {
		return p0.Mode() == ModeGameOver && p1.Mode() == ModeGameOver
	}, "team 0 to receive the win")
	waitUntil(t, 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return wonByID["p0"] && wonByID["p1"]
	}, "team 0's engines to emit Won=true")

	// The game meta transitions to finished (CAS-deduped across the winners).
	waitUntil(t, 5*time.Second, func() bool {
		m, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
		return err == nil && (m.Status == config.GameStatusFinished || m.Status == config.GameStatusArchived)
	}, "game meta to transition to finished")
}
