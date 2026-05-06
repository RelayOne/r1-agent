// errors.go: typed error taxonomy for the browser package.
//
// Five structured errors cover the failure modes that matter for
// descent's classification loop (see BuildEnvFixFunc): transient
// network/launch problems get retried, selector-permanent misses do
// not. All errors carry context fields so operators can read the
// root cause without digging through a wrapped chain.
//
// errors.As/errors.Is both work via the standard Unwrap() method.

package browser

import (
	"errors"
	"fmt"
)

// ElementNotFoundError is returned when a click/type/extract action
// cannot locate its CSS selector. Considered a permanent failure —
// selector is stale or wrong, no amount of retry will help.
type ElementNotFoundError struct {
	Selector string
	Cause    error
}

func (e *ElementNotFoundError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("browser: element not found: selector=%q: %v", e.Selector, e.Cause)
	}
	return fmt.Sprintf("browser: element not found: selector=%q", e.Selector)
}

func (e *ElementNotFoundError) Unwrap() error { return e.Cause }

// NavigationFailedError fires when Navigate() itself errors out (DNS,
// protocol, TLS, connection refused). The underlying cause is kept
// in Cause for env-fix classification.
type NavigationFailedError struct {
	URL   string
	Cause error
}

func (e *NavigationFailedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("browser: navigation failed: url=%q: %v", e.URL, e.Cause)
	}
	return fmt.Sprintf("browser: navigation failed: url=%q", e.URL)
}

func (e *NavigationFailedError) Unwrap() error { return e.Cause }

// ActionTimeoutError fires when an action exceeds its configured
// per-action timeout (default 10s for waits, 30s for navigate).
// Kind is the failing action kind string for observability.
type ActionTimeoutError struct {
	Kind     string
	Selector string
	Cause    error
}

func (e *ActionTimeoutError) Error() string {
	if e.Selector != "" {
		return fmt.Sprintf("browser: action timeout: kind=%s selector=%q", e.Kind, e.Selector)
	}
	return fmt.Sprintf("browser: action timeout: kind=%s", e.Kind)
}

func (e *ActionTimeoutError) Unwrap() error { return e.Cause }

// ChromeLaunchFailedError fires when the launcher cannot start a
// headless Chromium. Transient — env-fix returns true so descent
// retries (subsequent launch may succeed if Chrome just-downloaded).
// Also used by the no-tag stub path to signal "rod not compiled in."
type ChromeLaunchFailedError struct {
	Cause error
}

func (e *ChromeLaunchFailedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("browser: chrome launch failed: %v", e.Cause)
	}
	return "browser: chrome launch failed"
}

func (e *ChromeLaunchFailedError) Unwrap() error { return e.Cause }

// InteractiveUnsupportedError fires when the stdlib Client is asked to
// run an action that requires a real browser (click / type / wait /
// screenshot). Tells the caller to construct a RodClient instead.
type InteractiveUnsupportedError struct {
	Kind ActionKind
}

func (e *InteractiveUnsupportedError) Error() string {
	return fmt.Sprintf("browser: action %q requires the stoke_rod build tag; "+
		"rebuild with 'go build -tags stoke_rod ./cmd/r1' or construct a RodClient", e.Kind)
}

// IsTransient returns true when the error is one the env-fix
// classifier should retry on (launch failures, timeouts, navigation
// network errors). ElementNotFound is permanent → returns false.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var launch *ChromeLaunchFailedError
	if errors.As(err, &launch) {
		return true
	}
	var tmo *ActionTimeoutError
	if errors.As(err, &tmo) {
		return tmo.Kind != string(ActionExtractText) && tmo.Kind != string(ActionExtractAttribute)
	}
	var nav *NavigationFailedError
	if errors.As(err, &nav) {
		return true
	}
	// ElementNotFound + InteractiveUnsupported are permanent.
	return false
}
