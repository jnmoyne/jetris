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

	"jetricks/internal/config"
	"jetricks/internal/nativeui"
	natspkg "jetricks/internal/nats"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>"
// (see .github/workflows/release.yml).
var version = "dev"

func main() {
	cfg := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.NATSURL != "" || cfg.NATSContext != "" {
		// Flags path: the launcher chose the server, so connect before the
		// window opens and fail fast on error (the pre-picker behavior).
		nc, js, kv, err := natspkg.Bootstrap(ctx, cfg)
		if err != nil {
			log.Fatalf("connect NATS: %v", err)
		}
		defer nc.Close()
		fmt.Printf("Connected to NATS at %s\n", nc.ConnectedUrl())
		runNative(ctx, cancel, nc, nativeui.New(js, kv))
		return
	}

	// No server flags: open the window immediately and let the login screen's
	// connection picker choose a NATS context or URL. The app dials NATS itself
	// once the player hits Play.
	names, selected, err := natspkg.ListContexts()
	if err != nil {
		log.Printf("warning: listing NATS contexts: %v", err)
	}
	runNative(ctx, cancel, nil, nativeui.NewWithPicker(cfg, names, selected))
}

// runNative opens the native (Gio) window. Gio's app.Main() owns the OS main
// thread and blocks forever, so all application logic runs on a goroutine; when
// the window closes (or on Ctrl-C) the process exits. nc is the main-owned
// connection (flags path) and may be nil (picker path, where the App owns the
// connection it dials — drained via a.DrainConn).
func runNative(ctx context.Context, cancel context.CancelFunc, nc *nats.Conn, a *nativeui.App) {
	drain := func() {
		if nc != nil {
			nc.Drain()
		}
		a.DrainConn()
	}

	go func() {
		defer cancel()
		if err := a.Run(ctx); err != nil {
			log.Printf("native UI error: %v", err)
		}
		drain()
		os.Exit(0)
	}()

	// Terminal Ctrl-C: exit the process (the OS main loop has no signal hook).
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sig:
			fmt.Println("\nShutting down...")
			drain()
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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("jetricks %s\n", version)
		os.Exit(0)
	}

	return cfg
}
