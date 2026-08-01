package agent

import (
	"context"
	"errors"
	"time"

	"jetris/internal/engine"
	"jetris/internal/game"
)

// Mover is the slice of *engine.Engine the executor drives. The engine
// satisfies it directly; tests substitute a synchronous fake.
type Mover interface {
	MoveLeft()
	MoveRight()
	RotateCW()
	RotateCCW()
	HardDrop()
	Playfield() *game.Playfield
	PieceIdx() uint64
	PlayerIdx() int
	Mode() engine.Mode
}

var (
	// ErrStalled means repeated dispatches of the same move had no observable
	// effect (rejected by collision, or dropped by CAS) — re-plan from live state.
	ErrStalled = errors.New("agent: move had no observable effect")
	// ErrBoardChanged means garbage rows landed after the plan was made — the
	// plan is stale, re-plan from live state.
	ErrBoardChanged = errors.New("agent: board changed mid-plan")
	// ErrGameOver means the engine left player mode (we topped out or won).
	ErrGameOver = errors.New("agent: engine left player mode")
)

// pollInterval is how often the executor re-reads the playfield while waiting
// for a dispatched move to become observable.
const pollInterval = 8 * time.Millisecond

// maxMoveAttempts is how many times the same move is dispatched without
// observable effect before Execute gives up with ErrStalled.
const maxMoveAttempts = 3

// executor intents, derived fresh from live state on every iteration.
type intent int

const (
	intentRotateCW intent = iota
	intentRotateCCW
	intentLeft
	intentRight
	intentDrop
	intentNone
)

// nextIntent returns the single next move that advances cur toward target:
// orientation first (shortest rotation direction), then column, then the
// terminal hard drop. Row is ignored — gravity is free to pull the piece down
// mid-script; the engine recomputes the drop row itself.
func nextIntent(cur, target game.Piece) intent {
	switch ((target.Orientation-cur.Orientation)%4 + 4) % 4 {
	case 1, 2:
		return intentRotateCW
	case 3:
		return intentRotateCCW
	}
	switch {
	case target.Col < cur.Col:
		return intentLeft
	case target.Col > cur.Col:
		return intentRight
	}
	return intentDrop
}

func dispatch(m Mover, in intent) {
	switch in {
	case intentRotateCW:
		m.RotateCW()
	case intentRotateCCW:
		m.RotateCCW()
	case intentLeft:
		m.MoveLeft()
	case intentRight:
		m.MoveRight()
	case intentDrop:
		m.HardDrop()
	}
}

// Execute drives the active piece to target with a sense–act loop: observe the
// committed playfield, dispatch exactly one move toward the target, wait for
// its effect to appear (the engine write-throughs each committed move, so the
// effect shows as soon as the publish commits), repeat, and finish with a hard
// drop. There is deliberately no fire-and-forget: moves can be rejected by
// collision or dropped by a CAS race with incoming garbage, and the engine
// never retries them, so every step is verified against observed state.
//
// startIdx is the engine's PieceIdx when the plan was made and baseGarbage the
// board's AdversarialRowCount at the same moment. Execute returns nil once
// PieceIdx advances past startIdx (the piece locked — by our drop or by
// gravity), ErrBoardChanged when new garbage invalidates the plan, ErrStalled
// when a move refuses to take effect, and ErrGameOver when the engine leaves
// player mode.
func Execute(ctx context.Context, m Mover, target Placement, tn Tuning, startIdx uint64, baseGarbage int) error {
	playerIdx := m.PlayerIdx()
	lastIntent := intentNone
	var lastPiece game.Piece
	attempts := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.Mode() != engine.ModePlayer {
			return ErrGameOver
		}
		if m.PieceIdx() > startIdx {
			return nil // locked (our drop, or gravity beat us to it)
		}
		pf := m.Playfield()
		if pf.AdversarialRowCount() > baseGarbage {
			return ErrBoardChanged
		}
		cur := pf.ActivePieceForPlayer(playerIdx)
		if cur == nil {
			// Between lock and the next spawn's write-through: the PieceIdx
			// check above resolves it on the next iteration.
			sleepCtx(ctx, pollInterval)
			continue
		}

		// A dispatch counts as ineffective only if it repeats the same intent
		// with the piece still in the same orientation and column (gravity may
		// move it down; that is neither progress nor failure). A slide that
		// lands changes the column and resets the counter, so a long run of
		// same-direction slides is not mistaken for a stall.
		in := nextIntent(*cur, target.Target)
		if in == lastIntent && cur.Orientation == lastPiece.Orientation && cur.Col == lastPiece.Col {
			attempts++
		} else {
			attempts = 1
		}
		lastIntent, lastPiece = in, *cur
		if attempts > maxMoveAttempts {
			return ErrStalled
		}

		dispatch(m, in)

		if in == intentDrop {
			// Hard drop publishes without CAS, so it cannot be silently
			// dropped; wait for the lock-in to advance PieceIdx (or the
			// respawn to top us out). On timeout the loop re-derives and,
			// via the attempts counter, eventually reports ErrStalled.
			waitFor(ctx, tn.dropTimeout(), func() bool {
				return m.PieceIdx() > startIdx || m.Mode() != engine.ModePlayer
			})
			continue
		}

		// Wait until the move's effect is observable: the piece's orientation
		// or column changed. A gravity tick moving the piece DOWN is not an
		// effect — the intent is re-derived either way, and the attempts
		// counter catches moves that never land.
		before := *cur
		waitFor(ctx, tn.moveTimeout(), func() bool {
			if m.PieceIdx() > startIdx || m.Mode() != engine.ModePlayer {
				return true
			}
			live := m.Playfield()
			if live.AdversarialRowCount() > baseGarbage {
				return true
			}
			c := live.ActivePieceForPlayer(playerIdx)
			return c == nil || c.Orientation != before.Orientation || c.Col != before.Col
		})

		sleepCtx(ctx, tn.MoveDelay)
	}
}

// waitFor polls cond every pollInterval until it returns true, the timeout
// elapses, or ctx is cancelled.
func waitFor(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		sleepCtx(ctx, pollInterval)
	}
}

// sleepCtx sleeps for d or until ctx is cancelled, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
