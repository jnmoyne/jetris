//go:build !js

package nativeui

import "gioui.org/app"

// attachView is the hook for the window's platform handles (app.ViewEvent).
// The desktop backends deliver keys to the window for as long as it is the
// active one, so there is nothing to wire up here; see view_js.go for the
// browser, which needs help.
func (a *App) attachView(app.ViewEvent) {}

// touchDebugFrame is the browser build's touch diagnostic (view_js.go); the
// desktop builds have no page to report to. frameBegin/frameEnd likewise
// bracket the frame for the browser page's input shim: on the desktop the
// window's own event loop delivers input between frames.
func (a *App) touchDebugFrame() {}
func (a *App) frameBegin()      {}
func (a *App) frameEnd()        {}
