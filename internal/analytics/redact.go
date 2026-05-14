package analytics

import (
	"regexp"
	"strings"
)

// RedactionChecker is the read interface the analytics package needs
// from internal/ledger/. Keeping the interface here (rather than
// importing the ledger directly) avoids a hard package coupling — the
// ledger is wired in by the startup path via WithRedactionChecker, and
// tests can supply an in-memory fake.
//
// The contract: given a ledger node ID, IsRedacted returns true when
// the content tier has been wiped (per specs/ledger-redaction.md §6).
// Errors are folded to "not redacted" by the caller — analytics never
// blocks a capture on a ledger I/O failure.
type RedactionChecker interface {
	IsRedacted(nodeID string) (bool, error)
}

// WithRedactionChecker installs a redaction checker after construction.
// Returns the receiver so callers can chain it onto FromEnv().
func (c *Client) WithRedactionChecker(r RedactionChecker) *Client {
	if c == nil {
		return nil
	}
	c.redaction = r
	return c
}

// piiPatterns are the property-value patterns we refuse to emit.
//   - email-shaped strings (anything containing @ then a dot)
//   - absolute filesystem paths beyond the top-level repo root
//   - bearer tokens / API keys (heuristic — long base64-ish strings)
var (
	emailPattern   = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	absPathPattern = regexp.MustCompile(`(?:^|[ \t])(?:/[a-z0-9._/-]{12,}|[A-Z]:\\[a-zA-Z0-9._\\-]{12,})`)
	bearerPattern  = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._-]{20,}`)
)

// redactedValue is the constant the analytics package writes in place
// of any property whose value looks PII-shaped, or whose backing ledger
// node is recorded as wiped.
const redactedValue = "[REDACTED]"

// applyRedaction walks a property map and replaces values that:
//
//  1. Look PII-shaped according to the patterns above.
//  2. Reference a ledger node whose content tier has been wiped (only
//     when the receiver was constructed with WithRedactionChecker).
//
// The map is mutated in place AND returned for ergonomics. Nil maps are
// safe; the function returns them unchanged.
func (c *Client) applyRedaction(props map[string]any) map[string]any {
	if props == nil {
		return props
	}
	for k, v := range props {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if looksPII(s) {
			props[k] = redactedValue
			continue
		}
		if c != nil && c.redaction != nil && isNodeIDKey(k) {
			if red, err := c.redaction.IsRedacted(s); err == nil && red {
				props[k] = redactedValue
			}
		}
	}
	return props
}

func looksPII(s string) bool {
	if s == "" {
		return false
	}
	if emailPattern.MatchString(s) {
		return true
	}
	if bearerPattern.MatchString(s) {
		return true
	}
	if absPathPattern.MatchString(s) {
		return true
	}
	return false
}

// isNodeIDKey reports whether a property name carries a ledger node ID
// — the redaction checker is only consulted for these keys to keep the
// hot-path lookup cost negligible.
func isNodeIDKey(name string) bool {
	if strings.HasSuffix(name, "_node_id") {
		return true
	}
	switch name {
	case "node_id", "ledger_node_id", "redaction_target":
		return true
	}
	return false
}
