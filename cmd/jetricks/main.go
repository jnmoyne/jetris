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

	// The window opens immediately; the login screen combines name entry with
	// the connection picker, and the app dials NATS when the player hits Play.
	// --server/--context don't connect here — they only seed the picker's
	// defaults (the URL field text, or which context radio starts selected).
	names, selected, err := natspkg.ListContexts()
	if err != nil {
		log.Printf("warning: listing NATS contexts: %v", err)
	}
	runNative(ctx, cancel, nativeui.NewWithPicker(cfg, names, selected))
}

// runNative opens the native (Gio) window. Gio's app.Main() owns the OS main
// thread and blocks forever, so all application logic runs on a goroutine; when
// the window closes (or on Ctrl-C) the process exits. The App owns the NATS
// connection it dials from the login screen; DrainConn is nil-safe.
func runNative(ctx context.Context, cancel context.CancelFunc, a *nativeui.App) {
	go func() {
		defer cancel()
		if err := a.Run(ctx); err != nil {
			log.Printf("native UI error: %v", err)
		}
		a.DrainConn()
		os.Exit(0)
	}()

	// Terminal Ctrl-C: exit the process (the OS main loop has no signal hook).
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sig:
			fmt.Println("\nShutting down...")
			a.DrainConn()
			os.Exit(0)
		case <-ctx.Done():
		}
	}()

	app.Main()
}

func parseFlags() config.Config {
	cfg := config.Config{}

	flag.StringVar(&cfg.NATSContext, "context", "", "NATS context to preselect in the connection picker")
	flag.StringVar(&cfg.NATSURL, "server", "", "NATS server URL to pre-fill in the connection picker (overrides --context as the default choice)")
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
