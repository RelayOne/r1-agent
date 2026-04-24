// Package policy provides the Cedar-style authorization gate for Stoke.
// Three backends are supported at runtime:
//   - NullClient — standalone dev mode (always allows with an explicit banner)
//   - HTTPClient — cedar-agent HTTP sidecar (PARC JSON protocol)
//   - YAMLClient — in-process rule engine reading a local policy YAML
//
// Zero-value Decision is DecisionDeny so a mis-constructed Result
// fails closed. Every backend that returns an error must return
// DecisionDeny with a non-empty Errors slice so callers uniformly
// fail-closed without inspecting the error kind.
package policy

import (
	"context"
	"errors"
)

// Decision is the binary authorization outcome returned by a policy
// backend. The zero value is DecisionDeny so any mis-constructed
// Result fails closed.
type Decision int

const (
	// DecisionDeny is the zero value — a mis-constructed Result
	// fails closed. All fail-closed paths return DecisionDeny.
	DecisionDeny Decision = iota
	// DecisionAllow indicates the backend authorized the request.
	DecisionAllow
)

// String returns "allow" for DecisionAllow and "deny" for
// DecisionDeny or any other (unknown) Decision value. The output
// is stable and suitable for logs and event payloads.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	default:
		return "deny"
	}
}

// Request is the PARC-shaped (principal/action/resource/context)
// authorization query a caller passes into Client.Check.
type Request struct {
	// Principal is the Cedar-style entity UID for the actor, e.g.
	// `Stoke::"<session-id>"`.
	Principal string
	// Action is the tool name being gated, e.g. "bash",
	// "file_write", or "mcp_linear_create_issue".
	Action string
	// Resource is what is being acted on — typically a file path
	// or the command being run.
	Resource string
	// Context carries freeform attributes used by the backend for
	// rule evaluation, e.g. {"trust_level": 3, "phase": "execute"}.
	Context map[string]any
}

// Result is the backend's verdict for a Request. A backend that
// could not render a verdict must populate Errors and set
// Decision to DecisionDeny so callers fail closed uniformly.
type Result struct {
	// Decision is the allow/deny outcome. Zero value is
	// DecisionDeny (fail-closed).
	Decision Decision
	// Reasons is the human-readable rationale from the backend,
	// typically matched rule IDs.
	Reasons []string
	// Errors is populated when the backend could not render a
	// verdict (transport failure, malformed response, etc.).
	Errors []string
}

// Client is the interface every policy backend satisfies. Check
// evaluates req and returns a Result plus an optional error.
// Callers MUST treat err != nil as fail-closed (DecisionDeny);
// backends must return DecisionDeny in the Result as well so
// inspection of err is never required for safety.
type Client interface {
	Check(ctx context.Context, req Request) (Result, error)
}

// ErrPolicyUnavailable is returned by transport-failed backends
// after they've returned a fail-closed Result. Callers usually
// don't need to distinguish this from generic errors — they
// should fail-closed on any err != nil.
var ErrPolicyUnavailable = errors.New("policy: backend unavailable")
