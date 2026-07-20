package agent

import (
	"regexp"
	"strings"
	"testing"
)

func TestComposeName(t *testing.T) {
	// Default stem: the stock codename, a 4-hex instance id, the difficulty.
	got, err := composeName("", DifficultyHard)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^` + Codename + `-[0-9a-f]{4}-hard$`).MatchString(got) {
		t.Fatalf("composeName(\"\", hard) = %q, want %s-<4 hex>-hard", got, Codename)
	}

	// Custom version stem for third-party agents.
	got, err = composeName("brainiac", DifficultyMedium)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^brainiac-[0-9a-f]{4}-medium$`).MatchString(got) {
		t.Fatalf("composeName(brainiac, medium) = %q", got)
	}

	// The instance id makes every connection's name unique.
	a, _ := composeName("", DifficultyEasy)
	b, _ := composeName("", DifficultyEasy)
	if a == b {
		t.Fatalf("two connections composed the same name %q", a)
	}

	// Every component must be usable in subjects AND the presence KV key
	// ("players.<name>"), whose charset is [-/_=.a-zA-Z0-9] only.
	for _, r := range got {
		if !(r == '-' || r == '_' || r == '=' || r == '.' || r == '/' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			t.Fatalf("composed name %q contains %q, invalid in a KV key", got, r)
		}
	}

	// Invalid version stem (space is not a valid subject token character).
	if _, err := composeName("HAL 9000", DifficultyEasy); err == nil {
		t.Fatal("stem with a space must be rejected")
	}

	// A stem that fits alone but overflows the 32-char cap once the instance
	// and difficulty are appended must be rejected.
	long := strings.Repeat("x", 25)
	if _, err := composeName(long, DifficultyMedium); err == nil {
		t.Fatal("stem overflowing the cap with instance+difficulty must be rejected")
	}
}
