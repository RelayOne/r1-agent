// Package logging — redact.go — exact-secret redaction registry.
//
// This file complements the pattern-based redactor in internal/redact
// (which recognizes common secret SHAPES — Bearer tokens, sk-ant-..
// keys, etc.) with a second layer that redacts EXACT known-secret
// values. The use case: the operator has configured an MCP server
// with AuthEnv=GITHUB_MCP_TOKEN, the env var holds
// "ghp_abcdef0123…" (which the pattern-based redactor will catch)
// but might also hold some arbitrary bespoke API key (e.g. an
// internal company token with no recognizable shape) that would
// otherwise slip through. By registering the concrete value at
// process start, every subsequent log line that happens to contain
// that exact string is rewritten to "<redacted>".
//
// Concurrency: the registry is protected by a sync.RWMutex so Redact
// (hot path) takes the read lock and Register / Unregister take the
// write lock. Operations are idempotent under reference-counting
// semantics: registering the same secret twice is fine, and the
// value only disappears from the registry once BOTH unregister
// closures have been called.
//
// This is consumed by internal/mcp/redact.go per specs/mcp-client.md
// §Auth / Secret Handling #4.

package logging

import (
	"regexp"
	"sync"
)

// redactPlaceholder is the string substituted in place of every
// registered secret. Kept as a package-level constant so callers
// (and tests) can refer to it without hard-coding the literal.
const redactPlaceholder = "<redacted>"

var (
	secretsMu sync.RWMutex
	// secrets maps secret-value → active refcount. Entries with
	// count 0 are removed so Redact's hot path only walks live
	// secrets.
	secrets = map[string]int{}
)

// patternEntry pairs a compiled regex with its replacement template.
// The replacement is passed verbatim to regexp.ReplaceAllString, so
// callers may use $1 / ${name} back-references.
type patternEntry struct {
	re          *regexp.Regexp
	replacement string
}

var (
	patternsMu sync.RWMutex
	// patterns is the package-level list of shape-based redactors
	// applied on top of the exact-match secrets registry. Seeded in
	// init() with provider-specific shapes added per specs/deploy-*.md
	// §Token Security (Fly / Vercel / Cloudflare).
	patterns []patternEntry
)

func init() {
	// Deploy phase 2 — Vercel + Cloudflare token shapes. Narrow
	// literal-form patterns chosen to avoid false positives on
	// ordinary log output.
	defaults := []struct {
		re, replacement string
	}{
		// Vercel: literal env-var assignment.
		{`VERCEL_TOKEN=\S+`, `VERCEL_TOKEN=` + redactPlaceholder},
		// Cloudflare: literal env-var assignments.
		{`CLOUDFLARE_API_TOKEN=\S+`, `CLOUDFLARE_API_TOKEN=` + redactPlaceholder},
		{`CLOUDFLARE_ACCOUNT_ID=\S+`, `CLOUDFLARE_ACCOUNT_ID=` + redactPlaceholder},
		// Vercel-specific token shape (modern `vercel_…` prefix).
		{`vercel_[a-zA-Z0-9_]{24,}`, redactPlaceholder},
		// CLI flags — keep the flag visible, mask the value. Both
		// --flag=value and --flag value forms; the flag-equals form
		// must come first because the space-separated pattern would
		// otherwise swallow the `=value` part verbatim.
		{`--token=\S+`, `--token=` + redactPlaceholder},
		{`--token \S+`, `--token ` + redactPlaceholder},
		{`--api-token=\S+`, `--api-token=` + redactPlaceholder},
		{`--api-token \S+`, `--api-token ` + redactPlaceholder},
		// HTTP Authorization header shape. Mask 20+ char opaque
		// values; a narrower lower bound would false-positive on
		// short `Bearer x` stubs seen in docs and test fixtures.
		{`Bearer \S{20,}`, `Bearer ` + redactPlaceholder},
	}
	for _, d := range defaults {
		// MustCompile is acceptable here because the patterns are
		// literal strings authored in-tree; a malformed one would
		// fail at process start rather than in production.
		patterns = append(patterns, patternEntry{
			re:          regexp.MustCompile(d.re),
			replacement: d.replacement,
		})
	}
}

// AddPattern registers a shape-based redaction rule. The regex is
// compiled once and applied to every subsequent Redact call; the
// replacement string is passed to regexp.ReplaceAllString, so
// capture-group back-references ($1, ${name}) are supported.
//
// Returns an error only when regex fails to compile; the registry is
// otherwise append-only (Reset is intentionally not exposed here to
// discourage callers from clearing deploy-phase secret shapes at
// runtime).
func AddPattern(regex, replacement string) error {
	re, err := regexp.Compile(regex)
	if err != nil {
		return err
	}
	patternsMu.Lock()
	patterns = append(patterns, patternEntry{re: re, replacement: replacement})
	patternsMu.Unlock()
	return nil
}

// Register adds secret to the exact-match redaction set and returns
// an unregister closure. Calling the closure more than once is safe
// (idempotent — subsequent calls are no-ops).
//
// Empty secret is a no-op: Register("") returns a no-op closure and
// does NOT add anything to the registry. This matters because
// callers may register values read from environment variables that
// could be blank — we should never start matching the empty string.
//
// Multiple calls with the same secret increment a refcount; the
// secret is only removed from the registry when every corresponding
// unregister has fired. This lets multiple MCP Registry instances
// (or tests running in parallel) share the same env-var without
// stepping on each other.
func Register(secret string) (unregister func()) {
	if secret == "" {
		return func() {}
	}
	secretsMu.Lock()
	secrets[secret]++
	secretsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			secretsMu.Lock()
			defer secretsMu.Unlock()
			if n, ok := secrets[secret]; ok {
				if n <= 1 {
					delete(secrets, secret)
				} else {
					secrets[secret] = n - 1
				}
			}
		})
	}
}

// Redact replaces every occurrence of every registered secret in s
// with the string "<redacted>" and then applies the package-level
// shape-based patterns (VERCEL_TOKEN, --token, Bearer, etc.) seeded
// in init() and extended via AddPattern.
//
// When neither the exact-match registry nor any patterns would fire
// on s, it is returned unchanged without reallocation.
//
// This does NOT invoke the broader pattern-based redactor in
// internal/redact; callers that want that layer too should chain:
// logging.Redact(redact.Redact(s)).
func Redact(s string) string {
	secretsMu.RLock()
	live := make([]string, 0, len(secrets))
	for k := range secrets {
		live = append(live, k)
	}
	secretsMu.RUnlock()

	out := s
	for _, sec := range live {
		out = replaceAll(out, sec, redactPlaceholder)
	}

	patternsMu.RLock()
	pats := patterns
	patternsMu.RUnlock()
	for _, p := range pats {
		out = p.re.ReplaceAllString(out, p.replacement)
	}
	return out
}

// replaceAll is strings.ReplaceAll inlined to keep this file free of
// the strings import (we want zero dependencies on other packages so
// internal/redact can one day depend on internal/logging without
// cycles — redact.go imports sync only).
func replaceAll(s, old, new string) string {
	if old == "" || old == new {
		return s
	}
	// Fast path: no occurrence → return s untouched (no alloc).
	idx := indexOf(s, old)
	if idx < 0 {
		return s
	}
	// Slow path: walk through replacing each occurrence.
	var b []byte
	b = append(b, s[:idx]...)
	b = append(b, new...)
	s = s[idx+len(old):]
	for {
		idx = indexOf(s, old)
		if idx < 0 {
			b = append(b, s...)
			return string(b)
		}
		b = append(b, s[:idx]...)
		b = append(b, new...)
		s = s[idx+len(old):]
	}
}

// indexOf is a tiny substring search — avoids pulling in "strings"
// on the hot path. Returns -1 when needle is not present.
func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// registrySize returns the number of distinct registered secrets.
// Exposed for tests that want to assert clean-up semantics without
// poking at the private map directly.
func registrySize() int {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	return len(secrets)
}
