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

// A rejected write's recoil (bridge.go, trackRecoil, drawBoard): the piece
// FLIES BACK from where the lost step wanted it to where the CAS failure
// puts it, and BUZZES there from the sudden stop. casSnapDur is the flight:
// the piece covers the whole way back in ten frames, speeding up all the way
// (a quadratic ease-in), so it arrives at full tilt and the buzz that follows
// reads as the impact of stopping dead. casKickDur/casKickCycles/casKickAmp
// shape that buzz: a sixth of a cell — half the garbage impact's wobble,
// and the piece alone rather than the whole well, so a lost move never
// reads as an attack landing — twelve swings in a second, a 12 Hz buzz
// a 60 Hz frame can still resolve (faster would only alias into a jitter),
// dying down slowly (a square-root decay: still at half strength three
// quarters of the way through). Nothing cuts it short: it follows the piece
// wherever the board draws it meanwhile — the repair replaying the moves
// that were queued behind the loss, gravity, the player steering on — and
// runs its full length. The flight starts from where the lost step wanted
// the piece (the flash's target, casKickFrom); on a fast link the frames
// never drew it there, so it is a lunge and back, the move that was lost
// drawn in the losing. casWantBlink is the period of the outline blinking
// where the lost step wanted the piece instead: blinks across flashDur, lit
// for the first half of each, the same hard arcade square wave as the row
// strobes.
const (
	casSnapDur    = 160 * time.Millisecond
	casKickDur    = 1000 * time.Millisecond
	casKickCycles = 12
	casKickAmp    = 6.0                     // the buzz's swing: a cell over this
	casRecoilDur  = casSnapDur + casKickDur // the whole recoil: the flight, then the buzz
	casWantBlink  = 150 * time.Millisecond
)

// casRecoilOffset is the recoil's paint offset (px) at `since` past the kick
// epoch. from is where the lost step wanted the piece relative to where it
// stands, in cells (columns across, rows down): for casSnapDur the piece is
// painted on its way back from there, then it buzzes along the line it
// arrived on. A zero from — a lost spawn, lock or gravity step, nothing that
// was headed anywhere — skips the flight and starts the buzz at once, along
// casKickOffset's diagonal. Zero outside the window, including the idle
// zero-epoch state, whose `since` is enormous.
func casRecoilOffset(cellPx int, from [2]float64, since time.Duration) image.Point {
	if since < 0 {
		return image.Point{}
	}
	if from == ([2]float64{}) {
		return casKickOffset(cellPx, since)
	}
	if since < casSnapDur {
		t := float64(since) / float64(casSnapDur)
		k := 1 - t*t // speeding up all the way: it hits the fallback at full tilt
		return image.Pt(int(math.Round(from[0]*float64(cellPx)*k)), int(math.Round(from[1]*float64(cellPx)*k)))
	}
	n := math.Hypot(from[0], from[1])
	return casVibration(cellPx, from[0]/n, from[1]/n, since-casSnapDur)
}

// casKickOffset is the buzz alone, from a standstill: a shudder back and
// forth along one diagonal — the full swing across, half that up and down
// (casVibration).
func casKickOffset(cellPx int, since time.Duration) image.Point {
	return casVibration(cellPx, 1, 0.5, since)
}

// casVibration is the buzz along the direction (dx, dy) at `since` past its
// start: a shudder of casKickAmp back and forth, dying down (square root of
// the time left) to rest across casKickCycles swings, and starting and
// ending exactly at rest. Zero outside the window.
func casVibration(cellPx int, dx, dy float64, since time.Duration) image.Point {
	if since < 0 || since >= casKickDur {
		return image.Point{}
	}
	t := float64(since) / float64(casKickDur)
	swing := float64(cellPx) / casKickAmp * math.Sqrt(1-t) * math.Sin(2*math.Pi*casKickCycles*t)
	return image.Pt(int(math.Round(swing*dx)), int(math.Round(swing*dy)))
}

// trackRecoil runs on the UI goroutine each frame the own board draws, and
// runs the rejected write's recoil on the local piece as the board draws it
// — from the kick epoch (casKickAt, from the pump) for casRecoilDur, on
// whatever cells the piece has this frame. Returns the cells to paint at the
// recoil's offset (casRecoilOffset), nil once it is over, and keeps the
// frames coming while it runs.
func (a *App) trackRecoil(gtx C, snap engine.BoardSnapshot, localIdx int, kickAt time.Time) map[[2]int]bool {
	if kickAt.IsZero() || gtx.Now.Sub(kickAt) >= casRecoilDur {
		return nil
	}
	cur := activePieceCells(snap, localIdx)
	if cur == nil {
		return nil
	}
	animate(gtx)
	return cur
}

// sameCells reports whether two cell sets hold the same squares.
func sameCells(a, b map[[2]int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for rc := range a {
		if !b[rc] {
			return false
		}
	}
	return true
}

// cellsDisplacement is where the squares of `from` sit relative to those of
// `to`, centroid to centroid, in cells: columns across, rows down. A shifted
// piece gives a whole step; a rotated one whatever its center moved.
func cellsDisplacement(from, to map[[2]int]bool) [2]float64 {
	centroid := func(cells map[[2]int]bool) (x, y float64) {
		for rc := range cells {
			x += float64(rc[1])
			y += float64(rc[0])
		}
		n := float64(len(cells))
		return x / n, y / n
	}
	fx, fy := centroid(from)
	tx, ty := centroid(to)
	return [2]float64{fx - tx, fy - ty}
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
