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

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/ui"
)

func main() {
	// 1. Parse flags
	cfg := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. Connect NATS
	nc, js, _, err := natspkg.Connect(cfg.NATSContext)
	if err != nil {
		log.Fatalf("connect NATS: %v", err)
	}
	defer nc.Close()
	fmt.Printf("Connected to NATS at %s\n", nc.ConnectedUrl())

	// 3. Ensure streams and KV
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

	// 4. Create and start UI server (lobby is created after user logs in)
	srv := ui.New(cfg.Port, js, kv, nc)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("start UI: %v", err)
	}
	defer srv.Stop()

	fmt.Printf("Jetricks running at http://localhost:%d\n", cfg.Port)

	// 5. Open browser
	go openBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))

	// 6. Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Println("\nShutting down...")
	case <-ctx.Done():
	}

	// 7. Shutdown
	cancel()
	nc.Drain()
}

func parseFlags() config.Config {
	cfg := config.Config{}

	flag.StringVar(&cfg.NATSContext, "context", "", "NATS context name (empty = default)")
	flag.IntVar(&cfg.Port, "port", 7777, "HTTP server port")
	flag.BoolVar(&cfg.Webview, "webview", false, "Launch as webview")
	flag.Parse()

	return cfg
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
