// Package redact strips secrets from text before it reaches any egress path
// that writes to disk or stdout — logs, replay recordings, ledger node
// contents, bus event payloads, and stream parser output.
//
// This is distinct from internal/scan, which detects secrets in source code
// at review time (inbound static analysis). redact operates on every byte
// Stoke emits at runtime, because sub-tool stdout, API error messages,
// and operator shell commands routinely carry tokens that we don't want
// pinned to disk.
//
// The design is deliberately conservative: short tokens (<18 chars) are
// fully masked, longer tokens preserve the first 6 and last 4 characters
// for debuggability ("sk-ant-xxxxxxxxxxxxxxxxxxxxxxxxbeef" →
// "sk-ant-[REDACTED-28]beef"). This borrows directly from the Hermes
// RedactingFormatter convention so operators reading logs across
// harnesses see the same shape.
package redact

import (
	"bytes"
	"io"
	"regexp"
	"sync"
)

// Pattern is one secret-shape recognizer. Name is used in masked output
// to hint at what kind of secret was stripped without leaking its value.
type Pattern struct {
	Name    string
	Regexp  *regexp.Regexp
	Replace func(match string) string // optional custom mask; default mask used when nil
}

var (
	patternsMu sync.RWMutex
	patterns   = defaultPatterns()
)

// defaultPatterns returns the built-in secret recognizers. The list is
// opinionated toward concrete, high-confidence shapes. We explicitly do
// not try to match generic "looks like a secret" heuristics because they
// produce false positives that make logs unreadable.
func defaultPatterns() []Pattern {
	return []Pattern{
		// Anthropic API keys: sk-ant-{api,admin,...}-<random>
		{Name: "anthropic-key", Regexp: regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`)},
		// Generic OpenAI-style: sk-<alphanumeric>
		{Name: "openai-key", Regexp: regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`)},
		// LiteLLM master key shape: sk-litellm-<hex>
		{Name: "litellm-key", Regexp: regexp.MustCompile(`sk-litellm-[a-zA-Z0-9-]{20,}`)},
		// GitHub PATs / app tokens: ghp_, gho_, ghu_, ghs_, ghr_
		{Name: "github-token", Regexp: regexp.MustCompile(`gh[pousr]_[a-zA-Z0-9]{20,}`)},
		// Slack tokens: xox[abpsor]-...
		{Name: "slack-token", Regexp: regexp.MustCompile(`xox[abpsor]-[a-zA-Z0-9-]{10,}`)},
		// AWS access keys
		{Name: "aws-access-key", Regexp: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
		// Google API keys
		{Name: "google-key", Regexp: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
		// Stripe live/test keys
		{Name: "stripe-key", Regexp: regexp.MustCompile(`(?:sk|rk|pk)_(?:live|test)_[0-9a-zA-Z]{20,}`)},
		// SendGrid
		{Name: "sendgrid-key", Regexp: regexp.MustCompile(`SG\.[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}`)},
		// Hugging Face
		{Name: "huggingface-token", Regexp: regexp.MustCompile(`hf_[a-zA-Z0-9]{30,}`)},
		// PEM private key blocks (entire block masked)
		{Name: "private-key", Regexp: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), Replace: func(string) string { return "[REDACTED-PRIVATE-KEY]" }},
		// Authorization: Bearer <token>
		{Name: "bearer-token", Regexp: regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic)\s+)([A-Za-z0-9._~+/=\-]{16,})`), Replace: func(m string) string {
			// Preserve the "Authorization: Bearer " prefix.
			idx := regexp.MustCompile(`(?i)(?:Bearer|Basic)\s+`).FindStringIndex(m)
			if idx == nil {
				return defaultMask(m)
			}
			prefix := m[:idx[1]]
			token := m[idx[1]:]
			return prefix + defaultMask(token)
		}},
		// Env-var assignments with secret-shaped names
		{Name: "env-secret-assignment", Regexp: regexp.MustCompile(`(?i)(\b(?:[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|API[_-]?KEY|PRIVATE[_-]?KEY|ACCESS[_-]?KEY|CREDENTIAL)[A-Z0-9_]*)\s*[=:]\s*)(["']?)([^"'\s][^\s"']{5,})(["']?)`), Replace: func(m string) string {
			re := regexp.MustCompile(`(?i)(\b(?:[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|API[_-]?KEY|PRIVATE[_-]?KEY|ACCESS[_-]?KEY|CREDENTIAL)[A-Z0-9_]*)\s*[=:]\s*)(["']?)([^"'\s][^\s"']{5,})(["']?)`)
			parts := re.FindStringSubmatch(m)
			if len(parts) < 5 {
				return defaultMask(m)
			}
			return parts[1] + parts[2] + defaultMask(parts[3]) + parts[4]
		}},
		// Postgres / MySQL / MongoDB URLs with embedded creds:
		//   scheme://user:password@host/...
		{Name: "db-url-creds", Regexp: regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/\s]+:)([^@/\s]+)(@)`), Replace: func(m string) string {
			re := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/\s]+:)([^@/\s]+)(@)`)
			parts := re.FindStringSubmatch(m)
			if len(parts) != 4 {
				return m
			}
			return parts[1] + defaultMask(parts[2]) + parts[3]
		}},
	}
}

// defaultMask returns a masked form of s: short strings become "[REDACTED-N]"
// where N is the original length; long strings preserve first 6 and last 4
// characters for debuggability ("sk-ant-xxxx...beef" shape).
func defaultMask(s string) string {
	n := len(s)
	if n < 18 {
		return "[REDACTED-" + lenStr(n) + "]"
	}
	return s[:6] + "[REDACTED-" + lenStr(n-10) + "]" + s[n-4:]
}

func lenStr(n int) string {
	if n < 0 {
		n = 0
	}
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}

// Redact scans s for every known secret shape and returns s with each match
// replaced by its mask. Stable: given the same input, the same output.
//
// Cheap fast-path: if s contains no character from the set of known
// secret prefixes ("sk-", "gh", "xox", "AKIA", "AIza", "hf_", "SG.",
// "Authorization", "BEGIN ", "://", "="), we skip the regex walk entirely.
// This matters because Redact is on the hot path for every log line.
func Redact(s string) string {
	if !mightContainSecret(s) {
		return s
	}
	patternsMu.RLock()
	ps := patterns
	patternsMu.RUnlock()
	for _, p := range ps {
		replace := p.Replace
		if replace == nil {
			replace = defaultMask
		}
		s = p.Regexp.ReplaceAllStringFunc(s, replace)
	}
	return s
}

// RedactBytes is Redact for a byte slice. Returns b unchanged when the
// prefilter doesn't fire AND when no regex actually matched. Callers on
// hot paths depend on this: the io.Writer wrapper is invoked on every
// log line, and pointless reallocation adds up.
func RedactBytes(b []byte) []byte {
	if !mightContainSecretBytes(b) {
		return b
	}
	out := Redact(string(b))
	if out == string(b) {
		return b
	}
	return []byte(out)
}

// mightContainSecret is the cheap prefilter. Distinctive markers only —
// including "=" or ":" would fire on every log line and defeat the point.
// Lines that contain env-assignment secrets (FOO_SECRET=bar) also contain
// one of the SECRET/TOKEN/PASSWORD/API_KEY/PRIVATE_KEY/ACCESS_KEY/CREDENTIAL
// substrings, so those are in the marker set.
func mightContainSecret(s string) bool {
	markers := []string{
		"sk-", "gh", "xox", "AKIA", "AIza", "hf_", "SG.",
		"Authorization", "BEGIN ",
		"SECRET", "TOKEN", "PASSWORD", "API_KEY", "APIKEY",
		"API-KEY", "PRIVATE_KEY", "ACCESS_KEY", "CREDENTIAL",
		"://",
	}
	for _, m := range markers {
		if containsASCII(s, m) {
			return true
		}
	}
	return false
}

func mightContainSecretBytes(b []byte) bool {
	markers := [][]byte{
		[]byte("sk-"), []byte("gh"), []byte("xox"), []byte("AKIA"),
		[]byte("AIza"), []byte("hf_"), []byte("SG."),
		[]byte("Authorization"), []byte("BEGIN "),
		[]byte("SECRET"), []byte("TOKEN"), []byte("PASSWORD"),
		[]byte("API_KEY"), []byte("APIKEY"), []byte("API-KEY"),
		[]byte("PRIVATE_KEY"), []byte("ACCESS_KEY"), []byte("CREDENTIAL"),
		[]byte("://"),
	}
	for _, m := range markers {
		if bytes.Contains(b, m) {
			return true
		}
	}
	return false
}

// containsASCII is a small, allocation-free substring check for ASCII needles.
// We avoid strings.Contains only because it's occasionally compiled differently
// on older toolchains; the semantics match.
func containsASCII(s, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(s) {
		return false
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Writer wraps an io.Writer and applies Redact to every byte sequence written
// through it. Not thread-safe with respect to the downstream writer — the
// caller is expected to serialize writes (which slog and logging.Init do
// already).
type Writer struct {
	W io.Writer
}

// NewWriter returns a Writer wrapping w. Safe to pass nil, in which case
// Write is a no-op that returns (len(p), nil).
func NewWriter(w io.Writer) *Writer { return &Writer{W: w} }

func (rw *Writer) Write(p []byte) (int, error) {
	if rw.W == nil {
		return len(p), nil
	}
	// Redact may reallocate; report the caller's original length so they
	// don't mistake a length change for a short write.
	out := RedactBytes(p)
	_, err := rw.W.Write(out)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// AddPattern registers an additional pattern. Intended for policy-driven
// extensions (e.g. customer-specific secret shapes loaded from stoke.policy.yaml).
// Safe to call concurrently with Redact.
func AddPattern(p Pattern) {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	patterns = append(patterns, p)
}

// Reset restores the default pattern set. Exposed for tests.
func Reset() {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	patterns = defaultPatterns()
}
