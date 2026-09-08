package main

import "testing"

// TestJoinableInProgress: an open game takes joiners before it starts and
// while it runs — never during the countdown, never when full or closed to
// agents — and an invite game takes nobody uninvited; the seat taken is the
// lowest free one, stable across departures; the start rule follows the
// GUI's (readyToStart).
func TestJoinableInProgress(t *testing.T) {
	listing := func(status string, inviteOnly bool, players []playerSummary) obj {
		g := obj{}
		g.set("mode", modeCompetitive)
		g.set("status", status)
		g.set("player_count", 3)
		g.set("max_agents", 2)
		g.set("invite_only", inviteOnly)
		g.set("players", players)
		return g
	}
	one := []playerSummary{{PlayerID: "a", Seat: 0}}
	if !joinable(listing("created", false, one)) || !joinable(listing("in_progress", false, one)) {
		t.Error("an open game with a free seat is not joinable before or during play")
	}
	if joinable(listing("starting", false, one)) || joinable(listing("finished", false, one)) {
		t.Error("a game in its countdown, or over, is joinable")
	}
	if joinable(listing("in_progress", true, one)) {
		t.Error("a running invite game is joinable")
	}
	full := []playerSummary{{PlayerID: "a", Seat: 0}, {PlayerID: "b", Seat: 1}, {PlayerID: "c", Seat: 2}}
	if joinable(listing("in_progress", false, full)) {
		t.Error("a full game is joinable")
	}

	// The lowest free seat: a departed middle seat is the next one taken.
	seat, _, ok := freeSeat(listing("in_progress", false, []playerSummary{{PlayerID: "a", Seat: 0}, {PlayerID: "c", Seat: 2}}), 0)
	if !ok || seat != 1 {
		t.Errorf("freeSeat = %d (%v), want the freed 1", seat, ok)
	}
	if _, _, ok := freeSeat(listing("in_progress", false, full), 0); ok {
		t.Error("a full game has a free seat")
	}
	teams := obj{}
	teams.set("mode", modeTeams)
	teams.set("team_count", 2)
	teams.set("team_size", 2)
	teams.set("player_count", 4)
	teams.set("players", []playerSummary{{PlayerID: "a", Seat: 2, Team: 1, TeamSlot: 0}})
	if seat, slot, ok := freeSeat(teams, 1); !ok || seat != 3 || slot != 1 {
		t.Errorf("team B's free seat = %d/%d (%v), want seat 3 slot 1", seat, slot, ok)
	}
	if seat, slot, ok := freeSeat(teams, 0); !ok || seat != 0 || slot != 0 {
		t.Errorf("team A's free seat = %d/%d (%v), want seat 0 slot 0", seat, slot, ok)
	}

	// The start rule.
	ready := func(id string, seat, team int) playerSummary {
		return playerSummary{PlayerID: id, Seat: seat, Team: team, Ready: true}
	}
	if !readyToStart(listing("created", false, []playerSummary{ready("a", 0, 0), ready("b", 1, 0), ready("c", 2, 0)})) {
		t.Error("an open competitive game with every board seated and ready is not ready")
	}
	if readyToStart(listing("created", false, []playerSummary{ready("a", 0, 0)})) {
		t.Error("an open competitive game with a board empty is ready")
	}
	coop := listing("created", false, []playerSummary{ready("a", 0, 0)})
	coop.set("mode", modeCooperative)
	if !readyToStart(coop) {
		t.Error("an open co-op game with one ready player is not ready")
	}
	invite := listing("created", true, []playerSummary{ready("a", 0, 0)})
	invite.set("mode", modeCooperative)
	if readyToStart(invite) {
		t.Error("an invite game short of its seats is ready")
	}
	teams.set("players", []playerSummary{ready("a", 2, 1)})
	if readyToStart(teams) {
		t.Error("an open teams game with team A empty is ready")
	}
	teams.set("players", []playerSummary{ready("a", 2, 1), ready("b", 0, 0)})
	if !readyToStart(teams) {
		t.Error("an open teams game with a ready player per team is not ready")
	}

	// A roster down to us alone after rivals played is a win.
	g := &Game{mode: modeCompetitive, eliminated: map[string]bool{}}
	g.a = &Agent{name: "me"}
	g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}, {PlayerID: "x", Seat: 1}})
	if g.winCheck() {
		t.Error("won with a rival standing")
	}
	g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}})
	if !g.winCheck() {
		t.Error("the last board standing after the rival left did not win")
	}
	alone := &Game{mode: modeCompetitive, eliminated: map[string]bool{}}
	alone.a = &Agent{name: "me"}
	alone.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}})
	if alone.winCheck() {
		t.Error("a game nobody else ever joined was won")
	}
}
