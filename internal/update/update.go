// Package update asks GitHub whether a newer Jetris release than the running
// build exists. Jetris checks once at startup, off the UI goroutine, and only
// tells the player where to download a newer version — it never downloads or
// installs anything.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Repo is the GitHub repository releases are published from; ReleasesURL is
// the page a player is sent to for a newer version (GitHub resolves "latest"
// to the newest release).
const (
	Repo        = "jnmoyne/jetris"
	ReleasesURL = "https://github.com/" + Repo + "/releases/latest"
)

// latestURL is the GitHub API endpoint for the newest published release
// (drafts and pre-releases excluded). A variable so tests can point it at a
// local server.
var latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// maxBody caps how much of the API response is read: the fields used are in
// the first kilobyte, and a misbehaving proxy must not make the app slurp a
// page of HTML.
const maxBody = 1 << 20

// Release is one published release: its tag ("v0.5.1") and its page.
type Release struct {
	Tag string
	URL string
}

// Check reports the newest release when it is newer than current, this
// build's version as stamped by the release pipeline ("v0.5.0"). A build whose
// version is not a release tag — the "dev" of a plain go build — has nothing
// to compare against: Check returns newer=false without touching the network.
func Check(ctx context.Context, current string) (rel Release, newer bool, err error) {
	if _, ok := parseVersion(current); !ok {
		return Release{}, false, nil
	}
	rel, err = Latest(ctx)
	if err != nil {
		return Release{}, false, err
	}
	return rel, Newer(current, rel.Tag), nil
}

// Latest fetches the newest published release from GitHub.
func Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "jetris-update-check")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GET %s: %s", latestURL, resp.Status)
	}
	var body struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decoding %s: %w", latestURL, err)
	}
	if body.Tag == "" {
		return Release{}, errors.New("release has no tag")
	}
	if body.URL == "" {
		body.URL = ReleasesURL
	}
	return Release{Tag: body.Tag, URL: body.URL}, nil
}

// Newer reports whether the release tagged latest is a higher version than
// current. Either side that is not a version ("dev", "", garbage) makes the
// answer false: nothing is claimed that cannot be compared.
func Newer(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range c.nums {
		if l.nums[i] != c.nums[i] {
			return l.nums[i] > c.nums[i]
		}
	}
	// Same numbers: a final release is newer than a pre-release of it.
	return c.pre != "" && l.pre == ""
}

// version is a parsed release tag: major.minor.patch plus any pre-release
// suffix ("rc1" of "v1.2.0-rc1").
type version struct {
	nums [3]int
	pre  string
}

// parseVersion parses "v1.2.3", "1.2.3", "v1.2" (missing parts are 0) and
// "v1.2.3-rc1"; build metadata after "+" is ignored. ok is false for anything
// else, notably the "dev" of an unstamped build.
func parseVersion(s string) (v version, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, v.pre = s[:i], s[i+1:]
	}
	if s == "" {
		return version{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return version{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return version{}, false
		}
		v.nums[i] = n
	}
	return v, true
}
