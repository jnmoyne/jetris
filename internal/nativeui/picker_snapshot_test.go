package nativeui

// Opt-in visual verification for the login screen's context pull-down:
// renders the picker closed and open via a headless GPU window and writes
// PNGs for inspection. Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestPickerSnapshots

import (
	"image"
	"image/png"
	"os"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
)

func TestPickerSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render picker snapshots")
	}
	w, err := headless.NewWindow(1200, 820)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	a := NewWithPicker(config.Config{}, []string{"alpha", "beta", "demo", "prod-cluster"}, "beta")
	a.th = newTestApp().th

	for _, st := range []struct {
		name     string
		open     bool
		embedded bool
	}{{"closed", false, false}, {"open", true, false}, {"embedded", false, true}} {
		a.connDropOpen = st.open
		if st.embedded {
			// LAN mode selected: the shareable "Your server's URL is" line
			// appears under the port row.
			a.connEnum.Value = "embedded"
		} else {
			a.connEnum.Value = "context"
		}
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
		}
		a.layout(gtx)
		if err := w.Frame(&ops); err != nil {
			t.Fatalf("frame %s: %v", st.name, err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1200, 820))
		if err := w.Screenshot(img); err != nil {
			t.Fatalf("screenshot %s: %v", st.name, err)
		}
		f, err := os.Create(dir + "/picker_" + st.name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
