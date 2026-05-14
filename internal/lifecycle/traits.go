package lifecycle

import (
	"sort"
	"strings"
	"time"
)

// TraitsAllowlist is the exhaustive set of keys permitted on any
// Identify payload sent to Customer.io. The list is intentionally
// short — five keys, the email + four pieces of non-PII workspace
// metadata — and is enforced by traits_test.go via reflection. Adding
// a new key requires (a) editing this slice, (b) updating the test,
// and (c) re-reading §9 of the spec to confirm the new field is not
// PII.
//
// The order of the slice is alphabetical so the test's diff is stable
// when a key is added or removed.
var TraitsAllowlist = []string{
	"email",
	"plan_tier",
	"signup_date",
	"signup_source",
	"tenant_id",
}

// traitKey is a typed string used as the only permitted map key inside
// the lifecycle package. Callers outside the package cannot fabricate a
// new key — the Build function below is the only function permitted to
// populate identify traits, per §6.6 of the spec.
type traitKey string

const (
	traitEmail        traitKey = "email"
	traitTenantID     traitKey = "tenant_id"
	traitPlanTier     traitKey = "plan_tier"
	traitSignupDate   traitKey = "signup_date"
	traitSignupSource traitKey = "signup_source"
)

// Build constructs the Customer.io trait map for a single user. The
// function is the only sanctioned producer of identify-trait maps in
// the lifecycle package; the traits_test.go reflection test asserts
// the output's key set is exactly TraitsAllowlist.
//
// Empty values are dropped (Customer.io rejects empty-string trait
// values with a 400). signupDate is rendered as RFC3339 in UTC.
//
// signupSource is the high-level provenance of the user record:
// "sso" for a RelayOne SSO login, "self-serve" for a self-service
// signup, "import" for a backfilled legacy user, empty otherwise.
func Build(email, tenantID, planTier, signupSource string, signupDate time.Time) map[string]any {
	out := make(map[string]any, len(TraitsAllowlist))
	if v := strings.TrimSpace(email); v != "" {
		out[string(traitEmail)] = v
	}
	if v := strings.TrimSpace(tenantID); v != "" {
		out[string(traitTenantID)] = v
	}
	if v := strings.TrimSpace(planTier); v != "" {
		out[string(traitPlanTier)] = v
	}
	if !signupDate.IsZero() {
		out[string(traitSignupDate)] = signupDate.UTC().Format(time.RFC3339)
	}
	if v := strings.TrimSpace(signupSource); v != "" {
		out[string(traitSignupSource)] = v
	}
	return out
}

// SortedAllowlist returns a defensive copy of TraitsAllowlist in
// alphabetical order. Used by traits_test.go to compare against the
// reflected key set.
func SortedAllowlist() []string {
	out := make([]string, len(TraitsAllowlist))
	copy(out, TraitsAllowlist)
	sort.Strings(out)
	return out
}

// IsAllowedTrait reports whether the given key is on TraitsAllowlist.
// Used by the bus subscriber to assert at compile-time (via a unit
// test) that no new trait key has leaked outside the allowlist.
func IsAllowedTrait(key string) bool {
	for _, k := range TraitsAllowlist {
		if k == key {
			return true
		}
	}
	return false
}
