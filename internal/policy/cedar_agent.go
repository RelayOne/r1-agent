package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClientDefaultTimeout is the default timeout applied to the
// underlying *http.Client. Per spec §"Library Preferences" the
// cedar-agent round-trip must complete within 2 seconds or fail
// closed.
const httpClientDefaultTimeout = 2 * time.Second

// HTTPClient is the cedar-agent HTTP sidecar backend. It speaks
// the PARC JSON protocol to a cedar-agent process reachable at
// endpoint. The token is sent as a Bearer credential when non-empty.
//
// PARC request body sent to POST <endpoint>/v1/is_authorized,
// verbatim from the policy-engine spec §"API Endpoints":
//
//	{
//	  "principal": "User::\"alice\"",
//	  "action":    "Action::\"bash.exec\"",
//	  "resource":  "Tool::\"bash\"",
//	  "context": {
//	    "command": "rm -rf /tmp/x",
//	    "trust_level": 3,
//	    "budget_remaining_usd": 4.12,
//	    "worktree": "wt-42"
//	  },
//	  "entities": []
//	}
//
// Stoke v1 always sends entities as an empty array; entity
// hierarchy is reserved for future work.
//
// Response body is parsed from:
//
//	{
//	  "decision": "Allow",
//	  "diagnostics": { "reason": ["policy0"], "errors": [] }
//	}
//
// Any transport error, non-2xx status, or malformed body yields a
// fail-closed Result (DecisionDeny) plus a wrapped
// ErrPolicyUnavailable so callers can uniformly short-circuit. No
// retries are attempted — a down backend must not mask as
// "engine absent" (see spec §"Fail-Closed Rationale").
type HTTPClient struct {
	endpoint string
	token    string
	hc       *http.Client
}

// parcBody is the on-wire PARC request shape. Field ordering is
// set by the struct literal in Check; JSON key names match the
// cedar-agent contract verbatim.
type parcBody struct {
	Principal string         `json:"principal"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Context   map[string]any `json:"context"`
	Entities  []any          `json:"entities"`
}

// parcDiagnostics is the nested diagnostics object returned by
// cedar-agent — reason holds matched policy IDs, errors holds
// evaluation errors.
type parcDiagnostics struct {
	Reason []string `json:"reason"`
	Errors []string `json:"errors"`
}

// parcResponse is the on-wire cedar-agent response body.
type parcResponse struct {
	Decision    string          `json:"decision"`
	Diagnostics parcDiagnostics `json:"diagnostics"`
}

// NewHTTPClient constructs an HTTPClient targeting endpoint with
// an optional Bearer token. Endpoint must be non-empty. The
// returned *HTTPClient uses a stdlib *http.Client with a 2s
// Timeout — the same deadline applies to connection, TLS
// handshake, request write, and response read combined.
func NewHTTPClient(endpoint, token string) (*HTTPClient, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("policy: HTTPClient endpoint must be non-empty")
	}
	return &HTTPClient{
		endpoint: endpoint,
		token:    token,
		hc:       &http.Client{Timeout: httpClientDefaultTimeout},
	}, nil
}

// Check performs a single PARC authorization query against the
// cedar-agent backend. On any transport failure, non-2xx status,
// or malformed response body it returns a fail-closed
// Result{DecisionDeny, Errors:[...]} AND a wrapped
// ErrPolicyUnavailable. Zero retries are attempted — per spec
// §"Error Handling" masking network faults as allow would defeat
// the control.
//
// The returned error is always non-nil when Result.Decision ==
// DecisionDeny due to a backend fault; callers should treat any
// non-nil error as fail-closed without inspecting its kind.
func (c *HTTPClient) Check(ctx context.Context, req Request) (Result, error) {
	body := parcBody{
		Principal: req.Principal,
		Action:    req.Action,
		Resource:  req.Resource,
		Context:   req.Context,
		Entities:  []any{},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return c.unavailable(fmt.Sprintf("marshal PARC body: %v", err)), fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}

	url := strings.TrimRight(c.endpoint, "/") + "/v1/is_authorized"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return c.unavailable(fmt.Sprintf("build request: %v", err)), fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return c.unavailable(fmt.Sprintf("transport: %v", err)), fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("status %d", resp.StatusCode)
		return c.unavailable(msg), fmt.Errorf("%w: %s", ErrPolicyUnavailable, msg)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.unavailable(fmt.Sprintf("read body: %v", err)), fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}

	var parsed parcResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return c.unavailable(fmt.Sprintf("decode body: %v", err)), fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}

	switch parsed.Decision {
	case "Allow":
		return Result{
			Decision: DecisionAllow,
			Reasons:  parsed.Diagnostics.Reason,
			Errors:   parsed.Diagnostics.Errors,
		}, nil
	case "Deny":
		return Result{
			Decision: DecisionDeny,
			Reasons:  parsed.Diagnostics.Reason,
			Errors:   parsed.Diagnostics.Errors,
		}, nil
	default:
		msg := fmt.Sprintf("unknown decision %q", parsed.Decision)
		return c.unavailable(msg), fmt.Errorf("%w: %s", ErrPolicyUnavailable, msg)
	}
}

// unavailable builds the canonical fail-closed Result for a
// backend fault. The error message is folded into the Errors
// slice and prefixed with the spec-mandated "policy-engine
// unavailable: " banner so downstream event emitters can match
// on a stable prefix.
func (c *HTTPClient) unavailable(reason string) Result {
	return Result{
		Decision: DecisionDeny,
		Reasons:  nil,
		Errors:   []string{"policy-engine unavailable: " + reason},
	}
}

// Compile-time assertion that *HTTPClient satisfies Client.
var _ Client = (*HTTPClient)(nil)
