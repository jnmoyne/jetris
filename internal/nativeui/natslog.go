package nativeui

import (
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// streamMsg is one game-stream message captured for the "Show NATS messages"
// panel: the JetStream stream timestamp (from the message metadata, not the
// local receive time), the subject, and the raw JSON payload.
type streamMsg struct {
	ts      time.Time
	subject string
	payload string
}

// msgLogCap bounds the in-memory message log; the panel shows the tail.
const msgLogCap = 200

// msgPanelHeight is the height of the bottom message strip.
const msgPanelHeight = 170

// JSON syntax colors for the panel payloads (keys NATS blue, strings NATS
// green, numbers gold, true/false/null orange, punctuation muted).
var (
	colJSONKey = colAccent
	colJSONStr = colNATSGreen
	colJSONNum = colGold
	colJSONLit = colOrange
)

// recordStreamMsg is installed as engine.OnStreamMsg and runs on the engine's
// consumer goroutines. Collection is gated on the checkbox mirror so an
// unchecked panel costs one flag check per message.
func (a *App) recordStreamMsg(ts time.Time, subject string, payload []byte) {
	a.mu.Lock()
	if !a.msgShow {
		a.mu.Unlock()
		return
	}
	a.msgLog = append(a.msgLog, streamMsg{ts: ts, subject: subject, payload: string(payload)})
	if len(a.msgLog) > msgLogCap {
		a.msgLog = a.msgLog[len(a.msgLog)-msgLogCap:]
	}
	a.mu.Unlock()
	a.invalidate()
}

// natsMsgPanel renders the bottom strip listing the captured stream messages,
// newest at the bottom (the list sticks to the end).
func (a *App) natsMsgPanel(gtx C) D {
	a.mu.Lock()
	msgs := append([]streamMsg(nil), a.msgLog...)
	a.mu.Unlock()

	h := gtx.Dp(unit.Dp(msgPanelHeight))
	gtx.Constraints.Min.Y = h
	gtx.Constraints.Max.Y = h
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
		return bordered(gtx, func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max // fill the strip even while empty
			if len(msgs) == 0 {
				return layout.Center.Layout(gtx, a.body("Waiting for stream messages…", colMuted))
			}
			return material.List(a.th, &a.msgList).Layout(gtx, len(msgs), func(gtx C, i int) D {
				return a.msgRow(gtx, msgs[i])
			})
		})
	})
}

// msgRow renders one message: timestamp · subject · payload, in monospace
// with the payload syntax-colored.
func (a *App) msgRow(gtx C, m streamMsg) D {
	children := []layout.FlexChild{
		layout.Rigid(a.mono(m.ts.Format("15:04:05.000")+"  ", colMuted)),
		layout.Rigid(a.mono(m.subject+"  ", colAccent)),
	}
	for _, s := range jsonSpans(m.payload) {
		children = append(children, layout.Rigid(a.mono(s.text, s.col)))
	}
	return layout.Inset{Top: unit.Dp(1), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx, children...)
	})
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
