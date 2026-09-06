package nativeui

import (
	"regexp"
	"strings"
	"testing"

	"jetris/internal/config"
	"jetris/internal/testutil"
)

// Every handle, dressed as a dealt name, is one the server accepts.
func TestPlayerNamesValid(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range playerNames {
		if seen[n] {
			t.Errorf("%q listed twice", n)
		}
		seen[n] = true
		for _, prefix := range []string{anonymousPrefix, watcherPrefix} {
			if err := config.ValidatePlayerName(prefix + n + "99"); err != nil {
				t.Errorf("%q: %v", prefix+n, err)
			}
		}
	}
}

// A dealt name is the prefix, a listed handle and two digits.
func TestDealName(t *testing.T) {
	re := regexp.MustCompile(`^Watcher_([A-Za-z0-9_]+?)([0-9][0-9])$`)
	for i := 0; i < 200; i++ {
		got := dealName(watcherPrefix)
		m := re.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("dealName(Watcher_) = %q, want Watcher_<handle><two digits>", got)
		}
		found := false
		for _, n := range playerNames {
			if n == m[1] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dealName(Watcher_) = %q, handle %q is not in the list", got, m[1])
		}
	}
}

// The field starts blank; Play with it blank deals a name, shows it in the
// field, and joins the lobby under it. A name given up front is kept as is.
func TestBlankNameDealsRandomOne(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url})
	if got := a.loginEd.Text(); got != "" {
		t.Fatalf("name field starts as %q, want blank", got)
	}
	a.submitLogin()
	dealt := a.loginEd.Text()
	if err := config.ValidatePlayerName(dealt); err != nil {
		t.Fatalf("dealt name %q: %v (loginErr %q)", dealt, err, a.loginErr)
	}
	waitFor(t, "the lobby", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenLobby || a.loginErr != ""
	})
	if a.loginErr != "" || a.screen != screenLobby {
		t.Fatalf("login with a dealt name: err %q, screen %v, want the lobby", a.loginErr, a.screen)
	}
	lb := a.getLobby()
	if lb == nil {
		t.Fatal("no lobby joined")
	}
	if got := lb.PlayerName(); got != dealt {
		t.Fatalf("lobby name = %q, want the dealt %q", got, dealt)
	}

	a = NewWithPicker(config.Config{PlayerName: "alice"}, nil, "", nil)
	if got := a.loginEd.Text(); got != "alice" {
		t.Fatalf("login field = %q, want the given name", got)
	}
}

// A replay link with no name asks for none: the screen deals a Watcher_
// name, arms the auto-login, and the landing goes on to the replay. A name
// on the link is used as given.
func TestReplayLinkDealsWatcherName(t *testing.T) {
	a := NewWithPicker(config.Config{ReplayGameID: "game-1"}, nil, "", nil)
	got := a.loginEd.Text()
	if !strings.HasPrefix(got, watcherPrefix) {
		t.Fatalf("name field = %q, want a dealt Watcher_ name", got)
	}
	if err := config.ValidatePlayerName(got); err != nil {
		t.Fatalf("dealt name %q: %v", got, err)
	}
	if !a.autoLogin {
		t.Fatal("auto-login not armed by a replay link")
	}
	if a.linkedReplay != "game-1" {
		t.Fatalf("linked replay = %q, want the link's game", a.linkedReplay)
	}
	a = NewWithPicker(config.Config{ReplayGameID: "game-1", PlayerName: "alice"}, nil, "", nil)
	if got := a.loginEd.Text(); got != "alice" || !a.autoLogin {
		t.Fatalf("with a name on the link: field %q, auto-login %v; want alice, armed", got, a.autoLogin)
	}
}
