// envelope.go — Slack-style response envelope for the r1.* MCP catalog.
//
// Per specs/agentic-test-harness.md §3 ("Existing Patterns to Follow"),
// every tool response from the r1_server.go boundary is normalized to
// the same shape:
//
//   {
//     "ok":            bool,
//     "data":          any,        // present when ok=true
//     "error_code":    string,     // present when ok=false (stokerr code)
//     "error_message": string,     // present when ok=false
//     "links": {
//       "self":         "...",     // tool-name reference
//       "related":      ["..."],   // related tool names
//       "deprecations": ["..."]    // deprecation warnings (e.g. stoke_*)
//     }
//   }
//
// This file owns the envelope shape and the helpers that wrap a raw
// handler return value (or error) into the envelope. The corresponding
// stokerr/ taxonomy mapping is in stokerr_map.go (lands with item 8).
package mcp

import (
	"encoding/json"
)

// Envelope is the canonical Slack-style response shape for every r1.*
// tool. Encoded to JSON at the wire boundary.
type Envelope struct {
	OK           bool            `json:"ok"`
	Data         json.RawMessage `json:"data,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Links        *EnvelopeLinks  `json:"links,omitempty"`
}

// EnvelopeLinks carries tool-name references for clients building
// follow-up calls. self/related are tool names; deprecations are
// human-readable warnings.
type EnvelopeLinks struct {
	Self         string   `json:"self,omitempty"`
	Related      []string `json:"related,omitempty"`
	Deprecations []string `json:"deprecations,omitempty"`
}

// OKEnvelope wraps a successful result in the Slack-style envelope.
// If data is nil the Data field is omitted from the JSON output.
func OKEnvelope(toolName string, data any, related ...string) Envelope {
	env := Envelope{OK: true}
	if data != nil {
		raw, err := json.Marshal(data)
		if err == nil {
			env.Data = raw
		}
	}
	env.Links = buildLinks(toolName, related)
	return env
}

// ErrEnvelope wraps an error in the Slack-style envelope using the
// supplied stokerr taxonomy code and message. Use this at the
// r1_server.go boundary when a tool handler returns an error.
func ErrEnvelope(toolName, code, message string, related ...string) Envelope {
	return Envelope{
		OK:           false,
		ErrorCode:    code,
		ErrorMessage: message,
		Links:        buildLinks(toolName, related),
	}
}

// WithDeprecation attaches a deprecation message to the envelope (e.g.
// stoke_* alias warnings emitted from r1.session.start). Mutates and
// returns the envelope for chaining.
func (e Envelope) WithDeprecation(msg string) Envelope {
	if e.Links == nil {
		e.Links = &EnvelopeLinks{}
	}
	e.Links.Deprecations = append(e.Links.Deprecations, msg)
	return e
}

// buildLinks constructs the Links section. Returns nil when no fields
// would be populated so omitempty can drop it cleanly.
func buildLinks(self string, related []string) *EnvelopeLinks {
	if self == "" && len(related) == 0 {
		return nil
	}
	links := &EnvelopeLinks{Self: self}
	if len(related) > 0 {
		links.Related = append([]string(nil), related...)
	}
	return links
}

// MarshalEnvelope is a convenience wrapper that JSON-encodes an
// Envelope, returning ([]byte, error). The error path is reserved for
// pathological data (e.g. unmarshalable types in Data); routine errors
// from the tool handler should be encoded as ErrEnvelope, not bubbled
// up as a marshal error.
func MarshalEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}
