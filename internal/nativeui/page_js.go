//go:build js

package nativeui

import "syscall/js"

// currentPageURL is this page's address without its query or fragment —
// where the browser build is served from, and so where a share link built
// here sends whoever opens it (webdist.ReplayLink resolves join.html
// against it).
func currentPageURL() string {
	loc := js.Global().Get("location")
	if !loc.Truthy() {
		return ""
	}
	return loc.Get("origin").String() + loc.Get("pathname").String()
}

// copyText puts text on the clipboard: the browser's Clipboard API where the
// page has it — a secure context: https, or localhost — and the old
// execCommand("copy") over a throwaway textarea where it does not (a LAN
// party's plain-http page), which Gio's own clipboard write would silently
// skip. Reports whether either took the text.
func copyText(gtx C, text string) bool {
	nav := js.Global().Get("navigator")
	if clip := nav.Get("clipboard"); clip.Truthy() && clip.Get("writeText").Truthy() {
		clip.Call("writeText", text)
		return true
	}
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return false
	}
	ta := doc.Call("createElement", "textarea")
	ta.Set("value", text)
	ta.Get("style").Set("position", "fixed")
	ta.Get("style").Set("opacity", "0")
	doc.Get("body").Call("appendChild", ta)
	ta.Call("select")
	ok := false
	if doc.Get("execCommand").Truthy() {
		ok = doc.Call("execCommand", "copy").Truthy()
	}
	doc.Get("body").Call("removeChild", ta)
	return ok
}
