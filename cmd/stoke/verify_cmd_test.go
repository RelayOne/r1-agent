package main

// verify_cmd_test.go -- work-stoke TASK 21 tests.
//
// Exercises the POST /verify HTTP endpoint through an httptest.Server
// so the real handler + decoder + response encoder all run. No mocks
// of plan.VerificationDescent: the descent engine runs for real with
// the minimal DescentConfig the verify server sets up. That is
// intentional -- the whole point of the wrapper is that it composes
// with the real engine.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestVerifyServer spins up a real httptest.Server wrapping the
// verify handler, and returns the base URL + cleanup func.
func newTestVerifyServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := newVerifyServer()
	ts := httptest.NewServer(srv.handler())
	return ts.URL, ts.Close
}

// postVerify POSTs a VerifyRequest and returns the decoded response
// along with the raw HTTP status code. Helper used by multiple tests.
func postVerify(t *testing.T, baseURL string, req VerifyRequest) (int, VerifyResponse, string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/verify", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded VerifyResponse
	// 4xx responses use text/plain; only try to decode JSON on 2xx.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("json decode (status=%d body=%q): %v", resp.StatusCode, string(raw), err)
		}
	}
	return resp.StatusCode, decoded, string(raw)
}

// TestVerifyServe_AllPass -- two ACs that both exit 0 must return
// passed=true with one evidence string per AC and a populated
// tiers_attempted list (T1 + T2 at minimum).
func TestVerifyServe_AllPass(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	status, resp, raw := postVerify(t, baseURL, VerifyRequest{
		Output: "initial worker output",
		Criteria: []VerifyCriterion{
			{ID: "AC1", Command: "true"},
			{ID: "AC2", Command: "exit 0"},
		},
	})

	if status != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", status, raw)
	}
	if !resp.Passed {
		t.Errorf("passed=false, want true (evidence=%v)", resp.Evidence)
	}
	if len(resp.Evidence) != 2 {
		t.Errorf("len(evidence)=%d, want 2", len(resp.Evidence))
	}
	for i, ev := range resp.Evidence {
		if ev == "" {
			t.Errorf("evidence[%d] empty", i)
		}
	}
	if len(resp.TiersAttempted) == 0 {
		t.Errorf("tiers_attempted empty, want at least T1+T2")
	}
	// T1 (intent-match) and T2 (run-ac) must have fired for a pass.
	seen := map[string]bool{}
	for _, tier := range resp.TiersAttempted {
		seen[tier] = true
	}
	if !seen["T1"] {
		t.Errorf("tiers_attempted missing T1: %v", resp.TiersAttempted)
	}
	if !seen["T2"] {
		t.Errorf("tiers_attempted missing T2: %v", resp.TiersAttempted)
	}
}

// TestVerifyServe_Fails -- one AC that always fails with an assertion
// must return passed=false. Evidence should carry enough info to see
// the failure (the AC id + output tail).
func TestVerifyServe_Fails(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	status, resp, raw := postVerify(t, baseURL, VerifyRequest{
		Output: "worker output",
		Criteria: []VerifyCriterion{
			{ID: "AC-fail", Command: `echo "expected foo got bar" && exit 1`},
		},
	})

	if status != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", status, raw)
	}
	if resp.Passed {
		t.Errorf("passed=true, want false (evidence=%v)", resp.Evidence)
	}
	if len(resp.Evidence) != 1 {
		t.Fatalf("len(evidence)=%d, want 1", len(resp.Evidence))
	}
	if !strings.Contains(resp.Evidence[0], "AC-fail") {
		t.Errorf("evidence[0]=%q, want it to mention AC-fail", resp.Evidence[0])
	}
	if !strings.Contains(resp.Evidence[0], "FAIL") {
		t.Errorf("evidence[0]=%q, want it to carry FAIL outcome", resp.Evidence[0])
	}
}

// TestVerifyServe_Concurrent -- five simultaneous requests must each
// return independently with the correct per-request result. Catches
// cross-request state leakage in the tier-event collector.
func TestVerifyServe_Concurrent(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	const N = 5
	type reqOut struct {
		idx    int
		status int
		resp   VerifyResponse
		raw    string
	}
	results := make(chan reqOut, N)

	for i := 0; i < N; i++ {
		i := i
		go func() {
			// Alternate pass/fail per request so an accidental shared
			// tier map or pass flag would produce the wrong answer.
			var cmd string
			if i%2 == 0 {
				cmd = "true"
			} else {
				cmd = `echo "boom" && exit 1`
			}
			st, rs, raw := postVerify(t, baseURL, VerifyRequest{
				Output: "concurrent output",
				Criteria: []VerifyCriterion{
					{ID: "AC-con", Command: cmd},
				},
			})
			results <- reqOut{idx: i, status: st, resp: rs, raw: raw}
		}()
	}
	// Collect each goroutine's completion with an overall timeout so
	// a deadlock in one doesn't hang the whole test suite.
	deadline := time.After(15 * time.Second)
	collected := make([]reqOut, 0, N)
	for len(collected) < N {
		select {
		case r := <-results:
			collected = append(collected, r)
		case <-deadline:
			t.Fatalf("timeout waiting for concurrent /verify requests (%d/%d completed)",
				len(collected), N)
		}
	}

	for _, r := range collected {
		if r.status != http.StatusOK {
			t.Errorf("req[%d] status=%d body=%q, want 200", r.idx, r.status, r.raw)
			continue
		}
		wantPass := r.idx%2 == 0
		if r.resp.Passed != wantPass {
			t.Errorf("req[%d] passed=%v, want %v (evidence=%v)",
				r.idx, r.resp.Passed, wantPass, r.resp.Evidence)
		}
		if len(r.resp.Evidence) != 1 {
			t.Errorf("req[%d] len(evidence)=%d, want 1", r.idx, len(r.resp.Evidence))
		}
	}
}

// TestVerifyServe_BadJSON -- malformed JSON must produce 400, not
// a panic, not a 500, and no descent should run.
func TestVerifyServe_BadJSON(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/verify",
		strings.NewReader("{this is not json"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// TestVerifyServe_EmptyCriteria -- an empty criteria list is a client
// error: there is literally nothing to verify. Must return 400.
func TestVerifyServe_EmptyCriteria(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	httpReq, _ := http.NewRequest(http.MethodPost, baseURL+"/verify",
		strings.NewReader(`{"output":"x","criteria":[]}`))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// TestVerifyServe_MethodNotAllowed -- GET /verify must return 405 so
// a mis-wired consumer sees an explicit signal rather than a confusing
// 400 "bad request".
func TestVerifyServe_MethodNotAllowed(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	resp, err := http.Get(baseURL + "/verify")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", resp.StatusCode)
	}
}

// TestVerifyServe_Healthz -- trivial liveness endpoint.
func TestVerifyServe_Healthz(t *testing.T) {
	baseURL, stop := newTestVerifyServer(t)
	defer stop()

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}
