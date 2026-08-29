package nats

import (
	"testing"
	"time"

	"jetris/internal/testutil"
)

// TestLinkOptionsApplied: every app connection carries the flaky-link
// tuning — quick, unlimited reconnects and a short ping so a dead socket is
// noticed in seconds — with the caller's own options still winning.
func TestLinkOptionsApplied(t *testing.T) {
	url, _ := testutil.StartServer(t)
	nc, _, err := ConnectURL(url, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	o := nc.Opts
	if o.ReconnectWait != 500*time.Millisecond || o.MaxReconnect != -1 || o.PingInterval != 5*time.Second || o.MaxPingsOut != 3 {
		t.Fatalf("options: ReconnectWait %v, MaxReconnect %d, PingInterval %v, MaxPingsOut %d", o.ReconnectWait, o.MaxReconnect, o.PingInterval, o.MaxPingsOut)
	}
}
