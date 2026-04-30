package studioclient

import (
	"context"
	"encoding/json"
	"time"
)

// Transport is the pluggable interface every studio transport
// satisfies. Skills call Invoke(ctx, tool, input); the resolver picks
// HTTP or stdio-MCP per studio_config.
//
// Contract:
//
//   - tool is the R1 skill name, e.g. "studio.scaffold_site". The
//     transport maps it to the underlying Studio tool / endpoint.
//   - input is any JSON-serializable value. Implementations must
//     NOT mutate it.
//   - The returned json.RawMessage is the Studio response body
//     verbatim (the caller validates against the skill's OutputSchema).
//   - On failure the error is always a *StudioError wrapping one of
//     the sentinel causes in errors.go.
//   - ctx is respected for cancellation. Per-call timeouts live in the
//     transport's own config (work order §R1S-2.6).
type Transport interface {
	Invoke(ctx context.Context, tool string, input any) (json.RawMessage, error)

	// Name returns "http" or "stdio-mcp" for logging / diagnostics.
	Name() string

	// Close releases long-lived resources (subprocesses, keep-alive
	// connections). Safe to call multiple times; idempotent.
	Close() error
}

// EventPublisher is the minimal surface studioclient calls after every
// invocation. Implementations forward to hub.Bus or any equivalent
// telemetry sink. nil is always a valid value — the transport checks
// before emitting.
type EventPublisher interface {
	Publish(InvocationEvent)
}

// InvocationEvent is the observability record emitted per skill call.
// No PII, no payload, no credential material — only fields safe to
// write to a shared log sink.
type InvocationEvent struct {
	// Transport identifies the source, e.g. "http" or "stdio-mcp".
	Transport string

	// Tool is the R1 skill name.
	Tool string

	// Status is the HTTP status code for HTTP transports; 0 for stdio.
	Status int

	// Duration is the wall-clock time the call took.
	Duration time.Duration

	// OK is true on 2xx (HTTP) or successful RPC response (stdio).
	OK bool

	// ErrorKind is the sentinel string ("auth", "scope", "unavailable",
	// ...) when OK is false. Empty on success.
	ErrorKind string
}

// eventPublisherFunc adapts a plain function to EventPublisher. Used
// by tests and by callers who don't want a full type.
type eventPublisherFunc func(InvocationEvent)

func (f eventPublisherFunc) Publish(ev InvocationEvent) { f(ev) }

// PublisherFunc wraps a function as an EventPublisher. Handy for tests.
func PublisherFunc(f func(InvocationEvent)) EventPublisher { return eventPublisherFunc(f) }

// errorKind returns the sentinel-to-string label used in telemetry.
// Kept in this file because both transports emit it.
func errorKind(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case isCause(err, ErrStudioAuth):
		return "auth"
	case isCause(err, ErrStudioScope):
		return "scope"
	case isCause(err, ErrStudioNotFound):
		return "not_found"
	case isCause(err, ErrStudioValidation):
		return "validation"
	case isCause(err, ErrStudioTimeout):
		return "timeout"
	case isCause(err, ErrStudioServer):
		return "server"
	case isCause(err, ErrStudioDisabled):
		return "disabled"
	case isCause(err, ErrStudioUnavailable):
		return "unavailable"
	default:
		return "unknown"
	}
}

// isCause is a thin wrapper around errors.Is that is cheap to inline
// without pulling in the errors package at every call site.
func isCause(err, target error) bool {
	// This indirection keeps the public API free of a stdlib-dependent
	// signature while letting us adjust classification later.
	se, ok := err.(*StudioError)
	if !ok {
		return false
	}
	return se.Cause == target
}
