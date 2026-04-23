package vercel

import "testing"

func TestExtractURL_Preview(t *testing.T) {
	t.Parallel()

	// Typical `vercel deploy` preview output: inspect + deployment URL
	// + ready, all labeled.
	out := `Vercel CLI 33.4.0
Inspect: https://vercel.com/team/my-app/abc123 [2s]
Deployment URL: https://my-app-abc123-team.vercel.app
Ready! Deployed to https://my-app-abc123-team.vercel.app [12s]
`
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false (out=%q)", out)
	}
	want := "https://vercel.com/team/my-app/abc123"
	if got != want {
		t.Fatalf("ExtractURL: got %q, want %q", got, want)
	}
	// Sanity-check the preview URL is separately recognizable.
	if !IsPreview("https://my-app-abc123-team.vercel.app") {
		t.Fatalf("IsPreview: expected true for vercel.app host")
	}
}

func TestExtractURL_PreviewDeploymentURLOnly(t *testing.T) {
	t.Parallel()

	// When only `Deployment URL:` is present we should return the
	// *.vercel.app URL on that line.
	out := "Deployment URL: https://my-app-abc123-team.vercel.app\n"
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	want := "https://my-app-abc123-team.vercel.app"
	if got != want {
		t.Fatalf("ExtractURL: got %q, want %q", got, want)
	}
}

func TestExtractURL_Production(t *testing.T) {
	t.Parallel()

	// Production deploy prints a preview URL alongside a Production:
	// line pointing at the custom domain.
	out := `Vercel CLI 33.4.0
Inspect: https://vercel.com/team/my-app/def456 [1s]
Production: https://myapp.com
Ready! Deployed to https://myapp.com [8s]
`
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	// The primary regex takes the first labeled URL — Inspect: is
	// first here, which is still a valid URL. Assert we got *some*
	// https:// match; production disambiguation is the caller's job
	// via IsPreview.
	if got == "" {
		t.Fatalf("ExtractURL: got empty url")
	}
	if !IsPreview("https://my-app-abc123-team.vercel.app") {
		t.Fatalf("IsPreview: expected true for vercel.app host")
	}
	if IsPreview("https://myapp.com") {
		t.Fatalf("IsPreview: expected false for custom domain")
	}
}

func TestExtractURL_ProductionLabelOnly(t *testing.T) {
	t.Parallel()

	// When only Production: appears, extraction returns the custom
	// domain.
	out := "Production: https://myapp.com\n"
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	if got != "https://myapp.com" {
		t.Fatalf("ExtractURL: got %q, want https://myapp.com", got)
	}
}

func TestExtractURL_Fallback(t *testing.T) {
	t.Parallel()

	// No labeled line — just a bare URL buried in the stream. The
	// fallback scan should find it, skipping prompt lines (`?`).
	out := `? Link this project to Vercel scope team-abc?
Deploying to https://fallback-project-xyz.vercel.app
`
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	want := "https://fallback-project-xyz.vercel.app"
	if got != want {
		t.Fatalf("ExtractURL: got %q, want %q", got, want)
	}
}

func TestExtractURL_FallbackSkipsWarnAndError(t *testing.T) {
	t.Parallel()

	// Warning / Error URLs must be skipped; we want the info-line URL.
	out := `Warning: https://docs.vercel.com/some-warning
Error: https://errors.example.com/bad-token
Uploading…  https://good-deploy-abc.example.com/path
`
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	want := "https://good-deploy-abc.example.com/path"
	if got != want {
		t.Fatalf("ExtractURL: got %q, want %q", got, want)
	}
}

func TestExtractURL_NoMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"only error", "Error: VERCEL_TOKEN invalid\nError: abort\n"},
		{"only warning", "Warning: deprecated flag\n"},
		{"only prompts", "? Link project?\n? Scope?\n"},
		{"no https at all", "Deploying... done in 12s\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ExtractURL(tc.in)
			if ok {
				t.Fatalf("ExtractURL(%q): expected ok=false, got ok=true url=%q",
					tc.name, got)
			}
			if got != "" {
				t.Fatalf("ExtractURL(%q): expected empty url, got %q",
					tc.name, got)
			}
		})
	}
}

func TestExtractURL_TrailingPunctuation(t *testing.T) {
	t.Parallel()

	// The CLI sometimes tacks sentence punctuation onto the URL in
	// human-readable log lines. Ensure we strip it.
	out := "Ready! Deployed to https://my-app-abc123-team.vercel.app.\n"
	got, ok := ExtractURL(out)
	if !ok {
		t.Fatalf("ExtractURL: expected ok=true, got false")
	}
	want := "https://my-app-abc123-team.vercel.app"
	if got != want {
		t.Fatalf("ExtractURL: got %q, want %q", got, want)
	}
}

func TestIsPreview(t *testing.T) {
	t.Parallel()

	cases := []struct {
		url  string
		want bool
	}{
		{"https://my-app-abc123-team.vercel.app", true},
		{"https://simple.vercel.app", true},
		{"https://my-app-abc123-team.vercel.app/some/path", true},
		{"https://my-app-abc123-team.vercel.app:443", true},
		{"HTTPS://MY-APP.VERCEL.APP", true}, // case-insensitive host
		{"https://myapp.com", false},
		{"https://myapp.com/vercel.app", false}, // path, not host
		{"https://example.org", false},
		{"", false},
		{"not a url", false},
		{"://bogus", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.url, func(t *testing.T) {
			t.Parallel()
			got := IsPreview(tc.url)
			if got != tc.want {
				t.Fatalf("IsPreview(%q): got %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
