package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"gioui.org/app"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/nativeui"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/ui"
)

func main() {
	cfg := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect NATS (shared by both UIs).
	nc, js, err := connectNATS(cfg)
	if err != nil {
		log.Fatalf("connect NATS: %v", err)
	}
	defer nc.Close()
	fmt.Printf("Connected to NATS at %s\n", nc.ConnectedUrl())

	// Ensure streams and KV (shared by both UIs).
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

	if cfg.Web {
		runWeb(ctx, cancel, cfg, nc, js, kv)
		return
	}
	runNative(ctx, cancel, nc, js, kv)
}

// runWeb starts the HTTP/SSE UI server and opens a browser (the legacy front end).
func runWeb(ctx context.Context, cancel context.CancelFunc, cfg config.Config, nc *nats.Conn, js jetstream.JetStream, kv jetstream.KeyValue) {
	srv := ui.New(cfg.Port, js, kv, nc)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("start UI: %v", err)
	}
	defer srv.Stop()

	fmt.Printf("Jetricks running at http://localhost:%d\n", cfg.Port)
	go openBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Println("\nShutting down...")
	case <-ctx.Done():
	}
	cancel()
	nc.Drain()
}

// runNative opens the native (Gio) window. Gio's app.Main() owns the OS main
// thread and blocks forever, so all application logic runs on a goroutine; when
// the window closes (or on Ctrl-C) the process exits.
func runNative(ctx context.Context, cancel context.CancelFunc, nc *nats.Conn, js jetstream.JetStream, kv jetstream.KeyValue) {
	a := nativeui.New(nc, js, kv)

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
	flag.IntVar(&cfg.Port, "port", 7777, "HTTP server port (web UI)")
	flag.BoolVar(&cfg.Webview, "webview", false, "Launch as webview")
	flag.BoolVar(&cfg.Web, "web", false, "Use the web browser UI instead of the native window")
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

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return
	}
	_ = exec.Command(cmd, args...).Start()
}
