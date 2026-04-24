// Package stokerr provides structured error types with consistent codes.
// Every error has a Code, Message, and optional Cause.
package stokerr

import (
	"errors"
	"fmt"
)

// Code classifies an error.
type Code string

// The error code set below is the Stoke-wide taxonomy: every structured
// error produced in the codebase should carry exactly one of these codes
// so downstream code (retry logic, HTTP status mapping, ledger audit
// emission) can switch on a stable value instead of string-matching.
const (
	// ErrValidation signals input that failed structural or semantic
	// validation (e.g., malformed IDs, missing required fields). Callers
	// should surface to the user; retrying without changes will not help.
	ErrValidation Code = "validation"
	// ErrNotFound signals a lookup miss for a resource the caller
	// addressed by ID. Distinct from ErrValidation: the request was
	// well-formed, just unsatisfiable.
	ErrNotFound Code = "not_found"
	// ErrConflict signals a concurrent-mutation collision or a state
	// precondition failure (e.g., write against a stale version).
	// Callers may retry after re-reading state.
	ErrConflict Code = "conflict"
	// ErrAppendOnly signals an attempt to mutate content-addressed or
	// append-only storage (ledger nodes, WAL entries). Never retryable:
	// the data model forbids the operation, not just the current state.
	ErrAppendOnly Code = "append_only_violation"
	// ErrPermission signals that an RBAC or sandbox policy blocked the
	// call. Not retryable without role/config changes.
	ErrPermission Code = "permission_denied"
	// ErrBudgetExceeded signals that the mission/task hit its cost or
	// token budget. Callers must either raise the budget or abort.
	ErrBudgetExceeded Code = "budget_exceeded"
	// ErrTimeout signals a deadline or process-timeout tripped. The
	// underlying work may or may not have completed; callers decide
	// whether the operation was idempotent and safe to retry.
	ErrTimeout Code = "timeout"
	// ErrCrashRecovery signals that state was restored from a crash
	// checkpoint and the caller may be observing partial or replayed
	// work. Used to gate "fresh state required" operations.
	ErrCrashRecovery Code = "crash_recovery"
	// ErrSchemaVersion signals a data-format version mismatch (on-disk
	// artifact newer/older than this binary supports). Requires a
	// migration or upgrade, not a retry.
	ErrSchemaVersion Code = "schema_version"
	// ErrInternal is the catch-all for unexpected invariants. Its
	// presence in a response indicates a bug and should be reported.
	ErrInternal Code = "internal"
)

// Error is a structured error carrying a classification code.
type Error struct {
	Code    Code
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause, supporting errors.Unwrap.
func (e *Error) Unwrap() error {
	return e.Cause
}

// Is reports whether target matches this error's code. It supports
// matching against another *Error by code.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// New creates an Error with the given code and message.
func New(code Code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Wrap creates an Error that wraps an underlying cause.
func Wrap(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, Cause: cause}
}

// Validationf creates a validation error with a formatted message.
func Validationf(format string, args ...any) *Error {
	return &Error{Code: ErrValidation, Message: fmt.Sprintf(format, args...)}
}

// NotFoundf creates a not-found error with a formatted message.
func NotFoundf(format string, args ...any) *Error {
	return &Error{Code: ErrNotFound, Message: fmt.Sprintf(format, args...)}
}

// Conflictf creates a conflict error with a formatted message.
func Conflictf(format string, args ...any) *Error {
	return &Error{Code: ErrConflict, Message: fmt.Sprintf(format, args...)}
}

// AppendOnlyf creates an append-only violation error with a formatted message.
func AppendOnlyf(format string, args ...any) *Error {
	return &Error{Code: ErrAppendOnly, Message: fmt.Sprintf(format, args...)}
}

// Permissionf creates a permission-denied error with a formatted message.
func Permissionf(format string, args ...any) *Error {
	return &Error{Code: ErrPermission, Message: fmt.Sprintf(format, args...)}
}

// BudgetExceededf creates a budget-exceeded error with a formatted message.
func BudgetExceededf(format string, args ...any) *Error {
	return &Error{Code: ErrBudgetExceeded, Message: fmt.Sprintf(format, args...)}
}

// Timeoutf creates a timeout error with a formatted message.
func Timeoutf(format string, args ...any) *Error {
	return &Error{Code: ErrTimeout, Message: fmt.Sprintf(format, args...)}
}

// CrashRecoveryf creates a crash-recovery error with a formatted message.
func CrashRecoveryf(format string, args ...any) *Error {
	return &Error{Code: ErrCrashRecovery, Message: fmt.Sprintf(format, args...)}
}

// SchemaVersionf creates a schema-version error with a formatted message.
func SchemaVersionf(format string, args ...any) *Error {
	return &Error{Code: ErrSchemaVersion, Message: fmt.Sprintf(format, args...)}
}

// Internalf creates an internal error with a formatted message.
func Internalf(format string, args ...any) *Error {
	return &Error{Code: ErrInternal, Message: fmt.Sprintf(format, args...)}
}

// HasCode reports whether err (or any error in its chain) has the given code.
func HasCode(err error, code Code) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}
