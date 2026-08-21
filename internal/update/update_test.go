package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.5.0", "v0.5.1", true},
		{"v0.5.1", "v0.5.1", false},
		{"v0.5.2", "v0.5.1", false},
		{"v0.9.9", "v1.0.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"0.5.0", "v0.5.1", true},      // the stamped tag may lack its v
		{"v0.5", "v0.5.1", true},       // missing parts read as 0
		{"v0.5.1-rc1", "v0.5.1", true}, // a final release follows its pre-release
		{"v0.5.1", "v0.5.1-rc2", false},
		{"v0.5.1", "v0.5.1+build7", false}, // build metadata is not a version
		{"dev", "v0.5.1", false},           // an unstamped build has no version to beat
		{"", "v0.5.1", false},
		{"v0.5.0", "", false},
		{"v0.5.0", "latest", false},
		{"v0.5.0", "v1.2.3.4", false},
	} {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v0.5.1","html_url":"https://github.com/jnmoyne/jetris/releases/tag/v0.5.1","name":"Upgrade connection UI"}`))
	}))
	defer srv.Close()
	latestURL = srv.URL

	rel, newer, err := Check(context.Background(), "v0.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if !newer || rel.Tag != "v0.5.1" || rel.URL != "https://github.com/jnmoyne/jetris/releases/tag/v0.5.1" {
		t.Fatalf("Check(v0.5.0) = %+v newer=%v, want v0.5.1 newer", rel, newer)
	}
	if _, newer, err := Check(context.Background(), "v0.5.1"); err != nil || newer {
		t.Fatalf("Check(v0.5.1) newer=%v err=%v, want up to date", newer, err)
	}
	if hits != 2 {
		t.Fatalf("GitHub asked %d times, want 2", hits)
	}

	// A dev build never goes out to the network.
	if rel, newer, err := Check(context.Background(), "dev"); err != nil || newer || rel != (Release{}) {
		t.Fatalf("Check(dev) = %+v newer=%v err=%v, want nothing", rel, newer, err)
	}
	if hits != 2 {
		t.Fatalf("a dev build asked GitHub (%d hits)", hits)
	}
}

func TestLatestErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"rate-limited": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"not-json":     func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>nope</html>")) },
		"no-tag":       func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"html_url":"x"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			latestURL = srv.URL
			if _, _, err := Check(context.Background(), "v0.5.0"); err == nil {
				t.Fatal("want an error")
			}
		})
	}

	// The page URL falls back to the releases page when the API omits it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v9.0.0"}`))
	}))
	defer srv.Close()
	latestURL = srv.URL
	rel, err := Latest(context.Background())
	if err != nil || rel.URL != ReleasesURL {
		t.Fatalf("Latest without html_url = %+v err=%v, want the releases page", rel, err)
	}

	// A cancelled context aborts the request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Latest(ctx); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
}
