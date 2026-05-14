// Package analytics ships R1's product analytics integration with PostHog.
//
// This file is the A4-coordination shim. Spec B1 (PostHog analytics,
// BUILD_ORDER 38) depends on A4 (RelayOne SSO, BUILD_ORDER 36) for the
// tenant identity that drives PostHog Group Analytics. The two specs
// are built in parallel — A4 introduces an authenticated session that
// stamps a TenantID onto the request correlation context; B1 reads
// that TenantID and binds it to every captured event.
//
// To avoid blocking B1 on A4, this shim provides the *single* place
// the analytics package extracts a tenant identifier from a context:
//
//   - When A4 is merged and a tenant-bound session is in flight,
//     correlation.FromContext(ctx).TenantID is non-empty and the
//     analytics subscriber attaches it as both a top-level event
//     property AND a PostHog Groups["tenant"] binding.
//
//   - When A4 has NOT yet merged (or a session has no tenant), the
//     field is the empty string. The analytics subscriber treats an
//     empty TenantID as "no group binding" and emits the event with
//     no tenant attached — matching the spec §7 mechanism (3) rule.
//
// The shim is the seam: nothing else in B1 needs to change once A4
// merges; tenant tagging activates automatically the first time a
// tenant-bound request flows through.
package analytics

import (
	"context"

	"github.com/RelayOne/r1/internal/correlation"
)

// TenantIDFromContext returns the tenant identifier currently bound to
// ctx, or the empty string when no tenant has been resolved. An empty
// return value MUST be interpreted by callers as "do not attach a
// PostHog Groups{tenant} binding" — never as "missing data". The
// distinction matters because a partial tenant binding (group present
// but key empty) is rejected server-side by PostHog with a 400.
//
// The function is deliberately the only direct reader of correlation
// state inside the analytics package. Centralising the read makes the
// A4 cutover a single-line change and gives the build a single test
// hook for the "tenant not yet wired" case.
func TenantIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return correlation.FromContext(ctx).TenantID
}

// WithTenantID is the inverse of TenantIDFromContext: it returns a ctx
// whose correlation IDs carry tenantID, preserving any other IDs that
// were already bound. An empty tenantID returns ctx unchanged.
//
// This is the helper the bus subscriber uses to "hydrate" a tenant
// onto a delivery context when the per-mission lookup found one for an
// event whose inbound ctx had no tenant bound.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tenantID == "" {
		return ctx
	}
	ids := correlation.FromContext(ctx)
	ids.TenantID = tenantID
	return correlation.WithIDs(ctx, ids)
}
