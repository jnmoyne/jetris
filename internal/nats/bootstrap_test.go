package nats

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"jetris/internal/config"
	"jetris/internal/testutil"
)

func TestBootstrapURL(t *testing.T) {
	url, _ := testutil.StartServer(t)
	ctx := context.Background()

	nc, js, kv, err := Bootstrap(ctx, config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	// The three provisioned resources must exist and be usable.
	if _, err := js.Stream(ctx, config.ChatStream); err != nil {
		t.Fatalf("lobby chat stream missing: %v", err)
	}
	if _, err := js.Stream(ctx, config.ArchiveStream); err != nil {
		t.Fatalf("archive stream missing: %v", err)
	}
	if _, err := kv.Put(ctx, "players.bootstrap-test", []byte("{}")); err != nil {
		t.Fatalf("lobby KV not usable: %v", err)
	}
}

func TestCheckConnection(t *testing.T) {
	url, _ := testutil.StartServer(t)

	res, err := CheckConnection(config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerURL == "" {
		t.Fatal("expected the connected server URL")
	}
	if res.ServerID == "" {
		t.Fatal("expected the connected server ID")
	}
	if res.RTT <= 0 {
		t.Fatalf("ping = %v, want > 0", res.RTT)
	}
	if res.RTT > 5*time.Second {
		t.Fatalf("ping = %v, want a plausible loopback round trip", res.RTT)
	}

	if _, err := CheckConnection(config.Config{NATSURL: "nats://127.0.0.1:1"}); err == nil {
		t.Fatal("expected error checking an unroutable URL")
	}
}

// The ping must be a real publish→subscribe round trip, not a protocol PING:
// once the server is gone the message cannot come back, so the ping must fail
// rather than report a time. Uses its own server so the test can stop it.
func TestCoreNATSPing(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	defer srv.Shutdown()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("server not ready")
	}

	nc, err := nats.Connect(srv.ClientURL(), nats.RetryOnFailedConnect(false), nats.MaxReconnects(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	rtt, err := CoreNATSPing(nc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rtt <= 0 || rtt > 5*time.Second {
		t.Fatalf("ping = %v, want a plausible loopback round trip", rtt)
	}

	srv.Shutdown()
	if _, err := CoreNATSPing(nc, 500*time.Millisecond); err == nil {
		t.Fatal("expected a ping error once the server is gone")
	}
}

func TestBootstrapBadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nc, _, _, err := Bootstrap(ctx, config.Config{NATSURL: "nats://127.0.0.1:1"})
	if err == nil {
		nc.Close()
		t.Fatal("expected error connecting to an unroutable URL")
	}
	if nc != nil {
		t.Fatal("Bootstrap returned a connection together with an error")
	}
}
