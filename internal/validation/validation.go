// Package validation provides input validation functions for use at public API
// boundaries. All validators return descriptive errors suitable for user display.
package validation

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NonEmpty validates that s is not empty after trimming whitespace.
// label is used in the error message (e.g., "task ID").
func NonEmpty(s, label string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	return nil
}

// Positive validates that n > 0.
func Positive(n int, label string) error {
	if n <= 0 {
		return fmt.Errorf("%s must be positive, got %d", label, n)
	}
	return nil
}

// PositiveFloat validates that f > 0.
func PositiveFloat(f float64, label string) error {
	if f <= 0 {
		return fmt.Errorf("%s must be positive, got %f", label, f)
	}
	return nil
}

// InRange validates that n is within [min, max] inclusive.
func InRange(n, min, max int, label string) error {
	if n < min || n > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", label, min, max, n)
	}
	return nil
}

// PositiveDuration validates that d is positive and not zero.
func PositiveDuration(d time.Duration, label string) error {
	if d <= 0 {
		return fmt.Errorf("%s must be a positive duration, got %v", label, d)
	}
	return nil
}

// OneOf validates that s is one of the allowed values.
func OneOf(s string, allowed []string, label string) error {
	for _, a := range allowed {
		if s == a {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %v, got %q", label, allowed, s)
}

// ValidURL validates that s is a parseable URL with a scheme and host.
func ValidURL(s, label string) error {
	if err := NonEmpty(s, label); err != nil {
		return err
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", label, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must include scheme and host, got %q", label, s)
	}
	return nil
}

// SafeID validates that s contains only safe characters for use in file paths
// and identifiers: alphanumeric, dash, underscore, dot.
func SafeID(s, label string) error {
	if err := NonEmpty(s, label); err != nil {
		return err
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("%s contains invalid character %q", label, string(c))
		}
	}
	return nil
}

// All runs multiple validation checks and returns the first error, or nil if all pass.
func All(checks ...error) error {
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}
