package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRender_AllSupportedPairs asserts each of the four (provider, stack) pairs
// renders without error when valid params are supplied.
func TestRender_AllSupportedPairs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		provider string
		stack    string
		params   map[string]string
		wantPath string
		mustHave []string // substrings that must appear in content
	}{
		{
			provider: "vercel",
			stack:    "next",
			params:   map[string]string{},
			wantPath: "vercel.json",
			mustHave: []string{`"framework": "nextjs"`, `"version": 2`, `"regions": ["iad1"]`},
		},
		{
			provider: "vercel",
			stack:    "static",
			params:   map[string]string{},
			wantPath: "vercel.json",
			mustHave: []string{`"@vercel/node"`, `"src": "server.js"`, `"/(.*)"`},
		},
		{
			provider: "cloudflare",
			stack:    "workers",
			params:   map[string]string{"NAME": "my-worker", "DATE": "2026-04-21"},
			wantPath: "wrangler.toml",
			mustHave: []string{
				`name = "my-worker"`,
				`compatibility_date = "2026-04-21"`,
				`[assets]`,
				`binding = "ASSETS"`,
			},
		},
		{
			provider: "cloudflare",
			stack:    "pages",
			params:   map[string]string{"NAME": "my-pages", "DATE": "2026-04-21"},
			wantPath: "wrangler.toml",
			mustHave: []string{
				`name = "my-pages"`,
				`compatibility_date = "2026-04-21"`,
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.provider+"_"+tc.stack, func(t *testing.T) {
			t.Parallel()
			path, content, err := Render(tc.provider, tc.stack, tc.params)
			if err != nil {
				t.Fatalf("Render(%s, %s) error: %v", tc.provider, tc.stack, err)
			}
			if path != tc.wantPath {
				t.Errorf("path: got %q, want %q", path, tc.wantPath)
			}
			for _, needle := range tc.mustHave {
				if !strings.Contains(content, needle) {
					t.Errorf("content missing %q:\n%s", needle, content)
				}
			}
			// No unfilled tokens should remain.
			if strings.Contains(content, "{{") {
				t.Errorf("content still contains unfilled tokens:\n%s", content)
			}
		})
	}
}

// TestRender_PagesContentHasNoAssetsBlock confirms the pages template does not
// include the [assets] block (per spec, "content stripped of [assets] block").
func TestRender_PagesContentHasNoAssetsBlock(t *testing.T) {
	t.Parallel()
	_, content, err := Render("cloudflare", "pages", map[string]string{
		"NAME": "p",
		"DATE": "2026-04-21",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(content, "[assets]") {
		t.Errorf("pages template should not contain [assets] block:\n%s", content)
	}
}

// TestRender_MissingParam asserts missing required tokens produce a descriptive
// error naming the missing key.
func TestRender_MissingParam(t *testing.T) {
	t.Parallel()
	_, _, err := Render("cloudflare", "workers", map[string]string{"DATE": "2026-04-21"})
	if err == nil {
		t.Fatal("expected error for missing NAME, got nil")
	}
	if !strings.Contains(err.Error(), "NAME") {
		t.Errorf("error should name missing key NAME: %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should say missing: %v", err)
	}
}

// TestRender_DateAutoDefaults asserts DATE token auto-defaults to today when
// absent from params (per wrangler.toml template comment).
func TestRender_DateAutoDefaults(t *testing.T) {
	t.Parallel()
	_, content, err := Render("cloudflare", "workers", map[string]string{"NAME": "auto"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(content, `compatibility_date = "`) {
		t.Errorf("no compatibility_date line found:\n%s", content)
	}
	if strings.Contains(content, "{{DATE}}") {
		t.Errorf("DATE token was not substituted:\n%s", content)
	}
}

// TestRender_UnknownProvider asserts unknown providers error descriptively.
func TestRender_UnknownProvider(t *testing.T) {
	t.Parallel()
	_, _, err := Render("aws", "lambda", map[string]string{})
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "aws") {
		t.Errorf("error should name unknown provider: %v", err)
	}
	if !strings.Contains(err.Error(), "cloudflare") || !strings.Contains(err.Error(), "vercel") {
		t.Errorf("error should list known providers: %v", err)
	}
}

// TestRender_UnknownStack asserts unknown stacks for a known provider error
// descriptively.
func TestRender_UnknownStack(t *testing.T) {
	t.Parallel()
	_, _, err := Render("vercel", "bogus", map[string]string{})
	if err == nil {
		t.Fatal("expected error for unknown stack, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name unknown stack: %v", err)
	}
	if !strings.Contains(err.Error(), "vercel") {
		t.Errorf("error should name provider: %v", err)
	}
	if !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), "static") {
		t.Errorf("error should list known stacks: %v", err)
	}
}

// TestWriteIfAbsent_WritesNewFile asserts WriteIfAbsent creates the file when
// absent and reports wrote=true with the expected content.
func TestWriteIfAbsent_WritesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, wrote, err := WriteIfAbsent(dir, "vercel", "next", map[string]string{})
	if err != nil {
		t.Fatalf("WriteIfAbsent: %v", err)
	}
	if !wrote {
		t.Errorf("wrote = false, want true for new file")
	}
	if path != filepath.Join(dir, "vercel.json") {
		t.Errorf("path = %q, want %q", path, filepath.Join(dir, "vercel.json"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(data), `"framework": "nextjs"`) {
		t.Errorf("written content missing expected marker:\n%s", data)
	}
}

// TestWriteIfAbsent_DoesNotOverwrite asserts WriteIfAbsent returns wrote=false
// without modifying the file when the target already exists.
func TestWriteIfAbsent_DoesNotOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "vercel.json")
	original := []byte(`{"preserved": true}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	path, wrote, err := WriteIfAbsent(dir, "vercel", "next", map[string]string{})
	if err != nil {
		t.Fatalf("WriteIfAbsent: %v", err)
	}
	if wrote {
		t.Errorf("wrote = true, want false when file exists")
	}
	if path != target {
		t.Errorf("path = %q, want %q", path, target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read after call: %v", err)
	}
	if string(data) != string(original) {
		t.Errorf("file was modified:\ngot:  %s\nwant: %s", data, original)
	}
}

// TestWriteIfAbsent_CloudflareWorkers exercises the TOML path with params.
func TestWriteIfAbsent_CloudflareWorkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, wrote, err := WriteIfAbsent(dir, "cloudflare", "workers", map[string]string{
		"NAME": "stoke-test",
		"DATE": "2026-04-21",
	})
	if err != nil {
		t.Fatalf("WriteIfAbsent: %v", err)
	}
	if !wrote {
		t.Fatal("expected wrote=true for new wrangler.toml")
	}
	if path != filepath.Join(dir, "wrangler.toml") {
		t.Errorf("path = %q, want wrangler.toml", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `name = "stoke-test"`) {
		t.Errorf("missing name line:\n%s", data)
	}
}

// TestWriteIfAbsent_PropagatesRenderError asserts a missing param produces an
// error (not a partial write).
func TestWriteIfAbsent_PropagatesRenderError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, wrote, err := WriteIfAbsent(dir, "cloudflare", "workers", map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing NAME")
	}
	if wrote {
		t.Error("wrote = true, want false on error")
	}
	// Confirm nothing was written.
	if _, statErr := os.Stat(filepath.Join(dir, "wrangler.toml")); !os.IsNotExist(statErr) {
		t.Errorf("expected no wrangler.toml, stat err = %v", statErr)
	}
}
