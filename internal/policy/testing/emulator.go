// Package policytesting provides an httptest-based fake cedar-agent
// server used by integration tests that exercise the policy
// package's HTTPClient against a controllable PARC endpoint.
//
// The package is deliberately named policytesting (and not testing)
// because Go reserves the short name "testing" for the stdlib unit
// test package. Importers typically alias this package as
// policytesting "github.com/.../internal/policy/testing".
//
// The emulator is intentionally minimal: it only implements the
// handful of PARC request/response fields Stoke's HTTPClient
// exchanges with cedar-agent. It is NOT a conformance-grade
// cedar-agent replacement.
package policytesting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// Rule is a single (action, resource, decision) fixture used to
// steer the emulator's responses. Rules are evaluated in order and
// the FIRST match wins. An empty Action or Resource field acts as
// a wildcard that matches any value from the incoming PARC request.
//
// Decision must be either "Allow" or "Deny" — the emulator echoes
// it verbatim on the wire so HTTPClient maps it back to the
// corresponding policy.Decision value.
//
// Reason is copied into the response's diagnostics.reason array so
// tests can assert on matched-rule provenance.
type Rule struct {
	Action   string // exact match; empty = wildcard
	Resource string // exact match; empty = wildcard
	Decision string // "Allow" or "Deny"
	Reason   string // copied into diagnostics.reason
}

// parcRequest mirrors the on-wire PARC request shape produced by
// policy.HTTPClient. Only Action and Resource participate in rule
// matching — Principal, Context, and Entities are accepted and
// ignored so integration tests can exercise the full request path
// without the emulator caring about payload contents.
type parcRequest struct {
	Principal string         `json:"principal"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Context   map[string]any `json:"context"`
	Entities  []any          `json:"entities"`
}

// parcDiagnostics is the nested diagnostics object returned to the
// cedar-agent HTTP client — reason holds matched policy IDs (or
// the canned Rule.Reason), errors is always an empty slice in the
// emulator.
type parcDiagnostics struct {
	Reason []string `json:"reason"`
	Errors []string `json:"errors"`
}

// parcResponse is the on-wire response body the emulator returns.
type parcResponse struct {
	Decision    string          `json:"decision"`
	Diagnostics parcDiagnostics `json:"diagnostics"`
}

// NewServer returns an *httptest.Server that speaks just enough of
// the cedar-agent PARC protocol to drive Stoke's policy.HTTPClient
// in integration tests.
//
// It accepts POST requests on /v1/is_authorized only; any other
// path or method returns 404. The request body is parsed as a PARC
// request; the emulator walks rules in order and returns the first
// whose Action and Resource match (empty = wildcard). When no rule
// matches, the emulator returns:
//
//	{"decision":"Deny","diagnostics":{"reason":["no-match"],"errors":[]}}
//
// The caller owns the returned server and MUST Close() it when the
// test finishes to release the underlying net.Listener.
func NewServer(rules []Rule) *httptest.Server {
	// Copy rules so later mutation by the caller cannot race with
	// handler goroutines servicing concurrent requests.
	local := make([]Rule, len(rules))
	copy(local, rules)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/is_authorized", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		var req parcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Malformed body — fail closed with an explicit
			// diagnostics error so the calling test can
			// distinguish a decode failure from a clean no-match.
			writeJSON(w, parcResponse{
				Decision: "Deny",
				Diagnostics: parcDiagnostics{
					Reason: []string{"decode-error"},
					Errors: []string{err.Error()},
				},
			})
			return
		}

		for _, rule := range local {
			if !matches(rule.Action, req.Action) {
				continue
			}
			if !matches(rule.Resource, req.Resource) {
				continue
			}
			writeJSON(w, parcResponse{
				Decision: rule.Decision,
				Diagnostics: parcDiagnostics{
					Reason: []string{rule.Reason},
					Errors: []string{},
				},
			})
			return
		}

		// No rule matched — canonical fail-closed response so
		// tests that forget to specify a catch-all rule still
		// observe a well-formed Deny.
		writeJSON(w, parcResponse{
			Decision: "Deny",
			Diagnostics: parcDiagnostics{
				Reason: []string{"no-match"},
				Errors: []string{},
			},
		})
	})

	return httptest.NewServer(mux)
}

// matches returns true when the rule's pattern matches the request
// value. An empty pattern is a wildcard.
func matches(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	return pattern == value
}

// writeJSON writes v as a JSON body with status 200. Any encode
// error is silently swallowed — by the time encoding fails the
// response headers are already on the wire and the test will
// observe a truncated body, which is the desired behaviour.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
