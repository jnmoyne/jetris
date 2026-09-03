package nativeui

import (
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
)

// The on-screen keyboard a text field asks for.
//
// Gio's editor carries a key.InputHint and the browser backend turns it into
// the hidden text element's attributes (app/os_js.go keyboard): HintAny —
// every editor's default — and HintText both ask for autocorrection,
// sentence capitalisation, spellcheck and autocomplete, which on a phone puts
// a row of word predictions over the keyboard and capitalises the first
// letter of a name that has none. HintURL and HintEmail turn every one of
// those off, but they are URL and address keyboards: no space bar, a ".com"
// key. There is no hint for a field that takes a single word.
//
// hintPlain is that hint. It is no value Gio names, and every backend answers
// one it does not know with its plainest keyboard: the browser's default
// branch is a text keyboard with autocorrect, autocapitalize, spellcheck and
// autocomplete all off; Android's is TYPE_CLASS_TEXT without the correction
// flags; the desktop backends take no notice of hints at all. Gio applies a
// hint only when it differs from the focused field's previous one, and a
// fresh window starts at HintAny — so a hint of our own is also what makes
// the browser set the attributes at all on the first focus.
const hintPlain key.InputHint = 0x80

// enterKeyHint is the label a phone's on-screen keyboard puts on its return
// key while an editor has the focus, in the words the enterkeyhint HTML
// attribute takes: "go" on the login screen's fields, which submit the login,
// "send" on a chat line, "done" everywhere else. gtx.Focused reads the input
// router's CURRENT state, so after the frame has been handed to the window
// this is the editor the keyboard is up for.
func (a *App) enterKeyHint(gtx layout.Context) string {
	for _, ed := range []*widget.Editor{&a.loginEd, &a.connAddLabelEd, &a.connAddURLEd,
		&a.connNameEd, &a.connHostEd, &a.connPortEd, &a.connWSPortEd, &a.connHTTPPortEd} {
		if gtx.Focused(ed) {
			return "go"
		}
	}
	if gtx.Focused(&a.chatEd) || gtx.Focused(&a.gameChatEd) {
		return "send"
	}
	return "done"
}
