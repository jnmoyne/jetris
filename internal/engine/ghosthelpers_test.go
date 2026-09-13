package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// The helpers of the stale-piece tests (coopcollapseghost_test.go,
// spawnblocked_test.go, peerstale_test.go, spawnhold_test.go,
// straysweep_test.go): a crew's game with an engine per seat, a seat's
// piece written to the stream by hand — a piece some client left there, as
// the engines under test see it arrive — and the stream's own account of a
// seat's cells.

// startCrewGame brings up an in-progress cooperative game of seats seats on
// a board extra columns wider per seat beyond the first, with an engine for
// each of the first engines seats, every built seat's first piece on every
// built engine's board.
func startCrewGame(t *testing.T, gameID string, seats, engines, extra int) (js jetstream.JetStream, es []*Engine) {
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
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: seats, ExtraColumns: extra,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < engines; i++ {
		es = append(es, New(js, gameID, fmt.Sprintf("p%d", i), "", config.ModeCooperative, ModePlayer, i, 0, 0))
	}
	for _, e := range es {
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
	}
	waitUntil(t, 5*time.Second, func() bool {
		for _, e := range es {
			pf := e.Playfield()
			for seat := range es {
				if pf.ActivePieceForPlayer(seat) == nil {
					return false
				}
			}
		}
		return true
	}, "every built seat's first piece on every board")
	return js, es
}

// publishSeatCells writes p's four cells onto the crew's board as seat's
// active piece, straight to the stream (no CAS).
func publishSeatCells(t *testing.T, js jetstream.JetStream, gameID string, p game.Piece, seat int) {
	t.Helper()
	publishSeatCellsAt(t, js, gameID, p, seat, p.Cells())
}

// publishSeatCellsAt is publishSeatCells for the given cells of p only — a
// piece in part, as a stale collapse once copied one.
func publishSeatCellsAt(t *testing.T, js jetstream.JetStream, gameID string, p game.Piece, seat int, cells [][2]int) {
	t.Helper()
	ctx := context.Background()
	for _, c := range cells {
		data, err := game.Cell{Active: true, PieceType: p.Type, Orientation: p.Orientation, AnchorRow: p.Row, AnchorCol: p.Col, PlayerIdx: seat}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, c[0], c[1]), data); err != nil {
			t.Fatal(err)
		}
	}
}

// vacateCellsDirect empties the given cells on the stream (no CAS).
func vacateCellsDirect(t *testing.T, js jetstream.JetStream, gameID string, cells [][2]int) {
	t.Helper()
	ctx := context.Background()
	for _, c := range cells {
		if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, c[0], c[1]), []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
}

// boardHasCells reports whether every given cell is an active cell of
// seat's on pf.
func boardHasCells(pf *game.Playfield, cells [][2]int, seat int) bool {
	for _, c := range cells {
		cell := pf.Rows[c[0]].Cells[c[1]]
		if !cell.Active || cell.PlayerIdx != seat {
			return false
		}
	}
	return true
}

// boardHasNoneOf reports whether none of the given cells is active on pf.
func boardHasNoneOf(pf *game.Playfield, cells [][2]int) bool {
	for _, c := range cells {
		if pf.Rows[c[0]].Cells[c[1]].Active {
			return false
		}
	}
	return true
}

// seatCellsOnStream returns seat's active cells as the stream's last
// message per cell has them, each with the anchor it carries.
func seatCellsOnStream(t *testing.T, js jetstream.JetStream, gameID string, pf *game.Playfield, seat int) map[game.CellPos]game.Piece {
	t.Helper()
	subjects := make([]string, 0, pf.Width*pf.Height)
	for r := 0; r < pf.Height; r++ {
		for c := 0; c < pf.Width; c++ {
			subjects = append(subjects, config.CoopCellSubject(gameID, r, c))
		}
	}
	msgs, err := natspkg.FetchPlayfieldState(context.Background(), js, gameID, subjects)
	if err != nil {
		t.Fatal(err)
	}
	out := map[game.CellPos]game.Piece{}
	for _, m := range msgs {
		c, err := game.UnmarshalCell(m.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if c.Active && c.PlayerIdx == seat {
			out[game.CellPos{Row: m.Row, Col: m.Col}] = game.Piece{Type: c.PieceType, Orientation: c.Orientation, Row: c.AnchorRow, Col: c.AnchorCol}
		}
	}
	return out
}

// anchorsOf groups a seat's cells by the anchor they carry: one anchor with
// four cells is a whole piece, and nothing else.
func anchorsOf(cells map[game.CellPos]game.Piece) map[game.Piece]int {
	out := map[game.Piece]int{}
	for _, p := range cells {
		out[p]++
	}
	return out
}

// keepSeatPieceMoving rewrites p's cells on the stream every interval, a
// column to the left and back, until ctx ends: a crewmate's live piece as
// the engines see one, with no engine playing it. New cells first, the old
// ones vacated after — the order the engines write a move in.
func keepSeatPieceMoving(ctx context.Context, js jetstream.JetStream, gameID string, p game.Piece, seat int, every time.Duration) {
	go func() {
		cur, left := p, true
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
			next := cur
			if left {
				next.Col--
			} else {
				next.Col++
			}
			left = !left
			newSet := map[[2]int]bool{}
			for _, c := range next.Cells() {
				newSet[c] = true
				data, _ := game.Cell{Active: true, PieceType: next.Type, Orientation: next.Orientation, AnchorRow: next.Row, AnchorCol: next.Col, PlayerIdx: seat}.Marshal()
				_, _ = js.Publish(ctx, config.CoopCellSubject(gameID, c[0], c[1]), data)
			}
			for _, c := range cur.Cells() {
				if !newSet[c] {
					_, _ = js.Publish(ctx, config.CoopCellSubject(gameID, c[0], c[1]), []byte("{}"))
				}
			}
			cur = next
		}
	}()
}

// collectFlashes drains e.Updates until ctx ends, keeping every CAS flash,
// and returns a reader of them.
func collectFlashes(ctx context.Context, e *Engine) func() []EngineUpdate {
	ch := make(chan EngineUpdate, 256)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-e.Updates:
				if u.Kind == UpdateCASFlash {
					select {
					case ch <- u:
					default:
					}
				}
			}
		}
	}()
	var got []EngineUpdate
	return func() []EngineUpdate {
		for {
			select {
			case u := <-ch:
				got = append(got, u)
			default:
				return append([]EngineUpdate(nil), got...)
			}
		}
	}
}
