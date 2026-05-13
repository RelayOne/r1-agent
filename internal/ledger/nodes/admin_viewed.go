package nodes

// admin_viewed.go — A5 admin panel audit trail.
//
// Every authorized page view of /admin/* and /api/admin/* emits one
// AdminViewed node so the audit panel can answer "who looked at what
// and when". Stored fields are deliberately small — no payload, just
// the metadata operators need to correlate a view against an incident.
//
// Spec: specs/admin-panel.md §4.1 and §10 task 3.

import (
	"fmt"
	"time"
)

// AdminViewed records an authenticated admin's page view. RemoteAddr
// is redacted to a /24 prefix before persistence so the ledger never
// captures the full client IP.
//
// ID prefix: adv-
type AdminViewed struct {
	// Path is the admin route the user hit, e.g. "/admin/sessions".
	// Always a server-side string (never user-controlled body data).
	Path string `json:"path"`

	// User is the SSO subject claim — typically an email address.
	User string `json:"user"`

	// Tenant is the tenant slug from the JWT's tenant_id claim. Empty
	// when the token carries no tenant binding (org-admin scope).
	Tenant string `json:"tenant,omitempty"`

	// RemoteAddr is the truncated client IP (/24 IPv4 or /48 IPv6) —
	// see internal/server/admin_middleware.go::redactRemoteAddr.
	RemoteAddr string `json:"remote_addr,omitempty"`

	// Method is "GET" for every Phase-1 route; recorded explicitly so
	// the audit view can show shape changes once Phase 2 adds mutation
	// surfaces.
	Method string `json:"method"`

	// Status is the final HTTP status code emitted for this view. The
	// emit-on-view hook captures this after the handler runs so 200s,
	// 404s, and 500s all land in the ledger with the right code.
	Status int `json:"status"`

	// Timestamp is the server clock at emission time. Tests inject a
	// fixed clock so golden snapshots are stable.
	Timestamp time.Time `json:"timestamp"`

	Version int `json:"schema_version"`
}

// NodeType returns the registry key. Stable string — changing it would
// invalidate every historical AdminViewed row in the ledger.
func (a *AdminViewed) NodeType() string { return "admin_viewed" }

// SchemaVersion exposes the embedded version number. A future schema
// migration would bump this and update Validate accordingly.
func (a *AdminViewed) SchemaVersion() int { return a.Version }

// Validate enforces the required-field invariants. Path, User, Method,
// Status, and Timestamp are all required; Tenant and RemoteAddr are
// optional. An unset Status (zero) is rejected because the emit-on-view
// hook always captures the status before persisting.
func (a *AdminViewed) Validate() error {
	if a.Path == "" {
		return fmt.Errorf("admin_viewed: path is required")
	}
	if a.User == "" {
		return fmt.Errorf("admin_viewed: user is required")
	}
	if a.Method == "" {
		return fmt.Errorf("admin_viewed: method is required")
	}
	if a.Status == 0 {
		return fmt.Errorf("admin_viewed: status is required")
	}
	if a.Timestamp.IsZero() {
		return fmt.Errorf("admin_viewed: timestamp is required")
	}
	return nil
}

func init() {
	Register("admin_viewed", func() NodeTyper { return &AdminViewed{Version: 1} })
}
