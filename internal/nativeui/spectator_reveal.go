package nativeui

import (
	"fmt"
	"sort"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// A spectator's screen gets the replay's ending live. The moment the game is
// decided — the last player standing, a team fully out, the cooperative crew
// topped out — the winning board(s) wear the winner show (replay_winner.go:
// the frame pulse, the rising WINNER / WINNERS / GAME OVER banner under the
// rank-graded trophy and its prize piece), the beaten boards read OUT behind
// a wash, the winners' names go gold in bold italic on the boards and in the
// legend while the beaten keep their board colors, and a result box beside
// the boards sets the verdict in bold italic. The show's clock is the frame
// the decision first landed on this screen (App.decidedAt); its rank is the
// game's standing in its replay bucket (config.ReplayRank) — provisional
// from the totals the spectator's engine folded, then final the moment the
// lobby receives the archive record. A winning player's own screen wears
// the crown too: the show floats over their board, on top of the victory
// fireworks (crownBoardOnTop), beside their game-over box, and their legend
// reveals the winners the same way; a beaten player's screen stays as it
// was — the reveal is the spectators' (and the replay's) to watch.

// liveOutcome is a decided live game as a spectator's — or a winning
// player's — screen sees it; the zero value (winTeam -1) while the game is
// undecided, and on a beaten player's screen.
type liveOutcome struct {
	decided  bool
	winners  map[string]bool // player IDs on the winning side (co-op: the whole crew)
	winTeam  int             // teams: the winning team; -1 otherwise, and on a draw
	scores   map[string]int  // each player's line-clear score as the spectator's engine folded it
	banner   string          // WINNER / WINNERS / GAME OVER
	verdict  string          // TEAM A WINS! / ALICE WINS! / DRAW / FINAL SCORE n
	at       time.Time       // when the decision first landed on this screen: the show's clock
	rank, of int             // the game's standing in its replay bucket
}

// wins reports whether a player is on the decided game's winning side.
func (oc liveOutcome) wins(id string) bool { return oc.decided && oc.winners[id] }

// fx is the crown the outcome's winning boards wear.
func (oc liveOutcome) fx() winnerFX { return newWinnerFX(oc.at, oc.rank, oc.of, oc.banner) }

// spectatorVerdict resolves a live game's outcome from what a spectator's
// engine knows — the roster, the eliminations it folded from the game_over
// events, and (co-op) the shared game over: whether the game is decided,
// the winning side's player IDs, and the winning team (-1 unless teams, or
// on a draw). Competitive is decided once all but one player are out — the
// survivor wins; all out at once is a draw. Teams is decided once a team is
// fully out — the other team wins; both at once is a draw. Co-op ends for
// everyone at the first top-out, and the crew shares the board.
func spectatorVerdict(gmode config.GameMode, players []lobby.PlayerSummary, eliminated func(string) bool, gameOver bool) (decided bool, winners map[string]bool, winTeam int) {
	winners, winTeam = map[string]bool{}, -1
	switch gmode {
	case config.ModeTeams:
		members, alive := [config.TeamCount]int{}, [config.TeamCount]int{}
		for _, p := range players {
			if p.Team < 0 || p.Team >= config.TeamCount {
				continue
			}
			members[p.Team]++
			if !eliminated(p.PlayerID) {
				alive[p.Team]++
			}
		}
		out := func(t int) bool { return members[t] > 0 && alive[t] == 0 }
		switch {
		case out(0) && out(1):
			decided = true
		case out(0):
			decided, winTeam = true, 1
		case out(1):
			decided, winTeam = true, 0
		}
		for _, p := range players {
			if winTeam >= 0 && p.Team == winTeam {
				winners[p.PlayerID] = true
			}
		}
	case config.ModeCooperative:
		decided = gameOver
		for _, p := range players {
			if decided {
				winners[p.PlayerID] = true
			}
		}
	default:
		elim := 0
		for _, p := range players {
			if eliminated(p.PlayerID) {
				elim++
			}
		}
		decided = len(players) > 1 && elim >= len(players)-1
		for _, p := range players {
			if decided && !eliminated(p.PlayerID) {
				winners[p.PlayerID] = true
			}
		}
	}
	return decided, winners, winTeam
}

// playerVerdict resolves a live game's outcome on a player's own screen,
// from their engine's verdict (view.won, UpdateGameOver): a competitive win
// is the last player standing — the winner alone; a teams win is the other
// team fully out — the whole team wins, including members topped out
// earlier (the engine re-emits the win to them); the cooperative crew shares
// one board and the shared game over ends the run for all of them, so the
// crew is crowned the way it is on a spectator's screen — the run's rank is
// the prize. A beaten player's screen resolves nothing.
func playerVerdict(gmode config.GameMode, view gameView, me string, myTeam int) (decided bool, winners map[string]bool, winTeam int) {
	winners, winTeam = map[string]bool{}, -1
	switch gmode {
	case config.ModeCooperative:
		decided = view.gameOver
		for _, p := range view.players {
			if decided {
				winners[p.PlayerID] = true
			}
		}
	case config.ModeTeams:
		decided = view.gameOver && view.won
		if decided {
			winTeam = myTeam
			for _, p := range view.players {
				if p.Team == myTeam {
					winners[p.PlayerID] = true
				}
			}
		}
	default:
		decided = view.gameOver && view.won
		if decided {
			winners[me] = true
		}
	}
	return decided, winners, winTeam
}

// liveVerdict is the result box's verdict line for a decided game.
func liveVerdict(gmode config.GameMode, players []lobby.PlayerSummary, winners map[string]bool, winTeam int, score int) string {
	switch gmode {
	case config.ModeTeams:
		if winTeam >= 0 && winTeam < config.TeamCount {
			return "TEAM " + teamName(winTeam) + " WINS!"
		}
		return "DRAW"
	case config.ModeCooperative:
		return fmt.Sprintf("FINAL SCORE %d", score)
	}
	var names []string
	for _, p := range players {
		if winners[p.PlayerID] {
			names = append(names, p.Name)
		}
	}
	return winnersVerdict(names)
}

// liveBanner is the word the winning board floats.
func liveBanner(gmode config.GameMode) string {
	switch gmode {
	case config.ModeTeams:
		return "WINNERS"
	case config.ModeCooperative:
		return "GAME OVER"
	}
	return "WINNER"
}

// resolveOutcome is the frame's outcome for the game screen: the zero value
// while the game is undecided and on a beaten player's screen; otherwise the
// decision — a spectator's from the eliminations their engine folded
// (spectatorVerdict), a player's from their engine's own verdict
// (playerVerdict); the engine's initial mode tells the two apart, its current
// one having moved on to game over on a finished player's screen (and on a
// co-op spectator's) — stamped with the moment it first landed here (the
// show's clock) and ranked (rankLiveGame). UI goroutine.
func (a *App) resolveOutcome(eng *engine.Engine, view gameView, gmode config.GameMode, now time.Time) liveOutcome {
	none := liveOutcome{winTeam: -1}
	var decided bool
	var winners map[string]bool
	var winTeam int
	if eng.InitialMode() == engine.ModeSpectator {
		decided, winners, winTeam = spectatorVerdict(gmode, view.players, eng.IsEliminated, view.gameOver)
	} else {
		decided, winners, winTeam = playerVerdict(gmode, view, eng.PlayerID(), eng.TeamIdx())
	}
	if !decided {
		return none
	}
	oc := liveOutcome{
		decided: true, winners: winners, winTeam: winTeam, scores: eng.PlayerScores(),
		banner: liveBanner(gmode), verdict: liveVerdict(gmode, view.players, winners, winTeam, view.score),
	}
	a.mu.Lock()
	if a.decidedAt.IsZero() {
		a.decidedAt = now
	}
	oc.at = a.decidedAt
	rank, of, final := a.liveRank, a.liveOf, a.liveRankFinal
	a.mu.Unlock()
	if !final {
		rank, of, final = a.rankLiveGame(eng, view, oc, gmode, now, rank, of)
		a.mu.Lock()
		a.liveRank, a.liveOf, a.liveRankFinal = rank, of, final
		a.mu.Unlock()
	}
	oc.rank, oc.of = rank, of
	return oc
}

// rankLiveGame places a decided game in its replay bucket (config.ReplayRank,
// the order behind the history's TOP 10 mark): against its archive record
// once the lobby has it (final — the archiver publishes the record moments
// after the game ends), until then against a provisional record built from
// what the spectator's engine folded (liveRecord), computed once and kept
// (rank, of are the values kept so far; 0 = none yet). Without a lobby the
// game simply stands alone.
func (a *App) rankLiveGame(eng *engine.Engine, view gameView, oc liveOutcome, gmode config.GameMode, now time.Time, rank, of int) (int, int, bool) {
	lb := a.getLobby()
	if lb == nil {
		return 1, 1, true
	}
	if rec, ok := lb.ArchiveFor(eng.GameID()); ok {
		rank, of = config.ReplayRank(lb.Archives(), rec)
		return rank, of, true
	}
	if rank == 0 {
		rank, of = config.ReplayRank(lb.Archives(), liveRecord(eng, view, oc, gmode, now))
	}
	return rank, of, false
}

// liveRecord is the provisional archive record of a just-decided game, as
// the spectator's engine can reconstruct it: the roster with each player's
// line-clear score, team and agent seat (the bucket and the winners), the
// team totals or the shared total — the same headline numbers the archiver
// will publish, so the provisional rank normally IS the final one. The
// game's duration is unknown here (zero), which only matters as a tie-break
// between equal headline scores.
func liveRecord(eng *engine.Engine, view gameView, oc liveOutcome, gmode config.GameMode, now time.Time) config.ArchiveRecord {
	rec := config.ArchiveRecord{
		GameID: eng.GameID(), Mode: gmode, PlayerCount: eng.PlayerCount(), TeamSize: eng.TeamSize(),
		WinningTeam: oc.winTeam, FinishedAt: now,
	}
	for _, p := range view.players {
		rec.Players = append(rec.Players, config.PlayerResult{
			PlayerID: p.PlayerID, Score: oc.scores[p.PlayerID], Team: p.Team, Agent: p.Agent, Winner: oc.winners[p.PlayerID],
		})
	}
	switch gmode {
	case config.ModeCooperative:
		rec.TotalScore = view.score
	case config.ModeTeams:
		rec.TeamScores = append([]int(nil), view.teamScores[:]...)
		rec.TeamLevels = append([]int(nil), view.teamLevels[:]...)
	}
	return rec
}

// spectatorResultBox announces a decided competitive or teams game to a
// spectator beside the boards (never over them — the final playfields stay
// fully visible): GAME OVER, the verdict in bold italic gold (a draw plainly),
// the final scores — both teams', or every player's, winners first — and the
// Back to Lobby button.
func (a *App) spectatorResultBox(gtx C, view gameView, oc liveOutcome, gmode config.GameMode) D {
	verdict := a.pixelEmph(unit.Sp(12), oc.verdict, colGold)
	if oc.verdict == "DRAW" {
		verdict = a.pixel(unit.Sp(12), "DRAW", colMuted).Layout
	}
	var score string
	if gmode == config.ModeTeams {
		score = fmt.Sprintf("TEAM %s %d (lvl %d) · TEAM %s %d (lvl %d)",
			teamName(0), view.teamScores[0], view.teamLevels[0],
			teamName(1), view.teamScores[1], view.teamLevels[1])
	} else {
		players := append([]lobby.PlayerSummary(nil), view.players...)
		sort.SliceStable(players, func(i, j int) bool {
			if wi, wj := oc.winners[players[i].PlayerID], oc.winners[players[j].PlayerID]; wi != wj {
				return wi
			}
			return oc.scores[players[i].PlayerID] > oc.scores[players[j].PlayerID]
		})
		parts := make([]string, 0, len(players))
		for _, p := range players {
			parts = append(parts, fmt.Sprintf("%s %d", agentName(p.Name, p.Agent), oc.scores[p.PlayerID]))
		}
		score = joinParts(parts)
	}
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, colBg, func(gtx C) D {
				return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.pixel(unit.Sp(18), "GAME OVER", colFg).Layout),
						layout.Rigid(spacer(10)),
						layout.Rigid(verdict),
						layout.Rigid(spacer(8)),
						layout.Rigid(func(gtx C) D {
							l := material.Body1(a.th, score)
							l.Color = colGold
							return l.Layout(gtx)
						}),
						layout.Rigid(spacer(14)),
						layout.Rigid(func(gtx C) D {
							return a.secondaryButton(gtx, &a.backBtn, "Back to Lobby")
						}),
					)
				})
			})
		})
	})
}

// boardLabel is a board's (or a legend line's) name in its color — gold in
// bold italic once it has won.
func (a *App) boardLabel(name string, col colorN, won bool) layout.Widget {
	return func(gtx C) D {
		l := material.Body2(a.th, name)
		l.Color = col
		if won {
			l.Color, l.Font.Weight, l.Font.Style = colGold, font.Bold, font.Italic
		}
		return l.Layout(gtx)
	}
}

// joinParts joins score parts with the history's middle-dot separator.
func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}
