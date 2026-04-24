package stokerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorMessage(t *testing.T) {
	e := New(ErrValidation, "bad input")
	got := e.Error()
	if !strings.Contains(got, "validation") || !strings.Contains(got, "bad input") {
		t.Fatalf("unexpected error string: %s", got)
	}
}

func TestErrorMessageWithCause(t *testing.T) {
	cause := fmt.Errorf("disk full")
	e := Wrap(ErrInternal, "write failed", cause)
	got := e.Error()
	if !strings.Contains(got, "disk full") {
		t.Fatalf("expected cause in message: %s", got)
	}
}

func TestUnwrap(t *testing.T) {
	cause := fmt.Errorf("root cause")
	e := Wrap(ErrTimeout, "timed out", cause)
	if !errors.Is(e.Unwrap(), cause) {
		t.Fatal("Unwrap did not return cause")
	}
}

func TestUnwrapNil(t *testing.T) {
	e := New(ErrNotFound, "missing")
	if e.Unwrap() != nil {
		t.Fatal("expected nil Unwrap")
	}
}

func TestIs(t *testing.T) {
	e := New(ErrConflict, "duplicate")
	target := New(ErrConflict, "other")
	if !errors.Is(e, target) {
		t.Fatal("expected Is to match same code")
	}
}

func TestIsNonMatch(t *testing.T) {
	e := New(ErrConflict, "dup")
	target := New(ErrNotFound, "missing")
	if errors.Is(e, target) {
		t.Fatal("expected Is not to match different codes")
	}
}

func TestHasCode(t *testing.T) {
	e := Wrap(ErrBudgetExceeded, "over limit", fmt.Errorf("inner"))
	wrapped := fmt.Errorf("outer: %w", e)
	if !HasCode(wrapped, ErrBudgetExceeded) {
		t.Fatal("HasCode should find code through wrapping")
	}
	if HasCode(wrapped, ErrTimeout) {
		t.Fatal("HasCode should not match wrong code")
	}
}

func TestFormattedConstructors(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		code Code
	}{
		{"Validationf", Validationf("field %s invalid", "name"), ErrValidation},
		{"NotFoundf", NotFoundf("item %d", 42), ErrNotFound},
		{"Conflictf", Conflictf("key %s", "x"), ErrConflict},
		{"AppendOnlyf", AppendOnlyf("cannot delete %s", "row"), ErrAppendOnly},
		{"Permissionf", Permissionf("denied for %s", "user"), ErrPermission},
		{"BudgetExceededf", BudgetExceededf("$%.2f over", 1.5), ErrBudgetExceeded},
		{"Timeoutf", Timeoutf("after %ds", 30), ErrTimeout},
		{"CrashRecoveryf", CrashRecoveryf("pid %d", 123), ErrCrashRecovery},
		{"SchemaVersionf", SchemaVersionf("v%d unsupported", 99), ErrSchemaVersion},
		{"Internalf", Internalf("unexpected %s", "state"), ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.code {
				t.Errorf("code: got %s, want %s", tc.err.Code, tc.code)
			}
			if tc.err.Message == "" {
				t.Error("message should not be empty")
			}
		})
	}
}
