package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// StartServer starts an embedded NATS server with JetStream enabled.
// Returns the server URL and a context file path.
func StartServer(t *testing.T) (serverURL string, contextFile string) {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("server not ready")
	}
	t.Cleanup(s.Shutdown)

	url := s.ClientURL()

	// Write a minimal NATS context file
	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "test.json")
	data, _ := json.Marshal(map[string]string{"url": url})
	if err := os.WriteFile(ctxFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	return url, ctxFile
}
