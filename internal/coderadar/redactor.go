// Package coderadar — privacy + redaction layer.
//
// Two hard layers:
//
//  1. Allowlist (HARD): any prop key not in AllowedProps[event_name]
//     is dropped silently — the value never leaves the process.
//  2. Promptguard scrub: free-form string fields that DO pass the
//     allowlist (error_message, abort_reason, reason) are scanned by
//     internal/promptguard and any matching segment is replaced with
//     a [REDACTED:<class>] marker.
//
// On top of the two layers, the Redactor SHA256-hashes high-cardinality
// fields (file_path → file_path_sha256) and truncates error_message
// to 200 chars while preserving a sha256 of the original for queries.
//
// Hard prohibitions (asserted in redactor_test.go):
//   - raw prompts
//   - raw tool inputs
//   - plain file paths (always _sha256-hashed)
//   - API keys / Authorization headers (caught by promptguard pattern)
package coderadar

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/RelayOne/r1/internal/promptguard"
)

// ErrorMessageMaxChars caps free-form error_message length before emit.
// The original is preserved as a SHA256 via the _sha256 sibling field.
const ErrorMessageMaxChars = 200

// Redactor enforces the per-event allowlist and the promptguard scrub.
// Stateless after construction — safe for concurrent use.
type Redactor struct {
	allowed map[string]map[string]struct{}
}

// NewRedactor builds a Redactor from the package-level AllowedProps map.
// Each event gets an O(1) lookup set so the hot path is allocation-free.
func NewRedactor() *Redactor {
	r := &Redactor{
		allowed: make(map[string]map[string]struct{}, len(AllowedProps)),
	}
	for name, props := range AllowedProps {
		set := make(map[string]struct{}, len(props))
		for _, p := range props {
			set[p] = struct{}{}
		}
		r.allowed[name] = set
	}
	return r
}

// Redact returns a new Event with disallowed props stripped, high-card
// fields hashed, and free-form strings scrubbed via promptguard. The
// input is not mutated.
//
// If the event name is not canonical, Redact returns the event with an
// empty Props map (the subscriber should drop entirely).
func (r *Redactor) Redact(in Event) Event {
	out := in
	allowed, ok := r.allowed[in.Name]
	if !ok {
		out.Props = nil
		return out
	}
	if len(in.Props) == 0 {
		out.Props = nil
		return out
	}
	scrubbed := make(map[string]any, len(in.Props))

	// Promote raw file_path to file_path_sha256 before the allowlist
	// pass so the original key is never serialized even by accident.
	if v, ok := in.Props["file_path"]; ok {
		if s, isStr := v.(string); isStr && s != "" {
			scrubbed["file_path_sha256"] = sha256Hex(s)
		}
	}

	// Same for raw `path` aliases that some hub events use.
	if _, present := in.Props["file_path_sha256"]; !present {
		if v, ok := in.Props["path"]; ok {
			if s, isStr := v.(string); isStr && s != "" {
				scrubbed["file_path_sha256"] = sha256Hex(s)
			}
		}
	}

	for k, v := range in.Props {
		if _, ok := allowed[k]; !ok {
			// Disallowed prop — drop silently. Hard layer.
			continue
		}
		// Free-form strings get the promptguard scrub.
		if s, isStr := v.(string); isStr {
			switch k {
			case "error_message":
				orig := s
				if len(s) > ErrorMessageMaxChars {
					s = s[:ErrorMessageMaxChars]
				}
				if scrub, _, err := promptguard.Sanitize(s, promptguard.ActionStrip, "coderadar.error_message"); err == nil {
					s = scrub
				}
				scrubbed[k] = s
				scrubbed["error_message_sha256"] = sha256Hex(orig)
			case "abort_reason", "reason":
				if scrub, _, err := promptguard.Sanitize(s, promptguard.ActionStrip, "coderadar."+k); err == nil {
					s = scrub
				}
				scrubbed[k] = s
			default:
				scrubbed[k] = s
			}
			continue
		}
		scrubbed[k] = v
	}

	// Final allowlist sweep: anything we computed (error_message_sha256,
	// file_path_sha256) must still satisfy the allowlist.
	for k := range scrubbed {
		if _, ok := allowed[k]; !ok {
			delete(scrubbed, k)
		}
	}

	out.Props = scrubbed
	return out
}

// sha256Hex returns the lowercase hex SHA256 of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return strings.ToLower(hex.EncodeToString(sum[:]))
}

// HashFilePath is exported for callers that build Event props directly
// and want to honor the "no plain paths" boundary without rolling their
// own hash. Returns "" for empty input.
func HashFilePath(path string) string {
	if path == "" {
		return ""
	}
	return sha256Hex(path)
}
