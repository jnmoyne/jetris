package update

// Opt-in check against the real GitHub API (needs network):
//
//	JETRIS_LIVE_UPDATE_CHECK=1 go test ./internal/update/ -run TestLiveLatest -v

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveLatest(t *testing.T) {
	if os.Getenv("JETRIS_LIVE_UPDATE_CHECK") == "" {
		t.Skip("set JETRIS_LIVE_UPDATE_CHECK=1 to ask GitHub for the latest release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseVersion(rel.Tag); !ok || rel.URL == "" {
		t.Fatalf("latest = %+v, want a version tag and a page", rel)
	}
	t.Logf("latest release: %s at %s; newer than v0.0.1: %v", rel.Tag, rel.URL, Newer("v0.0.1", rel.Tag))
	if !Newer("v0.0.1", rel.Tag) {
		t.Fatalf("%s should beat v0.0.1", rel.Tag)
	}
}
