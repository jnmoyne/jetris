package nativeui

// A single click on a browser row probes the server it names — the player
// should not have to find the ↻ chip to size up a server they just picked.
// Probes run several at once, one per server: a second row clicked while the
// first is still probing is probed right away, and a row already probing is
// left to finish. When the page opens, every favorite is probed in one
// round, sorted by ping once the last result is in, the fastest selected.

import (
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/prefs"
)

func probingKeys(a *App) map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]bool, len(a.connProbing))
	for k := range a.connProbing {
		out[k] = true
	}
	return out
}

func waitProbeIdle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(probingKeys(a)) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("probes never finished")
}

func hasProbe(a *App, key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.connProbes[key]
	return ok
}

func setProbe(a *App, key string, p probeResult) {
	a.mu.Lock()
	a.connProbes[key] = p
	a.mu.Unlock()
}

// pickerApp is the picker-mode App (it owns the browser state) with three
// favorites on unroutable ports, so the probes fail fast — all the tests
// need.
func pickerApp() (*App, [3]string) {
	a := NewWithPicker(config.Config{}, nil, "", []prefs.Favorite{
		{Label: "A", URL: "nats://127.0.0.1:1"},
		{Label: "B", URL: "nats://127.0.0.1:2"},
		{Label: "C", URL: "nats://127.0.0.1:3"},
	})
	a.connTab = connTabBrowser
	return a, [3]string{urlKey("nats://127.0.0.1:1"), urlKey("nats://127.0.0.1:2"), urlKey("nats://127.0.0.1:3")}
}

func TestRowClickProbes(t *testing.T) {
	a, k := pickerApp()

	// A click probes the row at once; a click on another row while the first
	// is still out probes that one too — several at a time.
	a.connSel = k[0]
	a.probeRow(k[0])
	a.connSel = k[1]
	a.probeRow(k[1])
	if p := probingKeys(a); !p[k[0]] || !p[k[1]] {
		t.Fatalf("after clicking A then B: probing %v, want both", p)
	}
	// A row already probing is left to finish, not probed twice.
	a.mu.Lock()
	a.connProbing[k[2]] = true
	a.mu.Unlock()
	a.probeRow(k[2])
	if p := probingKeys(a); len(p) != 3 {
		t.Fatalf("probing %v, want exactly A, B and the parked C", p)
	}
	a.mu.Lock()
	delete(a.connProbing, k[2])
	a.mu.Unlock()
	waitProbeIdle(t, a)
	if !hasProbe(a, k[0]) || !hasProbe(a, k[1]) {
		t.Fatal("the clicks never produced probe results")
	}
}

// TestRefreshFavoritesProbesAllAtOnce: opening the page probes every
// favorite in one go — every row reads as refreshing — and once the round
// is in, the favorites are ordered and the round is spent.
func TestRefreshFavoritesProbesAllAtOnce(t *testing.T) {
	a, k := pickerApp()
	a.refreshFavorites()
	if p := probingKeys(a); len(p) != 3 {
		t.Fatalf("after the refresh: probing %v, want all three favorites", p)
	}
	for _, key := range k {
		if txt, _ := probeSummary(probeResult{}, true); txt != "refreshing…" {
			t.Fatalf("row %s while probing reads %q, want refreshing…", key, txt)
		}
	}
	waitProbeIdle(t, a)
	a.applyRefreshRound()
	// Every probe failed: the list keeps its own order, and the selection
	// (the first favorite, the default) stays.
	if got := a.favoriteIndices(); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("order after an all-OFFLINE round = %v, want the list's own", got)
	}
	if a.connSel != k[0] {
		t.Fatalf("selection after an all-OFFLINE round = %q, want the default %q", a.connSel, k[0])
	}
	a.mu.Lock()
	done := a.connRoundDone
	a.mu.Unlock()
	if done {
		t.Fatal("the round was not spent by applyRefreshRound")
	}
}

// TestRefreshRoundSortsByPing: with the round's results in, the favorites
// list fastest first — the unreachable last — and the fastest becomes the
// selection; unless the player already picked a row, which stands.
func TestRefreshRoundSortsByPing(t *testing.T) {
	a, k := pickerApp()
	setProbe(a, k[0], probeResult{ok: true, msg: "✓", rtt: 30 * time.Millisecond, lobby: true})
	setProbe(a, k[1], probeResult{ok: true, msg: "✓", rtt: 5 * time.Millisecond, lobby: true})
	setProbe(a, k[2], probeResult{msg: "✗ unreachable"})
	a.mu.Lock()
	a.connRoundDone = true
	a.mu.Unlock()
	a.connSel = k[0]
	a.applyRefreshRound()
	if got := a.favoriteIndices(); len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 2 {
		t.Fatalf("order = %v, want B (5 ms), A (30 ms), C (offline)", got)
	}
	if a.connSel != k[1] {
		t.Fatalf("selection = %q, want the fastest, %q", a.connSel, k[1])
	}
	// The browser lists them in that order.
	secs := a.connSections()
	if secs[0].entries[0].label != "B" || secs[0].entries[1].label != "A" || secs[0].entries[2].label != "C" {
		t.Fatalf("FAVORITES rows = %s %s %s, want B A C", secs[0].entries[0].label, secs[0].entries[1].label, secs[0].entries[2].label)
	}

	// A row the player picked during the round stands.
	a.mu.Lock()
	a.connRoundDone = true
	a.mu.Unlock()
	a.connSel, a.connPicked = k[2], true
	a.applyRefreshRound()
	if a.connSel != k[2] {
		t.Fatalf("the player's pick %q was overridden with %q", k[2], a.connSel)
	}

	// A list change drops the order until the next round.
	a.favSave = func([]prefs.Favorite) error { return nil }
	a.persistFavorites()
	if got := a.favoriteIndices(); got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("order after the list changed = %v, want the list's own", got)
	}
}

// TestHeadCount words the lobby's players and agents apart.
func TestHeadCount(t *testing.T) {
	for _, c := range []struct {
		players, agents int
		want            string
	}{{0, 0, "nobody"}, {1, 0, "1 player"}, {3, 0, "3 players"}, {3, 1, "3 players · 1 agent"}, {0, 2, "0 players · 2 agents"}, {1, 1, "1 player · 1 agent"}} {
		if got := headCount(c.players, c.agents); got != c.want {
			t.Fatalf("headCount(%d, %d) = %q, want %q", c.players, c.agents, got, c.want)
		}
	}
	if got := playersText(2, 1, true); got != "2 players · 1 agent online" {
		t.Fatalf("playersText = %q", got)
	}
	if got := playersText(0, 0, false); got != "no lobby yet" {
		t.Fatalf("playersText with no lobby = %q", got)
	}
}
