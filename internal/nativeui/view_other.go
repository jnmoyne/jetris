//go:build !js

package nativeui

import "gioui.org/app"

// attachView is the hook for the window's platform handles (app.ViewEvent).
// The desktop backends deliver keys to the window for as long as it is the
// active one, so there is nothing to wire up here; see view_js.go for the
// browser, which needs help.
func (a *App) attachView(app.ViewEvent) {}
