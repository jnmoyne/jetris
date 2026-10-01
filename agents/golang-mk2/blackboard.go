package main

// The blackboard (guide §1.4): where the agents of one team tell each other
// what they are about to do. A KV bucket, JETRIS_BLACKBOARD — a JetStream
// stream with last-value-per-key, a watch, a per-key TTL and one global
// revision order, the same pattern the lobby is built on — holds one key
// per seat of a playfield, <gameID>.<board>.<seat>, whose value is that
// seat's CLAIM: the piece it holds, where it means to put it, and whether
// it has (claim). A claim is written straight to the key's subject with a
// per-message TTL (claimTTL), so an agent that dies takes its claims with
// it; and it is read by the seat's teammates alone — the crew of a
// cooperative game, or one team of a teams game — the way a team's voice
// room is (an agent never reads another board's keys). Nothing goes on the
// blackboard that its writer may not see (guide §1): the piece in play and
// an intention about it, never a preview, a seed or a sequence number.
//
// The bucket is one ordered stream, so its revisions order every claim the
// same for everyone: an agent posts its claim, waits for its own echo on
// its watch (awaitRev), and has then seen every claim made before its —
// the linearization coord.go's conflict rules run on.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	blackboardBucket    = "JETRIS_BLACKBOARD"
	claimTTL            = 10 * time.Second // per-message TTL on every claim: a dead agent's claims evaporate
	blackboardMarkerTTL = 30 * time.Second // LimitMarkerTTL: enables the per-key TTL, and is how long an expired key's marker lingers
	echoTimeout         = 2 * time.Second  // post-then-look: a claim whose echo never comes is played uncoordinated
	claimVersion        = 1
	phasePlanned        = "planned"
	phaseLocked         = "locked"
)

// claim is one seat's intention for the piece it holds, as the blackboard
// carries it. Cells are [row, col] in the claimant's frame at claim time —
// a teammate re-derives a planned claim's landing from its column and
// orientation on its own board (buildProjection), so a collapse between
// the two frames costs nothing.
type claim struct {
	V        int       `json:"v"`
	Agent    string    `json:"agent"`
	Seat     int       `json:"seat"`
	Piece    int       `json:"piece"`
	Type     int       `json:"type"`
	Rot      int       `json:"rot"`
	Col      int       `json:"col"`
	Cells    [4][2]int `json:"cells"`
	Lines    int       `json:"lines"`
	Phase    string    `json:"phase"`
	ETAms    int       `json:"eta_ms"`
	SpawnCol int       `json:"spawn_col"`
	T        string    `json:"t"`

	rev uint64 // the KV revision the watcher delivered it at (not on the wire)
}

// coordinator is this game's place on the blackboard: the board we play
// on, our key, and the filter the watch follows.
type coordinator struct {
	board string
	key   string
	watch string
}

// boardName names the playfield a seat coordinates on: "crew" on the crew's
// board, "t<N>" on a team's; "" where there is no team to coordinate with —
// a competitive board, a crew scored per seat, a playfield of one.
func boardName(mode, team int, individual bool, seatsOnPF int) string {
	if seatsOnPF <= 1 {
		return ""
	}
	switch mode {
	case modeCooperative:
		if individual {
			return ""
		}
		return "crew"
	case modeTeams:
		return "t" + strconv.Itoa(team)
	}
	return ""
}

// claimKey is a seat's key on the blackboard. A game ID with a dot in it
// (none exists: names are letters, digits, - and _, and UUIDs use dashes)
// would break the key's tokens, so it gets no key — no coordination —
// rather than a broken one.
func claimKey(gameID, board string, seat int) string {
	if gameID == "" || board == "" || strings.Contains(gameID, ".") {
		return ""
	}
	return gameID + "." + board + "." + strconv.Itoa(seat)
}

// ensureBlackboard creates or updates the blackboard bucket: memory
// storage (nothing on it outlives a piece), LimitMarkerTTL so every claim
// can carry its TTL. Idempotent, so it serves both connect and the watch's
// recovery of a bucket deleted underneath a running agent. A server too
// old for per-key TTLs (ErrLimitMarkerTTLNotSupported) means no blackboard.
func (a *Agent) ensureBlackboard(ctx context.Context) (jetstream.KeyValue, error) {
	return a.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: a.bbBucket, Storage: jetstream.MemoryStorage, LimitMarkerTTL: blackboardMarkerTTL})
}

// blackboard is the bucket handle, nil while there is none.
func (a *Agent) blackboard() jetstream.KeyValue {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.bb
}

func (a *Agent) setBlackboard(kv jetstream.KeyValue) {
	a.stateMu.Lock()
	a.bb = kv
	a.stateMu.Unlock()
}

// coordinating reports whether this game is played with the blackboard: a
// team to coordinate with, and a bucket to do it on.
func (g *Game) coordinating() bool { return g.coord != nil }

// claimFor is our claim for the piece p at the placement pl, in the phase
// given, expected to land eta from now.
func (g *Game) claimFor(p active, pl placement, phase string, eta time.Duration) claim {
	c := claim{V: claimVersion, Agent: g.a.name, Seat: g.idx, Piece: g.pieceIdx, Type: p.pt, Rot: pl.orient, Col: pl.col,
		Lines: pl.lines, Phase: phase, ETAms: int(eta / time.Millisecond), SpawnCol: g.spawnC, T: nowRFC()}
	for i, d := range pl.dest {
		c.Cells[i] = [2]int{d.r, d.c}
	}
	return c
}

// postClaim writes the claim to our key — a plain KV put carrying the
// per-message TTL, published straight to the key's subject as the lobby's
// presence writes are (the KV client's Put drops TTL headers) — and returns
// its revision. A round trip: NEVER call with g.mu held.
func (g *Game) postClaim(ctx context.Context, c claim) (uint64, error) {
	if g.coord == nil {
		return 0, errors.New("no blackboard")
	}
	b, _ := json.Marshal(c)
	ack, err := g.a.js.Publish(ctx, "$KV."+g.a.bbBucket+"."+g.coord.key, b, jetstream.WithMsgTTL(claimTTL))
	if err != nil {
		return 0, err
	}
	return ack.Sequence, nil
}

// postLockedAsync announces that the piece has locked where it did, without
// waiting for the ack: teammates stop projecting the claim and, until the
// lock's cells reach them off the game stream, project the cells it names.
func (g *Game) postLockedAsync(c claim) {
	if g.coord == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := g.postClaim(ctx, c); err != nil {
			g.bbLogOnce.Do(func() { log.Printf("blackboard: %v (repeats not logged)", err) })
		}
	}()
}

// awaitRev waits until our watch has delivered the blackboard's revision
// rev — our own claim's echo, and with it every claim made before it —
// or the timeout (timedOut then), the game or the agent ends it. NEVER
// call with g.mu held: the watcher folds under it.
func (g *Game) awaitRev(ctx context.Context, rev uint64, timeout time.Duration) (ok, timedOut bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		g.mu.Lock()
		ok := g.bbRev >= rev
		ch := g.bbNotify
		g.mu.Unlock()
		if ok {
			return true, false
		}
		select {
		case <-ch:
		case <-deadline.C:
			return false, true
		case <-ctx.Done():
			return false, false
		case <-g.a.stopCh:
			return false, false
		case <-g.ended:
			return false, false
		}
	}
}

// claimWatch follows our board's keys on the blackboard for the life of
// the game, folding every claim under mu (foldClaimRaw). A watch that ends
// — the bucket deleted underneath us, as a shared server's purge does —
// is re-established, the bucket recreated if need be, and since a new
// bucket's revisions start over, the claims and the revision mark are
// dropped and the piece in play re-planned (replanBBReset).
func (g *Game) claimWatch(ctx context.Context) {
	if g.coord == nil {
		return
	}
	backoff := 500 * time.Millisecond
	established := false
	for !g.a.done(ctx) && !g.isEnded() {
		kv := g.a.blackboard()
		if kv == nil {
			fresh, err := g.a.ensureBlackboard(ctx)
			if err != nil {
				g.bbLogOnce.Do(func() { log.Printf("blackboard: %v (repeats not logged)", err) })
				if !g.wait(ctx, backoff) {
					return
				}
				continue
			}
			g.a.setBlackboard(fresh)
			kv = fresh
		}
		w, err := kv.Watch(ctx, g.coord.watch)
		if err != nil {
			if !g.a.done(ctx) {
				// The bucket is gone (or the server is): recreate it on the
				// way back in, through a fresh handle.
				if fresh, err2 := g.a.ensureBlackboard(ctx); err2 == nil {
					g.a.setBlackboard(fresh)
				} else {
					g.a.setBlackboard(nil)
				}
			}
			if !g.wait(ctx, backoff) {
				return
			}
			continue
		}
		g.mu.Lock()
		if established {
			g.bbEpoch++
			g.bbRev, g.myRev = 0, 0
			g.claims = map[int]claim{}
			if g.piece != nil {
				g.requestReplan(replanBBReset)
			}
			log.Printf("blackboard watch re-established (epoch %d)", g.bbEpoch)
		}
		established = true
		g.mu.Unlock()
		for e := range w.Updates() {
			if e == nil {
				continue // the initial values are in
			}
			g.mu.Lock()
			g.foldClaimRaw(e.Key(), e.Value(), e.Revision(), e.Operation())
			g.mu.Unlock()
		}
		_ = w.Stop()
		if g.a.done(ctx) || g.isEnded() {
			return
		}
		// The watch ended on its own: the bucket was deleted underneath
		// it (nats.go closes the subscription once the consumer's
		// heartbeats stop). Make sure it is there, then watch again.
		if fresh, err := g.a.ensureBlackboard(ctx); err == nil {
			g.a.setBlackboard(fresh)
		}
		if !g.wait(ctx, backoff) {
			return
		}
	}
}

// foldClaimRaw folds one delivery of the watch into the claims: a put is
// the seat's claim (a version we do not speak is none), a delete or a
// purge — a TTL expiry arrives as one — is the seat's claim gone. Every
// delivery moves the revision mark and wakes awaitRev. Caller holds mu.
func (g *Game) foldClaimRaw(key string, val []byte, rev uint64, op jetstream.KeyValueOp) {
	if g.claims == nil {
		g.claims = map[int]claim{}
	}
	if seat, err := strconv.Atoi(key[strings.LastIndexByte(key, '.')+1:]); err == nil {
		var c claim
		if op == jetstream.KeyValuePut && json.Unmarshal(val, &c) == nil && c.V == claimVersion {
			c.rev = rev
			g.claims[seat] = c
		} else {
			delete(g.claims, seat)
		}
	}
	if rev > g.bbRev {
		g.bbRev = rev
	}
	if g.bbNotify != nil {
		close(g.bbNotify)
	}
	g.bbNotify = make(chan struct{})
}

// dropClaim purges our key on the way out of a game (the TTL would take
// it within claimTTL anyway; the purge's own marker expires after
// blackboardMarkerTTL).
func (g *Game) dropClaim(ctx context.Context) {
	if g.coord == nil {
		return
	}
	if kv := g.a.blackboard(); kv != nil {
		_ = kv.Purge(ctx, g.coord.key, jetstream.PurgeTTL(blackboardMarkerTTL))
	}
}

// seatAboutToSpawn reports whether a teammate's claim says their piece has
// locked: their next spawn is imminent, so their spawn box is best kept
// clear (teammateBoxes).
func (g *Game) seatAboutToSpawn(seat int) bool {
	c, ok := g.claims[seat]
	return ok && c.Phase == phaseLocked
}
