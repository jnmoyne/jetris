package main

import (
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// The claim's wire format: the field names the guide documents, round-tripped.
func TestClaimJSON(t *testing.T) {
	g := sweepTestGame(2)
	g.pieceIdx = 17
	p := active{2, 0, spawnRow, g.spawnC}
	pl := placement{orient: 1, col: 5, dropRow: 21, dest: pieceCells(2, 1, 21, 5), lines: 1}
	c := g.claimFor(p, pl, phasePlanned, 120e6)
	b, _ := json.Marshal(c)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	for _, k := range []string{"v", "agent", "seat", "piece", "type", "rot", "col", "cells", "lines", "phase", "eta_ms", "spawn_col", "t"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("claim JSON lacks %q: %s", k, b)
		}
	}
	if len(fields) != 13 {
		t.Errorf("claim JSON carries %d fields, want 13: %s", len(fields), b)
	}
	var back claim
	if err := json.Unmarshal(b, &back); err != nil || back != c {
		t.Errorf("round trip: %+v != %+v (%v)", back, c, err)
	}
	if c.V != claimVersion || c.Seat != 0 || c.Piece != 17 || c.Type != 2 || c.Rot != 1 || c.Col != 5 || c.ETAms != 120 || c.SpawnCol != g.spawnC || c.Lines != 1 {
		t.Errorf("claim fields: %+v", c)
	}
	if c.Cells != [4][2]int{{21, 5}, {22, 5}, {23, 5}, {22, 6}} && c.Cells[0] != [2]int{pl.dest[0].r, pl.dest[0].c} {
		t.Errorf("claim cells: %v", c.Cells)
	}
}

// Who coordinates with whom: the crew of a board scored together, one team
// of a teams game; nobody on a competitive board, a board scored per seat,
// or a playfield of one.
func TestBoardName(t *testing.T) {
	cases := []struct {
		mode, team int
		individual bool
		seats      int
		want       string
	}{
		{modeCooperative, 0, false, 2, "crew"},
		{modeCooperative, 0, true, 2, ""},
		{modeCooperative, 0, false, 1, ""},
		{modeTeams, 1, false, 2, "t1"},
		{modeTeams, 0, false, 1, ""},
		{modeCompetitive, 0, false, 1, ""},
		{modeCompetitive, 0, false, 4, ""},
	}
	for _, c := range cases {
		if got := boardName(c.mode, c.team, c.individual, c.seats); got != c.want {
			t.Errorf("boardName(%d, %d, %v, %d) = %q, want %q", c.mode, c.team, c.individual, c.seats, got, c.want)
		}
	}
}

func TestClaimKey(t *testing.T) {
	if got := claimKey("550e8400-e29b-41d4-a716-446655440000", "crew", 1); got != "550e8400-e29b-41d4-a716-446655440000.crew.1" {
		t.Errorf("uuid key: %q", got)
	}
	if got := claimKey("friday-night", "t2", 5); got != "friday-night.t2.5" {
		t.Errorf("named key: %q", got)
	}
	if got := claimKey("a.b", "crew", 0); got != "" {
		t.Errorf("a dotted id got a key: %q", got)
	}
	if got := claimKey("x", "", 0); got != "" {
		t.Errorf("no board got a key: %q", got)
	}
}

// The watch's deliveries: a put is the seat's claim, a delete or a purge
// (a TTL expiry) its end, a version we do not speak nothing; every delivery
// moves the revision mark and wakes whoever waits on it.
func TestFoldClaimRaw(t *testing.T) {
	g := sweepTestGame(2)
	c := claim{V: claimVersion, Seat: 1, Piece: 3, Type: 4, Rot: 1, Col: 8, Phase: phasePlanned}
	b, _ := json.Marshal(c)
	notify := g.bbNotify
	g.foldClaimRaw("g.crew.1", b, 7, jetstream.KeyValuePut)
	select {
	case <-notify:
	default:
		t.Error("a delivery did not wake the waiters")
	}
	if got := g.claims[1]; got.rev != 7 || got.Col != 8 || g.bbRev != 7 {
		t.Errorf("after the put: %+v, rev mark %d", got, g.bbRev)
	}
	g.foldClaimRaw("g.crew.1", nil, 9, jetstream.KeyValueDelete)
	if _, ok := g.claims[1]; ok || g.bbRev != 9 {
		t.Errorf("after the delete: %v, rev mark %d", g.claims, g.bbRev)
	}
	g.foldClaimRaw("g.crew.1", b, 10, jetstream.KeyValuePut)
	g.foldClaimRaw("g.crew.1", nil, 11, jetstream.KeyValuePurge)
	if _, ok := g.claims[1]; ok {
		t.Error("a purge (a TTL expiry) kept the claim")
	}
	c2 := c
	c2.V = 2
	b2, _ := json.Marshal(c2)
	g.foldClaimRaw("g.crew.1", b2, 12, jetstream.KeyValuePut)
	if _, ok := g.claims[1]; ok {
		t.Error("a claim of a version we do not speak was folded")
	}
	g.foldClaimRaw("g.crew.1", b, 5, jetstream.KeyValuePut)
	if g.bbRev != 12 {
		t.Errorf("the revision mark went backwards: %d", g.bbRev)
	}
}
