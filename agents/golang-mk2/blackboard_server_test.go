package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// sharedTestPeer is a second Game on g0's board — another seat, its own
// Agent on the same connection — for tests of two agents on one stream and
// one blackboard.
func sharedTestPeer(t *testing.T, ctx context.Context, g0 *Game, seat int) *Game {
	t.Helper()
	a := &Agent{nc: g0.a.nc, js: g0.a.js, name: fmt.Sprintf("peer%d", seat), pub: pubSync,
		listings: map[string]obj{}, invites: map[string]obj{}, streams: map[string]jetstream.Stream{}, stopCh: make(chan struct{})}
	g := newGame(a, g0.id, seat)
	g.mode, g.playerCount, g.seatsOnPF, g.extra, g.stream = g0.mode, g0.playerCount, g0.seatsOnPF, g0.extra, g0.stream
	g.team, g.teamCount = g0.team, g0.teamCount
	if g.mode == modeTeams {
		g.teamSlot = seat - g.team*g0.teamSize()
		g.teamScores, g.teamLines = make([]int, 2), make([]int, 2)
	}
	g.w, g.h = g0.w, g0.h
	g.spawnC = g.spawnColumn(nil)
	g.pub = pubSync
	return g
}

// withBlackboard gives the games a private blackboard bucket and their
// coordinators, and starts their claim watches (they end with ctx).
func withBlackboard(t *testing.T, ctx context.Context, games ...*Game) string {
	t.Helper()
	bucket := "JETRIS_BLACKBOARD_TEST_" + randID(6)
	for _, g := range games {
		g.a.coordinate, g.a.bbBucket = true, bucket
		kv, err := g.a.ensureBlackboard(ctx)
		if err != nil {
			t.Fatal(err)
		}
		g.a.setBlackboard(kv)
		b := boardName(g.mode, g.team, g.individual, g.seatsOnPF)
		g.coord = &coordinator{board: b, key: claimKey(g.id, b, g.idx), watch: g.id + "." + b + ".*"}
		go g.claimWatch(ctx)
	}
	t.Cleanup(func() { _ = games[0].a.js.DeleteKeyValue(context.Background(), bucket) })
	return bucket
}

func testClaim(g *Game, col int) claim {
	p := active{2, 0, spawnRow, g.spawnC}
	return g.claimFor(p, placement{orient: 0, col: col, dropRow: 21, dest: pieceCells(2, 0, 21, col)}, phasePlanned, 100*time.Millisecond)
}

// Two agents' claims reach both in the bucket's revision order, each
// agent's own echo included; a claim's TTL takes it off both.
func TestClaimsAreSeenInRevisionOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	h := sharedTestPeer(t, ctx, g, 1)
	bucket := withBlackboard(t, ctx, g, h)

	revA, err := g.postClaim(ctx, testClaim(g, 3))
	if err != nil {
		t.Fatal(err)
	}
	revB, err := h.postClaim(ctx, testClaim(h, 8))
	if err != nil {
		t.Fatal(err)
	}
	if revB <= revA {
		t.Fatalf("revisions not ordered: %d then %d", revA, revB)
	}
	for _, x := range []*Game{g, h} {
		if ok, _ := x.awaitRev(ctx, revB, 5*time.Second); !ok {
			t.Fatalf("%s never saw revision %d (mark %d)", x.a.name, revB, x.bbRev)
		}
		x.mu.Lock()
		a, b := x.claims[0], x.claims[1]
		x.mu.Unlock()
		if a.rev != revA || b.rev != revB || a.Col != 3 || b.Col != 8 || a.Agent != "me" || b.Agent != "peer1" {
			t.Errorf("%s's claims: %+v / %+v", x.a.name, a, b)
		}
	}
	// A claim with a short TTL vanishes from both within a few seconds.
	short, _ := json.Marshal(testClaim(h, 9))
	if _, err := h.a.js.Publish(ctx, "$KV."+bucket+"."+h.coord.key, short, jetstream.WithMsgTTL(time.Second)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g.mu.Lock()
		_, still := g.claims[1]
		g.mu.Unlock()
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the expired claim never left the board")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The bucket deleted underneath the watch (a shared server's purge): the
// watch comes back on a recreated bucket, the claims and the revision
// mark start over, the piece in play is re-planned, and new claims are
// seen again.
func TestBlackboardWatchSurvivesBucketDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	h := sharedTestPeer(t, ctx, g, 1)
	bucket := withBlackboard(t, ctx, g, h)
	rev, err := h.postClaim(ctx, testClaim(h, 8))
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := g.awaitRev(ctx, rev, 5*time.Second); !ok {
		t.Fatal("the first claim never arrived")
	}
	g.mu.Lock()
	g.piece = &active{2, 0, spawnRow, g.spawnC}
	g.mu.Unlock()
	if err := g.a.js.DeleteKeyValue(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		g.mu.Lock()
		epoch, replan, n := g.bbEpoch, g.replan, len(g.claims)
		g.mu.Unlock()
		if epoch == 1 {
			if replan != replanBBReset || n != 0 {
				t.Errorf("after the reset: replan %s, %d claim(s)", replan, n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watch never came back")
		}
		time.Sleep(250 * time.Millisecond)
	}
	// The bucket is back, and a claim posted on it reaches the watch again.
	var rev2 uint64
	for attempt := 0; ; attempt++ {
		if rev2, err = h.postClaim(ctx, testClaim(h, 5)); err == nil {
			break
		}
		if attempt > 20 {
			t.Fatalf("posting after the recovery: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if ok, _ := g.awaitRev(ctx, rev2, 10*time.Second); !ok {
		t.Fatalf("the claim after the recovery never arrived (mark %d, want %d)", g.bbRev, rev2)
	}
	g.mu.Lock()
	c := g.claims[1]
	g.mu.Unlock()
	if c.Col != 5 || c.rev != rev2 {
		t.Errorf("claim after the recovery: %+v", c)
	}
}
