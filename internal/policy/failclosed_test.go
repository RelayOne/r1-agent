package policy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failclosed_test.go is the end-to-end fail-closed invariant suite
// for the policy.Client contract, independent of the per-backend
// unit tests. Each test exercises a distinct path that must resolve
// to Result{Decision: DecisionDeny, Errors: populated} AND (for
// transport-backed paths) a wrapped ErrPolicyUnavailable. The goal
// is an unconditional safety check: every route to "could not
// render a verdict" must fail closed.

// TestFailClosed_CedarTransportClosed asserts that when the
// cedar-agent endpoint is reachable at construction time but the
// socket is closed before Check runs, HTTPClient.Check returns a
// wrapped ErrPolicyUnavailable and a DecisionDeny Result.
func TestFailClosed_CedarTransportClosed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // close immediately so the connection attempt fails

	c, err := NewHTTPClient(srv.URL, "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on closed transport")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrPolicyUnavailable)", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("Errors empty, want populated Errors on fail-closed Result")
	}
	if !strings.HasPrefix(res.Errors[0], "policy-engine unavailable: ") {
		t.Fatalf("Errors[0] = %q, want policy-engine unavailable prefix", res.Errors[0])
	}
}

// TestFailClosed_CedarTimeout asserts that a blocking cedar-agent
// handler combined with a sub-second client timeout fails closed
// with ErrPolicyUnavailable + DecisionDeny. Defer ordering is
// LIFO: close(block) runs first to unblock the in-flight handler,
// then srv.Close() returns without hanging on it.
func TestFailClosed_CedarTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c, err := NewHTTPClient(srv.URL, "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	c.hc.Timeout = 50 * time.Millisecond

	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on timeout")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrPolicyUnavailable)", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("Errors empty, want populated Errors on fail-closed Result")
	}
}

// TestFailClosed_Cedar5xx asserts that a 500 response from the
// cedar-agent fails closed with ErrPolicyUnavailable, DecisionDeny,
// and an Errors slice containing "status 500".
func TestFailClosed_Cedar5xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewHTTPClient(srv.URL, "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on 5xx")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrPolicyUnavailable)", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	found := false
	for _, msg := range res.Errors {
		if strings.Contains(msg, "status 500") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Errors = %v, want one entry containing \"status 500\"", res.Errors)
	}
}

// TestFailClosed_CedarMalformedJSON asserts that a 200 response
// carrying un-parsable JSON still fails closed. A backend that
// replies but says nothing coherent must never be treated as
// allow — the spec §"Error Handling" requires deny + wrapped
// ErrPolicyUnavailable.
func TestFailClosed_CedarMalformedJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()

	c, err := NewHTTPClient(srv.URL, "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	res, err := c.Check(context.Background(), Request{Action: "bash.run"})
	if err == nil {
		t.Fatalf("Check err = nil, want non-nil on malformed JSON body")
	}
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrPolicyUnavailable)", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (fail-closed)", res.Decision)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("Errors empty, want populated Errors on fail-closed Result")
	}
}

// TestFailClosed_ZeroValueResultIsDeny asserts the core safety
// invariant of the types package: the zero value of Result must
// carry DecisionDeny so a mis-constructed or uninitialised Result
// fails closed. Without this guarantee every other fail-closed
// path is undermined.
func TestFailClosed_ZeroValueResultIsDeny(t *testing.T) {
	t.Parallel()

	var r Result
	if r.Decision != DecisionDeny {
		t.Fatalf("zero Result.Decision = %v, want DecisionDeny (zero value must fail closed)", r.Decision)
	}
}

// TestFailClosed_YAMLDefaultDeny asserts that a YAMLClient loaded
// from an empty rules list denies every request with the canonical
// "default-deny" reason. This is the in-process backend's
// fail-closed analogue — the deny-by-default, first-match-wins
// semantics documented on YAMLClient.
func TestFailClosed_YAMLDefaultDeny(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty-policy.yaml")
	if err := os.WriteFile(path, []byte("rules: []\n"), 0o600); err != nil {
		t.Fatalf("write empty yaml: %v", err)
	}
	c, err := NewYAMLClient(path)
	if err != nil {
		t.Fatalf("NewYAMLClient: %v", err)
	}
	res, err := c.Check(context.Background(), Request{
		Principal: `Stoke::"s-fc"`,
		Action:    "bash.run",
		Resource:  "/tmp/x",
	})
	if err != nil {
		// YAMLClient.Check returns nil error for default-deny — it's
		// not a backend fault, it's a deliberate verdict.
		t.Fatalf("Check err = %v, want nil (default-deny is a verdict, not a fault)", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %v, want DecisionDeny (default-deny)", res.Decision)
	}
	if len(res.Reasons) != 1 || res.Reasons[0] != "default-deny" {
		t.Fatalf("Reasons = %v, want [default-deny]", res.Reasons)
	}
}
