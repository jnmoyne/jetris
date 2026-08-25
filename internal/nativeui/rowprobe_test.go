package nativeui

// A single click on a browser row probes the server it names — the player
// should not have to find the ↻ chip to size up a server they just picked.
// The probe slot is single-occupancy, so a row clicked while another probe is
// still running is parked and probed as soon as the slot frees, provided it
// is still the browser tab's selection by then.

import (
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/prefs"
)

func probingKey(a *App) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connProbing
}

func setProbing(a *App, key string) {
	a.mu.Lock()
	a.connProbing = key
	a.mu.Unlock()
}

func waitProbeIdle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if probingKey(a) == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("probe never finished")
}

func hasProbe(a *App, key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.connProbes[key]
	return ok
}

func TestRowClickProbes(t *testing.T) {
	// The picker-mode App (it owns the browser state); unroutable ports so
	// the probes fail fast, which is all the test needs.
	a := NewWithPicker(config.Config{}, nil, "", []prefs.Favorite{
		{Label: "A", URL: "nats://127.0.0.1:1"},
		{Label: "B", URL: "nats://127.0.0.1:2"},
	})
	a.connTab = connTabBrowser
	kA, kB := urlKey("nats://127.0.0.1:1"), urlKey("nats://127.0.0.1:2")

	// Idle slot: the click probes the row at once.
	a.connSel = kA
	a.probeRow(kA)
	if got := probingKey(a); got != kA {
		t.Fatalf("after clicking A: probing %q, want %q", got, kA)
	}
	waitProbeIdle(t, a)
	if !hasProbe(a, kA) {
		t.Fatal("A's click never produced a probe result")
	}

	// Busy slot: the click is parked (the latest click wins) and nothing
	// starts until the running probe is done — the drain is a no-op.
	setProbing(a, "elsewhere")
	a.connSel = kA
	a.probeRow(kA)
	a.connSel = kB
	a.probeRow(kB)
	a.drainQueuedProbe()
	if got := probingKey(a); a.connProbeQueued != kB || got != "elsewhere" {
		t.Fatalf("clicks during a probe: queued %q, probing %q; want B queued and the running probe untouched", a.connProbeQueued, got)
	}

	// The running probe finishes after the player moved to the LAN tab: the
	// parked click is dropped, not fired at a stale selection.
	setProbing(a, "")
	a.connTab = connTabLAN
	a.drainQueuedProbe()
	if got := probingKey(a); a.connProbeQueued != "" || got != "" {
		t.Fatalf("queued click after leaving the browser tab: queued %q, probing %q; want both dropped", a.connProbeQueued, got)
	}

	// …but while B is still the selection, it is probed as soon as the slot frees.
	a.connTab = connTabBrowser
	setProbing(a, "elsewhere")
	a.probeRow(kB)
	setProbing(a, "")
	a.drainQueuedProbe()
	if got := probingKey(a); got != kB || a.connProbeQueued != "" {
		t.Fatalf("slot freed with B selected: probing %q, queued %q; want B probing", got, a.connProbeQueued)
	}
	waitProbeIdle(t, a)
	if !hasProbe(a, kB) {
		t.Fatal("B's parked click never produced a probe result")
	}
}
