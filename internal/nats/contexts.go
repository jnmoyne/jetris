package nats

import (
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
	parent := os.Getenv("XDG_CONFIG_HOME")
	if parent == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", err
		}
		parent = filepath.Join(home, ".config")
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
