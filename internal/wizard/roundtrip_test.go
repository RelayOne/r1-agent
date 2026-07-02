package wizard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/config"
)

// TestGenerateYAMLRoundTripsThroughLoadPolicy reproduces audit A004: the
// wizard's generated policy must (a) protect the file the wizard actually
// writes (r1.policy.yaml — it listed the nonexistent stoke.policy.yaml)
// and (b) survive config.LoadPolicy so the protection list is enforced
// rather than silently parsed to empty.
func TestGenerateYAMLRoundTripsThroughLoadPolicy(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	w.autoDetect()
	w.applyDefaults()

	out := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(out, []byte(w.GenerateYAML()), 0o644); err != nil {
		t.Fatal(err)
	}

	pol, err := config.LoadPolicy(out)
	if err != nil {
		t.Fatalf("wizard-generated policy does not load: %v", err)
	}

	want := map[string]bool{"r1.policy.yaml": false, ".claude/": false, "CLAUDE.md": false}
	for _, p := range pol.Files.Protected {
		if _, ok := want[p]; ok {
			want[p] = true
		}
		if p == "stoke.policy.yaml" {
			t.Error("protected list still names stoke.policy.yaml — the wizard writes r1.policy.yaml")
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("protected list missing %q (got %v)", name, pol.Files.Protected)
		}
	}
}
