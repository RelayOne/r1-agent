// Package stokerr provides structured error types with consistent codes.
// Every error has a Code, Message, and optional Cause.
package stokerr

import (
	"errors"
	"fmt"
)

// Code classifies an error.
type Code string

const (
	ErrValidation     Code = "validation"
	ErrNotFound       Code = "not_found"
	ErrConflict       Code = "conflict"
	ErrAppendOnly     Code = "append_only_violation"
	ErrPermission     Code = "permission_denied"
	ErrBudgetExceeded Code = "budget_exceeded"
	ErrTimeout        Code = "timeout"
	ErrCrashRecovery  Code = "crash_recovery"
	ErrSchemaVersion  Code = "schema_version"
	ErrInternal       Code = "internal"
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
