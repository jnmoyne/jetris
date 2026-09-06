package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"gioui.org/app"

	"jetris/internal/config"
	"jetris/internal/nativeui"
	natspkg "jetris/internal/nats"
	"jetris/internal/prefs"
	"jetris/internal/update"
)

// version is overridden at release time via -ldflags "-X main.version=<tag>"
// (see .github/workflows/release.yml).
var version = "dev"

// updateCheckTimeout bounds the startup lookup of the latest release: the
// check is a courtesy, never something the player waits on.
const updateCheckTimeout = 10 * time.Second

func main() {
	cfg, noUpdateCheck := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The window opens immediately; the login screen combines name entry with
	// the connection page (NATS server browser / LAN mode), and the app dials
	// NATS when the player hits Play. --server/--context don't connect here —
	// they only seed the browser's selection. --name does connect: with the
	// name already answered there is nothing left on that screen to ask, so
	// it plays its own Play button on the first frame.
	nativeui.SetVersion(version) // shown in the window's top-right corner

	names, selected, err := natspkg.ListContexts()
	if err != nil {
		log.Printf("warning: listing NATS contexts: %v", err)
	}
	favorites, err := prefs.LoadFavorites()
	if err != nil {
		log.Printf("warning: loading server favorites: %v", err)
	}
	handling, err := prefs.LoadHandling()
	if err != nil {
		log.Printf("warning: loading handling tuning: %v", err)
	}
	panels, err := prefs.LoadPanels()
	if err != nil {
		log.Printf("warning: loading panel switches: %v", err)
	}
	voicePrefs, err := prefs.LoadVoice()
	if err != nil {
		log.Printf("warning: loading voice settings: %v", err)
	}
	a := nativeui.NewWithPicker(cfg, names, selected, favorites)
	a.SetHandling(handling.DASMs, handling.ARRMs, handling.SDF, handling.DropGuardMs)
	a.SetPanels(panels)
	a.SetVoice(voicePrefs)
	if !noUpdateCheck {
		go checkForUpdate(ctx, a)
	}
	runNative(ctx, cancel, a)
}

// checkForUpdate asks GitHub once for the newest release and, when it is newer
// than this build, tells the player where to get it — on the login screen and
// the version plate (App.NotifyUpdate) and in the log. Runs off the UI
// goroutine; a failed lookup (offline, rate-limited) is logged and otherwise
// ignored, and a dev build, having no version to compare, never asks.
func checkForUpdate(ctx context.Context, a *nativeui.App) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	rel, newer, err := update.Check(ctx, version)
	if err != nil {
		log.Printf("update check: %v", err)
		return
	}
	if newer {
		log.Printf("Jetris %s is available (this is %s) — download it at %s", rel.Tag, version, rel.URL)
		a.NotifyUpdate(rel.Tag, rel.URL)
	}
}

// runNative opens the native (Gio) window. Gio's app.Main() owns the OS main
// thread and blocks forever, so all application logic runs on a goroutine; when
// the window closes (or on Ctrl-C) the process exits. The App owns the NATS
// connection it dials from the login screen; Shutdown is nil-safe.
func runNative(ctx context.Context, cancel context.CancelFunc, a *nativeui.App) {
	go func() {
		defer cancel()
		if err := a.Run(ctx); err != nil {
			log.Printf("native UI error: %v", err)
		}
		a.Shutdown()
		os.Exit(0)
	}()

	// Terminal Ctrl-C: exit the process (the OS main loop has no signal hook).
	// A no-op in the browser build.
	go watchSignals(ctx, a)

	app.Main()
}

// parseFlags returns the connection config seeded from the flags, and whether
// --no-update-check was given.
func parseFlags() (cfg config.Config, noUpdateCheck bool) {

	flag.StringVar(&cfg.NATSContext, "context", "", "NATS context to preselect in the login screen's server browser")
	flag.StringVar(&cfg.NATSURL, "server", "", "NATS server URL to preselect in the login screen's server browser (overrides --context as the default choice)")
	flag.StringVar(&cfg.NATSUser, "user", "", "NATS username (used with --server)")
	flag.StringVar(&cfg.NATSPassword, "password", "", "NATS password (used with --server)")
	flag.StringVar(&cfg.PlayerName, "name", "", "player name: given here, the login screen skips itself and joins the selected server's lobby straight away")
	flag.StringVar(&cfg.ReplayGameID, "replay", "", "game ID whose replay to open on landing in the lobby (what a replay's share link carries)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(&noUpdateCheck, "no-update-check", false, "skip the startup check for a newer release on GitHub")
	flag.Parse()
	applyPageParams(&cfg) // browser build: ?server=… stands in for --server

	if *showVersion {
		fmt.Printf("jetris %s\n", version)
		os.Exit(0)
	}

	return cfg, noUpdateCheck
}
