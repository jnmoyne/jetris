package nativeui

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// msgSectionFrame lays out the NATS message section through a real Gio input
// router (so the divider's drag gesture sees genuinely routed pointer events)
// and returns the height the message strip settled on.
func msgSectionFrame(a *App, r *input.Router, h int, queue ...pointer.Event) int {
	var ops op.Ops
	gtx := layout.Context{
		Ops:    &ops,
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Source: r.Source(),
		// A Rigid child of the game screen's Flex: free to be as short as it
		// likes, bounded by what the board and chat leave over.
		Constraints: layout.Constraints{Max: image.Pt(900, h)},
	}
	dims := a.natsMsgSection(gtx)
	r.Frame(&ops)
	for _, e := range queue {
		r.Queue(e)
	}
	return dims.Size.Y - gtx.Dp(unit.Dp(msgDividerH))
}

// TestMsgPanelDividerDrag proves the divider resizes the message strip: it
// starts at the window-reactive default, dragging up makes it taller by the
// dragged distance, and dragging far down stops at the floor instead of
// collapsing.
func TestMsgPanelDividerDrag(t *testing.T) {
	a := newTestApp()
	var r input.Router
	const winH = 600

	press := func(y float32) pointer.Event {
		return pointer.Event{
			Kind: pointer.Press, Source: pointer.Mouse,
			Buttons: pointer.ButtonPrimary, Position: f32.Pt(400, y),
		}
	}
	drag := func(y float32) pointer.Event {
		return pointer.Event{
			Kind: pointer.Move, Source: pointer.Mouse,
			Buttons: pointer.ButtonPrimary, Position: f32.Pt(400, y),
		}
	}
	release := func(y float32) pointer.Event {
		return pointer.Event{
			Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(400, y),
		}
	}

	// Default: the reactive height (20% of the window, floored at msgPanelHeight).
	base := msgSectionFrame(a, &r, winH, press(6))
	if want := max(msgPanelHeight, winH*20/100); base != want {
		t.Fatalf("default strip height = %d, want %d", base, want)
	}

	// Grab the divider at y=6 and pull it 100px up: the strip grows by 100.
	msgSectionFrame(a, &r, winH, drag(-94))
	if got := msgSectionFrame(a, &r, winH); got != base+100 {
		t.Errorf("after dragging up 100px: strip height = %d, want %d", got, base+100)
	}

	// Pull it far past the bottom of the window: it stops at the floor.
	msgSectionFrame(a, &r, winH, drag(2000), release(2000))
	if got := msgSectionFrame(a, &r, winH); got != msgPanelMinH {
		t.Errorf("after dragging down past the window: strip height = %d, want the %d floor", got, msgPanelMinH)
	}
}

// TestMsgGroupOrdinals checks the transaction grouping behind the row tints:
// every message of one atomic batch shares an ordinal (even when another
// consumer's message interleaves), successive batches get successive ordinals
// so neighbours never share a color, and un-batched publishes never join a
// transaction.
func TestMsgGroupOrdinals(t *testing.T) {
	a := newTestApp()
	a.msgShow = true
	rec := func(batchID string) {
		a.recordStreamMsg(time.Now(), "jetricks.game.g1.playfield.cell.1.1", []byte(`{}`), batchID)
	}
	rec("batch-A")
	rec("batch-A")
	rec("") // a meta publish interleaving between two cells of the same batch
	rec("batch-A")
	rec("batch-B")

	if len(a.msgLog) != 5 {
		t.Fatalf("recorded %d messages, want 5", len(a.msgLog))
	}
	g := func(i int) int { return a.msgLog[i].group }
	if g(0) != g(1) || g(0) != g(3) {
		t.Errorf("batch-A rows have ordinals %d/%d/%d, want one shared ordinal", g(0), g(1), g(3))
	}
	if g(2) == g(0) {
		t.Errorf("the interleaved single publish shares batch-A's ordinal %d", g(2))
	}
	if a.msgLog[2].batched {
		t.Error("a single-message publish is marked as part of a transaction")
	}
	if g(4) == g(0) {
		t.Error("batch-B reuses batch-A's ordinal; neighbouring transactions would share a tint")
	}
	if c0, c4 := g(0)%len(msgGroupPalette), g(4)%len(msgGroupPalette); c0 == c4 {
		t.Errorf("batch-A and batch-B both tint with palette slot %d", c0)
	}

	// The id itself is kept, so the row that opens a batch can name it.
	if a.msgLog[0].batch != "batch-A" {
		t.Errorf("row 0 kept batch id %q, want %q", a.msgLog[0].batch, "batch-A")
	}
	if got := shortBatchID("8f3a2c91d4"); got != "8f3a2c" {
		t.Errorf("shortBatchID trimmed to %q, want %q", got, "8f3a2c")
	}
	if got := shortBatchID("ab"); len(got) != msgBatchIDLen {
		t.Errorf("shortBatchID(%q) = %q (%d chars), want the gutter padded to %d so columns stay aligned",
			"ab", got, len(got), msgBatchIDLen)
	}

	// The id → ordinal map stays bounded as batches scroll by.
	for i := 0; i < msgGroupCap*2; i++ {
		rec(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(a.msgGroupOf) > msgGroupCap {
		t.Errorf("batch ordinal map grew to %d entries, cap is %d", len(a.msgGroupOf), msgGroupCap)
	}
}
