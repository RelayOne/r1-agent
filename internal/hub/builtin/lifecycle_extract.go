package builtin

import "github.com/RelayOne/r1/internal/hub"

// extractLifecycleUserID pulls a user_id from a bus event. Tries (in
// order): top-level UserID field if present on the event struct,
// then Custom["user_id"], then Custom["sub"] (JWT subject). Returns
// "" when none is present — the caller treats that as anonymous and
// drops the event.
func extractLifecycleUserID(e *hub.Event) string {
	if e == nil {
		return ""
	}
	if e.Custom != nil {
		if v, ok := e.Custom["user_id"].(string); ok && v != "" {
			return v
		}
		if v, ok := e.Custom["sub"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// extractLifecycleTenantID pulls a tenant_id from a bus event. Mirrors
// extractLifecycleUserID. Returns "" for sessions without a tenant
// (single-tenant local dev) — Customer.io is happy with empty tenant
// IDs and the flagstore PK handles empty strings.
func extractLifecycleTenantID(e *hub.Event) string {
	if e == nil {
		return ""
	}
	if e.Custom != nil {
		if v, ok := e.Custom["tenant_id"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// propsFromEvent builds the Customer.io track props for a generic bus
// event. We carry session_id, mission_id, task_id, and cost_usd when
// present. Raw content (file paths, prompts, mission titles) is NOT
// emitted — the PII allowlist contract from traits.Build applies to
// track props too.
func propsFromEvent(e *hub.Event) map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{}
	if e.Custom != nil {
		if v, ok := e.Custom["session_id"].(string); ok && v != "" {
			out["session_id"] = v
		}
		if v, ok := e.Custom["mission_id"].(string); ok && v != "" {
			out["mission_id"] = v
		}
		if v, ok := e.Custom["task_id"].(string); ok && v != "" {
			out["task_id"] = v
		}
		if v, ok := e.Custom["cost_usd"].(float64); ok && v != 0 {
			out["cost_usd"] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
