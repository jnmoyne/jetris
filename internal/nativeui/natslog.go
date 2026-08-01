package nativeui

import (
	"image"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// streamMsg is one game-stream message captured for the "Show NATS messages"
// panel: the JetStream stream timestamp (from the message metadata, not the
// local receive time), the subject, the raw JSON payload, and the transaction
// it belongs to — batch is the atomic batch's Nats-Batch-Id ("" for a plain
// single-message publish) and group the ordinal assigned to that id, which
// picks the row tint.
type streamMsg struct {
	ts      time.Time
	subject string
	payload string
	batch   string
	group   int
	batched bool
}

// msgLogCap bounds the in-memory message log; the panel shows the tail, and
// the list scrolls back through the rest. It is deep enough to hold several
// minutes of play (a busy board publishes a few hundred cell messages a
// minute) — the rows are small structs and Gio only lays out the visible ones,
// so the depth costs memory, not frame time.
const msgLogCap = 5000

// msgGroupCap bounds the batch-id → ordinal map. A batch's cells arrive
// back-to-back, but messages from the engine's other consumers can interleave,
// so a handful of ids must stay resolvable to keep a split batch one color.
const msgGroupCap = 64

// Message-strip geometry: msgPanelHeight is the default minimum height (the
// strip also grows with the window, see natsMsgPanel), msgPanelMinH the floor
// the resize handle may drag it to, msgPanelKeepH the space always left above
// it for the board and chat, and msgDividerH the thickness of the handle.
const (
	msgPanelHeight = 170
	msgPanelMinH   = 56
	msgPanelKeepH  = 120
	msgDividerH    = 12
)

// JSON syntax colors for the panel payloads (keys NATS blue, strings NATS
// green, numbers gold, true/false/null orange, punctuation muted).
var (
	colJSONKey = colAccent
	colJSONStr = colNATSGreen
	colJSONNum = colGold
	colJSONLit = colOrange
)

// msgGroupPalette tints the rows of one atomic batch (transaction). Successive
// transactions take successive entries, so neighbouring batches never share a
// color and a multi-cell move reads as one block.
var msgGroupPalette = []colorN{
	colAccent,
	colNATSGreen,
	colGold,
	{R: 0xc0, G: 0x7f, B: 0xff, A: 0xff}, // violet
	{R: 0xff, G: 0x7f, B: 0xa8, A: 0xff}, // pink
	{R: 0x4f, G: 0xd8, B: 0xd8, A: 0xff}, // teal
}

// recordStreamMsg is installed as engine.OnStreamMsg and runs on the engine's
// consumer goroutines. Collection is gated on the checkbox mirror so an
// unchecked panel costs one flag check per message.
func (a *App) recordStreamMsg(ts time.Time, subject string, payload []byte, batchID string) {
	a.mu.Lock()
	if !a.msgShow {
		a.mu.Unlock()
		return
	}
	a.msgLog = append(a.msgLog, streamMsg{
		ts:      ts,
		subject: subject,
		payload: string(payload),
		batch:   batchID,
		group:   a.msgGroup(batchID),
		batched: batchID != "",
	})
	if len(a.msgLog) > msgLogCap {
		a.msgLog = a.msgLog[len(a.msgLog)-msgLogCap:]
	}
	a.mu.Unlock()
	a.invalidate()
}

// msgGroup returns the tint ordinal for a batch id, assigning a fresh one the
// first time that batch is seen. Non-batched messages get their own throwaway
// ordinal so they never share a run with a neighbouring transaction. Called
// with a.mu held.
func (a *App) msgGroup(batchID string) int {
	if batchID == "" {
		a.msgGroupSeq++
		return a.msgGroupSeq
	}
	if g, ok := a.msgGroupOf[batchID]; ok {
		return g
	}
	a.msgGroupSeq++
	a.msgGroupOf[batchID] = a.msgGroupSeq
	a.msgGroupSeen = append(a.msgGroupSeen, batchID)
	if len(a.msgGroupSeen) > msgGroupCap {
		delete(a.msgGroupOf, a.msgGroupSeen[0])
		a.msgGroupSeen = a.msgGroupSeen[1:]
	}
	return a.msgGroupSeq
}

// resetMsgGroups clears the transaction bookkeeping alongside msgLog, so a new
// game starts its tints from the top. Called with a.mu held.
func (a *App) resetMsgGroups() {
	a.msgGroupOf = map[string]int{}
	a.msgGroupSeen = nil
	a.msgGroupSeq = 0
}

// natsMsgSection is the bottom message strip plus the divider that resizes it.
func (a *App) natsMsgSection(gtx C) D {
	h := a.msgPanelHeightPx(gtx)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.msgPanelDivider),
		layout.Rigid(func(gtx C) D { return a.natsMsgPanel(gtx, h) }),
	)
}

// msgPanelHeightPx resolves this frame's strip height and folds in any drag on
// the divider. Until the handle is first dragged the strip is height-reactive:
// at least msgPanelHeight dp, growing with the window (20% of the available
// height) so a taller window shows more messages. Once dragged, msgPanelDp
// pins it — still clamped to the window, so shrinking the window never buries
// the board and re-growing it restores the chosen height.
func (a *App) msgPanelHeightPx(gtx C) int {
	avail := gtx.Constraints.Max.Y
	lo := gtx.Dp(unit.Dp(msgPanelMinH))
	hi := max(lo, avail-gtx.Dp(unit.Dp(msgPanelKeepH)))

	h := max(gtx.Dp(unit.Dp(msgPanelHeight)), avail*20/100)
	if a.msgPanelDp > 0 {
		h = gtx.Dp(unit.Dp(a.msgPanelDp))
	}
	h = min(max(h, lo), hi)

	// Only the last drag event of the frame matters: every event's position is
	// relative to the divider as it was laid out LAST frame, so applying more
	// than one would count the same movement twice.
	var drag *pointer.Event
	for {
		ev, ok := a.msgDrag.Update(gtx.Metric, gtx.Source, gesture.Vertical)
		if !ok {
			break
		}
		switch ev.Kind {
		case pointer.Press:
			a.msgGrabY = ev.Position.Y
		case pointer.Drag:
			e := ev
			drag = &e
		}
	}
	if drag != nil {
		// Dragging up (a smaller Y than the grab point) grows the strip.
		h = min(max(h+int(a.msgGrabY-drag.Position.Y), lo), hi)
		a.msgPanelDp = float32(h) / gtx.Metric.PxPerDp
	}
	return h
}

// msgPanelDivider draws the grab bar between the chat panel and the message
// strip: a full-width rule with a centered grip, lit while it is being dragged.
func (a *App) msgPanelDivider(gtx C) D {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(unit.Dp(msgDividerH))

	active := a.msgDrag.Pressed()
	rule, grip := colBorder, colMuted
	if active {
		rule, grip = colAccent, colAccent
	}
	y := h / 2
	fillRect(gtx.Ops, image.Rect(gtx.Dp(12), y-1, w-gtx.Dp(12), y+1), rule)
	gw, gh := gtx.Dp(56), gtx.Dp(4)
	fillRect(gtx.Ops, image.Rect(w/2-gw/2, y-gh/2, w/2+gw/2, y+gh/2), grip)

	// The whole bar is the drag target, and it keeps the resize cursor.
	defer clip.Rect(image.Rect(0, 0, w, h)).Push(gtx.Ops).Pop()
	a.msgDrag.Add(gtx.Ops)
	pointer.CursorRowResize.Add(gtx.Ops)
	return D{Size: image.Pt(w, h)}
}

// natsMsgPanel renders the bottom strip listing the captured stream messages,
// newest at the bottom (the list sticks to the end).
func (a *App) natsMsgPanel(gtx C, h int) D {
	a.mu.Lock()
	msgs := append([]streamMsg(nil), a.msgLog...)
	a.mu.Unlock()

	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
		return bordered(gtx, func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max // fill the strip even while empty
			if len(msgs) == 0 {
				return layout.Center.Layout(gtx, a.body("Waiting for stream messages…", colMuted))
			}
			return material.List(a.th, &a.msgList).Layout(gtx, len(msgs), func(gtx C, i int) D {
				first := i == 0 || msgs[i-1].group != msgs[i].group
				last := i == len(msgs)-1 || msgs[i+1].group != msgs[i].group
				return a.msgRow(gtx, msgs[i], first, last)
			})
		})
	})
}

// msgRow renders one message: batch id · timestamp · subject · payload, in
// monospace with the payload syntax-colored. Rows committed in the same atomic
// batch share a tinted background and are wrapped in a bracket down the left
// edge — a bar spanning every row of the batch, closed by a stub at the top of
// the first row and the bottom of the last — with the batch's id printed in the
// gutter of its first row, in the batch's own color. Only the first row of a
// run carries the gap that separates one transaction from the next, so a
// multi-cell move reads as a single bracketed block naming the batch it
// committed in.
func (a *App) msgRow(gtx C, m streamMsg, firstOfGroup, lastOfGroup bool) D {
	c := msgGroupPalette[m.group%len(msgGroupPalette)]

	// The gutter is a fixed monospace width on EVERY row (blank unless this row
	// opens a batch), so the timestamp and subject columns stay aligned down the
	// whole strip.
	gutterCol, gutter := colMuted, strings.Repeat(" ", msgBatchIDLen)
	if m.batched && firstOfGroup {
		gutterCol, gutter = c, shortBatchID(m.batch)
	}
	children := []layout.FlexChild{
		layout.Rigid(a.mono(gutter+"  ", gutterCol)),
		layout.Rigid(a.mono(m.ts.Format("15:04:05.000")+"  ", colMuted)),
		layout.Rigid(a.mono(m.subject+"  ", colAccent)),
	}
	for _, s := range jsonSpans(m.payload) {
		children = append(children, layout.Rigid(a.mono(s.text, s.col)))
	}

	gap := unit.Dp(0)
	if firstOfGroup {
		gap = unit.Dp(3)
	}
	return layout.Inset{Top: gap}.Layout(gtx, func(gtx C) D {
		// Record the text first: the background and bracket have to be painted
		// underneath it but can only be sized once the row's height is known.
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Left: unit.Dp(12), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
			return layout.Flex{}.Layout(gtx, children...)
		})
		call := macro.Stop()

		if m.batched {
			h := dims.Size.Y
			bar, stub, th := gtx.Dp(3), gtx.Dp(9), max(gtx.Dp(2), 2)
			fillRect(gtx.Ops, image.Rect(0, 0, max(dims.Size.X, gtx.Constraints.Max.X), h), withAlpha(c, 0.07))
			fillRect(gtx.Ops, image.Rect(0, 0, bar, h), withAlpha(c, 0.85))
			if firstOfGroup {
				fillRect(gtx.Ops, image.Rect(0, 0, stub, th), withAlpha(c, 0.85))
			}
			if lastOfGroup {
				fillRect(gtx.Ops, image.Rect(0, h-th, stub, h), withAlpha(c, 0.85))
			}
		}
		call.Add(gtx.Ops)
		return dims
	})
}

// msgBatchIDLen is how much of a Nats-Batch-Id the gutter shows — enough to
// tell one live batch from another without stealing the row's width.
const msgBatchIDLen = 6

// shortBatchID trims (or pads) a batch id to the gutter width, so every row
// lines up whether or not it opens a batch.
func shortBatchID(id string) string {
	if len(id) > msgBatchIDLen {
		return id[:msgBatchIDLen]
	}
	return id + strings.Repeat(" ", msgBatchIDLen-len(id))
}

// mono is body() in the Go Mono face at the panel's smaller size.
func (a *App) mono(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = c
		l.TextSize = unit.Sp(12)
		l.Font.Typeface = font.Typeface("Go Mono")
		return l.Layout(gtx)
	}
}

// jsonSpan is one syntax-colored run of payload text.
type jsonSpan struct {
	text string
	col  colorN
}

// jsonSpans splits a JSON payload into colored runs: keys, string values,
// numbers, true/false/null, and punctuation. Display-only scanner — anything
// it does not recognize just falls through as punctuation-colored text.
func jsonSpans(s string) []jsonSpan {
	var spans []jsonSpan
	emit := func(text string, col colorN) {
		if text == "" {
			return
		}
		if n := len(spans); n > 0 && spans[n-1].col == col {
			spans[n-1].text += text
			return
		}
		spans = append(spans, jsonSpan{text: text, col: col})
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && (s[j] != '"' || s[j-1] == '\\') {
				j++
			}
			if j < len(s) {
				j++ // include the closing quote
			}
			// A string followed by ':' is an object key.
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			col := colJSONStr
			if k < len(s) && s[k] == ':' {
				col = colJSONKey
			}
			emit(s[i:j], col)
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(s) && strings.ContainsRune("-+.eE0123456789", rune(s[j])) {
				j++
			}
			emit(s[i:j], colJSONNum)
			i = j
		case strings.HasPrefix(s[i:], "true"):
			emit("true", colJSONLit)
			i += len("true")
		case strings.HasPrefix(s[i:], "false"):
			emit("false", colJSONLit)
			i += len("false")
		case strings.HasPrefix(s[i:], "null"):
			emit("null", colJSONLit)
			i += len("null")
		default:
			emit(string(c), colMuted)
			i++
		}
	}
	return spans
}
