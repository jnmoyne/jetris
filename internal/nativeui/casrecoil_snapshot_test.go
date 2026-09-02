package nativeui

// Opt-in visual verification of what a rejected write looks like: the piece
// vibrating where the CAS failure put it back, and the outline blinking where
// the lost step wanted it. One strip of frames through the effect, and the
// same board at rest for comparison. Skipped unless FW_SNAPSHOT_DIR is set
// (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestCASRecoilSnapshots

import (
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
)

func TestCASRecoilSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render CAS-recoil snapshots")
	}
	size := image.Pt(1360, 900)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	// The board of sampleBoard: a T piece the player tried to move LEFT, the
	// step lost. It stands where it stood, vibrating; the outline blinks one
	// column left, where it wanted to be.
	snap := sampleBoard()
	base := time.Now()
	want := map[[2]int]time.Time{}
	for _, rc := range (game.Piece{Type: game.PieceT, Row: 3, Col: 2}).Cells() {
		want[[2]int{rc[0], rc[1]}] = base
	}
	fx := func() *boardFX {
		return &boardFX{kick: activePieceCells(snap, 0), kickAt: base, want: want, frame: colFocus}
	}
	const cell = 24
	strip := func(gtx C, offsets []time.Duration) D {
		children := make([]layout.FlexChild, 0, len(offsets))
		for _, off := range offsets {
			at := base.Add(off)
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.UniformInset(6).Layout(gtx, func(gtx C) D {
					return drawBoard(gtx, snap, 0, cell, true, fx(), at)
				})
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	}

	// Top row: the recoil's first turns, 20 ms apart — about a frame each at
	// 60 Hz, so this is what the buzz actually looks like running. Bottom
	// row: the piece settled, the outline still blinking through the rainbow.
	early := []time.Duration{0, 20, 40, 60, 80}
	late := []time.Duration{120, 180, 260, 380, 520}
	ms := func(in []time.Duration) []time.Duration {
		out := make([]time.Duration, len(in))
		for i, v := range in {
			out[i] = v * time.Millisecond
		}
		return out
	}
	snapshotPNGSized(t, w, dir, "cas_recoil_strip", size, func(gtx C) {
		fillRect(gtx.Ops, image.Rect(0, 0, size.X, size.Y), colBg)
		layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return strip(gtx, ms(early)) }),
			layout.Rigid(func(gtx C) D { return strip(gtx, ms(late)) }),
		)
	})

	// The whole game screen mid-recoil, through the real layout path: the
	// pump's state (casKickAt, casWant) folded into the frame's gameView and
	// on into the board's fx, at a gtx.Now inside both windows.
	t.Run("screen", func(t *testing.T) {
		size := image.Pt(1200, 820)
		w, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Fatalf("headless window: %v", err)
		}
		defer w.Release()
		a := newTestApp()
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
		e := engine.New(nil, "g1", "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		e.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
		a.eng = e
		at := base.Add(20 * time.Millisecond)
		a.casKickAt = base
		a.casWant = map[[2]int]time.Time{}
		for _, rc := range (game.Piece{Type: game.PieceT, Row: 8, Col: 3}).Cells() {
			a.casWant[[2]int{rc[0], rc[1]}] = base
		}
		snapshotPNGSized(t, w, dir, "cas_recoil_screen", size, func(gtx C) {
			gtx.Now = at
			a.layout(gtx)
		})
	})

	// The same board with nothing rejected, at the strip's cell size.
	snapshotPNGSized(t, w, dir, "cas_recoil_rest", size, func(gtx C) {
		fillRect(gtx.Ops, image.Rect(0, 0, size.X, size.Y), colBg)
		layout.UniformInset(6).Layout(gtx, func(gtx C) D {
			return drawBoard(gtx, snap, 0, cell, true, &boardFX{frame: colFocus}, base)
		})
	})
}
