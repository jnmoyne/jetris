package nativeui

// Opt-in visual verification for the victory fireworks: renders the overlay at
// several points in the show via a headless GPU window and writes PNGs for
// inspection. Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU), e.g.:
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestFireworksSnapshots

import (
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

func TestFireworksSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render fireworks snapshots")
	}
	w, err := headless.NewWindow(1200, 820)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	start := time.Unix(1_000_000, 0)
	fw := newFireworksShow(start)
	t.Logf("show cycle: %v", fw.cycle)

	for _, dt := range []time.Duration{
		700 * time.Millisecond,
		1600 * time.Millisecond,
		2500 * time.Millisecond,
		4 * time.Second,
		6 * time.Second,
		fw.cycle + 2500*time.Millisecond, // wrapped into the second cycle
	} {
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
			Now:         start.Add(dt),
		}
		paint.Fill(gtx.Ops, colBg)
		fireworksOverlay(gtx, fw)
		if err := w.Frame(&ops); err != nil {
			t.Fatalf("frame at +%v: %v", dt, err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1200, 820))
		if err := w.Screenshot(img); err != nil {
			t.Fatalf("screenshot at +%v: %v", dt, err)
		}
		f, err := os.Create(dir + "/fw_" + dt.String() + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
