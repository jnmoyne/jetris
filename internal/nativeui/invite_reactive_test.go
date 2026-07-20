package nativeui

import (
	"testing"

	"jetricks/internal/lobby"
)

// The picker candidate list tracks live lobby presence: new lobby players are
// added, players who leave or enter a game are dropped, existing selections
// survive, and you never appear in your own picker.
func TestReconcileInvitePicker(t *testing.T) {
	inLobby := func(name string, agent bool) lobby.PlayerPresence {
		return lobby.PlayerPresence{PlayerID: name, Name: name, Agent: agent, Status: lobby.StatusInLobby}
	}

	picker := map[string]*inviteChoice{}

	// First reconcile: two eligible players show up; "me" is excluded.
	live := map[string]lobby.PlayerPresence{
		"me":    inLobby("me", false),
		"alice": inLobby("alice", false),
		"nova":  inLobby("nova", true),
	}
	reconcileInvitePicker(picker, live, "me")
	if len(picker) != 2 || picker["alice"] == nil || picker["nova"] == nil || picker["me"] != nil {
		t.Fatalf("after first reconcile: %v", keys(picker))
	}
	if !picker["nova"].agent {
		t.Error("nova should be flagged as an agent")
	}

	// Select alice, then a new player joins and alice enters a game.
	picker["alice"].sel.Value = true
	aliceChoice := picker["alice"]
	live["bob"] = inLobby("bob", false)
	live["alice"] = lobby.PlayerPresence{PlayerID: "alice", Name: "alice", Status: lobby.StatusInGame}
	reconcileInvitePicker(picker, live, "me")

	if picker["alice"] != nil {
		t.Error("alice entered a game — should be dropped from the picker")
	}
	if picker["bob"] == nil {
		t.Error("bob joined the lobby — should be added")
	}
	if picker["nova"] != aliceChoice && picker["nova"] == nil {
		t.Error("nova stayed in the lobby — should be preserved")
	}

	// nova (selected) quits entirely: gone from presence → dropped.
	picker["nova"].sel.Value = true
	novaChoice := picker["nova"]
	delete(live, "nova")
	reconcileInvitePicker(picker, live, "me")
	if picker["nova"] != nil {
		t.Error("nova left the lobby — should be dropped")
	}
	_ = novaChoice

	// A staying player keeps its exact widget (selection preserved).
	picker["bob"].sel.Value = true
	bobChoice := picker["bob"]
	reconcileInvitePicker(picker, live, "me")
	if picker["bob"] != bobChoice || !picker["bob"].sel.Value {
		t.Error("bob stayed — its selection widget must be preserved")
	}
}

func keys(m map[string]*inviteChoice) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
