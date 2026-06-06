package nativeui

import (
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
		name := strings.TrimSpace(a.loginEd.Text())
		if err := config.ValidatePlayerName(name); err != nil {
			a.mu.Lock()
			a.loginErr = err.Error()
			a.mu.Unlock()
		} else {
			a.mu.Lock()
			a.loginErr = ""
			a.loggingIn = true
			a.mu.Unlock()
			go a.doLogin(name, false)
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

func (a *App) loginNormalContent(gtx C, loggingIn bool, loginErr string) D {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.editorBox(gtx, &a.loginEd, "Enter your name")
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
					b := material.Button(a.th, &a.collisionNo, "Cancel")
					b.Background = colPanel
					return b.Layout(gtx)
				}),
			)
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
