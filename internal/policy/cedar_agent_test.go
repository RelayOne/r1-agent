package policy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient wraps NewHTTPClient with a t.Fatalf on error and
// swaps in the supplied timeout so timeout-path tests can override
// the 2s production default.
func newTestClient(t *testing.T, endpoint, token string, timeout time.Duration) *HTTPClient {
	t.Helper()
	c, err := NewHTTPClient(endpoint, token)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if timeout > 0 {
		c.hc.Timeout = timeout
	}
	return c
}

func TestHTTPClient_CheckAllow(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/is_authorized" {
			t.Errorf("path = %q, want /v1/is_authorized", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		// Verify the PARC body shape carries entities as an empty array.
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body not valid JSON: %v", err)
		}
		if _, ok := body["principal"]; !ok {
			t.Errorf("body missing principal: %v", body)
		}
		if ents, ok := body["entities"].([]any); !ok || len(ents) != 0 {
			t.Errorf("entities = %v, want []", body["entities"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"Allow","diagnostics":{"reason":["policy0","policy7"],"errors":[]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "", 0)
	res, err := c.Check(context.Background(), Request{
		Principal: `User::"alice"`,
		Action:    `Action::"bash.exec"`,
		Resource:  `Tool::"bash"`,
		Context:   map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("Check returned err: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("Decision = %v, want DecisionAllow", res.Decision)
	}
	if len(res.Reasons) != 2 || res.Reasons[0] != "policy0" {
		t.Fatalf("Reasons = %v, want [policy0 policy7]", res.Reasons)
	}
}

func TestHTTPClient_CheckDeny(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"Deny","diagnostics":{"reason":["deny-etc-write"],"errors":[]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "", 0)
	res, err := c.Check(context.Background(), Request{
		Principal: `Stoke::"s1"`,
		Action:    "file.write",
		Resource:  "/etc/passwd",
	})
	if err != nil {
		t.Fatalf("Check returned err: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny", res.Decision)
	}
	if len(res.Reasons) != 1 || res.Reasons[0] != "deny-etc-write" {
		t.Fatalf("Reasons = %v, want [deny-etc-write]", res.Reasons)
	}
}

func TestHTTPClient_CheckTransportError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // close immediately so the client cannot connect

	c := newTestClient(t, srv.URL, "", 0)
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on transport failure")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyUnavailable", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	if len(res.Errors) == 0 || !strings.HasPrefix(res.Errors[0], "policy-engine unavailable: ") {
		t.Fatalf("Errors = %v, want policy-engine unavailable prefix", res.Errors)
	}
}

func TestHTTPClient_CheckTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block // block until test teardown
	}))
	// LIFO: close(block) runs FIRST (unblocks handler), then srv.Close()
	// completes without waiting on the in-flight handler.
	defer srv.Close()
	defer close(block)

	c := newTestClient(t, srv.URL, "", 50*time.Millisecond)
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on timeout")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyUnavailable", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
}

func TestHTTPClient_Check5xxIsFailClosed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "", 0)
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on 5xx")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyUnavailable", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0], "status 500") {
		t.Fatalf("Errors = %v, want status 500", res.Errors)
	}
}

func TestHTTPClient_CheckMalformedBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "", 0)
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on malformed body")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyUnavailable", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
}

func TestHTTPClient_CheckWithTokenSendsAuthHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"Allow","diagnostics":{"reason":[],"errors":[]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "secret-token-xyz", 0)
	if _, err := c.Check(context.Background(), Request{Action: "bash.run"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if gotAuth != "Bearer secret-token-xyz" {
		t.Fatalf("Authorization = %q, want Bearer secret-token-xyz", gotAuth)
	}
}

func TestHTTPClient_CheckEmptyTokenOmitsAuthHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"Allow","diagnostics":{"reason":[],"errors":[]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "", 0)
	if _, err := c.Check(context.Background(), Request{Action: "bash.run"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if sawAuth {
		t.Fatalf("Authorization header present = %q, want absent", gotAuth)
	}
}

func TestNewHTTPClient_RejectsEmptyEndpoint(t *testing.T) {
	t.Parallel()

	if _, err := NewHTTPClient("", ""); err == nil {
		t.Fatalf("NewHTTPClient(\"\") err = nil, want non-nil")
	}
	if _, err := NewHTTPClient("   ", ""); err == nil {
		t.Fatalf("NewHTTPClient(whitespace) err = nil, want non-nil")
	}
}

func TestNewHTTPClient_DefaultTimeoutIs2s(t *testing.T) {
	t.Parallel()

	c, err := NewHTTPClient("http://example.invalid", "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if c.hc.Timeout != 2*time.Second {
		t.Fatalf("Timeout = %v, want 2s", c.hc.Timeout)
	}
}
