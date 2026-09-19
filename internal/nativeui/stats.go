package nativeui

import (
	"fmt"
	"image"
	"strconv"

	"gioui.org/layout"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
)

// A player's stats — what their locks did over the game, by the Guideline's
// names (config.PlayerStats) — are shown three times over: in the game-over
// box the moment the game ends (the local player's own), on the history's
// View board (every player's, from the record) and at the end of a replay
// (every player's, from the record, or folded back off the recording for a
// game archived before records carried them). One list of lines serves all
// three (playerStatLines), laid out as a column of label/value rows
// (statRows) under a player's name, or as a two-column grid where a box
// beside the playfield had better stay short (statGrid).

// statLine is one row of a player's stats: the label and the value.
type statLine struct{ label, value string }

// statUnknown is the value of a figure the record does not have — an older
// record's piece count for a survivor, a time nobody stamped.
const statUnknown = "—"

// playerStatLines lists a player's stats, top to bottom: the figures every
// record has — the score and the level where asked for (the game-over box
// has its own score line, and a co-op record from before per-player scores
// has no score of the player's own to show), then the lines, the pieces,
// the time played and the pieces per second — then the tally
// (config.PlayerStats): the clears by size, the spins, the Back-to-Backs,
// the longest combo, the perfect clears and, where the game attacks
// (competitive, teams), the garbage rows sent. A record from before the
// tally lists the figures alone.
func playerStatLines(p config.PlayerResult, mode config.GameMode, score, level bool) []statLine {
	var out []statLine
	if score {
		out = append(out, statLine{"SCORE", strconv.Itoa(p.Score)})
	}
	if level {
		out = append(out, statLine{"LEVEL", strconv.Itoa(p.Level)})
	}
	out = append(out, statLine{"LINES", strconv.Itoa(p.Lines)})
	pieces := statUnknown
	if p.PieceCount > 0 {
		pieces = strconv.FormatUint(p.PieceCount, 10)
	}
	out = append(out, statLine{"PIECES", pieces})
	st := p.Stats
	played, pps := statUnknown, statUnknown
	if st != nil && st.PlayedMs > 0 {
		played = formatSurvived(st.Played())
		if p.PieceCount > 0 {
			pps = fmt.Sprintf("%.2f", float64(p.PieceCount)/st.Played().Seconds())
		}
	}
	out = append(out, statLine{"TIME", played}, statLine{"PPS", pps})
	if st == nil {
		return out
	}
	out = append(out,
		statLine{"SINGLES", strconv.Itoa(st.Singles)},
		statLine{"DOUBLES", strconv.Itoa(st.Doubles)},
		statLine{"TRIPLES", strconv.Itoa(st.Triples)},
		statLine{"QUADS", strconv.Itoa(st.Quads)},
		statLine{"T-SPINS", strconv.Itoa(st.TSpins)},
		statLine{"MINI T-SPINS", strconv.Itoa(st.MiniTSpins)},
		statLine{"BACK-TO-BACK", strconv.Itoa(st.BackToBacks)},
		statLine{"MAX COMBO", strconv.Itoa(st.MaxCombo)},
		statLine{"PERFECT CLEARS", strconv.Itoa(st.PerfectClears)},
	)
	if mode != config.ModeCooperative {
		out = append(out, statLine{"GARBAGE SENT", strconv.Itoa(st.Attack)})
	}
	return out
}

// ownResult is the local player's result as the game-over box lists it:
// their own totals read live off their engine — the score and the lines of
// their own locks, the level their board reached — and their tally
// (Engine.OwnStats), which knows first-hand the pieces they drew and how
// long they played.
func ownResult(eng *engine.Engine) config.PlayerResult {
	st := eng.OwnStats()
	return config.PlayerResult{
		PlayerID:   eng.PlayerID(),
		Score:      eng.OwnScore(),
		Level:      eng.AchievedLevel(),
		Lines:      eng.OwnLines(),
		PieceCount: uint64(st.Pieces),
		Stats:      &st,
	}
}

// statColW is the width of one column of stat rows: room for the longest
// label and a value beside it.
const statColW = unit.Dp(180)

// statRows lays stat lines out as a column of label/value rows, colW wide:
// the labels muted down the left, the values down the right.
func (a *App) statRows(lines []statLine, colW unit.Dp) layout.Widget {
	return func(gtx C) D {
		w := min(gtx.Dp(colW), gtx.Constraints.Max.X)
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
		kids := make([]layout.FlexChild, 0, len(lines))
		for _, l := range lines {
			l := l
			kids = append(kids, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(a.pixel(unit.Sp(8), l.label, colMuted).Layout),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Max.X, 0)} }),
						layout.Rigid(a.pixel(unit.Sp(9), l.value, colFg).Layout),
					)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
}

// statGrid lays stat lines out in two columns side by side, each colW wide
// — the game-over box's, which stands beside the playfield (or under it on
// the compact screen) and had better stay short.
func (a *App) statGrid(lines []statLine, colW unit.Dp) layout.Widget {
	half := (len(lines) + 1) / 2
	return func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(a.statRows(lines[:half], colW)),
			layout.Rigid(hSpacer(16)),
			layout.Rigid(a.statRows(lines[half:], colW)),
		)
	}
}
