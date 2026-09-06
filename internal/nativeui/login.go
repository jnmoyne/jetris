package nativeui

import (
	"errors"
	"fmt"
	"image"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/prefs"
)

// The connection page's two tabs.
const (
	connTabBrowser = "browser" // NATS server browser: favorites + contexts
	connTabLAN     = "lan"     // LAN mode: host the embedded NATS server
)

// probeKeyLAN is the connProbes key of the embedded server's check result.
const probeKeyLAN = "lan"

// Browser section titles (also the keys of connSecClosed/connSecBtns).
const (
	secFavorites = "FAVORITES"
	secContexts  = "CONTEXTS"
	secCLI       = "COMMAND LINE"
)

// outdatedHint is the readout of a favorite that was an official server of
// an earlier release (prefs.Outdated), in place of its ping: the same in
// both builds, unlike undialableHint.
const outdatedHint = "OUTDATED"

// loginCardW is the width of the login card. connPanelH is the fixed height
// of the connection page's panel — the same on both tabs, so switching never
// moves the Play button.
const (
	loginCardW = unit.Dp(560)
	connPanelH = unit.Dp(326)
)

// probeResult is the outcome of one server probe (Refresh / Check embedded
// server): the full ✓/✗ line for the detail row, plus the parts the browser
// rows show inline.
type probeResult struct {
	ok      bool
	msg     string
	rtt     time.Duration
	players int // human players in the server's lobby
	agents  int // agent players in it
	lobby   bool
}

// connEntry is one selectable server row of the browser: a URL entry (a
// favorite or the --server flag) or a NATS CLI context.
type connEntry struct {
	key    string // selection/probe key: "url:<url>" or "ctx:<name>"
	label  string
	detail string // muted second line (the URL; "" when unknown or same as label)
	url    string // dial target for URL entries
	ctx    string // context name for context entries
	fav    int    // index into a.favorites for deletable rows, -1 otherwise
	// named marks a row whose label is a name someone chose for the server —
	// a favorite's, or the one a join link gave — rather than where the row
	// came from. The lobby header shows those, "<name> (<url>)".
	named bool
	// dialable is false for a URL this build cannot dial (a nats:// one in
	// the browser): the row is listed greyed out and cannot be selected.
	dialable bool
	// outdated marks a favorite that was an official server of an earlier
	// release and is one no longer (prefs.Outdated): its readout says so,
	// the automatic selection passes it over, and the FAVORITES section's
	// cleanup row removes it.
	outdated bool
}

// connSection is one collapsible group of the browser tree.
type connSection struct {
	title   string
	entries []connEntry
	hint    string // shown in place of entries when there are none
	addRow  bool   // FAVORITES: ends with the "+ Add a NATS URL…" and "Reset favorites…" rows (and, when there is anything to clean up, "Remove outdated servers")
}

func urlKey(url string) string  { return "url:" + url }
func ctxKey(name string) string { return "ctx:" + name }

func (a *App) layoutLogin(gtx C) D {
	// --- event handling ---
	submitted := a.loginBtn.Clicked(gtx)
	// A name that came with the connection (--name, or the join page's
	// ?player=) plays this screen's Play button for it, once: the server was
	// chosen by whoever wrote the link and the name is answered, so there is
	// nothing here left to ask. Cleared before the submit, so a connect that
	// fails leaves the player on this screen — error shown, name still in the
	// field, every server in the browser one tap away — and quitting the
	// lobby comes back here without rejoining.
	if a.autoLogin {
		a.autoLogin = false
		submitted = true
	}
	for {
		ev, ok := a.loginEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			submitted = true
		}
	}
	for _, ed := range []*widget.Editor{&a.connNameEd, &a.connHostEd, &a.connPortEd, &a.connWSPortEd, &a.connHTTPPortEd} {
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				submitted = true
			}
		}
	}
	if a.pickerActive() {
		// The page just opened (the app's start, or back from the lobby):
		// size up every favorite at once, then sort them by ping.
		a.mu.Lock()
		fresh := !a.connRefreshed
		a.connRefreshed = true
		a.mu.Unlock()
		if fresh {
			a.refreshFavorites()
		}
		a.handleConnPage(gtx)
	}

	a.mu.Lock()
	collision := a.loginCollision
	loggingIn := a.loggingIn
	loginErr := a.loginErr
	a.mu.Unlock()

	if collision {
		if a.collisionYes.Clicked(gtx) {
			name := strings.TrimSpace(a.loginEd.Text())
			a.mu.Lock()
			a.loginCollision = false
			a.loggingIn = true
			a.mu.Unlock()
			go a.doLogin(name, true)
		}
		if a.collisionNo.Clicked(gtx) {
			a.mu.Lock()
			a.loginCollision = false
			a.mu.Unlock()
		}
	} else if submitted && !loggingIn && !a.connResetOpen {
		a.submitLogin()
	}

	// --- render ---
	// The artwork fills the window behind the card; the reset-favorites
	// confirmation, when up, scrims both and sits on top.
	layers := []layout.StackChild{
		layout.Expanded(a.loginBackdrop),
		layout.Stacked(func(gtx C) D {
			// The column — title, card, tagline — scrolls in the window: a
			// phone with its keyboard up has a third of its height left,
			// and a Flex clamped to that drew the card's frame at the
			// window's foot with the server browser running on out of it,
			// under the keyboard. In a list every row keeps its height and
			// the rest is a swipe away, the name field at the top still.
			// While the window has room for the whole column it is centred
			// in it, as before: a list's row has no bottom to centre
			// against, so the column is placed by its height of the frame
			// before (loginColH), a frame behind on a resize.
			gtx.Constraints.Min = gtx.Constraints.Max
			winH := gtx.Constraints.Max.Y
			return material.List(a.th, &a.loginList).Layout(gtx, 1, func(gtx C, _ int) D {
				if a.loginColH == 0 {
					// The first frame measures the column before placing it,
					// into a recording that is thrown away, rather than draw
					// it once at the wrong height: no input has arrived yet
					// for the two layouts to share.
					measure := op.Record(gtx.Ops)
					a.loginColH = a.loginColumn(gtx, collision, loggingIn, loginErr).Size.Y
					measure.Stop()
				}
				top := max(0, (winH-a.loginColH)/2)
				defer op.Offset(image.Pt(0, top)).Push(gtx.Ops).Pop()
				d := a.loginColumn(gtx, collision, loggingIn, loginErr)
				if d.Size.Y != a.loginColH {
					a.loginColH = d.Size.Y
					gtx.Execute(op.InvalidateCmd{}) // placed by this height next frame
				}
				d.Size.Y += top
				return d
			})
		}),
	}
	if a.connResetOpen {
		layers = append(layers,
			layout.Expanded(a.modalScrim),
			layout.Stacked(func(gtx C) D {
				gtx.Constraints.Min = gtx.Constraints.Max
				return a.confirmResetOverlay(gtx)
			}),
		)
	}
	return layout.Stack{Alignment: layout.Center}.Layout(gtx, layers...)
}

// loginColumn is the login screen's column: the marquee title, the card, the
// tagline and the update notice, centred on each other.
func (a *App) loginColumn(gtx C, collision, loggingIn bool, loginErr string) D {
	// The card is loginCardW wide, or the window's width where that
	// is narrower — a phone's is. Pinned to 560 dp regardless, the
	// name field and the server browser hang off both edges of the
	// screen and the game cannot be reached at all.
	// The column is centred across the window it scrolls in: a list draws
	// its row at the left edge (its Alignment centres rows on the widest of
	// them, and the column is the only row), so the row is the window's
	// width and the column is offset within it.
	winW := gtx.Constraints.Max.X
	cardW := min(gtx.Dp(loginCardW), winW-gtx.Dp(12))
	gtx.Constraints.Max.X = cardW
	gtx.Constraints.Min.X = cardW
	defer op.Offset(image.Pt(max(0, (winW-cardW)/2), 0)).Push(gtx.Ops).Pop()
	d := layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// The title flanked by NATS "N" logos, arcade-marquee style,
			// centered over the card. On a compact screen it centres in
			// the room the version plate leaves it (versionBadge, the
			// frame's top-right corner) rather than running its second
			// logo under the plate.
			inset := layout.Inset{}
			if a.form.compact {
				inset.Right = unit.Dp(96)
			}
			return inset.Layout(gtx, func(gtx C) D {
				return layout.Center.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return natsLogo(gtx, 36) }),
						layout.Rigid(hSpacer(14)),
						layout.Rigid(a.pixel(unit.Sp(28), "JETRIS", colAccent).Layout),
						layout.Rigid(hSpacer(14)),
						layout.Rigid(func(gtx C) D { return natsLogo(gtx, 36) }),
					)
				})
			})
		}),
		layout.Rigid(spacer(14)),
		layout.Rigid(func(gtx C) D {
			return a.loginCard(gtx, func(gtx C) D {
				if collision {
					return a.loginCollisionContent(gtx)
				}
				return a.loginNormalContent(gtx, loggingIn, loginErr)
			})
		}),
		layout.Rigid(spacer(14)),
		layout.Rigid(a.loginTagline),
		layout.Rigid(a.updateNotice),
	)
	d.Size.X = winW
	return d
}

// modalScrim dims the screen under a modal and takes every press aimed at
// what it covers: its window-sized pointer area is registered after the
// screen's widgets, so it is the foremost handler under the pointer and Gio
// hands it their presses; the modal's own buttons, laid out after it, stay
// on top. The presses are drained and dropped.
func (a *App) modalScrim(gtx C) D {
	sz := gtx.Constraints.Max
	fillRect(gtx.Ops, image.Rect(0, 0, sz.X, sz.Y), withAlpha(colBg, 0xc0))
	defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &a.scrimTag)
	for {
		if _, ok := gtx.Source.Event(pointer.Filter{Target: &a.scrimTag, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}
	return D{Size: sz}
}

// confirmResetOverlay is the modal behind the FAVORITES section's "Reset
// favorites…" row: it spells out that the current bookmarks go and the
// fresh-install defaults come back, and asks before doing it.
func (a *App) confirmResetOverlay(gtx C) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 460)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colErr, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "RESET FAVORITES?", colErr).Layout),
							layout.Rigid(spacer(10)),
							layout.Rigid(a.body("This will reset the favorites to the defaults", colFg)),
							layout.Rigid(a.body("and delete all the current favorites.", colFg)),
							layout.Rigid(spacer(6)),
							layout.Rigid(a.body("Are you sure you want to do that?", colFg)),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &a.connResetYes, "Yes, reset") }),
									layout.Rigid(hSpacer(10)),
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.connResetNo, "Cancel") }),
								)
							}),
						)
					})
				})
			})
		})
	})
}

// updateNotice, under the tagline, tells the player a newer release exists and
// where to download it — only once the startup check has found one (nothing is
// laid out otherwise): a gold pixel headline with the new version, then the
// release page's URL in plain type so it can be read off and typed.
func (a *App) updateNotice(gtx C) D {
	tag, url := a.update()
	if tag == "" {
		return D{}
	}
	return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx C) D {
		// Shrink to the text so the card's column centers the block and the
		// block centers its own two lines (a flex hands its full-width
		// minimum down, which would left-align both).
		gtx.Constraints.Min.X = 0
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(9), "▲ UPDATE AVAILABLE · JETRIS "+strings.ToUpper(strings.TrimPrefix(tag, "v")), colGold).Layout),
			layout.Rigid(spacer(4)),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(a.body("Download it at ", colMuted)),
					layout.Rigid(a.body(url, colFg)),
				)
			}),
		)
	})
}

// loginCard frames the login form like the create-game wizard — accent
// border over a hard shadow — on a near-opaque panel so the backdrop artwork
// shows through only faintly.
func (a *App) loginCard(gtx C, content layout.Widget) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, withAlpha(colBg, 0.93), func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.UniformInset(unit.Dp(18)).Layout(gtx, content)
			})
		})
	})
}

// submitLogin validates the entered name and kicks off the async login: in
// picker mode it first resolves the connection choice (the browser's selected
// server, or the LAN-mode server) and dispatches doConnectAndLogin; otherwise
// (already connected) plain doLogin. Runs on the UI goroutine.
func (a *App) submitLogin() {
	name := strings.TrimSpace(a.loginEd.Text())
	if name == "" {
		// Play with the field blank deals a name rather than demanding one:
		// Anonymous_, a listed handle and two digits, put in the field so the
		// player sees who they are — and can retype it if the server has it.
		name = dealName(anonymousPrefix)
		a.loginEd.SetText(name)
	}
	if err := config.ValidatePlayerName(name); err != nil {
		a.setLoginErr(err.Error())
		return
	}
	if !a.pickerActive() {
		a.setLoginErr("")
		a.mu.Lock()
		a.loggingIn = true
		a.mu.Unlock()
		go a.doLogin(name, false)
		return
	}

	cfg, err := a.pickerConfig()
	if err != nil {
		a.setLoginErr(err.Error())
		return
	}
	// A server that has a name — a favorite's, or the one a join link gave —
	// keeps it for the lobby header.
	favorite := ""
	if e, ok := a.connEntry(a.connSel); ok && !cfg.RunEmbedded && e.named {
		favorite = e.label
	}
	a.setLoginErr("")
	a.mu.Lock()
	a.loggingIn = true
	a.mu.Unlock()
	go a.doConnectAndLogin(name, cfg, favorite)
}

// pickerConfig resolves the connection page into a config: on the LAN tab the
// embedded-server mark plus the entered name, IP and ports; on the browser tab the
// selected row — a URL entry dials its URL, a context entry connects through
// that NATS CLI context (errors when nothing is selected). The base is
// connCfg, so --user/--password flags carry through to URL connects. Runs on
// the UI goroutine (reads widgets).
func (a *App) pickerConfig() (config.Config, error) {
	cfg := a.connCfg
	cfg.NATSURL, cfg.NATSContext, cfg.RunEmbedded = "", "", false
	cfg.EmbeddedHost, cfg.EmbeddedPort, cfg.EmbeddedWSPort, cfg.EmbeddedHTTPPort, cfg.EmbeddedName = "", 0, 0, 0, ""
	if a.connTab == connTabLAN {
		cfg.RunEmbedded = true
		name, err := a.pickerName()
		if err != nil {
			return cfg, err
		}
		cfg.EmbeddedName = name
		host, err := a.pickerHost()
		if err != nil {
			return cfg, err
		}
		cfg.EmbeddedHost = host
		cfg.EmbeddedPort, cfg.EmbeddedWSPort, cfg.EmbeddedHTTPPort, err = a.pickerPorts()
		if err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	return a.entryConfig(a.connSel)
}

// entryConfig is the config that dials browser entry key — a favorite's or
// the --server flag's URL (with any --user/--password), or a NATS CLI
// context — the way Play and the probes dial it.
func (a *App) entryConfig(key string) (config.Config, error) {
	cfg := a.connCfg
	cfg.NATSURL, cfg.NATSContext, cfg.RunEmbedded = "", "", false
	cfg.EmbeddedHost, cfg.EmbeddedPort, cfg.EmbeddedWSPort, cfg.EmbeddedHTTPPort, cfg.EmbeddedName = "", 0, 0, 0, ""
	e, ok := a.connEntry(key)
	if !ok {
		// Nothing selected. When a URL was handed to this build that it
		// cannot dial — a nats:// one in a browser, a ws:// one on an https
		// page — it was listed but never selected (NewWithPicker), and
		// saying so beats sending the player back to a list to find out.
		if u := a.connCfg.NATSURL; u != "" && !dialable(u) {
			return cfg, errors.New(u + ": " + undialableErr)
		}
		return cfg, errors.New("select a server in the browser (or add one to your favorites)")
	}
	if !e.dialable {
		return cfg, errors.New(undialableErr + " — pick another server")
	}
	if e.url != "" {
		cfg.NATSURL = e.url
	} else {
		cfg.NATSContext = e.ctx
	}
	return cfg, nil
}

// lanNameMax bounds the LAN party's server name: it heads every guest's lobby
// bar and rides in the join link's QR code, and neither wants a paragraph.
const lanNameMax = 64

// pickerName reads the LAN-mode Name field — what the server is called, in
// the host's lobby header ("<you> @ <name>"), on the join page and in every
// guest's header: empty means the default. Runs on the UI goroutine.
func (a *App) pickerName() (string, error) {
	name := strings.TrimSpace(a.connNameEd.Text())
	if name == "" {
		return "", nil
	}
	if len(name) > lanNameMax {
		return "", fmt.Errorf("keep the server name under %d characters", lanNameMax)
	}
	return name, nil
}

// pickerHost parses the LAN-mode IP field: empty means "detect the LAN
// address again at connect time", anything else is the address Jetris
// advertises and dials (the server itself still listens on every interface, so
// any address that actually reaches this machine works — including a host name
// or an IPv6 literal). Runs on the UI goroutine (reads the widget).
func (a *App) pickerHost() (string, error) {
	host := strings.TrimSpace(a.connHostEd.Text())
	if host == "" {
		return "", nil
	}
	// Colons are legal in an IPv6 literal and nowhere else here; the rest are
	// the classic paste-the-whole-URL mistakes ("nats://host:4222").
	if net.ParseIP(host) == nil && strings.ContainsAny(host, " \t/:@") {
		return "", errors.New("enter a valid IP address or host name (no scheme or port)")
	}
	return host, nil
}

// pickerPort parses the LAN-mode NATS port field: empty means the default,
// anything else must be a valid TCP port. Runs on the UI goroutine (reads the
// widget).
func (a *App) pickerPort() (int, error) {
	return portField(&a.connPortEd, config.DefaultEmbeddedPort, "NATS")
}

// pickerWSPort is pickerPort for the WebSocket listener's port.
func (a *App) pickerWSPort() (int, error) {
	return portField(&a.connWSPortEd, config.DefaultEmbeddedWSPort, "WebSocket")
}

// pickerHTTPPort is pickerPort for the browser build's HTTP port.
func (a *App) pickerHTTPPort() (int, error) {
	return portField(&a.connHTTPPortEd, config.DefaultEmbeddedHTTPPort, "HTTP")
}

// portField parses one port editor: empty means def, anything else must be a
// valid TCP port; what names the port in the error.
func portField(ed *widget.Editor, def int, what string) (int, error) {
	text := strings.TrimSpace(ed.Text())
	if text == "" {
		return def, nil
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("enter a valid %s port number (1-65535)", what)
	}
	return port, nil
}

// pickerPorts is the LAN party's three ports — NATS, WebSocket, HTTP — each
// parsed from its field, and checked against each other: three listeners
// cannot share a port. Runs on the UI goroutine.
func (a *App) pickerPorts() (nats, ws, http int, err error) {
	if nats, err = a.pickerPort(); err != nil {
		return
	}
	if ws, err = a.pickerWSPort(); err != nil {
		return
	}
	if http, err = a.pickerHTTPPort(); err != nil {
		return
	}
	err = distinctPorts(nats, ws, http)
	return
}

// distinctPorts is the error for two of the LAN party's ports being the same,
// nil when all three differ.
func distinctPorts(nats, ws, http int) error {
	if nats == ws || nats == http || ws == http {
		return errors.New("the NATS, WebSocket and HTTP ports must all differ")
	}
	return nil
}

// pickerAddr is the "<ip>:<port>" the LAN-mode rows advertise for NATS: the
// entered address and port, each falling back to its auto-detected/default
// value while its field is empty or not (yet) valid — the line keeps showing
// a usable address while the player is mid-edit. Runs on the UI goroutine.
func (a *App) pickerAddr() string {
	port, err := a.pickerPort()
	if err != nil {
		port = config.DefaultEmbeddedPort
	}
	return net.JoinHostPort(a.pickerHostOrDetected(), strconv.Itoa(port))
}

// pickerHTTPAddr is pickerAddr for the browser build's page.
func (a *App) pickerHTTPAddr() string {
	port, err := a.pickerHTTPPort()
	if err != nil {
		port = config.DefaultEmbeddedHTTPPort
	}
	return net.JoinHostPort(a.pickerHostOrDetected(), strconv.Itoa(port))
}

// pickerHostOrDetected is the IP field, or the detected LAN address while the
// field is empty or not (yet) valid.
func (a *App) pickerHostOrDetected() string {
	host, err := a.pickerHost()
	if err != nil || host == "" {
		host = a.lanIP
	}
	return host
}

// setLoginErr sets (or clears) the login screen's error line.
func (a *App) setLoginErr(msg string) {
	a.mu.Lock()
	a.loginErr = msg
	a.mu.Unlock()
}

// loginNormalContent is the card's body: the name entry on top, the two-tab
// connection page in the middle, the big Play button at the bottom.
func (a *App) loginNormalContent(gtx C, loggingIn bool, loginErr string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("YOUR NAME")),
		layout.Rigid(func(gtx C) D {
			return a.editorBox(gtx, &a.loginEd, "Enter your name")
		}),
		layout.Rigid(func(gtx C) D {
			l := material.Body2(a.th, "No spaces, dots, or wildcards; max 32 characters. Blank gets you a random one.")
			l.Color = colMuted
			return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, l.Layout)
		}),
		layout.Rigid(spacer(14)),
		layout.Rigid(func(gtx C) D {
			if a.pickerActive() {
				return a.connPage(gtx)
			}
			return D{}
		}),
		layout.Rigid(spacer(16)),
		layout.Rigid(func(gtx C) D {
			label := "PLAY"
			if loggingIn {
				label = "CONNECTING…"
			}
			// Play is the action this whole screen is waiting on: the
			// attract treatment, at marquee size, across the card.
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return a.bigAttractButton(gtx, &a.loginBtn, label)
		}),
		layout.Rigid(func(gtx C) D {
			if loginErr == "" {
				return D{}
			}
			l := material.Body2(a.th, loginErr)
			l.Color = colErr
			return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, l.Layout)
		}),
	)
}

func (a *App) loginCollisionContent(gtx C) D {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			l := material.Body1(a.th, "That name is already in use. Join anyway?")
			l.Color = colFg
			return l.Layout(gtx)
		}),
		layout.Rigid(spacer(12)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.collisionYes, "Yes, join") }),
				layout.Rigid(hSpacer(10)),
				layout.Rigid(func(gtx C) D {
					return a.secondaryButton(gtx, &a.collisionNo, "Cancel")
				}),
			)
		}),
	)
}

// --- the connection page: tabs, server browser, LAN mode ---

// connSections builds the browser tree from the current state: FAVORITES
// (always, with its add row), CONTEXTS (always — a hint when the machine has
// none) and COMMAND LINE only while --server names a URL that isn't a
// favorite. Built fresh every frame; rows are cheap.
func (a *App) connSections() []connSection {
	favs := connSection{title: secFavorites, addRow: true, hint: "no favorites yet — add a NATS URL below"}
	for _, i := range a.favoriteIndices() {
		f := a.favorites[i]
		detail := f.URL
		if detail == f.Label {
			detail = ""
		}
		favs.entries = append(favs.entries, connEntry{key: urlKey(f.URL), label: f.Label, detail: detail, url: f.URL, fav: i, named: true, dialable: dialable(f.URL), outdated: prefs.Outdated(f.URL)})
	}
	ctxs := connSection{title: secContexts, hint: "no NATS CLI contexts on this machine (nats context add …)"}
	for _, name := range a.connContexts {
		label := name
		if name == a.connSelected {
			// The nats CLI's own current context (nats context select) —
			// worded so it is never confused with the browser's selection.
			label += " (nats CLI current)"
		}
		ctxs.entries = append(ctxs.entries, connEntry{key: ctxKey(name), label: label, detail: a.connCtxURLs[name], ctx: name, fav: -1, dialable: true})
	}
	sections := []connSection{favs, ctxs}
	if url := a.connCfg.NATSURL; url != "" && !a.isFavorite(url) {
		// Where the row came from is its label — unless the link that
		// brought the URL also named the server (the page's ?name=), which
		// is a better thing to show than "--server".
		label, named := "--server", false
		if a.connCfg.ServerLabel != "" {
			label, named = a.connCfg.ServerLabel, true
		}
		sections = append(sections, connSection{title: secCLI, entries: []connEntry{
			{key: urlKey(url), label: label, detail: url, url: url, fav: -1, named: named, dialable: dialable(url)},
		}})
	}
	return sections
}

// connEntry finds the browser entry with the given key.
func (a *App) connEntry(key string) (connEntry, bool) {
	if key == "" {
		return connEntry{}, false
	}
	for _, sec := range a.connSections() {
		for _, e := range sec.entries {
			if e.key == key {
				return e, true
			}
		}
	}
	return connEntry{}, false
}

// isFavorite reports whether url is already bookmarked.
func (a *App) isFavorite(url string) bool {
	for _, f := range a.favorites {
		if f.URL == url {
			return true
		}
	}
	return false
}

// clickable returns the per-key widget from m, creating it on first use.
func clickable(m map[string]*widget.Clickable, key string) *widget.Clickable {
	b, ok := m[key]
	if !ok {
		b = &widget.Clickable{}
		m[key] = b
	}
	return b
}

// handleConnPage dispatches every click on the connection page: tab
// switches, section toggles, row selection (which also probes the row),
// favorite add/delete, "Refresh all servers" and LAN mode's Check embedded
// server. Runs on the UI goroutine, before the frame is drawn.
func (a *App) handleConnPage(gtx C) {
	a.applyRefreshRound()
	if a.connTabBtns[0].Clicked(gtx) {
		a.connTab = connTabBrowser
	}
	if a.connTabBtns[1].Clicked(gtx) {
		a.connTab = connTabLAN
	}
	for _, sec := range a.connSections() {
		if clickable(a.connSecBtns, sec.title).Clicked(gtx) {
			a.connSecClosed[sec.title] = !a.connSecClosed[sec.title]
		}
		for _, e := range sec.entries {
			if e.dialable && clickable(a.connRowBtns, e.key).Clicked(gtx) {
				a.connSel = e.key
				a.connPicked = true // the player's own pick: the refresh round leaves it be
				a.setLoginErr("")
				a.probeRow(e.key)
			}
			if e.fav >= 0 && clickable(a.connDelBtns, e.key).Clicked(gtx) {
				a.deleteFavorite(e.fav)
			}
		}
	}
	if a.connAddRowBtn.Clicked(gtx) {
		a.connAddOpen = true
		a.connAddScroll = true
		a.connSecClosed[secFavorites] = false
		gtx.Execute(key.FocusCmd{Tag: &a.connAddURLEd})
	}
	add := a.connAddBtn.Clicked(gtx)
	for _, ed := range []*widget.Editor{&a.connAddLabelEd, &a.connAddURLEd} {
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				add = true
			}
		}
	}
	if add {
		a.addFavorite()
	}
	if a.connAddCancel.Clicked(gtx) {
		a.closeAddForm()
	}
	if a.connCleanupBtn.Clicked(gtx) {
		a.removeOutdatedFavorites()
	}
	if a.connResetRowBtn.Clicked(gtx) {
		a.connResetOpen = true
	}
	if a.connResetYes.Clicked(gtx) {
		a.connResetOpen = false
		a.resetFavorites()
	}
	if a.connResetNo.Clicked(gtx) {
		a.connResetOpen = false
	}

	if a.connRefreshAll.Clicked(gtx) && a.connTab == connTabBrowser {
		a.refreshAll()
	}
	if a.connCheckBtn.Clicked(gtx) && a.connTab == connTabLAN {
		a.startProbe(probeKeyLAN)
	}
}

// probeRow probes a browser row the player just clicked, so a single click
// both selects a server and sizes it up — clicking the selected row again is
// how one server is re-checked, "Refresh all servers" how they all are.
// Probes run several at once, one per server; a row already being probed
// is left to finish. Runs on the UI goroutine.
func (a *App) probeRow(key string) { a.startProbe(key) }

// startProbe kicks off the probe of key — a browser row or the LAN-mode
// server — unless that key is already being probed; the choice must resolve
// to a config first (nothing selected, a bad port… land on the error line).
func (a *App) startProbe(key string) {
	a.mu.Lock()
	running := a.connProbing[key]
	a.mu.Unlock()
	if running {
		return
	}
	var (
		cfg config.Config
		err error
	)
	if key == probeKeyLAN {
		cfg, err = a.pickerConfig()
	} else {
		cfg, err = a.entryConfig(key)
	}
	if err != nil {
		a.setLoginErr(err.Error())
		return
	}
	a.setLoginErr("")
	a.mu.Lock()
	a.connProbing[key] = true
	delete(a.connProbes, key)
	a.mu.Unlock()
	go a.doCheckConn(key, cfg)
}

// refreshFavorites probes every favorite this build can dial, all at once —
// their rows read "refreshing" meanwhile — as one round: when the last
// result is in, applyRefreshRound sorts the favorites by ping and selects
// the fastest. Runs on the UI goroutine, when the connection page opens.
func (a *App) refreshFavorites() {
	var keys []string
	for _, f := range a.favorites {
		if dialable(f.URL) {
			keys = append(keys, urlKey(f.URL))
		}
	}
	a.connPicked = false // the page just opened: the round's fastest is the pick
	a.startRound(keys)
}

// refreshAll is the browser's "Refresh all servers" row: one round over every
// row this build can dial — the contexts and the --server row too, not just
// the favorites — so the whole list's pings and head counts are current. The
// player's own selection stands (only a round they never picked in, the
// page-opening one, moves it). Runs on the UI goroutine.
func (a *App) refreshAll() {
	var keys []string
	for _, sec := range a.connSections() {
		for _, e := range sec.entries {
			if e.dialable {
				keys = append(keys, e.key)
			}
		}
	}
	a.startRound(keys)
}

// startRound probes keys as one refresh round: their rows read "refreshing"
// meanwhile, and doCheckConn flags the round done once the last result is in
// (applyRefreshRound then sorts and selects). A key already being probed is
// left to finish — its result closes the round all the same.
func (a *App) startRound(keys []string) {
	a.mu.Lock()
	a.connRound = make(map[string]bool, len(keys))
	for _, k := range keys {
		a.connRound[k] = true
	}
	a.connRoundDone = false
	a.mu.Unlock()
	for _, k := range keys {
		a.startProbe(k)
	}
}

// roundRunning reports whether a refresh round is still out.
func (a *App) roundRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.connRound) > 0
}

// applyRefreshRound acts on a finished refresh round (doCheckConn flags it):
// the favorites are sorted by ping — the reachable ones fastest first, then
// the ones that failed, then the outdated ones, then the ones this build
// cannot dial, the list's own order breaking ties — and the fastest becomes
// the selection, unless the player picked a row meanwhile or Play is set on
// a context or the --server flag (their explicit choice stands). An
// outdated server is never the automatic pick, reachable or not: right
// after a move it may still answer, at the same ping as its successor.
// Called every frame by handleConnPage, on the UI goroutine.
func (a *App) applyRefreshRound() {
	a.mu.Lock()
	done := a.connRoundDone
	a.connRoundDone = false
	probes := make(map[string]probeResult, len(a.connProbes))
	for k, v := range a.connProbes {
		probes[k] = v
	}
	a.mu.Unlock()
	if !done {
		return
	}
	a.favOrder = favoriteOrder(a.favorites, probes)
	if a.connPicked {
		return
	}
	if e, ok := a.connEntry(a.connSel); ok && e.fav < 0 {
		return
	}
	for _, i := range a.favOrder {
		key := urlKey(a.favorites[i].URL)
		if p := probes[key]; p.ok && !prefs.Outdated(a.favorites[i].URL) {
			a.connSel = key
			return
		}
	}
}

// favoriteOrder is the favorites' display order after a refresh round:
// indices into favs, the reachable ones by ping ascending, then the ones
// whose probe failed, then the outdated ones (prefs.Outdated — grouped at
// the bottom, above the cleanup row that removes them, whether or not they
// still answer), then the ones this build cannot dial — the list's own
// order breaking ties, so the sort is stable and predictable.
func favoriteOrder(favs []prefs.Favorite, probes map[string]probeResult) []int {
	rank := func(i int) (int, time.Duration) {
		f := favs[i]
		if !dialable(f.URL) {
			return 3, 0
		}
		if prefs.Outdated(f.URL) {
			return 2, 0
		}
		if p, ok := probes[urlKey(f.URL)]; ok && p.ok {
			return 0, p.rtt
		}
		return 1, 0
	}
	order := make([]int, len(favs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		rx, tx := rank(order[x])
		ry, ty := rank(order[y])
		if rx != ry {
			return rx < ry
		}
		return tx < ty
	})
	return order
}

// favoriteIndices is the order the favorites are listed in: the last
// refresh round's (favOrder) while it still fits the list, the list's own
// otherwise.
func (a *App) favoriteIndices() []int {
	if len(a.favOrder) == len(a.favorites) {
		return a.favOrder
	}
	order := make([]int, len(a.favorites))
	for i := range order {
		order[i] = i
	}
	return order
}

// addFavorite reads the add form, bookmarks the URL (label defaults to the
// URL without its scheme), persists the list, selects the new row and closes
// the form. A URL already bookmarked is selected instead of duplicated.
func (a *App) addFavorite() {
	url := strings.TrimSpace(a.connAddURLEd.Text())
	label := strings.TrimSpace(a.connAddLabelEd.Text())
	if url == "" {
		a.setLoginErr("enter the NATS URL to add (nats://host:4222)")
		return
	}
	if strings.ContainsAny(url, " \t") {
		a.setLoginErr("a NATS URL cannot contain spaces")
		return
	}
	if !dialable(url) {
		a.setLoginErr(undialableErr + " (" + addURLHint + ")")
		return
	}
	if a.isFavorite(url) {
		a.connSel = urlKey(url)
		a.closeAddForm()
		return
	}
	if label == "" {
		label = strings.TrimPrefix(strings.TrimPrefix(url, "nats://"), "tls://")
	}
	a.favorites = append(a.favorites, prefs.Favorite{Label: label, URL: url})
	a.connSel = urlKey(url)
	a.closeAddForm()
	a.persistFavorites()
}

// deleteFavorite removes favorite i, moving the selection to the next best row
// if it was selected, and persists the list.
func (a *App) deleteFavorite(i int) {
	if i < 0 || i >= len(a.favorites) {
		return
	}
	key := urlKey(a.favorites[i].URL)
	a.favorites = append(a.favorites[:i:i], a.favorites[i+1:]...)
	if a.connSel == key {
		a.connSel = a.firstDialableEntry()
	}
	a.persistFavorites()
}

// firstDialableEntry is the key of the first browser row this build can
// dial, section by section, "" when there is none. An outdated favorite is
// passed over, as by every automatic selection: it is listed for the player
// to click or clean up, not chosen for them.
func (a *App) firstDialableEntry() string {
	for _, sec := range a.connSections() {
		for _, e := range sec.entries {
			if e.dialable && !e.outdated {
				return e.key
			}
		}
	}
	return ""
}

// outdatedCount is how many favorites are outdated (prefs.Outdated): the
// count the cleanup row shows, and whether it shows at all.
func (a *App) outdatedCount() int {
	n := 0
	for _, f := range a.favorites {
		if prefs.Outdated(f.URL) {
			n++
		}
	}
	return n
}

// removeOutdatedFavorites is the FAVORITES section's "Remove outdated
// servers" row: it drops every outdated favorite (prefs.RemoveOutdated) —
// the player's own bookmarks and the current official servers stay, in
// their order — moving the selection to the next best row if it was one of
// them, and persists the list. The add form, if open, is left alone: it is
// not about the rows that went.
func (a *App) removeOutdatedFavorites() {
	var n int
	a.favorites, n = prefs.RemoveOutdated(a.favorites)
	if n == 0 {
		return
	}
	if _, ok := a.connEntry(a.connSel); !ok {
		a.connSel = a.firstDialableEntry()
	}
	a.persistFavorites()
}

// resetFavorites replaces the bookmarks with the fresh-install defaults
// (prefs.DefaultFavorites) and persists the list. The selection stays where
// it is if that row still exists and can be dialed (a context, a default
// server); otherwise it moves to the first default this build can dial, as
// on a fresh install. The add form, if open, is dropped with the list it was
// extending.
func (a *App) resetFavorites() {
	a.favorites = prefs.DefaultFavorites()
	a.closeAddForm()
	if e, ok := a.connEntry(a.connSel); !ok || !e.dialable {
		a.connSel = a.firstDialableFavorite()
	}
	a.persistFavorites()
}

// persistFavorites saves the favorites; a failure is shown on the error line
// (the in-memory list still works for this session).
func (a *App) persistFavorites() {
	a.favOrder = nil // the list changed: its own order until the next refresh round
	if a.favSave == nil {
		return
	}
	favs := make([]prefs.Favorite, len(a.favorites)) // never nil: an emptied list must save as []
	copy(favs, a.favorites)
	if err := a.favSave(favs); err != nil {
		a.setLoginErr("couldn't save favorites: " + err.Error())
	} else {
		a.setLoginErr("")
	}
}

// loginTagline is the branding line at the foot of the login card. It runs
// along one line where the card holds it and breaks after "made with" where
// it does not: squeezed, its last rigid child was left a few dp and split
// "JetStream" across two lines mid-word.
func (a *App) loginTagline(gtx C) D {
	const lead = "peer to peer · made with "
	tag := func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.natsTag(18, 9)),
			layout.Rigid(a.pixel(unit.Sp(9), " JetStream", colAccent).Layout),
		)
	}
	// The lead, the "N" chip and the two words beside it, measured in the
	// pixel face they are set in.
	want := a.pixelWidth(gtx, unit.Sp(9), lead+"NATS.io JetStream") + gtx.Dp(30)
	return layout.Center.Layout(gtx, func(gtx C) D {
		if want <= gtx.Constraints.Max.X {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.pixel(unit.Sp(9), lead, colMuted).Layout),
				layout.Rigid(tag),
			)
		}
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.pixel(unit.Sp(9), "peer to peer · made with", colMuted).Layout),
			layout.Rigid(spacer(4)),
			layout.Rigid(tag),
		)
	})
}

// closeAddForm collapses the add-favorite form and clears its fields.
func (a *App) closeAddForm() {
	a.connAddOpen = false
	a.connAddLabelEd.SetText("")
	a.connAddURLEd.SetText("")
}

// connPage renders the two-tab connection page: the tab chips on top and the
// active tab's panel below, framed as one unit.
func (a *App) connPage(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("CONNECT TO")),
		layout.Rigid(func(gtx C) D {
			// The two tabs name themselves in full where the card is wide
			// enough for both and in short where it is not: a chip squeezed
			// under its label does not elide, it wraps the label into stacked
			// lines and grows up over the tab beside it.
			browser, lan := "NATS SERVER BROWSER", "LAN PARTY MODE (EMBEDDED NATS SERVER)"
			if a.pixelWidth(gtx, unit.Sp(8), browser+lan)+gtx.Dp(52) > gtx.Constraints.Max.X {
				browser, lan = "SERVER BROWSER", "LAN PARTY MODE"
			}
			return layout.Flex{Alignment: layout.End}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return a.tabChip(gtx, &a.connTabBtns[0], browser, a.connTab == connTabBrowser)
				}),
				layout.Rigid(func(gtx C) D {
					if !embeddedAvailable {
						return D{} // no server to host in the browser build
					}
					return hSpacer(4)(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					if !embeddedAvailable {
						return D{}
					}
					return a.tabChip(gtx, &a.connTabBtns[1], lan, a.connTab == connTabLAN)
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colPanel, func(gtx C) D {
					// Exact height whichever tab is up (see connPanelH).
					gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(connPanelH)))
					return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx C) D {
						if a.connTab == connTabLAN {
							return a.lanTab(gtx)
						}
						return a.browserTab(gtx)
					})
				})
			})
		}),
	)
}

// tabChip is one tab: a pixel-face label on a chunky chip — accent-filled
// while active, panel-colored with a muted label otherwise — sitting on the
// panel's top border like a file-folder tab. The connection page's two tabs
// and the lobby's (lobbyPanel) are the same chip.
func (a *App) tabChip(gtx C, btn *widget.Clickable, label string, active bool) D {
	bg, fg, border := colPanel, colMuted, colBorder
	if active {
		bg, fg, border = colAccent, colBg, colAccent
	}
	return material.Clickable(gtx, btn, func(gtx C) D {
		return widget.Border{Color: border, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
			return background(gtx, bg, func(gtx C) D {
				return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx,
					a.pixel(unit.Sp(8), label, fg).Layout)
			})
		})
	})
}

// browserTab is the NATS server browser: the collapsible tree of servers in a
// list filling the panel, the SELECTED line naming the row Play will use, and
// that server's probe state on the line under it.
func (a *App) browserTab(gtx C) D {
	a.mu.Lock()
	probes := make(map[string]probeResult, len(a.connProbes))
	for k, v := range a.connProbes {
		probes[k] = v
	}
	probing := make(map[string]bool, len(a.connProbing))
	for k := range a.connProbing {
		probing[k] = true
	}
	a.mu.Unlock()

	rows := []layout.Widget{a.refreshAllRow(a.roundRunning())}
	for _, sec := range a.connSections() {
		sec := sec
		rows = append(rows, a.sectionRow(sec))
		if a.connSecClosed[sec.title] {
			continue
		}
		if len(sec.entries) == 0 {
			rows = append(rows, a.hintRow(sec.hint))
		}
		for _, e := range sec.entries {
			e := e
			rows = append(rows, a.entryRow(e, probes, probing))
		}
		if sec.addRow {
			if n := a.outdatedCount(); n > 0 {
				rows = append(rows, a.cleanupRow(n))
			}
			if a.connAddOpen {
				if a.connAddScroll {
					// Just opened: bring the form into view (it may sit
					// below the list's visible window).
					a.connBrowserLst.ScrollTo(len(rows))
					a.connAddScroll = false
				}
				rows = append(rows, a.addForm)
			} else {
				rows = append(rows, a.addRow)
			}
			rows = append(rows, a.resetRow)
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			return widget.Border{Color: colBorder, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					gtx.Constraints.Min = gtx.Constraints.Max
					return material.List(a.th, &a.connBrowserLst).Layout(gtx, len(rows), func(gtx C, i int) D {
						return rows[i](gtx)
					})
				})
			})
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(a.connSelectedLine),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D { return a.connStatusLine(gtx, a.connSel) }),
	)
}

// connSelectedLine names the browser row Play will connect through — a
// SELECTED chip in the accent, then the entry's name and URL (selectionCaption)
// — so the choice stays in view even when its row is scrolled out of the list
// or its section is collapsed. With nothing selected it says so, in the
// warning color, since Play would only error.
func (a *App) connSelectedLine(gtx C) D {
	tag, tagBg, label, labelCol, detail := "SELECTED", colAccent, "", colAccent, ""
	if e, ok := a.connEntry(a.connSel); ok {
		label, detail = selectionCaption(e)
	} else {
		tag, tagBg, label, labelCol = "NOTHING SELECTED", colWarn, "click a server above", colMuted
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return background(gtx, tagBg, func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(6), Right: unit.Dp(6)}.Layout(gtx,
					a.pixel(unit.Sp(8), tag, colBg).Layout)
			})
		}),
		layout.Rigid(hSpacer(8)),
		layout.Rigid(a.body(label, labelCol)),
		layout.Flexed(1, func(gtx C) D {
			if detail == "" {
				return D{}
			}
			// One line: a long URL ends in an ellipsis rather than wrapping
			// under the chip.
			l := material.Body2(a.th, " · "+detail)
			l.Color = colMuted
			l.MaxLines = 1
			return l.Layout(gtx)
		}),
	)
}

// selectionCaption words a browser entry for the SELECTED line: a favorite's
// name and URL, and likewise a server a join link named; a context as
// "context <name>" (without the row's nats-CLI marker) and its URL when
// known; an unnamed --server row as its URL, attributed to the flag.
func selectionCaption(e connEntry) (label, detail string) {
	switch {
	case e.ctx != "":
		return "context " + e.ctx, e.detail
	case !e.named:
		return e.url, "from --server"
	}
	return e.label, e.detail
}

// sectionRow is a collapsible section header: ▼/▶ plus the title in the
// pixel face, full width, on a panel stripe.
func (a *App) sectionRow(sec connSection) layout.Widget {
	return func(gtx C) D {
		arrow := "▼ "
		if a.connSecClosed[sec.title] {
			arrow = "▶ "
		}
		return material.Clickable(gtx, clickable(a.connSecBtns, sec.title), func(gtx C) D {
			return background(gtx, colPanel, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.body(arrow, colAccent)),
						layout.Rigid(a.pixel(unit.Sp(9), sec.title, colAccent).Layout),
						layout.Rigid(func(gtx C) D {
							n := fmt.Sprintf("  (%d)", len(sec.entries))
							return a.pixel(unit.Sp(8), n, colMuted).Layout(gtx)
						}),
					)
				})
			})
		})
	}
}

// entryRow is one selectable server. The selected row is a solid accent band
// across the list with dark type — the same "this one is active" treatment as
// the tab chips, unmistakable next to the plain rows. Every row shows its
// label over the URL in a second line, the last probe's inline summary on the
// right (on the selected row in a dark pill, so the green/red keeps reading
// against the accent), and — for favorites — a ✕ to delete. Clicking a row
// probes it, the selected one included, so no row needs a refresh chip of
// its own.
func (a *App) entryRow(e connEntry, probes map[string]probeResult, probing map[string]bool) layout.Widget {
	return func(gtx C) D {
		selected := e.key == a.connSel
		bg, labelCol, detailCol, delCol := colBg, colFg, colMuted, colErr
		if selected {
			bg, labelCol, detailCol, delCol = colAccent, colBg, withAlpha(colBg, 0.7), colBg
		}
		if !e.dialable {
			// Out of this build's reach: greyed out, and not a button at all
			// (no hover, no click) — only its ✕ still works.
			labelCol, detailCol = colMuted, withAlpha(colMuted, 0.6)
		}
		// The row body is a button only when the row can be picked.
		rowButton := func(gtx C, w layout.Widget) D {
			if !e.dialable {
				return w(gtx)
			}
			return material.Clickable(gtx, clickable(a.connRowBtns, e.key), w)
		}
		return background(gtx, bg, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return rowButton(gtx, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(28), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
							nameCol := func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(a.body(e.label, labelCol)),
									layout.Rigid(func(gtx C) D {
										if e.detail == "" {
											return D{}
										}
										// One line: a URL longer than the row
										// ends in an ellipsis rather than
										// growing the row a second line.
										l := material.Caption(a.th, e.detail)
										l.Color, l.MaxLines = detailCol, 1
										return l.Layout(gtx)
									}),
								)
							}
							txt, col := probeSummary(probes[e.key], probing[e.key])
							if !e.dialable {
								txt, col = undialableHint, withAlpha(colMuted, 0.8)
							}
							if e.outdated {
								// Why it is (probably) OFFLINE, and what the
								// cleanup row below the list is about — worth
								// more than its ping, even when it still has
								// one.
								txt, col = outdatedHint, colWarn
							}
							if txt == "" {
								return nameCol(gtx)
							}
							summary := func(gtx C) D {
								lbl := a.pixel(unit.Sp(8), txt, col).Layout
								if !selected {
									return lbl(gtx)
								}
								return background(gtx, colBg, func(gtx C) D {
									return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(5), Right: unit.Dp(5)}.Layout(gtx, lbl)
								})
							}
							// The readout gives way before the NAME does. It
							// is a pixel-face line — "21 ms · 2 players · 3
							// agents" is 200 dp of it — and as a rigid child
							// beside a flexed name it took the row's width
							// first, leaving "Jetris EU central" to come out
							// one letter per line down the list. Beside the
							// name where both fit, under it where they do not.
							if a.bodyWidth(gtx, e.label)+gtx.Dp(16)+a.pixelWidth(gtx, unit.Sp(8), txt) <= gtx.Constraints.Max.X {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, nameCol),
									layout.Rigid(func(gtx C) D {
										return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, summary)
									}),
								)
							}
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(nameCol),
								layout.Rigid(func(gtx C) D {
									return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, summary)
								}),
							)
						})
					})
				}),
				layout.Rigid(func(gtx C) D {
					if e.fav < 0 {
						return D{}
					}
					return material.Clickable(gtx, clickable(a.connDelBtns, e.key), func(gtx C) D {
						return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(10)}.Layout(gtx,
							a.pixel(unit.Sp(9), "✕", delCol).Layout)
					})
				}),
			)
		})
	}
}

// refreshAllRow heads the browser list: "↻ Refresh all servers" probes every
// row this build can dial in one round (refreshAll). While the round is out —
// the page-opening one included — it reads as refreshing, in muted type, and
// is not a button at all.
func (a *App) refreshAllRow(busy bool) layout.Widget {
	return func(gtx C) D {
		txt, col := "Refresh all servers", colNATSGreen
		if busy {
			txt, col = "Refreshing all servers…", colMuted
		}
		row := func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(10), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(glyphWidget(glyphCW, 10, col)),
					layout.Rigid(hSpacer(7)),
					layout.Rigid(a.body(txt, col)),
				)
			})
		}
		if busy {
			return row(gtx)
		}
		return material.Clickable(gtx, &a.connRefreshAll, row)
	}
}

// hintRow is a section's muted placeholder when it has no entries.
func (a *App) hintRow(txt string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(5), Left: unit.Dp(28), Right: unit.Dp(8)}.Layout(gtx, a.body(txt, colMuted))
	}
}

// addRow is the FAVORITES section's trailing "+ Add a NATS URL…" action.
func (a *App) addRow(gtx C) D {
	return material.Clickable(gtx, &a.connAddRowBtn, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(28), Right: unit.Dp(8)}.Layout(gtx,
			a.body("+ Add a NATS URL…", colNATSGreen))
	})
}

// cleanupRow is the FAVORITES section's "Remove <n> outdated server(s)" row,
// listed right under the favorites — and only while some are outdated
// (prefs.Outdated, their readout the same warning color). One click removes
// them (removeOutdatedFavorites): no dialog, since the rows it takes are the
// tagged ones and nothing of the player's own.
func (a *App) cleanupRow(n int) layout.Widget {
	txt := fmt.Sprintf("Remove %d outdated servers", n)
	if n == 1 {
		txt = "Remove 1 outdated server"
	}
	return func(gtx C) D {
		return material.Clickable(gtx, &a.connCleanupBtn, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(28), Right: unit.Dp(8)}.Layout(gtx,
				a.body(txt, colWarn))
		})
	}
}

// resetRow is the FAVORITES section's last row, "Reset favorites…": it only
// opens the confirmation (confirmResetOverlay), so it is kept quiet — muted
// type, the ellipsis promising the dialog.
func (a *App) resetRow(gtx C) D {
	return material.Clickable(gtx, &a.connResetRowBtn, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(8), Left: unit.Dp(28), Right: unit.Dp(8)}.Layout(gtx,
			a.body("Reset favorites…", colMuted))
	})
}

// addForm is the expanded add-favorite form: label + URL editors and
// Add / Cancel, indented under the favorites.
func (a *App) addForm(gtx C) D {
	return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(8), Left: unit.Dp(28), Right: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body("Label:", colMuted)),
					layout.Rigid(hSpacer(6)),
					layout.Flexed(1, func(gtx C) D { return a.editorBox(gtx, &a.connAddLabelEd, "optional") }),
				)
			}),
			layout.Rigid(spacer(6)),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body("URL:", colMuted)),
					layout.Rigid(hSpacer(6)),
					layout.Flexed(1, func(gtx C) D { return a.editorBox(gtx, &a.connAddURLEd, addURLHint) }),
				)
			}),
			layout.Rigid(spacer(8)),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.connAddBtn, "Add to favorites") }),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.connAddCancel, "Cancel") }),
				)
			}),
		)
	})
}

// probeSummary is a row's inline probe readout: "refreshing…" while
// probing, the ping and head counts once it succeeded ("12 ms · 3 players ·
// 1 agent"), OFFLINE if it failed (the detail row under the list carries the
// error).
func probeSummary(p probeResult, probing bool) (string, colorN) {
	switch {
	case probing:
		return "refreshing…", colMuted
	case p.msg == "":
		return "", colMuted
	case !p.ok:
		return "OFFLINE", colErr
	}
	online := "no lobby"
	if p.lobby {
		online = headCount(p.players, p.agents)
	}
	return formatRTT(p.rtt) + " · " + online, colGo
}

// headCount words a lobby's players and agents, apart: "3 players · 1
// agent", "1 player", "0 players · 2 agents", "nobody" when it is empty.
func headCount(players, agents int) string {
	if players+agents == 0 {
		return "nobody"
	}
	plural := func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	if agents == 0 {
		return plural(players, "player")
	}
	return plural(players, "player") + " · " + plural(agents, "agent")
}

// playersText words a probe's lobby head count for the detail line.
func playersText(players, agents int, lobby bool) string {
	if !lobby {
		return "no lobby yet"
	}
	return headCount(players, agents) + " online"
}

// lanTab is LAN mode: what it does, the IP editor and the three port editors,
// the shareable URLs, and the Check embedded server row.
func (a *App) lanTab(gtx C) D {
	portBox := func(ed *widget.Editor, def int) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Max.X = gtx.Dp(64)
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return a.editorBox(gtx, ed, strconv.Itoa(def))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.body("Host a game on your own network: Jetris runs a JetStream-enabled NATS server inside this window, serves the browser version to the phones on it, and plays on it.", colMuted)),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			// What the server is called: the lobby bar's "<you> @ <name>", and
			// the name the join page — and so every guest's lobby bar —
			// gives it.
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body("Name:", colFg)),
				layout.Rigid(hSpacer(6)),
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.connNameEd, config.DefaultEmbeddedName)
				}),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			// Editable: the IP is only auto-DETECTED, and on a multi-homed or
			// VPN'd machine the detected one may not be the address friends
			// can reach.
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body("IP:", colFg)),
				layout.Rigid(hSpacer(6)),
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.connHostEd, a.lanIP)
				}),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			// The three listeners' ports: plain NATS for desktop builds and
			// agents, WebSocket for the browser build, HTTP for the page the
			// browser build is served from. Check tries all three.
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body("Ports — NATS:", colFg)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(portBox(&a.connPortEd, config.DefaultEmbeddedPort)),
				layout.Rigid(hSpacer(10)),
				layout.Rigid(a.body("WebSocket:", colFg)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(portBox(&a.connWSPortEd, config.DefaultEmbeddedWSPort)),
				layout.Rigid(hSpacer(10)),
				layout.Rigid(a.body("HTTP:", colFg)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(portBox(&a.connHTTPPortEd, config.DefaultEmbeddedHTTPPort)),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			// The URLs other players use — built from the fields above — so
			// the host can share them before even hitting Play: the page for
			// the phones (the lobby shows it as a QR code as well), the NATS
			// address for desktop builds and agents; plus where the server
			// keeps its data.
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(a.body("Browsers open ", colMuted)),
						layout.Rigid(a.body("https://"+a.pickerHTTPAddr(), colNATSGreen)),
					)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(a.body("Desktop builds and agents dial ", colMuted)),
						layout.Rigid(a.body("nats://"+a.pickerAddr(), colNATSGreen)),
					)
				}),
				layout.Rigid(a.body("The lobby shows the page as a QR code to scan · data in ./"+config.EmbeddedStoreDir, colMuted)),
			)
		}),
		layout.Flexed(1, func(gtx C) D { return D{Size: gtx.Constraints.Min} }),
		layout.Rigid(func(gtx C) D {
			a.mu.Lock()
			probing := a.connProbing[probeKeyLAN]
			a.mu.Unlock()
			label := "Check embedded server"
			if probing {
				label = "Checking…"
			}
			// Nested row so the button keeps its content width instead of
			// stretching to the panel like Play does.
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.connCheckBtn, label) }),
			)
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D { return a.connStatusLine(gtx, probeKeyLAN) }),
	)
}

// connStatusLine is the probe state for key, on the panel's full width:
// "Connecting…" while that key is being probed, then "✓ <server> · Core NATS
// ping <rtt> · <n> players online" in green or "✗ <error>" in red — or a muted
// hint when key was never probed.
func (a *App) connStatusLine(gtx C, key string) D {
	a.mu.Lock()
	res, has := a.connProbes[key]
	probing := a.connProbing[key]
	a.mu.Unlock()

	msg, col := "", colMuted
	switch {
	case probing && key != "":
		msg = "Connecting and measuring the core NATS ping…"
	case has:
		msg, col = res.msg, colErr
		if res.ok {
			col = colGo
		}
	case key == probeKeyLAN:
		msg = "Starts the servers on the three ports and pings them over the address above."
	default:
		msg = "Click a server (or Refresh all servers) to measure the core NATS ping and count who's in its lobby."
	}
	l := material.Body2(a.th, msg)
	l.Color = col
	return l.Layout(gtx)
}

// editorBox draws a bordered, padded box around a single-line editor.
func (a *App) editorBox(gtx C, ed *widget.Editor, hint string) D {
	return widget.Border{Color: colBorder, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return background(gtx, colPanel, func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				e := material.Editor(a.th, ed, hint)
				e.Color = colFg
				e.HintColor = colMuted
				return e.Layout(gtx)
			})
		})
	})
}

// spacer returns a rigid vertical spacer widget of n dp.
func spacer(n int) layout.Widget {
	return func(gtx C) D {
		return layout.Spacer{Height: unit.Dp(n)}.Layout(gtx)
	}
}

// hSpacer returns a rigid horizontal spacer widget of n dp (spacer is
// height-only and adds NO width inside a horizontal flex).
func hSpacer(n int) layout.Widget {
	return func(gtx C) D {
		return layout.Spacer{Width: unit.Dp(n)}.Layout(gtx)
	}
}
