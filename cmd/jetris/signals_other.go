//go:build !js

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"jetris/internal/nativeui"
)

// watchSignals exits the process on SIGINT/SIGTERM, draining the NATS
// connection first; returns when ctx ends.
func watchSignals(ctx context.Context, a *nativeui.App) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Println("\nShutting down...")
		a.DrainConn()
		os.Exit(0)
	case <-ctx.Done():
	}
}
