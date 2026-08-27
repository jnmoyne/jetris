//go:build js

package main

import (
	"context"

	"jetris/internal/nativeui"
)

// watchSignals: a wasm module gets no POSIX signals; closing the tab is the
// only way out and needs no cleanup on our side.
func watchSignals(ctx context.Context, a *nativeui.App) {}
