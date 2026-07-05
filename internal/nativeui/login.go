package nativeui

import (
	"errors"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetricks/internal/config"
)

func (a *App) layoutLogin(gtx C) D {
	// --- event handling ---
	submitted := a.loginBtn.Clicked(gtx)
	for {
		ev, ok := a.loginEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			submitted = true
		}
	}
	for {
		ev, ok := a.connURLEd.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			submitted = true
		case widget.ChangeEvent:
			// Typing a URL implies choosing the URL option — but the
			// constructor's programmatic SetText also queues one ChangeEvent
			// on the first frame, and that must not override the context
			// default.
			if a.connURLSeeded {
				a.connURLSeeded = false
			} else {
				a.connEnum.Value = "url"
			}
		}
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
	} else if submitted && !loggingIn {
		a.submitLogin()
	}

	if a.connCheckBtn.Clicked(gtx) && a.pickerActive() {
		a.mu.Lock()
		checking := a.connChecking
		a.mu.Unlock()
		if !checking {
			if cfg, err := a.pickerConfig(); err != nil {
				a.setLoginErr(err.Error())
			} else {
				a.setLoginErr("")
				a.mu.Lock()
				a.connChecking = true
				a.connCheckMsg = ""
				a.mu.Unlock()
				go a.doCheckConn(cfg)
			}
		}
	}

	// --- render ---
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(440)
		gtx.Constraints.Min.X = gtx.Dp(440)
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				t := material.H3(a.th, "JETRICKS")
				t.Color = colAccent
				return t.Layout(gtx)
			}),
			layout.Rigid(spacer(16)),
			layout.Rigid(func(gtx C) D {
				if collision {
					return a.loginCollisionContent(gtx)
				}
				return a.loginNormalContent(gtx, loggingIn, loginErr)
			}),
		)
	})
}

// submitLogin validates the entered name and kicks off the async login: in
// picker mode it first resolves the connection choice (context or URL) and
// dispatches doConnectAndLogin; otherwise (already connected) plain doLogin.
// Runs on the UI goroutine.
func (a *App) submitLogin() {
	name := strings.TrimSpace(a.loginEd.Text())
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
	a.setLoginErr("")
	a.mu.Lock()
	a.loggingIn = true
	a.mu.Unlock()
	go a.doConnectAndLogin(name, cfg)
}

// pickerConfig resolves the current CONNECT TO choice into a config: the URL
// field when the URL radio is active (errors when empty), otherwise the
// context chosen in the pull-down. The base is connCfg, so --user/--password
// flags carry through to URL connects. Runs on the UI goroutine (reads
// widgets).
func (a *App) pickerConfig() (config.Config, error) {
	cfg := a.connCfg
	cfg.NATSURL, cfg.NATSContext = "", ""
	if a.connEnum.Value == "url" {
		cfg.NATSURL = strings.TrimSpace(a.connURLEd.Text())
		if cfg.NATSURL == "" {
			return cfg, errors.New("enter a NATS URL")
		}
	} else {
		if a.connCtx == "" {
			return cfg, errors.New("no NATS context selected")
		}
		cfg.NATSContext = a.connCtx
	}
	return cfg, nil
}

// setLoginErr sets (or clears) the login screen's error line.
func (a *App) setLoginErr(msg string) {
	a.mu.Lock()
	a.loginErr = msg
	a.mu.Unlock()
}

func (a *App) loginNormalContent(gtx C, loggingIn bool, loginErr string) D {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.editorBox(gtx, &a.loginEd, "Enter your name")
		}),
		layout.Rigid(spacer(14)),
		layout.Rigid(func(gtx C) D {
			if a.pickerActive() {
				return a.connSection(gtx)
			}
			return D{}
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			label := "Play"
			if loggingIn {
				label = "Connecting…"
			}
			btn := material.Button(a.th, &a.loginBtn, label)
			return btn.Layout(gtx)
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			l := material.Body2(a.th, "No spaces, dots, or wildcards; max 32 characters.")
			l.Color = colMuted
			return l.Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D {
			if loginErr == "" {
				return D{}
			}
			l := material.Body2(a.th, loginErr)
			l.Color = colErr
			return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, l.Layout)
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
				layout.Rigid(material.Button(a.th, &a.collisionYes, "Yes, join").Layout),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D {
					return a.secondaryButton(gtx, &a.collisionNo, "Cancel")
				}),
			)
		}),
	)
}

// connSection renders the CONNECT TO chooser shown while the app has not yet
// connected (launched without --server/--context): a "Context:" radio with a
// pull-down button over the known NATS CLI contexts — preset to --context or
// the CLI's currently selected context — plus an always-present "NATS URL"
// radio with an editable URL pre-set to the demo server. Opening the
// pull-down expands a scroll-capped option list under the button; picking a
// row (or just touching the pull-down) also selects the context radio, the
// same way typing a URL selects the URL radio.
func (a *App) connSection(gtx C) D {
	if a.connDropBtn.Clicked(gtx) {
		a.connDropOpen = !a.connDropOpen
		a.connEnum.Value = "context" // touching the pull-down implies choosing the context option
	}
	for i := range a.connOptBtns {
		if a.connOptBtns[i].Clicked(gtx) {
			a.connCtx = a.connContexts[i]
			a.connEnum.Value = "context"
			a.connDropOpen = false
		}
	}

	children := []layout.FlexChild{
		layout.Rigid(a.header("CONNECT TO")),
		layout.Rigid(spacer(4)),
	}
	if len(a.connContexts) > 0 {
		children = append(children, layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					rb := material.RadioButton(a.th, &a.connEnum, "context", "Context:")
					rb.Color = colFg
					return rb.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx)
				}),
				layout.Flexed(1, a.connDropButton),
			)
		}))
		if a.connDropOpen {
			children = append(children,
				layout.Rigid(spacer(4)),
				layout.Rigid(a.connDropList),
			)
		}
		children = append(children, layout.Rigid(spacer(6)))
	}
	children = append(children,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					rb := material.RadioButton(a.th, &a.connEnum, "url", "NATS URL:")
					rb.Color = colFg
					return rb.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Spacer{Width: unit.Dp(6)}.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.connURLEd, "nats://host:4222")
				}),
			)
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.connCheckRow),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// connDropButton is the collapsed pull-down: an editor-style bordered box
// holding the chosen context name and a drop arrow (flipped while open).
func (a *App) connDropButton(gtx C) D {
	return material.Clickable(gtx, &a.connDropBtn, func(gtx C) D {
		return widget.Border{Color: colMuted, Width: unit.Dp(1), CornerRadius: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				arrow := "▼"
				if a.connDropOpen {
					arrow = "▲"
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, a.body(a.connCtx, colFg)),
					layout.Rigid(a.body(arrow, colAccent)),
				)
			})
		})
	})
}

// connDropList is the expanded pull-down: one clickable row per known
// context, scrolling inside a capped box. The CLI's currently selected
// context is marked "(selected)" and the pull-down's current choice is
// highlighted in the accent color.
func (a *App) connDropList(gtx C) D {
	return bordered(gtx, func(gtx C) D {
		if max := gtx.Dp(180); gtx.Constraints.Max.Y > max {
			gtx.Constraints.Max.Y = max
		}
		gtx.Constraints.Min.Y = 0
		return material.List(a.th, &a.connList).Layout(gtx, len(a.connContexts), func(gtx C, i int) D {
			name := a.connContexts[i]
			label := name
			if name == a.connSelected {
				label += " (selected)"
			}
			col := colFg
			if name == a.connCtx {
				col = colAccent
			}
			return material.Clickable(gtx, &a.connOptBtns[i], func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, a.body(label, col))
			})
		})
	})
}

// connCheckRow renders the "Check connection" button and, next to it, the last
// check's outcome: "✓ <server> · ping <rtt>" in green or "✗ <error>" in red.
func (a *App) connCheckRow(gtx C) D {
	a.mu.Lock()
	checking := a.connChecking
	ok := a.connCheckOK
	msg := a.connCheckMsg
	a.mu.Unlock()

	label := "Check connection"
	if checking {
		label = "Checking…"
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.secondaryButton(gtx, &a.connCheckBtn, label)
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Spacer{Width: unit.Dp(10)}.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx C) D {
			if msg == "" {
				return D{}
			}
			col := colErr
			if ok {
				col = colGo
			}
			l := material.Body2(a.th, msg)
			l.Color = col
			return l.Layout(gtx)
		}),
	)
}

// editorBox draws a bordered, padded box around a single-line editor.
func (a *App) editorBox(gtx C, ed *widget.Editor, hint string) D {
	return widget.Border{Color: colMuted, Width: unit.Dp(1), CornerRadius: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx C) D {
			e := material.Editor(a.th, ed, hint)
			e.Color = colFg
			e.HintColor = colMuted
			return e.Layout(gtx)
		})
	})
}

// spacer returns a rigid vertical spacer widget of n dp.
func spacer(n int) layout.Widget {
	return func(gtx C) D {
		return layout.Spacer{Height: unit.Dp(n)}.Layout(gtx)
	}
}
