package nativeui

import (
	"fmt"
	"image"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
)

// The transactions panel: the NATS messages of a recording — or of the game a
// spectator is watching — shown around NOW, transaction by transaction. Where
// the player's panel (natslog.go) is a log that scrolls past, this one is a
// window onto the stream with the playhead in it: the transactions just
// applied stand above a NOW marker, the ones about to be applied under it,
// each transaction its messages bracketed in its own color the way the log
// draws a batch. In a replay NOW is the playhead, so scrubbing moves the
// window and playing runs the transactions up through it; on a spectator's
// screen NOW is the live edge of the stream, and the marker sits under the
// last transaction delivered with nothing under it yet.

// txnWindowN is how many transactions the window gathers on either side of
// NOW. The panel's height says how many of them are seen (txnWindow clips
// the rest); a taller panel shows more.
const txnWindowN = 5

// txnWindowLabel is the panel's semantic label — how the tests find it on
// screen.
const txnWindowLabel = "nats transactions"

// replayDeckKeepDp is the room the replay screen keeps under its messages
// panel over and above the panel's own keep (msgPanelKeepH): the deck and the
// key legend under the panel, so the handle can never drag the panel over
// them and the boards keep a sliver.
const replayDeckKeepDp = 130

// replayMsgSection is the replay screen's panel, under the boards and over
// the deck: the resize handle the game screen's panel has (msgPanelDivider,
// the same height setting), and the window around the playhead.
func (a *App) replayMsgSection(gtx C, rv *replayView) D {
	h := a.msgPanelHeightPx(gtx, gtx.Dp(msgPanelKeepH)+gtx.Dp(replayDeckKeepDp))
	before, after := rv.tl.txnsAround(rv.head, txnWindowN)
	rows := func(txns []replayTxn) [][]streamMsg {
		out := make([][]streamMsg, len(txns))
		for i, t := range txns {
			out[i] = rv.txnRows(t)
		}
		return out
	}
	now := "NOW · " + replayClockMs(rv.head)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.msgPanelDivider),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
			return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
				return a.txnWindow(gtx, rows(before), rows(after), now)
			})
		}),
	)
}

// txnRows is one transaction of the recording as the panel draws it: its
// messages as log rows, stamped with the recording's clock (m:ss.mmm from
// the start, the clock the status line and the NOW marker read) and, where
// the recording carries one, the wall-clock time they were recorded at;
// tinted by the transaction's place in the recording, bracketed when it is
// a batch.
func (rv *replayView) txnRows(t replayTxn) []streamMsg {
	tl := rv.tl
	rows := make([]streamMsg, 0, t.end-t.start)
	for i := t.start; i < t.end; i++ {
		m := tl.msgs[i]
		var ts time.Time
		if !tl.origin.IsZero() {
			ts = tl.origin.Add(m.off)
		}
		rows = append(rows, streamMsg{
			ts: ts, clock: replayClockMs(m.off),
			subject: m.subject, payload: m.payload,
			batch: t.batch, group: t.idx,
			batched: t.batch != "" || t.end-t.start > 1,
		})
	}
	return rows
}

// replayClockMs formats a point on the recording's clock as m:ss.mmm — the
// status line's clock (replayClock) with the milliseconds a transaction
// needs, since a batch and the next are often the same second.
func replayClockMs(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	s := ms / 1000
	if h := s / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", h, (s/60)%60, s%60, ms%1000)
	}
	return fmt.Sprintf("%d:%02d.%03d", s/60, s%60, ms%1000)
}

// natsTxnSection is the spectator's panel: the same strip, under the boards,
// as the player's (natsMsgSection) — the handle and the height setting are
// shared — showing the game's last transactions over a NOW marker at the
// live edge. A spectator's engine applies every message the moment it is
// delivered, so there is never anything under the marker: what is above it
// is the stream as the boards show it.
func (a *App) natsTxnSection(gtx C) D {
	h := a.msgPanelHeightPx(gtx, gtx.Dp(msgPanelKeepH))
	a.mu.Lock()
	before := liveTxns(a.msgLog, txnWindowN*2)
	a.mu.Unlock()
	return a.tutMark(gtx, tutNatsPanel, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.msgPanelDivider),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
				return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
					return a.txnWindow(gtx, before, nil, "NOW · LIVE")
				})
			}),
		)
	})
}

// liveTxns cuts the tail of the live message log into its last n
// transactions — runs of one group ordinal (msgGroup: a batch's id, or a
// single publish's own), the log's own grouping — oldest first.
func liveTxns(log []streamMsg, n int) [][]streamMsg {
	var out [][]streamMsg
	end := len(log)
	for end > 0 && len(out) < n {
		start := end - 1
		for start > 0 && log[start-1].group == log[end-1].group {
			start--
		}
		out = append(out, log[start:end])
		end = start
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// txnWindow draws the window: a bordered strip with the NOW marker across
// its middle — or along its foot when nothing comes after it — the
// transactions before NOW stacked up from the marker, the most recent
// touching it, and the ones after stacked down from it, the next first.
// Whatever runs past the strip's edges is clipped: the marker stays where it
// is and the rows nearest it are the ones seen, however many a transaction
// holds. With no transactions on either side the strip says so.
func (a *App) txnWindow(gtx C, before, after [][]streamMsg, now string) D {
	return bordered(gtx, func(gtx C) D {
		gtx.Constraints.Min = gtx.Constraints.Max // fill the strip even while empty
		sz := gtx.Constraints.Max
		defer clip.Rect(image.Rectangle{Max: sz}).Push(gtx.Ops).Pop()
		semantic.LabelOp(txnWindowLabel).Add(gtx.Ops)
		if len(before) == 0 && len(after) == 0 {
			return layout.Center.Layout(gtx, a.body("Waiting for stream messages…", colMuted))
		}
		inner := gtx
		inner.Constraints.Min = image.Point{}
		record := func(w layout.Widget) (op.CallOp, D) {
			m := op.Record(gtx.Ops)
			d := w(inner)
			return m.Stop(), d
		}
		place := func(call op.CallOp, y int) {
			t := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			t.Pop()
		}
		marker, md := record(func(gtx C) D { return a.txnNowMarker(gtx, now) })
		y := (sz.Y - md.Size.Y) / 2
		if len(after) == 0 {
			y = sz.Y - md.Size.Y - gtx.Dp(2)
		}
		top := y
		for i := len(before) - 1; i >= 0 && top > 0; i-- {
			call, d := record(a.txnBlock(before[i]))
			top -= d.Size.Y
			place(call, top)
		}
		place(marker, y)
		bottom := y + md.Size.Y
		for _, rows := range after {
			if bottom >= sz.Y {
				break
			}
			call, d := record(a.txnBlock(rows))
			place(call, bottom)
			bottom += d.Size.Y
		}
		return D{Size: sz}
	})
}

// txnBlock is one transaction's rows, one under the other, bracketed as the
// log brackets a batch (msgRow): the gap over its first row is what sets one
// transaction apart from the next.
func (a *App) txnBlock(rows []streamMsg) layout.Widget {
	return func(gtx C) D {
		kids := make([]layout.FlexChild, len(rows))
		for i, m := range rows {
			i, m := i, m
			kids[i] = layout.Rigid(func(gtx C) D {
				return a.msgRow(gtx, m, i == 0, i == len(rows)-1)
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
}

// txnNowMarker is the NOW line: a gold rule across the strip with the label
// — the playhead's clock, or LIVE — set into it in the pixel face, so where
// the stream stands is the one thing on the strip not in the log's type.
func (a *App) txnNowMarker(gtx C, label string) D {
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
		w := gtx.Constraints.Max.X
		lead := gtx.Dp(12)
		macro := op.Record(gtx.Ops)
		d := a.pixel(unit.Sp(8), label, colGold).Layout(gtx)
		call := macro.Stop()
		h := max(d.Size.Y, gtx.Dp(10))
		rule := max(gtx.Dp(2), 2)
		ry := h/2 - rule/2
		fillRect(gtx.Ops, image.Rect(0, ry, lead, ry+rule), colGold)
		fillRect(gtx.Ops, image.Rect(lead+d.Size.X+2*gtx.Dp(6), ry, w, ry+rule), colGold)
		t := op.Offset(image.Pt(lead+gtx.Dp(6), (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
		return D{Size: image.Pt(w, h)}
	})
}
