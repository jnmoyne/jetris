package nats

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestStartEmbeddedServer pins the "Run own NATS server" backend: the server
// comes up JetStream-enabled with the given store dir, accepts a client, and
// can create a stream. Port -1 picks a free port so the test never collides
// with a real server on 4222.
func TestStartEmbeddedServer(t *testing.T) {
	srv, err := StartEmbeddedServer(t.TempDir(), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect to embedded server: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EMBED_TEST", Subjects: []string{"embed.test"}}); err != nil {
		t.Fatalf("JetStream not usable on embedded server: %v", err)
	}
	if _, ok := srv.Addr().(*net.TCPAddr); !ok {
		t.Fatalf("server Addr() = %T, want *net.TCPAddr", srv.Addr())
	}
}

// TestLanIP pins that LanIP always yields a parseable IPv4 address.
func TestLanIP(t *testing.T) {
	ip := net.ParseIP(LanIP())
	if ip == nil || ip.To4() == nil {
		t.Fatalf("LanIP() = %q, want a parseable IPv4 address", LanIP())
	}
}
