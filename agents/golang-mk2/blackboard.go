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
// an intention about it, and tentative placements for the pieces its OWN
// NEXT well shows — never a teammate's sequence, a seed or a sequence
// number.
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

	// Optional, and absent from a claim of an agent that does not say them
	// (guide §1.4): where the piece stood when the claim was written; how it
	// goes down once at its column (dropHard — the default — or dropSoft);
	// the seats whose pieces are in the writer's way, while it waits for
	// them where it is, falling with gravity; the writer's tentative
	// placements for the pieces its own NEXT well shows; and the column it
	// is keeping open for a four-row clear.
	At     *[3]int     `json:"at,omitempty"`
	Drop   string      `json:"drop,omitempty"`
	WaitOn []int       `json:"wait_on,omitempty"`
	Next   []nextClaim `json:"next,omitempty"`
	Well   *int        `json:"well,omitempty"`

	rev    uint64    // the KV revision the watcher delivered it at (not on the wire)
	prio   uint64    // its place in the right of way: the revision this seat FIRST claimed this placement of this piece at (foldClaimRaw)
	seenAt time.Time // when the watcher delivered it, on our clock: what its eta_ms counts from
}

// nextClaim is a tentative placement for a piece the writer's NEXT well
// shows: its piece index, type, orientation and anchor column.
type nextClaim struct {
	Piece int `json:"piece"`
	Type  int `json:"type"`
	Rot   int `json:"rot"`
	Col   int `json:"col"`
}

const (
	dropHard = "hard"
	dropSoft = "soft"
)

// samePlacement reports whether two claims are for the same placement of
// the same piece — a re-post of one claim (a wait announced, a preview
// refreshed), which keeps its place in the right of way.
func (c claim) samePlacement(o claim) bool {
	return c.Phase == phasePlanned && o.Phase == phasePlanned && c.Piece == o.Piece && c.Type == o.Type && c.Rot == o.Rot && c.Col == o.Col
}

// order is the claim's place in the right of way: the revision its
// placement was first claimed at, which is its own revision unless it was
// said again since.
func (c claim) order() uint64 {
	if c.prio != 0 {
		return c.prio
	}
	return c.rev
}

// waitsOn reports whether the claim says its writer waits on the seat.
func (c claim) waitsOn(seat int) bool {
	for _, s := range c.WaitOn {
		if s == seat {
			return true
		}
	}
	return false
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
// can carry its TTL. A server too old for per-key TTLs
// (ErrLimitMarkerTTLNotSupported) means no blackboard.
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
	if g.wellWanted() {
		w := g.wellColumn()
		c.Well = &w
	}
	if phase == phasePlanned {
		c.At = &[3]int{p.row, p.col, p.orient}
		c.Drop = dropHard
		if g.a.softDrop {
			c.Drop = dropSoft
		}
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
	// After every write still on its way (postAsync): this claim is the
	// newer word, and must be the last one on the key.
	g.mu.Lock()
	tail := g.postTail
	g.mu.Unlock()
	if tail != nil {
		select {
		case <-tail:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return g.publishClaim(ctx, c)
}

// publishClaim is the write itself (postClaim, postAsync).
func (g *Game) publishClaim(ctx context.Context, c claim) (uint64, error) {
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
// Caller holds mu.
func (g *Game) postLockedAsync(c claim) { g.postAsync(c) }

// postAsync writes a claim without waiting for its ack — the lock's
// announcement, a wait's — IN THE ORDER the calls were made: each write
// goes out once the one before it has (postTail), and the next claim made
// with postClaim waits for them all. Out of order, a wait announced for a
// piece could land on the blackboard after that piece's lock, and stand
// there as a planned claim for a piece that is stack. Caller holds mu.
func (g *Game) postAsync(c claim) {
	if g.coord == nil {
		return
	}
	prev := g.postTail
	done := make(chan struct{})
	g.postTail = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := g.publishClaim(ctx, c); err != nil {
			g.bbLogOnce.Do(func() { log.Printf("blackboard: %v (repeats not logged)", err) })
		}
	}()
}

// announceWait says on the blackboard that the piece in play waits on the
// seats' pieces, where it is, falling with gravity (claim.WaitOn): the
// claim as it stands, said again — it keeps its place in the right of way
// — so that the seats named know someone is held up by them (pause: their
// think pause ends now) and everyone knows this piece is staying in its
// columns. Caller holds mu.
func (g *Game) announceWait(seats []int) {
	if g.coord == nil || g.myClaim == nil || len(seats) == 0 || g.piece == nil {
		return
	}
	c := *g.myClaim
	c.WaitOn = append([]int(nil), seats...)
	c.At = &[3]int{g.piece.row, g.piece.col, g.piece.orient}
	c.T = nowRFC()
	g.postAsync(c)
}

// waitedOn reports whether a teammate's claim says, as of the last couple
// of seconds, that it waits on our piece. Caller holds mu.
func (g *Game) waitedOn(now time.Time) bool {
	for seat, c := range g.claims {
		if seat != g.idx && c.Phase == phasePlanned && c.waitsOn(g.idx) && now.Sub(c.seenAt) < 2*time.Second && g.seatPresentLocked(seat) {
			return true
		}
	}
	return false
}

// pause is the think pause of a piece on a coordinated board: the
// difficulty's, unless a teammate says it waits on our piece — then the
// piece goes as soon as its claim is confirmed (a move step), and the
// pause is cut short the moment such a claim arrives (foldClaimRaw closes
// hurry). "You go, I wait": the one in the way is the one that hurries.
// Returns false when the game or the agent stops.
func (g *Game) pause(ctx context.Context, d time.Duration) bool {
	if g.coord == nil || d <= g.a.tn.moveDelay {
		return g.wait(ctx, d)
	}
	g.mu.Lock()
	if g.waitedOn(time.Now()) {
		g.st.hurries++
		g.mu.Unlock()
		return g.wait(ctx, g.a.tn.moveDelay)
	}
	ch := make(chan struct{})
	g.hurry = ch
	g.mu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	hurried, ok := false, true
	select {
	case <-ch:
		hurried = true
	case <-timer.C:
	case <-ctx.Done():
		ok = false
	case <-g.a.stopCh:
		ok = false
	case <-g.ended:
		ok = false
	}
	g.mu.Lock()
	if g.hurry == ch {
		g.hurry = nil
	}
	if hurried {
		g.st.hurries++
	}
	g.mu.Unlock()
	return ok
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
// the game, folding every claim under mu (foldClaimRaw).
func (g *Game) claimWatch(ctx context.Context) {
	kv := g.a.blackboard()
	if g.coord == nil || kv == nil {
		return
	}
	w, err := kv.Watch(ctx, g.coord.watch)
	if err != nil {
		g.bbLogOnce.Do(func() { log.Printf("blackboard: %v (repeats not logged)", err) })
		return
	}
	defer func() { _ = w.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.a.stopCh:
			return
		case <-g.ended:
			return
		case e, ok := <-w.Updates():
			if !ok {
				return
			}
			if e == nil {
				continue // the initial values are in
			}
			g.mu.Lock()
			g.foldClaimRaw(e.Key(), e.Value(), e.Revision(), e.Operation())
			g.mu.Unlock()
		}
	}
}

// foldClaimRaw folds one delivery of the watch into the claims: a put is
// the seat's claim (a version we do not speak, or a piece type there is
// not, is none), a delete or a
// purge — a TTL expiry arrives as one — is the seat's claim gone. A claim
// takes its place in the right of way (prio) from the revision it was first
// made at: said again for the same placement of the same piece, it keeps
// it. Every delivery moves the revision mark and wakes awaitRev. Caller
// holds mu.
func (g *Game) foldClaimRaw(key string, val []byte, rev uint64, op jetstream.KeyValueOp) {
	if g.claims == nil {
		g.claims = map[int]claim{}
	}
	if seat, err := strconv.Atoi(key[strings.LastIndexByte(key, '.')+1:]); err == nil {
		var c claim
		if op == jetstream.KeyValuePut && json.Unmarshal(val, &c) == nil && c.V == claimVersion && c.Type >= 0 && c.Type < len(pieces) {
			c.rev, c.prio, c.seenAt = rev, rev, time.Now()
			if old, ok := g.claims[seat]; ok && old.samePlacement(c) {
				c.prio = old.order() // the same claim, said again: its place in the order is kept
			}
			g.claims[seat] = c
			if seat != g.idx && c.Phase == phasePlanned && c.waitsOn(g.idx) && g.hurry != nil {
				close(g.hurry) // a teammate waits on our piece: the think pause ends now (pause)
				g.hurry = nil
			}
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
