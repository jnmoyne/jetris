package nativeui

import (
	"image"
	"math"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/render"
)

// Client-local arcade effects: the hard-drop ghost, the landed-garbage
// detection behind the attacker-colored row strobes, the impact shake, and
// the recoil a rejected write gives the piece. Everything here is derived on
// the UI side from committed board state — nothing is published and nothing
// changes gameplay.

// shakeDur/shakeCycles shape the garbage impact judder: a horizontal sine
// wobble that decays to rest over shakeDur.
const (
	shakeDur    = 420 * time.Millisecond
	shakeCycles = 4
)

// boardShakeOffset is the impact-shake X offset (px) at `since` past the shake
// epoch: an amplitude of a third of a cell decaying linearly to zero across
// shakeCycles full wobbles. Zero outside the window — including the idle
// zero-epoch state, whose `since` is enormous.
func boardShakeOffset(cellPx int, since time.Duration) int {
	if since < 0 || since >= shakeDur {
		return 0
	}
	t := float64(since) / float64(shakeDur)
	amp := float64(cellPx) / 3 * (1 - t)
	return int(amp * math.Sin(2*math.Pi*shakeCycles*t))
}

// A rejected write's recoil (bridge.go, drawBoard): casKickDur/casKickCycles
// shape the vibration the piece does where the CAS failure puts it back. It
// is deliberately NOT the garbage impact's wobble — half its duration, a
// sixth of a cell against that one's third, and it moves the piece alone
// rather than the whole well — so a lost move never reads as an attack
// landing. casWantBlink is the period of the outline blinking where the lost
// step wanted the piece instead: four blinks across flashDur, lit for the
// first half of each, the same hard arcade square wave as the row strobes.
const (
	casKickDur    = 240 * time.Millisecond
	casKickCycles = 3
	casWantBlink  = 150 * time.Millisecond
)

// casKickOffset is the recoil's paint offset (px) at `since` past the kick
// epoch: a shudder back and forth along one diagonal — a sixth of a cell
// across, half that up and down — decaying linearly to rest across
// casKickCycles swings, and starting and ending exactly at rest. Three swings
// in 240 ms is a buzz a 60 Hz frame can still resolve; faster would only
// alias into a jitter. Zero outside the window, including the idle zero-epoch
// state, whose `since` is enormous.
func casKickOffset(cellPx int, since time.Duration) image.Point {
	if since < 0 || since >= casKickDur {
		return image.Point{}
	}
	t := float64(since) / float64(casKickDur)
	swing := float64(cellPx) / 6 * (1 - t) * math.Sin(2*math.Pi*casKickCycles*t)
	return image.Pt(int(math.Round(swing)), int(math.Round(swing/2)))
}

// activePieceCells is the local player's falling piece exactly as the drawn
// board has it — read off the snapshot rather than from a piece the engine
// holds, so the recoil vibrates whatever the player is looking at (the piece
// snapped back, and the moves the repair replays behind it). Nil when no
// piece of theirs is on the board.
func activePieceCells(snap engine.BoardSnapshot, playerIdx int) map[[2]int]bool {
	var cells map[[2]int]bool
	for r := 0; r < snap.Height && r < len(snap.Rows); r++ {
		row := snap.Rows[r]
		for c := 0; c < snap.Width && c < len(row.Cells); c++ {
			if cell := row.Cells[c]; cell.Active && cell.PlayerIdx == playerIdx {
				if cells == nil {
					cells = make(map[[2]int]bool, 4)
				}
				cells[[2]int{r, c}] = true
			}
		}
	}
	return cells
}

// ghostCells returns the hard-drop ghost for the local player's falling piece:
// the cells of its landing position keyed by (row, col), or nil when there is
// no falling piece or it already rests on its destination. Shared boards
// (coop/teams) drop through HardDropDestinationCoop — exactly the collision
// rules the real hard drop uses, other players' active pieces included.
func ghostCells(snap engine.BoardSnapshot, playerIdx int, gmode config.GameMode) map[[2]int]game.PieceType {
	pf := &game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows}
	p := pf.ActivePieceForPlayer(playerIdx)
	if p == nil {
		return nil
	}
	var dest game.Piece
	if gmode == config.ModeCompetitive {
		dest = game.HardDropDestination(*p, pf)
	} else {
		dest = game.HardDropDestinationCoop(*p, pf, playerIdx)
	}
	if dest.Row == p.Row {
		return nil // already resting on the stack: nothing to preview
	}
	cells := make(map[[2]int]game.PieceType, 4)
	for _, rc := range dest.Cells() {
		cells[[2]int{rc[0], rc[1]}] = p.Type
	}
	return cells
}

// landedGarbage returns the strobes for the garbage rows that arrived on a
// board since prev adversarial rows were last observed there — cur is the
// count now. New garbage always fills the BOTTOM rows (the shrink shifts older
// garbage up with the stack), so the new arrivals are the bottom cur−prev
// rows, each strobing in its own attacker's color. Nil when nothing landed.
func landedGarbage(snap engine.BoardSnapshot, prev, cur int, now time.Time) map[int]rowStrobe {
	if cur <= prev {
		return nil
	}
	rows := make(map[int]rowStrobe, cur-prev)
	for r := snap.Height - (cur - prev); r < snap.Height; r++ {
		rows[r] = rowStrobe{start: now, col: render.PlayerColorRGBA(garbageAttacker(snap.Rows[r]))}
	}
	return rows
}

// adversarialRows is the snapshot's bottom-anchored garbage row count.
func adversarialRows(snap engine.BoardSnapshot) int {
	pf := game.Playfield{Width: snap.Width, Height: snap.Height, Rows: snap.Rows}
	return pf.AdversarialRowCount()
}

// detectGarbage compares the own-board snapshot's adversarial-row count
// against the last one observed and, when it grew, strobes the newly landed
// rows in their attacker's color and kicks the impact shake. The first
// observation of a game only seeds the count — a rejoin must not celebrate
// the stack it finds. Runs on the UI goroutine each frame; the strobes land
// next frame (hence the animate), which at frame cadence is imperceptible.
func (a *App) detectGarbage(gtx C, snap engine.BoardSnapshot) {
	cur := adversarialRows(snap)
	a.mu.Lock()
	prev, seen := a.garbageRows, a.garbageSeen
	a.garbageRows, a.garbageSeen = cur, true
	if !seen {
		a.mu.Unlock()
		return
	}
	now := time.Now()
	rows := landedGarbage(snap, prev, cur, now)
	if rows == nil {
		a.mu.Unlock()
		return
	}
	for r, s := range rows {
		a.rowStrobes[r] = s
	}
	a.shakeStart = now
	a.mu.Unlock()
	animate(gtx)
}

// detectGarbageOn is detectGarbage for a SPECTATOR's watched board (keyed
// like specFlash: player index or team): the landed rows strobe there in the
// attacker's color, minus the shake — the impact is the victims' to feel. The
// first sight of a board only seeds its count, so a spectator arriving
// mid-game never strobes the garbage already on it.
func (a *App) detectGarbageOn(gtx C, board int, snap engine.BoardSnapshot) {
	cur := adversarialRows(snap)
	a.mu.Lock()
	prev, seen := a.specGarbageRows[board]
	a.specGarbageRows[board] = cur
	if !seen {
		a.mu.Unlock()
		return
	}
	rows := landedGarbage(snap, prev, cur, time.Now())
	if rows == nil {
		a.mu.Unlock()
		return
	}
	m := a.specRowStrobes[board]
	if m == nil {
		m = make(map[int]rowStrobe)
		a.specRowStrobes[board] = m
	}
	for r, s := range rows {
		m[r] = s
	}
	a.mu.Unlock()
	animate(gtx)
}

// garbageAttacker returns the PlayerIdx stamped on the row's adversarial cells
// — the attacker whose clear sent it. Rows from different attackers strobe in
// different colors.
func garbageAttacker(row game.Row) int {
	for _, c := range row.Cells {
		if c.Adversarial {
			return c.PlayerIdx
		}
	}
	return 0
}
