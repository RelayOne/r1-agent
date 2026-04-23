package provider

import "net/http"

// ReadTierHeaders extracts RelayGate's model-routing response headers
// (X-Model-Tier and X-Model-Resolved) from resp and surfaces them via
// the provided log function.
//
// RelayGate emits these on every /v1/messages and /v1/chat/completions
// response when tier aliases ("tier:reasoning", "smart", etc.) are
// resolved upstream. Consuming them here gives Stoke richer observability
// for cost / routing without needing to re-derive the resolution locally.
//
// The helper is intentionally log-only: each provider's response-handler
// calls it after a successful HTTP exchange. When neither header is
// present (standalone runs, non-RelayGate endpoints), it emits nothing.
// The log parameter may be nil to disable output entirely — useful in
// tests that only want to assert behavior.
//
// This implements AL-2 from specs/work-stoke-alignment.md.
func ReadTierHeaders(resp *http.Response, modelAlias string, log func(format string, args ...any)) {
	if resp == nil || log == nil {
		return
	}
	if tier := resp.Header.Get("X-Model-Tier"); tier != "" {
		log("model tier resolved: %s", tier)
	}
	if resolved := resp.Header.Get("X-Model-Resolved"); resolved != "" {
		log("model resolved: alias=%s resolved=%s", modelAlias, resolved)
	}
}
