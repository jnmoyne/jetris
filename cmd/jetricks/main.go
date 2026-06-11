package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gioui.org/app"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/nativeui"
	natspkg "jetricks/internal/nats"
)

func main() {
	cfg := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nc, js, err := connectNATS(cfg)
	if err != nil {
		log.Fatalf("connect NATS: %v", err)
	}
	defer nc.Close()
	fmt.Printf("Connected to NATS at %s\n", nc.ConnectedUrl())

	// Ensure streams and KV.
	if err := natspkg.EnsureLobbyChatStream(ctx, js); err != nil {
		log.Fatalf("ensure lobby chat stream: %v", err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		log.Fatalf("ensure lobby KV: %v", err)
	}
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		log.Fatalf("ensure archive stream: %v", err)
	}

	runNative(ctx, cancel, nc, js, kv)
}

// runNative opens the native (Gio) window. Gio's app.Main() owns the OS main
// thread and blocks forever, so all application logic runs on a goroutine; when
// the window closes (or on Ctrl-C) the process exits.
func runNative(ctx context.Context, cancel context.CancelFunc, nc *nats.Conn, js jetstream.JetStream, kv jetstream.KeyValue) {
	a := nativeui.New(js, kv)

	go func() {
		defer cancel()
		if err := a.Run(ctx); err != nil {
			log.Printf("native UI error: %v", err)
		}
		nc.Drain()
		os.Exit(0)
	}()

	// Terminal Ctrl-C: exit the process (the OS main loop has no signal hook).
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sig:
			fmt.Println("\nShutting down...")
			nc.Drain()
			os.Exit(0)
		case <-ctx.Done():
		}
	}()

	app.Main()
}

func parseFlags() config.Config {
	cfg := config.Config{}

	flag.StringVar(&cfg.NATSContext, "context", "", "NATS context name (empty = default)")
	flag.StringVar(&cfg.NATSURL, "server", "", "NATS server URL (overrides --context when set)")
	flag.StringVar(&cfg.NATSUser, "user", "", "NATS username (used with --server)")
	flag.StringVar(&cfg.NATSPassword, "password", "", "NATS password (used with --server)")
	flag.Parse()

	return cfg
}

func connectNATS(cfg config.Config) (*nats.Conn, jetstream.JetStream, error) {
	if cfg.NATSURL != "" {
		return natspkg.ConnectURL(cfg.NATSURL, cfg.NATSUser, cfg.NATSPassword)
	}
	nc, js, _, err := natspkg.Connect(cfg.NATSContext)
	return nc, js, err
}
