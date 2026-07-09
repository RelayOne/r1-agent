package seed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

func writeProfile(t *testing.T, root, profile string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveAssemblyOrder(t *testing.T) {
	root := t.TempDir()
	writeProfile(t, root, "sales", map[string]string{
		"role.md":   "You are a sales agent.",
		"domain.md": "Domain: CRM.",
		"task.md":   "Close the lead.",
	})
	out := NewFileSeedResolver(root).Resolve("sales", nil)
	want := "You are a sales agent.\n\nDomain: CRM.\n\nClose the lead."
	if out.Prefix != want {
		t.Fatalf("assembly order wrong:\n got  %q\n want %q", out.Prefix, want)
	}
	if len(out.Meta.Tiers) != 3 || out.Meta.Tiers[0] != "role" || out.Meta.Tiers[2] != "task" {
		t.Fatalf("unexpected tiers: %v", out.Meta.Tiers)
	}
}

func TestResolveSkipsMissingTiers(t *testing.T) {
	root := t.TempDir()
	// Only role and task present; domain absent must be skipped, not error.
	writeProfile(t, root, "support", map[string]string{
		"role.md": "You are support.",
		"task.md": "Resolve the ticket.",
	})
	out := NewFileSeedResolver(root).Resolve("support", nil)
	want := "You are support.\n\nResolve the ticket."
	if out.Prefix != want {
		t.Fatalf("missing-tier skip wrong:\n got  %q\n want %q", out.Prefix, want)
	}
	if len(out.Meta.Tiers) != 2 || out.Meta.Tiers[0] != "role" || out.Meta.Tiers[1] != "task" {
		t.Fatalf("unexpected tiers: %v", out.Meta.Tiers)
	}
}

func TestResolveUnknownProfileEmpty(t *testing.T) {
	root := t.TempDir()
	out := NewFileSeedResolver(root).Resolve("nope", nil)
	if out.Prefix != "" {
		t.Fatalf("unknown profile must yield empty prefix, got %q", out.Prefix)
	}
}

func TestResolvePrefixPreservesOriginalPrompt(t *testing.T) {
	root := t.TempDir()
	writeProfile(t, root, "p", map[string]string{"role.md": "SEED-ROLE"})
	prefix := ResolvePrefix("p", root)
	original := "ORIGINAL SYSTEM PROMPT"
	// The runtime PREPENDS: prefix + "\n\n" + original. Assert the original is
	// preserved verbatim as a suffix and the seed leads.
	assembled := prefix + "\n\n" + original
	if assembled != "SEED-ROLE\n\nORIGINAL SYSTEM PROMPT" {
		t.Fatalf("prepend must preserve original prompt: %q", assembled)
	}
	// Empty profile/path is a no-op prefix (original unchanged).
	if ResolvePrefix("", root) != "" || ResolvePrefix("p", "") != "" {
		t.Fatal("empty profile or path must resolve to an empty prefix")
	}
}

func TestFileResolverImplementsSeam(t *testing.T) {
	var _ honestcrypto.SeedResolver = NewFileSeedResolver("")
	if NewFileSeedResolver("").Backend() != "file" {
		t.Fatal("backend name must be 'file'")
	}
}
