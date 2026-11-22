package main

import (
	"context"
	"testing"
	"time"
)

// "You go, I wait", over the wire: a seat that announces it waits on a
// teammate's piece keeps its place in the right of way — on both agents'
// boards — and the teammate's think pause ends the moment the announcement
// reaches it; a pause begun while the announcement stands is a move step
// long. And the writes a seat makes without waiting for them reach the
// blackboard in the order it made them: the lock is the last word, not the
// wait announced before it.
func TestAWaitAnnouncedHurriesTheTeammate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	h := sharedTestPeer(t, ctx, g, 1)
	withBlackboard(t, ctx, g, h)
	g.a.tn, h.a.tn = difficultyTuning("easy"), difficultyTuning("easy") // a 1.5 s think pause

	// Seat 0 claims first, seat 1 after it.
	revG, err := g.postClaim(ctx, testClaim(g, 3))
	if err != nil {
		t.Fatal(err)
	}
	mine := testClaim(h, 8)
	revH, err := h.postClaim(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []*Game{g, h} {
		if ok, _ := x.awaitRev(ctx, revH, 5*time.Second); !ok {
			t.Fatalf("%s never saw revision %d", x.a.name, revH)
		}
	}

	// Seat 0 starts thinking; seat 1 finds seat 0's piece in its way.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		g.pause(ctx, g.a.tn.pieceDelay)
		done <- time.Since(start)
	}()
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	h.piece = &active{2, 0, spawnRow, h.spawnC}
	h.myClaim, h.myRev, h.myPrio = &mine, revH, revH
	h.announceWait([]int{0})
	h.mu.Unlock()
	select {
	case took := <-done:
		if took > time.Second {
			t.Errorf("the think pause ran %v with a teammate waiting on the piece, want it cut short", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the think pause never ended")
	}
	g.mu.Lock()
	theirs, hurries := g.claims[1], g.st.hurries
	g.mu.Unlock()
	if !theirs.waitsOn(0) || theirs.order() != revH || theirs.rev <= revH {
		t.Errorf("the wait as seat 0 sees it: waits on us %v, order %d (first claimed at %d), rev %d", theirs.waitsOn(0), theirs.order(), revH, theirs.rev)
	}
	if theirs.order() <= revG {
		t.Errorf("the waiting seat moved ahead of the claim made before it: order %d vs %d", theirs.order(), revG)
	}
	if hurries != 1 {
		t.Errorf("hurries counted: %d, want 1", hurries)
	}
	// While the wait stands, the next pause is a move step.
	start := time.Now()
	g.pause(ctx, g.a.tn.pieceDelay)
	if took := time.Since(start); took > time.Second {
		t.Errorf("a pause begun with a teammate waiting ran %v", took)
	}

	// The wait, then the lock, written without waiting: the lock is what
	// stays on the key.
	h.mu.Lock()
	h.announceWait([]int{0})
	locked := h.claimFor(*h.piece, placement{orient: 0, col: 8, dest: pieceCells(2, 0, 21, 8)}, phaseLocked, 0)
	h.postLockedAsync(locked)
	tail := h.postTail
	h.mu.Unlock()
	select {
	case <-tail:
	case <-time.After(5 * time.Second):
		t.Fatal("the writes never went out")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		c := g.claims[1]
		g.mu.Unlock()
		if c.Phase == phaseLocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the last word on seat 1's key is %q (wait on %v), want the lock", c.Phase, c.WaitOn)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // nothing overtakes it afterwards
	g.mu.Lock()
	c := g.claims[1]
	g.mu.Unlock()
	if c.Phase != phaseLocked {
		t.Errorf("a wait announced before the lock landed after it: %+v", c)
	}
}
