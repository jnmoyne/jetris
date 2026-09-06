//go:build !js

package nativeui

import (
	"io"
	"strings"

	"gioui.org/io/clipboard"
)

// currentPageURL: the desktop is served from no page; a share link built
// here goes to the GitHub Pages copy of the game (webdist.DefaultPage).
func currentPageURL() string { return "" }

// copyText puts text on the system clipboard through Gio's window, which
// always has one on the desktop.
func copyText(gtx C, text string) bool {
	gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(text))})
	return true
}
