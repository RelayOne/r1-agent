package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a tiny helper that makes a file (and its parent dir)
// inside root. Test fixtures are content-free so we keep the body
// empty; Detect's signal logic keys off presence, not content.
func writeFile(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// mkDir creates a directory (and parents) inside root. Used for
// signals like ".wrangler/" whose mere existence as a directory is
// the signal.
func mkDir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Join(root, rel), err)
	}
}

// TestDetect_Empty verifies an empty directory yields Provider=""
// and no Ambiguous flag, so callers know to fall back to
// Operator.Ask instead of picking a default.
func TestDetect_Empty(t *testing.T) {
	dir := t.TempDir()
	got := Detect(dir)
	if got.Provider != "" {
		t.Errorf("Provider = %q, want %q", got.Provider, "")
	}
	if got.Ambiguous {
		t.Errorf("Ambiguous = true, want false on empty dir")
	}
	if len(got.Signals) != 0 {
		t.Errorf("Signals = %v, want []", got.Signals)
	}
	if got.Note != "" {
		t.Errorf("Note = %q, want empty on empty dir", got.Note)
	}
}

// TestDetect_FlyOnly covers the fallback case: fly.toml alone →
// Provider="fly", no ambiguity, Signals lists the matched marker.
func TestDetect_FlyOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "fly.toml")

	got := Detect(dir)
	if got.Provider != "fly" {
		t.Errorf("Provider = %q, want %q", got.Provider, "fly")
	}
	if got.Ambiguous {
		t.Errorf("Ambiguous = true, want false with fly.toml only")
	}
	if len(got.Signals) != 1 || got.Signals[0] != "fly.toml" {
		t.Errorf("Signals = %v, want [fly.toml]", got.Signals)
	}
}

// TestDetect_VercelOnly covers the high-specificity case:
// vercel.json alone → Provider="vercel" unambiguously.
func TestDetect_VercelOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vercel.json")

	got := Detect(dir)
	if got.Provider != "vercel" {
		t.Errorf("Provider = %q, want %q", got.Provider, "vercel")
	}
	if got.Ambiguous {
		t.Errorf("Ambiguous = true, want false with vercel.json only")
	}
	if len(got.Signals) != 1 || got.Signals[0] != "vercel.json" {
		t.Errorf("Signals = %v, want [vercel.json]", got.Signals)
	}
}

// TestDetect_VercelProjectDotfile covers the second Vercel marker
// (.vercel/project.json). Treated with equal weight to vercel.json.
func TestDetect_VercelProjectDotfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, filepath.Join(".vercel", "project.json"))

	got := Detect(dir)
	if got.Provider != "vercel" {
		t.Errorf("Provider = %q, want %q", got.Provider, "vercel")
	}
	if got.Ambiguous {
		t.Errorf("Ambiguous = true, want false with .vercel/project.json only")
	}
}

// TestDetect_CloudflareOnly covers wrangler.toml → Provider="cloudflare".
// The other wrangler extensions are exercised by TestDetect_CloudflareExts.
func TestDetect_CloudflareOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wrangler.toml")

	got := Detect(dir)
	if got.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want %q", got.Provider, "cloudflare")
	}
	if got.Ambiguous {
		t.Errorf("Ambiguous = true, want false with wrangler.toml only")
	}
	if len(got.Signals) != 1 || got.Signals[0] != "wrangler.toml" {
		t.Errorf("Signals = %v, want [wrangler.toml]", got.Signals)
	}
}

// TestDetect_CloudflareExts confirms each wrangler config extension
// and the .wrangler/ state directory are each sufficient on their
// own to nominate Cloudflare.
func TestDetect_CloudflareExts(t *testing.T) {
	cases := []struct {
		name    string
		marker  string
		isDir   bool
		wantSig string
	}{
		{"json", "wrangler.json", false, "wrangler.json"},
		{"jsonc", "wrangler.jsonc", false, "wrangler.jsonc"},
		{"state-dir", ".wrangler", true, ".wrangler"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.isDir {
				mkDir(t, dir, tc.marker)
			} else {
				writeFile(t, dir, tc.marker)
			}
			got := Detect(dir)
			if got.Provider != "cloudflare" {
				t.Errorf("Provider = %q, want %q", got.Provider, "cloudflare")
			}
			if len(got.Signals) != 1 || got.Signals[0] != tc.wantSig {
				t.Errorf("Signals = %v, want [%s]", got.Signals, tc.wantSig)
			}
		})
	}
}

// TestDetect_VercelPlusFly exercises the primary ambiguity rule from
// §Provider Selection Matrix: vercel.json beats fly.toml, but we
// still flag Ambiguous=true and populate Note so the CLI can warn
// the operator that their fly.toml is being ignored.
func TestDetect_VercelPlusFly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vercel.json")
	writeFile(t, dir, "fly.toml")

	got := Detect(dir)
	if got.Provider != "vercel" {
		t.Errorf("Provider = %q, want %q", got.Provider, "vercel")
	}
	if !got.Ambiguous {
		t.Error("Ambiguous = false, want true when vercel.json + fly.toml both present")
	}
	if got.Note == "" {
		t.Error("Note empty, want tie-breaker explanation")
	}
	// Signal list should carry both markers so operators can see
	// exactly what caused the ambiguity flag.
	if len(got.Signals) != 2 {
		t.Errorf("Signals = %v, want both fly.toml and vercel.json", got.Signals)
	}
}

// TestDetect_VercelPlusCloudflare exercises the hard-ambiguity rule:
// two high-specificity providers → we refuse to guess. Provider must
// be "", Ambiguous=true, Note populated telling the caller to Ask.
func TestDetect_VercelPlusCloudflare(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vercel.json")
	writeFile(t, dir, "wrangler.toml")

	got := Detect(dir)
	if got.Provider != "" {
		t.Errorf("Provider = %q, want \"\" when vercel + cloudflare both present", got.Provider)
	}
	if !got.Ambiguous {
		t.Error("Ambiguous = false, want true on vercel + cloudflare collision")
	}
	if got.Note == "" {
		t.Error("Note empty, want tie-breaker explanation")
	}
}

// TestDetect_CloudflarePlusFly: wrangler beats fly cleanly (spec
// marks this as deterministic rather than ambiguous), so we expect
// Provider="cloudflare" + Ambiguous=false + a Note that explains the
// precedence for operator log clarity.
func TestDetect_CloudflarePlusFly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wrangler.toml")
	writeFile(t, dir, "fly.toml")

	got := Detect(dir)
	if got.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want %q", got.Provider, "cloudflare")
	}
	if got.Ambiguous {
		t.Error("Ambiguous = true, want false when wrangler takes precedence")
	}
	if got.Note == "" {
		t.Error("Note empty, want precedence explanation")
	}
}

// TestDetect_EmptyDirString checks the "" shorthand is accepted and
// resolved against the current process directory without panicking.
// We do not assert Provider here because the repo root may have
// signals of its own in the future; the contract is simply that
// Detect("") does not crash.
func TestDetect_EmptyDirString(t *testing.T) {
	_ = Detect("")
}

// TestDetect_NonExistentDir verifies Detect returns a zero-value
// result (no panic, no error field) when dir does not exist. The
// package contract prefers "nothing detected" over io/fs errors for
// this cheap read-only walk.
func TestDetect_NonExistentDir(t *testing.T) {
	got := Detect(filepath.Join(t.TempDir(), "does-not-exist"))
	if got.Provider != "" {
		t.Errorf("Provider = %q, want empty for non-existent dir", got.Provider)
	}
	if got.Ambiguous {
		t.Error("Ambiguous = true, want false on non-existent dir")
	}
	if len(got.Signals) != 0 {
		t.Errorf("Signals = %v, want empty", got.Signals)
	}
}
