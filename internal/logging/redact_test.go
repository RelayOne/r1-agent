package logging

import (
	"strings"
	"testing"
)

// TestRedact_PhaseTwo asserts that the shape-based patterns seeded
// at package load strip Vercel + Cloudflare tokens from the four
// surfaces enumerated in specs/deploy-phase2.md §Token Security:
//
//  1. Literal env-var assignment (VERCEL_TOKEN=, CLOUDFLARE_API_TOKEN=).
//  2. Short CLI flag (--token <value>, --token=<value>).
//  3. Vercel alternate CLI flag (--api-token <value>).
//  4. HTTP Authorization header (Bearer <value>).
//
// Each case is fed through logging.Redact; the secret must be gone
// AND the surrounding non-secret context must survive so operators
// still get useful log output.
func TestRedact_PhaseTwo(t *testing.T) {
	const vercelToken = "vercel_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123"
	const cfToken = "abcdef0123456789abcdef0123456789abcdef0123"
	const bearerVal = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aaaaaaaaaa.bbbbbbbbbb"

	cases := []struct {
		name      string
		input     string
		mustStrip []string
		mustKeep  []string
	}{
		{
			name:      "vercel env assignment",
			input:     "running: VERCEL_TOKEN=" + vercelToken + " npx vercel deploy",
			mustStrip: []string{vercelToken},
			mustKeep:  []string{"VERCEL_TOKEN=", "<redacted>", "npx vercel deploy"},
		},
		{
			name:      "cloudflare env assignment",
			input:     "spawn env: CLOUDFLARE_API_TOKEN=" + cfToken + " wrangler deploy",
			mustStrip: []string{cfToken},
			mustKeep:  []string{"CLOUDFLARE_API_TOKEN=", "<redacted>", "wrangler deploy"},
		},
		{
			name:      "short --token flag space form",
			input:     "argv: vercel deploy --token " + vercelToken + " --yes",
			mustStrip: []string{vercelToken},
			mustKeep:  []string{"vercel deploy", "--token ", "<redacted>", "--yes"},
		},
		{
			name:      "short --token flag equals form",
			input:     "argv: vercel deploy --token=" + vercelToken + " --yes",
			mustStrip: []string{vercelToken},
			mustKeep:  []string{"--token=", "<redacted>", "--yes"},
		},
		{
			name:      "api-token flag",
			input:     "argv: vercel deploy --api-token " + vercelToken + " --prod",
			mustStrip: []string{vercelToken},
			mustKeep:  []string{"--api-token ", "<redacted>", "--prod"},
		},
		{
			name:      "bearer authorization header",
			input:     "HTTP 401 Authorization: Bearer " + bearerVal + " (response body)",
			mustStrip: []string{bearerVal},
			mustKeep:  []string{"Authorization:", "Bearer ", "<redacted>", "(response body)"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.input)
			for _, needle := range tc.mustStrip {
				if strings.Contains(got, needle) {
					t.Fatalf("secret leaked: %q still contains %q", got, needle)
				}
			}
			for _, needle := range tc.mustKeep {
				if !strings.Contains(got, needle) {
					t.Fatalf("expected %q to survive redaction, got %q", needle, got)
				}
			}
		})
	}
}

// TestRedact_PhaseTwo_BenignPassthrough asserts the new patterns do
// NOT fire on ordinary log lines that merely mention token names in
// prose, on lone env-var names, or on short Bearer stand-ins that
// are clearly not live credentials. (Actual CLI argv like
// `--token FOO` is intentionally always masked — see
// TestRedact_PhaseTwo — because real vs stub values cannot be
// distinguished at runtime.)
func TestRedact_PhaseTwo_BenignPassthrough(t *testing.T) {
	benign := []string{
		"deploy complete — issuing Bearer auth tokens next",
		"operator note: set VERCEL_TOKEN in .env (no value here)",
		"Bearer x", // too short; should not be masked
		"Authorization header present but empty",
	}
	for _, in := range benign {
		if got := Redact(in); got != in {
			t.Fatalf("benign line was altered: input=%q output=%q", in, got)
		}
	}
}

// TestAddPattern_CompilesAndApplies asserts the public AddPattern
// helper registers a new regex and applies it on subsequent Redact
// calls.
func TestAddPattern_CompilesAndApplies(t *testing.T) {
	if err := AddPattern(`FAKE_SECRET_[A-Z0-9]{8}`, `<redacted>`); err != nil {
		t.Fatalf("AddPattern: unexpected error: %v", err)
	}
	in := "boot: FAKE_SECRET_ABCD1234 trailing"
	got := Redact(in)
	if strings.Contains(got, "FAKE_SECRET_ABCD1234") {
		t.Fatalf("new pattern did not apply: %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}

// TestAddPattern_InvalidRegex asserts AddPattern rejects malformed
// regex without mutating the pattern list.
func TestAddPattern_InvalidRegex(t *testing.T) {
	if err := AddPattern(`(unclosed`, `<redacted>`); err == nil {
		t.Fatal("expected error from invalid regex, got nil")
	}
}
