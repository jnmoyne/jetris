//go:build !js

package main

import "jetris/internal/config"

// applyPageParams is a no-op on the desktop: connection defaults come from
// the command line only.
func applyPageParams(cfg *config.Config) {}
