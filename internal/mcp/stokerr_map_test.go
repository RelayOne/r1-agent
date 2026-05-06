package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/RelayOne/r1/internal/stokerr"
)

func TestMapErrorToTaxonomy_NilReturnsEmpty(t *testing.T) {
	code, msg := MapErrorToTaxonomy(nil)
	if code != "" || msg != "" {
		t.Errorf("nil err: got code=%q msg=%q, want empty pair", code, msg)
	}
}

func TestMapErrorToTaxonomy_StokeerrPassthrough(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"validation", stokerr.Validationf("workdir is required"), "validation"},
		{"not_found", stokerr.NotFoundf("session s-1 not found"), "not_found"},
		{"conflict", stokerr.Conflictf("stale version"), "conflict"},
		{"permission", stokerr.Permissionf("blocked by policy"), "permission_denied"},
		{"timeout", stokerr.Timeoutf("deadline"), "timeout"},
		{"budget", stokerr.BudgetExceededf("cost cap"), "budget_exceeded"},
		{"crash", stokerr.CrashRecoveryf("replay"), "crash_recovery"},
		{"schema", stokerr.SchemaVersionf("version drift"), "schema_version"},
		{"appendonly", stokerr.AppendOnlyf("ledger frozen"), "append_only_violation"},
		{"internal", stokerr.Internalf("invariant"), "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := MapErrorToTaxonomy(tc.err)
			if code != tc.want {
				t.Errorf("got %q, want %q", code, tc.want)
			}
		})
	}
}

func TestMapErrorToTaxonomy_StokeerrWrappedInFmtErrorf(t *testing.T) {
	inner := stokerr.NotFoundf("lane l-9 missing")
	wrapped := fmt.Errorf("kill failed: %w", inner)
	code, _ := MapErrorToTaxonomy(wrapped)
	if code != "not_found" {
		t.Errorf("wrapped stokerr should still resolve via errors.As; got %q", code)
	}
}

func TestMapErrorToTaxonomy_ContextDeadlineMapsTimeout(t *testing.T) {
	code, _ := MapErrorToTaxonomy(context.DeadlineExceeded)
	if code != "timeout" {
		t.Errorf("context.DeadlineExceeded -> %q, want timeout", code)
	}
}

func TestMapErrorToTaxonomy_ContextCanceledMapsTimeout(t *testing.T) {
	code, _ := MapErrorToTaxonomy(context.Canceled)
	if code != "timeout" {
		t.Errorf("context.Canceled -> %q, want timeout", code)
	}
}

func TestMapErrorToTaxonomy_FsErrNotExistMapsNotFound(t *testing.T) {
	code, _ := MapErrorToTaxonomy(fs.ErrNotExist)
	if code != "not_found" {
		t.Errorf("fs.ErrNotExist -> %q, want not_found", code)
	}
}

func TestMapErrorToTaxonomy_OsErrPermissionMapsPermission(t *testing.T) {
	code, _ := MapErrorToTaxonomy(os.ErrPermission)
	if code != "permission_denied" {
		t.Errorf("os.ErrPermission -> %q, want permission_denied", code)
	}
}

func TestMapErrorToTaxonomy_StringHeuristic_RequiredField(t *testing.T) {
	err := errors.New("session_id is required")
	code, _ := MapErrorToTaxonomy(err)
	if code != "validation" {
		t.Errorf("required-field heuristic: got %q, want validation", code)
	}
}

func TestMapErrorToTaxonomy_StringHeuristic_NotFound(t *testing.T) {
	err := errors.New("mission m-9 not found")
	code, _ := MapErrorToTaxonomy(err)
	if code != "not_found" {
		t.Errorf("not-found heuristic: got %q, want not_found", code)
	}
}

func TestMapErrorToTaxonomy_UnknownErrorFallsBackToInternal(t *testing.T) {
	err := errors.New("ozymandias king of kings")
	code, _ := MapErrorToTaxonomy(err)
	if code != "internal" {
		t.Errorf("unknown err -> %q, want internal", code)
	}
}

func TestEnvelopeFromError_PopulatesCodeAndMessage(t *testing.T) {
	env := EnvelopeFromError("r1.session.cancel",
		stokerr.NotFoundf("session s-9 not found"))
	if env.OK {
		t.Fatal("EnvelopeFromError must set OK=false")
	}
	if env.ErrorCode != "not_found" {
		t.Errorf("ErrorCode = %q, want not_found", env.ErrorCode)
	}
	if env.Links == nil || env.Links.Self != "r1.session.cancel" {
		t.Errorf("Self link missing; got %+v", env.Links)
	}
}

func TestEnvelopeFromError_NilFallsBackToInternal(t *testing.T) {
	env := EnvelopeFromError("r1.session.cancel", nil)
	if env.OK {
		t.Fatal("EnvelopeFromError(nil) is still an error envelope (sentinel)")
	}
	if env.ErrorCode != "internal" {
		t.Errorf("nil err -> %q, want internal fallback", env.ErrorCode)
	}
}
