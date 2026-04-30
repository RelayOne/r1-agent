package studioclient

import (
	"errors"
	"fmt"
	"net"
)

// Typed errors returned by transports. Callers should use errors.Is /
// errors.As to branch on them rather than string-matching.
var (
	// ErrStudioDisabled — the config block has Enabled: false. Skills
	// must surface this as "actium_studio_disabled" to the agent; the
	// session continues normally. No retry.
	ErrStudioDisabled = errors.New("studio: disabled in studio_config")

	// ErrStudioUnavailable — a network-level failure the transport
	// could not classify more precisely (DNS, dial refused, connection
	// reset mid-request, subprocess crash, context cancelled by
	// operator). Skills surface this as "actium_studio_unavailable"
	// per work order §Degradation-stance.
	ErrStudioUnavailable = errors.New("studio: endpoint unavailable")

	// ErrStudioAuth — HTTP 401 from Studio or missing TokenEnv value.
	// Separately typed so operators see an actionable message rather
	// than a generic "unavailable" during a bad-token diagnosis.
	ErrStudioAuth = errors.New("studio: authentication failed")

	// ErrStudioScope — HTTP 403 from Studio. The token is valid but
	// lacks the X-Studio-Scopes required for the endpoint.
	ErrStudioScope = errors.New("studio: scope denied")

	// ErrStudioNotFound — HTTP 404 from Studio. Typically a stale site
	// ID passed to a scoped endpoint.
	ErrStudioNotFound = errors.New("studio: resource not found")

	// ErrStudioValidation — HTTP 400 / 422 from Studio. Skill input
	// passed JSON-schema validation on the R1 side but the server
	// rejected a semantic constraint. Callers propagate the message so
	// the agent can revise.
	ErrStudioValidation = errors.New("studio: request rejected by server")

	// ErrStudioTimeout — per-call deadline elapsed before a response.
	// Distinct from ErrStudioUnavailable because a timeout sometimes
	// warrants a longer-deadline retry while an unreachable endpoint
	// usually doesn't.
	ErrStudioTimeout = errors.New("studio: request timed out")

	// ErrStudioServer — HTTP 5xx from Studio. Retry policy applied by
	// the transport itself; this error is returned only after all
	// retries are exhausted.
	ErrStudioServer = errors.New("studio: server error")
)

// StudioError wraps one of the sentinel errors above with HTTP status
// code and response body excerpt for logging. Satisfies errors.Is via
// the embedded sentinel so callers can check `errors.Is(err, ErrStudioAuth)`
// without unwrapping manually.
type StudioError struct {
	// Tool is the R1 skill name that triggered the call, e.g.
	// "studio.scaffold_site". Empty when the failure happened before a
	// tool was resolved.
	Tool string

	// Status is the HTTP status code when the transport has one. 0 for
	// pre-HTTP failures (DNS, dial).
	Status int

	// BodyExcerpt is the first 512 bytes of the response body, stripped
	// of control characters. Bearer tokens / cookies are not included —
	// the transport only captures response bodies, never request ones.
	BodyExcerpt string

	// Cause is the wrapped sentinel (one of the package-level vars).
	Cause error

	// Underlying is the original transport error (DNS lookup failure,
	// *url.Error, json.SyntaxError, etc.) when available; nil otherwise.
	Underlying error
}

func (e *StudioError) Error() string {
	switch {
	case e == nil:
		return ""
	case e.Status == 0:
		return fmt.Sprintf("%s (tool=%q): %v", e.Cause, e.Tool, e.Underlying)
	case e.BodyExcerpt != "":
		return fmt.Sprintf("%s (tool=%q, status=%d): %s", e.Cause, e.Tool, e.Status, e.BodyExcerpt)
	default:
		return fmt.Sprintf("%s (tool=%q, status=%d)", e.Cause, e.Tool, e.Status)
	}
}

// Is lets errors.Is match on either the sentinel cause or the
// underlying error. This keeps callers' intent-level checks simple:
//
//	if errors.Is(err, studioclient.ErrStudioUnavailable) { ... }
func (e *StudioError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == e.Cause {
		return true
	}
	if e.Underlying != nil && errors.Is(e.Underlying, target) {
		return true
	}
	return false
}

// Unwrap exposes the underlying transport error for callers that need
// to branch on it (*url.Error etc.).
func (e *StudioError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Underlying != nil {
		return e.Underlying
	}
	return e.Cause
}

// IsUnavailable is the degradation-stance predicate: returns true when
// the skill should surface "actium_studio_unavailable" and the session
// should continue. Recognizes ErrStudioUnavailable, ErrStudioDisabled,
// ErrStudioTimeout, and any net.Error with Timeout() or a context-
// cancelled root cause.
//
// Does NOT include ErrStudioAuth / ErrStudioScope / ErrStudioValidation
// because those are actionable configuration errors, not reachability
// failures — they deserve different UI treatment.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStudioDisabled) ||
		errors.Is(err, ErrStudioUnavailable) ||
		errors.Is(err, ErrStudioTimeout) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

// classifyHTTPStatus maps an HTTP status code to the sentinel error.
// Returns nil for 2xx; ErrStudioServer for 5xx (caller decides whether
// to retry first).
func classifyHTTPStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 400 || status == 422:
		return ErrStudioValidation
	case status == 401:
		return ErrStudioAuth
	case status == 403:
		return ErrStudioScope
	case status == 404:
		return ErrStudioNotFound
	case status == 408 || status == 504:
		return ErrStudioTimeout
	case status >= 500:
		return ErrStudioServer
	default:
		// Unknown 3xx / 4xx — fall through to validation to make the
		// error actionable (3xx shouldn't reach this layer because the
		// HTTP client follows redirects).
		return ErrStudioValidation
	}
}

// sanitizeBody drops control characters and caps the excerpt length.
// Runs on every response body so log lines stay one-line-per-error.
func sanitizeBody(b []byte) string {
	const cap = 512
	if len(b) > cap {
		b = b[:cap]
	}
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			out = append(out, ' ')
		case c < 0x20 || c == 0x7F:
			// drop
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
