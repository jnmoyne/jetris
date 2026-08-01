package nativeui

import (
	"fmt"
	"image"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
)

func (a *App) layoutLobby(gtx C) D {
	lb := a.getLobby()
	if lb == nil {
		a.mu.Lock()
		a.screen = screenLogin
		a.mu.Unlock()
		return D{}
	}

	// --- event handling ---
	if a.quitBtn.Clicked(gtx) {
		go a.quit()
	}
	if a.createBtn.Clicked(gtx) {
		mode := config.ModeCooperative
		switch a.modeEnum.Value {
		case "competitive":
			mode = config.ModeCompetitive
		case "teams":
			mode = config.ModeTeams
		}
		count, err := strconv.Atoi(strings.TrimSpace(a.countEd.Text()))
		if mode == config.ModeTeams {
			// For teams the count editor means players PER TEAM.
			if err != nil || count < 1 {
				count = 1
			}
		} else if err != nil || count < 2 {
			count = 2
		}
		// Agent policy: how many seats idle jetricks-agent players may take.
		// Unchecked = 0 = agents may not join. Clamped to the game's total
		// player count (the count editor is per-team in teams mode).
		maxAgents := 0
		if a.allowAgentsCb.Value {
			total := count
			if mode == config.ModeTeams {
				total = config.TeamCount * count
			}
			maxAgents, err = strconv.Atoi(strings.TrimSpace(a.maxAgentsEd.Text()))
			if err != nil || maxAgents < 1 {
				maxAgents = 1
			}
			if maxAgents > total {
				maxAgents = total
			}
		}
		if a.inviteOnlyCb.Value {
			go a.openInvitePicker(mode, count)
		} else {
			go func() { a.createGame(mode, count, maxAgents, false) }()
		}
	}
	a.handleChatSubmit(gtx)

	games := sortedGames(lb.Games())
	players := sortedPlayers(lb.Players())
	abandoned := lb.AbandonedGames()

	// The lobby screen shows only lobby-scoped messages; per-game messages
	// (GameID != "") appear on that game's screen instead.
	a.mu.Lock()
	chat := make([]lobby.ChatMessage, 0, len(a.chatLog))
	for _, m := range a.chatLog {
		if m.GameID == "" {
			chat = append(chat, m)
		}
	}
	a.mu.Unlock()

	// dispatch per-game buttons
	for _, g := range games {
		btns := a.gameButtons(g.GameID)
		if btns.join.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 0)
		}
		if btns.joinA.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 0)
		}
		if btns.joinB.Clicked(gtx) {
			id := g.GameID
			go a.joinGame(id, 1)
		}
		if btns.spectate.Clicked(gtx) {
			id := g.GameID
			go a.spectateGame(id)
		}
		if btns.reinvite.Clicked(gtx) {
			// Re-open the picker for this already-created invite-only game so
			// the creator can invite more players after joining and returning
			// to the lobby.
			a.reopenInvitePicker(g)
		}
		if btns.del.Clicked(gtx) {
			a.confirmDeleteID = g.GameID
		}
		if btns.delYes.Clicked(gtx) {
			id := g.GameID
			a.confirmDeleteID = ""
			go a.deleteGame(id)
		}
		if btns.delNo.Clicked(gtx) {
			a.confirmDeleteID = ""
		}
	}

	// Modal overlays: the invitee picker (after creating an invite-only game)
	// and the incoming-invitation pop-up. Their buttons are dispatched here so
	// a click can't fall through to the lobby underneath.
	pickerOpen := a.handleInvitePicker(gtx)
	pendingInvite, inviteOpen := a.handleIncomingInvite(gtx)

	// --- render ---
	base := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.lobbyBanner),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
						return a.lobbyLeft(gtx, players, chat)
					})
				}),
				layout.Flexed(2, func(gtx C) D {
					return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx C) D {
						return a.lobbyRight(gtx, games, abandoned, a.archivesForDisplay(lb.Archives()), lb.PlayerName())
					})
				}),
			)
		}),
	)
	if !pickerOpen && !inviteOpen {
		return base
	}
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D { return base }),
		layout.Expanded(func(gtx C) D {
			// Scrim: dim the lobby and swallow clicks behind the modal.
			fillRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), withAlpha(colBg, 0xc0))
			return D{Size: gtx.Constraints.Max}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			if inviteOpen {
				return a.incomingInviteOverlay(gtx, pendingInvite)
			}
			return a.invitePickerOverlay(gtx)
		}),
	)
}

// logInvite logs an invitation-send failure without stopping the batch.
func logInvite(err error) { log.Printf("send invite: %v", err) }

func (a *App) lobbyLeft(gtx C, players []lobby.PlayerPresence, chat []lobby.ChatMessage) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("PLAYERS")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.playerList).Layout(gtx, len(players), func(gtx C, i int) D {
					p := players[i]
					return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{}.Layout(gtx,
							layout.Flexed(1, a.body(agentName(p.Name, p.Agent), colFg)),
							layout.Rigid(a.body(statusText(p.Status), colMuted)),
						)
					})
				})
			})
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.header("CHAT")),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.chatList).Layout(gtx, len(chat), func(gtx C, i int) D {
					m := chat[i]
					txt := fmt.Sprintf("%s: %s", m.Name, m.Text)
					return layout.Inset{Top: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, a.body(txt, colFg))
				})
			})
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.chatEd, "Message…")
				}),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.chatBtn, "Send") }),
			)
		}),
	)
}

func (a *App) lobbyRight(gtx C, games []lobby.GameListing, abandoned map[string]bool, archives []config.ArchiveRecord, playerName string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return a.pixel(unit.Sp(13), "LOBBY — "+playerName, colFg).Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return a.secondaryButton(gtx, &a.quitBtn, "Quit")
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			// While hosting the embedded server, show the address other
			// players should dial so the host can share it.
			addr := a.embeddedAddr()
			if addr == "" {
				return D{}
			}
			return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(a.pixel(unit.Sp(9), "YOUR SERVER'S URL IS  ", colMuted).Layout),
					layout.Rigid(a.pixel(unit.Sp(10), "nats://"+addr, colNATSGreen).Layout),
					layout.Rigid(a.body("  — share this address so others can join you", colMuted)),
				)
			})
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.createRow),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.header("GAMES")),
		layout.Flexed(2, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				return material.List(a.th, &a.gameList).Layout(gtx, len(games), func(gtx C, i int) D {
					return a.gameRow(gtx, games[i], abandoned[games[i].GameID])
				})
			})
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			// GAME HISTORY header with its sort selector and agent filter
			// grouped right beside it.
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.header("GAME HISTORY")),
				layout.Rigid(hSpacer(12)),
				layout.Rigid(func(gtx C) D {
					rb := material.RadioButton(a.th, &a.histSortEnum, "score", "By score")
					rb.Color = colFg
					return rb.Layout(gtx)
				}),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					rb := material.RadioButton(a.th, &a.histSortEnum, "date", "By date")
					rb.Color = colFg
					return rb.Layout(gtx)
				}),
				layout.Rigid(hSpacer(10)),
				layout.Rigid(func(gtx C) D {
					cb := material.CheckBox(a.th, &a.histAgentsCb, "Agent games")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}),
			)
		}),
		layout.Rigid(func(gtx C) D { return a.teamStandingsLine(gtx, archives) }),
		layout.Flexed(1, func(gtx C) D {
			return bordered(gtx, func(gtx C) D {
				if len(archives) == 0 {
					return layout.Inset{Top: unit.Dp(6), Left: unit.Dp(4)}.Layout(gtx,
						a.body("No finished games yet.", colMuted))
				}
				// An arcade high-score table: a pixel-font column header over a
				// scrolling list of games, each row a fixed SCORE / TIME / MODE
				// column trio (the score largest, in gold) and a flexed
				// winner-first PLAYERS column, ruled off from the next game.
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(a.archiveHistoryHeader),
					layout.Rigid(func(gtx C) D { return hrule(gtx, colAccent, 1) }),
					layout.Flexed(1, func(gtx C) D {
						return material.List(a.th, &a.archiveLst).Layout(gtx, len(archives), func(gtx C, i int) D {
							for len(a.archiveBtns) <= i {
								a.archiveBtns = append(a.archiveBtns, widget.Clickable{})
							}
							btn := &a.archiveBtns[i]
							if btn.Clicked(gtx) {
								a.openArchive(archives[i])
							}
							return a.archiveHistoryRow(gtx, archives[i], btn)
						})
					}),
				)
			})
		}),
	)
}

// archivesForDisplay applies the history controls: drop games with agent
// seats when the "Agent games" box is unchecked, then order by the selected
// key — headline score (default) or finish time, most recent first.
func (a *App) archivesForDisplay(recs []config.ArchiveRecord) []config.ArchiveRecord {
	if !a.histAgentsCb.Value {
		humanOnly := recs[:0:0]
		for _, r := range recs {
			if !r.HasAgents() {
				humanOnly = append(humanOnly, r)
			}
		}
		recs = humanOnly
	}
	if a.histSortEnum.Value == "date" {
		return sortedArchivesByDate(recs)
	}
	return sortedArchives(recs)
}

// teamStandings folds the teams-mode games among recs into overall per-team
// totals: wins (a draw counts for neither side) and points (sum of final team
// scores). games is how many teams games were counted.
func teamStandings(recs []config.ArchiveRecord) (wins, points [config.TeamCount]int, games int) {
	for _, r := range recs {
		if r.Mode != config.ModeTeams {
			continue
		}
		games++
		if r.WinningTeam >= 0 && r.WinningTeam < config.TeamCount {
			wins[r.WinningTeam]++
		}
		for t := 0; t < config.TeamCount && t < len(r.TeamScores); t++ {
			points[t] += r.TeamScores[t]
		}
	}
	return wins, points, games
}

// teamStandingsLine renders the all-time TEAM A vs TEAM B scoreboard over the
// teams games currently listed in the history (so the agent filter applies):
// wins and total points per team, the leading team (by wins, points as the
// tie-break) in gold. Nothing is drawn while no teams game has finished.
func (a *App) teamStandingsLine(gtx C, archives []config.ArchiveRecord) D {
	wins, points, games := teamStandings(archives)
	if games == 0 {
		return D{}
	}
	lead := -1 // -1: dead even, no highlight
	switch {
	case wins[0] != wins[1]:
		lead = 0
		if wins[1] > wins[0] {
			lead = 1
		}
	case points[0] != points[1]:
		lead = 0
		if points[1] > points[0] {
			lead = 1
		}
	}
	seg := func(t int) layout.FlexChild {
		col := colFg
		if t == lead {
			col = colGold
		}
		txt := fmt.Sprintf("TEAM %s %dW · %d PTS", teamName(t), wins[t], points[t])
		return layout.Rigid(a.pixel(unit.Sp(8), txt, col).Layout)
	}
	label := fmt.Sprintf("TEAMS OVERALL (%d GAMES)   ", games)
	if games == 1 {
		label = "TEAMS OVERALL (1 GAME)   "
	}
	return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(8), label, colMuted).Layout),
			seg(0),
			layout.Rigid(a.pixel(unit.Sp(8), "  —  ", colMuted).Layout),
			seg(1),
		)
	})
}

// sortedArchivesByDate orders the history list by finish time, most recent
// first (score as the tie-break).
func sortedArchivesByDate(recs []config.ArchiveRecord) []config.ArchiveRecord {
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].FinishedAt.Equal(recs[j].FinishedAt) {
			return recs[i].FinishedAt.After(recs[j].FinishedAt)
		}
		return archiveScore(recs[i]) > archiveScore(recs[j])
	})
	return recs
}

// sortedArchives orders the history list by headline score (highest first);
// between two games with the same score the shorter game ranks higher, and
// remaining ties show the most recently finished game first.
func sortedArchives(recs []config.ArchiveRecord) []config.ArchiveRecord {
	sort.SliceStable(recs, func(i, j int) bool {
		si, sj := archiveScore(recs[i]), archiveScore(recs[j])
		if si != sj {
			return si > sj
		}
		di, dj := archiveDuration(recs[i]), archiveDuration(recs[j])
		if di != dj {
			return di < dj
		}
		return recs[i].FinishedAt.After(recs[j].FinishedAt)
	})
	return recs
}

// archiveScore is the headline score a finished game is ranked by: the shared
// total for cooperative, the winning-side total for teams, and the best
// player's score for competitive.
func archiveScore(r config.ArchiveRecord) int {
	switch r.Mode {
	case config.ModeCooperative:
		return r.TotalScore
	case config.ModeTeams:
		if len(r.TeamScores) > 0 {
			best := r.TeamScores[0]
			for _, s := range r.TeamScores[1:] {
				if s > best {
					best = s
				}
			}
			return best
		}
	}
	best := 0
	for _, p := range r.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

// archiveDuration is how long the game lasted; zero for records missing either
// timestamp (or with a clock skew that made finish precede start).
func archiveDuration(r config.ArchiveRecord) time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	if d := r.FinishedAt.Sub(r.StartedAt); d > 0 {
		return d
	}
	return 0
}

// archiveWhen renders a record's start date/time (in the viewer's local
// timezone) and duration, e.g. "2026-07-06 14:03 PDT · 4m32s".
func archiveWhen(r config.ArchiveRecord) string {
	if r.StartedAt.IsZero() {
		return ""
	}
	s := r.StartedAt.Local().Format("2006-01-02 15:04 MST")
	if d := archiveDuration(r); d > 0 {
		s += " · " + d.Round(time.Second).String()
	}
	return s
}

// Column widths (dp) for the arcade-style GAME HISTORY table. SCORE is
// right-aligned (numbers line up), the rest left-aligned; PLAYERS takes the
// remaining width and wraps.
const (
	histScoreW = 92
	histTimeW  = 96
	histModeW  = 124
)

// fixedCol lays wdg inside a fixed-width (dp) column, aligned by dir.
func fixedCol(gtx C, w int, dir layout.Direction, wdg layout.Widget) D {
	cw := gtx.Dp(unit.Dp(w))
	gtx.Constraints.Min.X = cw
	gtx.Constraints.Max.X = cw
	return dir.Layout(gtx, wdg)
}

// hrule draws a full-width horizontal rule h dp tall in color c — the visual
// delimiter between history rows (and the header underline).
func hrule(gtx C, c colorN, h int) D {
	height := gtx.Dp(unit.Dp(h))
	w := gtx.Constraints.Max.X
	fillRect(gtx.Ops, image.Rect(0, 0, w, height), c)
	return D{Size: image.Pt(w, height)}
}

// archiveHistoryHeader is the pixel-font column-label row above the history
// list, aligned to the same column widths as archiveHistoryRow.
func (a *App) archiveHistoryHeader(gtx C) D {
	col := func(txt string, w int, dir layout.Direction) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return fixedCol(gtx, w, dir, a.pixel(unit.Sp(8), txt, colAccent).Layout)
		})
	}
	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(5), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			col("SCORE", histScoreW, layout.E),
			layout.Rigid(hSpacer(10)),
			col("TIME", histTimeW, layout.W),
			layout.Rigid(hSpacer(10)),
			col("MODE", histModeW, layout.W),
			layout.Rigid(hSpacer(10)),
			layout.Flexed(1, a.pixel(unit.Sp(8), "PLAYERS", colAccent).Layout),
		)
	})
}

// archiveHistoryRow renders one finished game as a table row: the headline
// SCORE (largest, gold), the game TIME (duration over date), the MODE, and a
// flexed winner-first PLAYERS column, closed by a rule separating it from the
// next game.
func (a *App) archiveHistoryRow(gtx C, rec config.ArchiveRecord, btn *widget.Clickable) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return fixedCol(gtx, histScoreW, layout.E, a.archiveScoreCell(rec)) }),
					layout.Rigid(hSpacer(10)),
					layout.Rigid(func(gtx C) D { return fixedCol(gtx, histTimeW, layout.W, a.archiveTimeCell(rec)) }),
					layout.Rigid(hSpacer(10)),
					layout.Rigid(func(gtx C) D { return fixedCol(gtx, histModeW, layout.W, a.archiveModeCell(rec)) }),
					layout.Rigid(hSpacer(10)),
					layout.Flexed(1, a.archivePlayersCell(rec)),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(func(gtx C) D { return a.viewBoardButton(gtx, btn) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hrule(gtx, colBorder, 1) }),
	)
}

// archiveScoreCell is the headline SCORE (gold pixel numerals) over a small
// achieved-level line — the game's most important figure, so the largest.
func (a *App) archiveScoreCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(13), strconv.Itoa(archiveScore(r)), colGold).Layout),
			layout.Rigid(a.caption(fmt.Sprintf("LVL %d", archiveHeadlineLevel(r)), colMuted)),
		)
	}
}

// archiveTimeCell is the game TIME: the duration (prominent, accent) over the
// start date (muted).
func (a *App) archiveTimeCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		dur := "—"
		if d := archiveDuration(r); d > 0 {
			dur = d.Round(time.Second).String()
		}
		var children []layout.FlexChild
		children = append(children, layout.Rigid(a.body(dur, colAccent)))
		if !r.StartedAt.IsZero() {
			children = append(children, layout.Rigid(a.caption(r.StartedAt.Local().Format("01-02 15:04"), colMuted)))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
}

// archiveModeCell names the MODE (pixel accent) over the player count / team
// shape (muted).
func (a *App) archiveModeCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		name, sub := "COOPERATIVE", fmt.Sprintf("%d PLAYERS", len(r.Players))
		switch r.Mode {
		case config.ModeCompetitive:
			name = "COMPETITIVE"
		case config.ModeTeams:
			name, sub = "TEAMS", fmt.Sprintf("%dv%d", r.TeamSize, r.TeamSize)
		}
		// Crew line: whether the game was human-vs-human or had agent seats,
		// so the two kinds can be told apart at a glance.
		crew, crewCol := "HUMANS", colNATSGreen
		if r.HasAgents() {
			crew, crewCol = "WITH AGENTS", colOrange
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(8), name, colAccent).Layout),
			layout.Rigid(a.caption(sub, colMuted)),
			layout.Rigid(a.caption(crew, crewCol)),
		)
	}
}

// archivePlayersCell is the flexed PLAYERS column: the winner(s) on the first
// line (gold, trophy), everyone else below (muted) — see archiveRosterLines.
func (a *App) archivePlayersCell(r config.ArchiveRecord) layout.Widget {
	return func(gtx C) D {
		lines := archiveRosterLines(r)
		children := make([]layout.FlexChild, 0, len(lines))
		for _, ln := range lines {
			children = append(children, layout.Rigid(a.body(ln.text, ln.col)))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
}

// caption is a small muted-scale label (dates, sub-counts) in the given color.
func (a *App) caption(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Caption(a.th, txt)
		l.Color = c
		return l.Layout(gtx)
	}
}

// rosterLine is one colored line of a history row's PLAYERS column.
type rosterLine struct {
	text string
	col  colorN
}

// archiveHeadlineLevel is the level that goes with archiveScore: the shared
// final level (co-op), the winning team's level (teams; the best if a draw),
// or the top-scoring player's level (competitive).
func archiveHeadlineLevel(r config.ArchiveRecord) int {
	switch r.Mode {
	case config.ModeCooperative:
		return r.FinalLevel
	case config.ModeTeams:
		if r.WinningTeam >= 0 && r.WinningTeam < len(r.TeamLevels) {
			return r.TeamLevels[r.WinningTeam]
		}
		best := 0
		for _, l := range r.TeamLevels {
			if l > best {
				best = l
			}
		}
		return best
	}
	best, found := config.PlayerResult{}, false
	for _, p := range r.Players {
		if !found || p.Score > best.Score {
			best, found = p, true
		}
	}
	return best.Level
}

// archiveRosterLines builds the PLAYERS column's colored lines for a record,
// winner(s) first and highlighted in gold: competitive lists players by
// winner-then-score with each score/level, teams one line per team (winner
// first, its members and totals), cooperative just the shared roster.
func archiveRosterLines(r config.ArchiveRecord) []rosterLine {
	switch r.Mode {
	case config.ModeTeams:
		return teamRosterLines(r)
	case config.ModeCooperative:
		return coopRosterLines(r)
	default:
		return competitiveRosterLines(r)
	}
}

func competitiveRosterLines(r config.ArchiveRecord) []rosterLine {
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].Winner != players[j].Winner {
			return players[i].Winner // winners first
		}
		return players[i].Score > players[j].Score
	})
	var winners, rest []string
	for _, p := range players {
		s := fmt.Sprintf("%s %d (lvl %d)", agentName(p.PlayerID, p.Agent), p.Score, p.Level)
		if p.Winner {
			winners = append(winners, s)
		} else {
			rest = append(rest, s)
		}
	}
	var out []rosterLine
	if len(winners) > 0 {
		out = append(out, rosterLine{"🏆 " + strings.Join(winners, " · "), colGold})
	}
	if len(rest) > 0 {
		out = append(out, rosterLine{strings.Join(rest, " · "), colMuted})
	}
	return out
}

func teamRosterLines(r config.ArchiveRecord) []rosterLine {
	order := []int{0, 1}
	if r.WinningTeam == 1 {
		order = []int{1, 0} // winning team first
	}
	out := make([]rosterLine, 0, len(order))
	for _, t := range order {
		var members []string
		for _, p := range r.Players {
			if p.Team == t {
				members = append(members, agentName(p.PlayerID, p.Agent))
			}
		}
		sort.Strings(members)
		stats := ""
		if t < len(r.TeamScores) {
			stats = fmt.Sprintf(" %d", r.TeamScores[t])
			if t < len(r.TeamLevels) {
				stats += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
			}
		}
		label := fmt.Sprintf("TEAM %s%s — %s", teamName(t), stats, strings.Join(members, ", "))
		col := colMuted
		if r.WinningTeam == t {
			label, col = "🏆 "+label, colGold
		}
		out = append(out, rosterLine{label, col})
	}
	return out
}

func coopRosterLines(r config.ArchiveRecord) []rosterLine {
	members := make([]string, 0, len(r.Players))
	for _, p := range r.Players {
		members = append(members, agentName(p.PlayerID, p.Agent))
	}
	sort.Strings(members)
	return []rosterLine{{strings.Join(members, ", "), colFg}}
}

// archiveLine summarizes a finished game for the history list.
func archiveLine(r config.ArchiveRecord) string {
	if when := archiveWhen(r); when != "" {
		return when + " · " + archiveModeLine(r)
	}
	return archiveModeLine(r)
}

// archiveModeLine is the mode-specific part of a history line (players,
// scores, levels, winners).
func archiveModeLine(r config.ArchiveRecord) string {
	if r.Mode == config.ModeTeams {
		// "teams · A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave"
		parts := make([]string, 0, config.TeamCount)
		for t := 0; t < config.TeamCount; t++ {
			var members []string
			for _, p := range r.Players {
				if p.Team == t {
					members = append(members, p.PlayerID)
				}
			}
			tag := ""
			if r.WinningTeam == t {
				tag = " 🏆"
			}
			stats := ""
			if t < len(r.TeamScores) {
				// Older records predate per-team totals — skip stats for those.
				stats = fmt.Sprintf(" %d", r.TeamScores[t])
				if t < len(r.TeamLevels) {
					stats += fmt.Sprintf(" (lvl %d)", r.TeamLevels[t])
				}
			}
			parts = append(parts, fmt.Sprintf("%s%s%s %s", teamName(t), tag, stats, strings.Join(members, ", ")))
		}
		return fmt.Sprintf("teams · %s", strings.Join(parts, " · "))
	}
	players := append([]config.PlayerResult(nil), r.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].Score > players[j].Score })
	parts := make([]string, 0, len(players))
	for _, p := range players {
		tag := ""
		if p.Winner {
			tag = " 🏆"
		}
		parts = append(parts, fmt.Sprintf("%s %d%s", p.PlayerID, p.Score, tag))
	}
	if r.Mode == config.ModeCooperative {
		return fmt.Sprintf("co-op · total %d (lvl %d) · %s", r.TotalScore, r.FinalLevel, strings.Join(parts, ", "))
	}
	// Competitive: append each player's achieved level to their score.
	parts = parts[:0]
	for _, p := range players {
		tag := ""
		if p.Winner {
			tag = " 🏆"
		}
		parts = append(parts, fmt.Sprintf("%s %d (lvl %d)%s", p.PlayerID, p.Score, p.Level, tag))
	}
	return fmt.Sprintf("competitive · %s", strings.Join(parts, ", "))
}

func (a *App) createRow(gtx C) D {
	countLabel := "Players:"
	if a.modeEnum.Value == "teams" {
		countLabel = "Per team:"
	}
	// The Create button is a top-level Rigid (measured before the Flexed
	// options), so no combination of options can squish it — when width runs
	// short, the options row gives, never the button.
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, a.createOptions(countLabel)),
		layout.Rigid(hSpacer(10)),
		layout.Rigid(func(gtx C) D {
			return a.primaryButton(gtx, &a.createBtn, "Create")
		}),
	)
}

// createOptions is the game-creation option cluster to the left of the Create
// button: mode radios, seat count, agent policy, and the invite-only toggle.
func (a *App) createOptions(countLabel string) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				rb := material.RadioButton(a.th, &a.modeEnum, "cooperative", "Co-op")
				rb.Color = colFg
				return rb.Layout(gtx)
			}),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D {
				rb := material.RadioButton(a.th, &a.modeEnum, "competitive", "Competitive")
				rb.Color = colFg
				return rb.Layout(gtx)
			}),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D {
				rb := material.RadioButton(a.th, &a.modeEnum, "teams", "Teams")
				rb.Color = colFg
				return rb.Layout(gtx)
			}),
			layout.Rigid(hSpacer(12)),
			layout.Rigid(a.body(countLabel, colMuted)),
			layout.Rigid(hSpacer(4)),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Max.X = gtx.Dp(48)
				gtx.Constraints.Min.X = gtx.Dp(48)
				return a.editorBox(gtx, &a.countEd, "2")
			}),
			// Agent policy: whether idle jetricks-agent players may take seats, and at
			// most how many. Hidden for invite-only games — there the agent policy
			// is per-invite (createGame is called with maxAgents 0), so the
			// "Allow agents" toggle doesn't apply; it reappears if invite-only is
			// unchecked.
			layout.Rigid(func(gtx C) D {
				if a.inviteOnlyCb.Value {
					return D{}
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(hSpacer(12)),
					layout.Rigid(func(gtx C) D {
						cb := material.CheckBox(a.th, &a.allowAgentsCb, "Allow agents")
						cb.Color = colFg
						cb.IconColor = colAccent
						return cb.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						if !a.allowAgentsCb.Value {
							return D{}
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(hSpacer(8)),
							layout.Rigid(a.body("Max:", colMuted)),
							layout.Rigid(hSpacer(4)),
							layout.Rigid(func(gtx C) D {
								gtx.Constraints.Max.X = gtx.Dp(40)
								gtx.Constraints.Min.X = gtx.Dp(40)
								return a.editorBox(gtx, &a.maxAgentsEd, "1")
							}),
						)
					}),
				)
			}),
			layout.Rigid(hSpacer(12)),
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(a.th, &a.inviteOnlyCb, "Invite only")
				cb.Color = colFg
				cb.IconColor = colAccent
				return cb.Layout(gtx)
			}),
		)
	}
}

// invitedTo reports whether this player holds a pending invitation to gameID.
func (a *App) invitedTo(gameID string) bool {
	lb := a.getLobby()
	if lb == nil {
		return false
	}
	return lb.InviteTo(gameID) != nil
}

// agentName appends the agent marker to a player's display name.
func agentName(name string, agent bool) string {
	if agent {
		return name + " [agent]"
	}
	return name
}

func (a *App) gameRow(gtx C, g lobby.GameListing, abandoned bool) D {
	btns := a.gameButtons(g.GameID)
	lb := a.getLobby()
	me := ""
	if lb != nil {
		me = lb.PlayerID()
	}
	joined := rosterHas(g, me)
	joinable := g.Status == config.GameStatusCreated || g.Status == config.GameStatusStarting
	canJoin := joinable && len(g.Players) < g.PlayerCount
	// Invite-only games are joined via the pop-up (or by the creator): don't
	// offer a Join button to random browsers. The creator, and anyone holding
	// a pending invitation to this game, keep theirs.
	if g.InviteOnly {
		if lb == nil || (me != g.CreatorID && !a.invitedTo(g.GameID)) {
			canJoin = false
		}
	}
	// A player who went "Back to Lobby" while holding a seat rejoins through
	// the same row — even into a full or already-running game.
	rejoin := joined && gameAlive(g.Status)
	canSpectate := !rejoin && (g.Status == config.GameStatusInProgress ||
		(joinable && len(g.Players) >= g.PlayerCount))
	// The creator of an invite-only game that still has open seats can re-open
	// the invitee picker to send more invitations — even after joining and
	// returning to the lobby (the picker only opens automatically at creation).
	canReinvite := g.InviteOnly && lb != nil && me == g.CreatorID &&
		joinable && len(g.Players) < g.PlayerCount

	teams := g.Mode == config.ModeTeams
	// In an invite-only game every roster member was let in by name, so spell
	// their state out for the inviter: joined, and ready or not.
	nameOf := func(p lobby.PlayerSummary) string {
		n := agentName(p.Name, p.Agent)
		switch {
		case g.InviteOnly && p.Ready:
			n += " (joined · ready ✓)"
		case g.InviteOnly:
			n += " (joined)"
		case p.Ready:
			n += " ✓"
		}
		return n
	}
	var names []string
	if teams {
		// Group the roster by team: "A: alice, bob · B: carol"
		for t := 0; t < config.TeamCount; t++ {
			var team []string
			for _, p := range g.Players {
				if p.Team != t {
					continue
				}
				team = append(team, nameOf(p))
			}
			names = append(names, fmt.Sprintf("%s: %s", teamName(t), strings.Join(team, ", ")))
		}
	} else {
		for _, p := range g.Players {
			names = append(names, nameOf(p))
		}
	}
	// The status token reads from this player's point of view: a game you hold
	// a seat in shows as "joined" (waiting to start) or "playing" (started).
	statusTxt, statusCol := " · "+string(g.Status), colFg
	if joined {
		statusCol = colNATSGreen
		if g.Status == config.GameStatusInProgress {
			statusTxt = " · playing"
		} else if joinable {
			statusTxt = " · joined"
		}
	}
	info := fmt.Sprintf("%s · %s · %d/%d", shortID(g.GameID), g.Mode.String(), len(g.Players), g.PlayerCount)
	var extra string
	if g.InviteOnly {
		extra += " · invite only"
	}
	if g.MaxAgents > 0 {
		extra += fmt.Sprintf(" · agents %d/%d", g.AgentCount(), g.MaxAgents)
	}

	// The creator of an invite-only game sees each outstanding invitation's
	// state under the roster line, with a retract/dismiss action per invitee.
	var invites []lobby.Invitation
	if g.InviteOnly && lb != nil && me == g.CreatorID {
		invites = lb.SentInvites(g.GameID)
	}

	sep := ", "
	if teams {
		sep = " · "
	}
	confirming := abandoned && a.confirmDeleteID == g.GameID
	infoCol := func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(a.body(info, colFg)),
					layout.Rigid(a.body(statusTxt, statusCol)),
					layout.Rigid(func(gtx C) D {
						if extra == "" {
							return D{}
						}
						return a.body(extra, colFg)(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						if !abandoned {
							return D{}
						}
						return a.body(" · abandoned", colErr)(gtx)
					}),
				)
			}),
			layout.Rigid(a.body(strings.Join(names, sep), colMuted)),
			layout.Rigid(func(gtx C) D { return a.inviteStatusRows(gtx, g, invites) }),
		)
	}
	return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		// The delete confirmation replaces the row's action buttons (so a stray
		// click can't join or delete while the question is up) and gets its own
		// line under the game info, so the question and buttons never squeeze
		// the info text.
		if confirming {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(infoCol),
				layout.Rigid(spacer(6)),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.body("Are you sure you want to delete this game?", colErr)),
						layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx) }),
						layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &btns.delYes, "Yes, delete") }),
						layout.Rigid(func(gtx C) D { return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx) }),
						layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &btns.delNo, "Cancel") }),
					)
				}),
			)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, infoCol),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						if !canReinvite {
							return D{}
						}
						return layout.Inset{Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
							return a.secondaryButton(gtx, &btns.reinvite, "Invite")
						})
					}),
					layout.Rigid(func(gtx C) D {
						if rejoin {
							// Back into the seat we already hold (any mode —
							// the roster remembers our team).
							return a.primaryButton(gtx, &btns.join, "Rejoin")
						}
						if !canJoin {
							return D{}
						}
						if teams {
							// One join button per team, each enabled while that team has room.
							return layout.Flex{}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									return a.teamJoinButton(gtx, &btns.joinA, g, 0)
								}),
								layout.Rigid(hSpacer(6)),
								layout.Rigid(func(gtx C) D {
									return a.teamJoinButton(gtx, &btns.joinB, g, 1)
								}),
							)
						}
						return a.primaryButton(gtx, &btns.join, "Join")
					}),
					layout.Rigid(func(gtx C) D {
						if canSpectate {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
								return a.secondaryButton(gtx, &btns.spectate, "Spectate")
							})
						}
						return D{}
					}),
					layout.Rigid(func(gtx C) D {
						if !abandoned {
							return D{}
						}
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
							return a.dangerButton(gtx, &btns.del, "Delete")
						})
					}),
				)
			}),
		)
	})
}

// inviteStatusRows renders the creator's per-invitation state lines on an
// invite-only game row: who is still invited (pending — retractable while
// unanswered) and who declined (dismissable). Accepted invitations don't
// appear here — accepting deletes the invitation and puts the player on the
// roster line above, marked "(joined)"/"(joined · ready ✓)".
func (a *App) inviteStatusRows(gtx C, g lobby.GameListing, invites []lobby.Invitation) D {
	if len(invites) == 0 {
		return D{}
	}
	teams := g.Mode == config.ModeTeams
	var rows []layout.FlexChild
	for _, inv := range invites {
		inv := inv
		btn := a.uninviteButton(g.GameID, inv.InviteeID)
		if btn.Clicked(gtx) {
			go a.uninvite(g.GameID, inv.InviteeID)
		}
		label, col, action := "", colGold, "Uninvite"
		if inv.Declined {
			label = fmt.Sprintf("✕ %s declined the invitation", inv.InviteeID)
			col, action = colErr, "Dismiss"
		} else {
			label = fmt.Sprintf("✉ %s invited — waiting…", inv.InviteeID)
			if teams {
				label = fmt.Sprintf("✉ %s invited to team %s — waiting…", inv.InviteeID, teamName(inv.Team))
			}
		}
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body(label, col)),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(func(gtx C) D { return a.smallActionButton(gtx, btn, action, col) }),
				)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// uninviteButton returns the (lazily created) Uninvite/Dismiss button for one
// invitation, keyed by game and invitee.
func (a *App) uninviteButton(gameID, inviteeID string) *widget.Clickable {
	k := gameID + "|" + inviteeID
	b, ok := a.uninviteBtns[k]
	if !ok {
		b = &widget.Clickable{}
		a.uninviteBtns[k] = b
	}
	return b
}

// smallActionButton is the compact bordered list-row action (like
// viewBoardButton) in an arbitrary accent color.
func (a *App) smallActionButton(gtx C, btn *widget.Clickable, label string, col colorN) D {
	return widget.Border{Color: col, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		b := pixelize(material.Button(a.th, btn, label))
		b.Background = colPanel
		b.Color = col
		b.TextSize = unit.Sp(9)
		b.Inset = layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(10), Right: unit.Dp(10)}
		return b.Layout(gtx)
	})
}

// teamJoinButton renders the "Join A (1/2)"-style button for one team of a
// teams-mode listing, hidden once that team is full.
func (a *App) teamJoinButton(gtx C, btn *widget.Clickable, g lobby.GameListing, team int) D {
	n := g.TeamMemberCount(team)
	if n >= g.TeamSize {
		return D{}
	}
	label := fmt.Sprintf("Join %s (%d/%d)", teamName(team), n, g.TeamSize)
	return a.primaryButton(gtx, btn, label)
}

// teamName renders a team index as its display letter.
func teamName(team int) string {
	if team == 0 {
		return "A"
	}
	return "B"
}

func (a *App) handleChatSubmit(gtx C) {
	send := a.chatBtn.Clicked(gtx)
	for {
		ev, ok := a.chatEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			send = true
		}
	}
	if send {
		text := strings.TrimSpace(a.chatEd.Text())
		if text != "" {
			a.chatEd.SetText("")
			go a.sendChat(text)
		}
	}
}

func (a *App) gameButtons(id string) *gameRowBtns {
	b, ok := a.gameBtns[id]
	if !ok {
		b = &gameRowBtns{}
		a.gameBtns[id] = b
	}
	return b
}

// --- small layout helpers ---

// pixelize restyles a material button into the 8-bit chrome: pixel face,
// square corners, and a smaller size (the pixel face runs large per point).
func pixelize(b material.ButtonStyle) material.ButtonStyle {
	b.CornerRadius = 0
	b.Font.Typeface = pixelTypeface
	b.TextSize = unit.Sp(11)
	return b
}

// primaryButton renders a filled-accent action (Join, Ready, Create Game,
// Send) in the 8-bit chrome: pixel face, square corners, hard offset shadow.
func (a *App) primaryButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return pixelize(material.Button(a.th, btn, label)).Layout(gtx)
	})
}

// secondaryButton renders a non-primary action (Spectate, Back to Lobby) so it
// reads as clearly clickable: accent-colored pixel label and a chunky accent
// border over the panel background, visually distinct from the filled-accent
// primary buttons without blending into the dark window background.
func (a *App) secondaryButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			b := pixelize(material.Button(a.th, btn, label))
			b.Background = colPanel
			b.Color = colAccent
			return b.Layout(gtx)
		})
	})
}

// dangerButton renders a destructive action (Delete, and its confirmation) in
// the secondary chrome but with the error red, so it cannot be mistaken for
// Join/Spectate.
func (a *App) dangerButton(gtx C, btn *widget.Clickable, label string) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colErr, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			b := pixelize(material.Button(a.th, btn, label))
			b.Background = colPanel
			b.Color = colErr
			return b.Layout(gtx)
		})
	})
}

// viewBoardButton is the compact accent-bordered "View board" action on each
// GAME HISTORY row, making it obvious that a finished game can be opened to see
// its final playfield. It mirrors secondaryButton's styling but with tighter
// padding and a smaller label so it fits a list row.
func (a *App) viewBoardButton(gtx C, btn *widget.Clickable) D {
	return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		b := pixelize(material.Button(a.th, btn, "View board"))
		b.Background = colPanel
		b.Color = colAccent
		b.TextSize = unit.Sp(9)
		b.Inset = layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(10), Right: unit.Dp(10)}
		return b.Layout(gtx)
	})
}

func (a *App) header(txt string) layout.Widget {
	return func(gtx C) D {
		l := a.pixel(unit.Sp(10), txt, colAccent)
		return layout.Inset{Bottom: unit.Dp(5)}.Layout(gtx, l.Layout)
	}
}

func (a *App) body(txt string, c colorN) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, txt)
		l.Color = c
		return l.Layout(gtx)
	}
}

func bordered(gtx C, w layout.Widget) D {
	return widget.Border{Color: colBorder, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(unit.Dp(4)).Layout(gtx, w)
	})
}

func statusText(s lobby.PresenceStatus) string {
	switch s {
	case lobby.StatusInGame:
		return "In Game"
	case lobby.StatusSpectating:
		return "Spectating"
	default:
		return "In Lobby"
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sortedGames(m map[string]lobby.GameListing) []lobby.GameListing {
	out := make([]lobby.GameListing, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func sortedPlayers(m map[string]lobby.PlayerPresence) []lobby.PlayerPresence {
	out := make([]lobby.PlayerPresence, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
