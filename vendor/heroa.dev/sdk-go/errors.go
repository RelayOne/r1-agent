package heroa

import "fmt"

// HeroaError wraps a non-2xx control-plane response. Every Client.Deploy
// error has Code set and (except for network-layer 0 statuses) a non-zero
// Status. RequestID is populated when the control plane emits one so
// customer support triage can chase the exact request.
type HeroaError struct {
	Code      ErrorCode
	Message   string
	Status    int
	RequestID string
	Details   map[string]string
}

// Error implements the error interface with a format the CLI / logs can
// round-trip cleanly.
func (e *HeroaError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("heroa: %s (status=%d code=%s request_id=%s)",
			e.Message, e.Status, e.Code, e.RequestID)
	}
	return fmt.Sprintf("heroa: %s (status=%d code=%s)", e.Message, e.Status, e.Code)
}

// IsRegionNotAllowed is a type-switch helper — the caller doesn't have to
// import the ErrorCode constant to branch on the common case.
func IsRegionNotAllowed(err error) bool { return codeIs(err, ErrCodeRegionNotAllowed) }

// IsRegionCapacity returns true for ErrCodeRegionCapacity errors.
func IsRegionCapacity(err error) bool { return codeIs(err, ErrCodeRegionCapacity) }

// IsAuth returns true for ErrCodeAuth errors.
func IsAuth(err error) bool { return codeIs(err, ErrCodeAuth) }

// IsValidation returns true for ErrCodeValidation errors.
func IsValidation(err error) bool { return codeIs(err, ErrCodeValidation) }

// IsQuotaExceeded returns true for ErrCodeQuotaExceeded errors.
func IsQuotaExceeded(err error) bool { return codeIs(err, ErrCodeQuotaExceeded) }

// IsPlacementFailed returns true for ErrCodePlacementFailed errors.
func IsPlacementFailed(err error) bool { return codeIs(err, ErrCodePlacementFailed) }

// IsIdempotencyConflict returns true for ErrCodeIdempotencyConflict errors.
func IsIdempotencyConflict(err error) bool { return codeIs(err, ErrCodeIdempotencyConflict) }

// IsTemplateNotFound returns true for ErrCodeTemplateNotFound errors.
func IsTemplateNotFound(err error) bool { return codeIs(err, ErrCodeTemplateNotFound) }

// IsTemplateRegionExcluded returns true for ErrCodeTemplateRegionExcluded.
func IsTemplateRegionExcluded(err error) bool { return codeIs(err, ErrCodeTemplateRegionExcluded) }

// IsInternal returns true for ErrCodeInternal errors.
func IsInternal(err error) bool { return codeIs(err, ErrCodeInternal) }

func codeIs(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*HeroaError); ok {
		return he.Code == code
	}
	return false
}
