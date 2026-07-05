package nats

import (
	"context"
	"testing"
	"time"

	"jetricks/internal/config"
	"jetricks/internal/testutil"
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
	if _, err := js.Stream(ctx, config.LobbyChatStream); err != nil {
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

	server, rtt, err := CheckConnection(config.Config{NATSURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if server == "" {
		t.Fatal("expected the connected server URL")
	}
	if rtt <= 0 {
		t.Fatalf("ping = %v, want > 0", rtt)
	}

	if _, _, err := CheckConnection(config.Config{NATSURL: "nats://127.0.0.1:1"}); err == nil {
		t.Fatal("expected error checking an unroutable URL")
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
