package nats

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// ListContexts returns the sorted names of the NATS CLI contexts defined under
// <XDG_CONFIG_HOME|~/.config>/nats/context/*.json plus the currently selected
// context name from <parent>/nats/context.txt — the same path resolution the
// orbit natscontext package uses to connect (it exposes no lister, hence this
// one). A missing context directory is not an error: (nil, "", nil). A selected
// name that no longer has a matching context file is reported as "".
func ListContexts() (names []string, selected string, err error) {
	parent, err := contextsParent()
	if err != nil {
		return nil, "", err
	}

	entries, err := os.ReadDir(filepath.Join(parent, "nats", "context"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)

	if b, err := os.ReadFile(filepath.Join(parent, "nats", "context.txt")); err == nil {
		if sel := strings.TrimSpace(string(b)); slices.Contains(names, sel) {
			selected = sel
		}
	}
	return names, selected, nil
}

// contextsParent resolves the NATS CLI's config parent: $XDG_CONFIG_HOME, else
// ~/.config (contexts live in <parent>/nats/context/).
func contextsParent() (string, error) {
	if parent := os.Getenv("XDG_CONFIG_HOME"); parent != "" {
		return parent, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// ContextURL returns the server URL a NATS CLI context points at — the "url"
// field of its <parent>/nats/context/<name>.json — for display next to the
// context's name. Best effort: "" when the file is missing or unreadable (the
// orbit natscontext package resolves the full settings only on connect).
func ContextURL(name string) string {
	parent, err := contextsParent()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(parent, "nats", "context", name+".json"))
	if err != nil {
		return ""
	}
	var ctx struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return ""
	}
	return strings.TrimSpace(ctx.URL)
}
