package nats

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeContextDir builds <tmp>/nats/context with the given .json context files
// and returns the parent dir (to be used as XDG_CONFIG_HOME).
func writeContextDir(t *testing.T, names ...string) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "nats", "context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

func TestListContextsMissingDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	names, selected, err := ListContexts()
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(names) != 0 || selected != "" {
		t.Fatalf("missing dir: got names=%v selected=%q, want empty", names, selected)
	}
}

func TestListContextsFiltersAndSorts(t *testing.T) {
	parent := writeContextDir(t, "zeta", "alpha", "mid")
	dir := filepath.Join(parent, "nats", "context")
	// Noise that must be excluded: a backup file, a non-json file, a subdir.
	if err := os.WriteFile(filepath.Join(dir, "old.json.bak"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", parent)

	names, selected, err := ListContexts()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if selected != "" {
		t.Fatalf("no context.txt: selected = %q, want empty", selected)
	}
}

func TestListContextsSelected(t *testing.T) {
	parent := writeContextDir(t, "alpha", "beta")
	// Trailing newline must be trimmed.
	if err := os.WriteFile(filepath.Join(parent, "nats", "context.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", parent)

	_, selected, err := ListContexts()
	if err != nil {
		t.Fatal(err)
	}
	if selected != "beta" {
		t.Fatalf("selected = %q, want beta", selected)
	}
}

func TestListContextsStaleSelection(t *testing.T) {
	parent := writeContextDir(t, "alpha")
	// context.txt names a context whose file no longer exists.
	if err := os.WriteFile(filepath.Join(parent, "nats", "context.txt"), []byte("gone"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", parent)

	names, selected, err := ListContexts()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"alpha"}) || selected != "" {
		t.Fatalf("stale selection: got names=%v selected=%q, want [alpha] and empty", names, selected)
	}
}
