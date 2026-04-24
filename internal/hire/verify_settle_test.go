package hire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/streamjson"
)

// mockSettlement is a test double for SettlementClient. It records
// every Settle / Dispute call so tests can assert what the flow
// decided. Concurrent-safe so we can exercise future parallel-AC
// variants without rewriting.
type mockSettlement struct {
	mu sync.Mutex

	settleCalls  []SettleRequest
	disputeCalls []DisputeEvidence

	// settleErr / disputeErr, when non-nil, are returned from
	// Settle / Dispute to exercise error-handling branches.
	settleErr  error
	disputeErr error
}

func (m *mockSettlement) Settle(_ context.Context, req SettleRequest) (SettleReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settleCalls = append(m.settleCalls, req)
	if m.settleErr != nil {
		return SettleReceipt{}, m.settleErr
	}
	return SettleReceipt{SettlementID: "settle-ok-" + req.ContractID, Note: req.Note}, nil
}

func (m *mockSettlement) Dispute(_ context.Context, ev DisputeEvidence) (DisputeReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disputeCalls = append(m.disputeCalls, ev)
	if m.disputeErr != nil {
		return DisputeReceipt{}, m.disputeErr
	}
	return DisputeReceipt{DisputeID: "dispute-ok-" + ev.ContractID, Note: "filed"}, nil
}

// parseEmittedEvents is a parser helper (not a test) that decodes
// the emitter's NDJSON output into a slice of event maps for
// assertion by the actual Test* functions below. Non-JSON lines
// are skipped defensively.
func parseEmittedEvents(buf *bytes.Buffer) []map[string]any {
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var events []map[string]any
	for sc.Scan() {
		trimmed := strings.TrimSpace(sc.Text())
		if trimmed == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
			continue
		}
		events = append(events, m)
	}
	return events
}

// countSubtypes returns how many events with type=="system" and
// subtype==want appear in events.
func countSubtypes(events []map[string]any, want string) int {
	n := 0
	for _, e := range events {
		if e["type"] == "system" && e["subtype"] == want {
			n++
		}
	}
	return n
}

func TestVerifyAndSettle_AllCriteriaPass_CallsSettle(t *testing.T) {
	set := &mockSettlement{}
	h := &Hirer{Settlement: set}

	spec := "translate the README from English to Japanese"
	delivery := []byte("translated README text in Japanese covering the README content")

	err := h.VerifyAndSettle(context.Background(), "contract-1", "did:tp:agent-a", delivery, spec)
	if err != nil {
		t.Fatalf("VerifyAndSettle: %v", err)
	}
	if len(set.settleCalls) != 1 {
		t.Fatalf("settle calls=%d, want 1", len(set.settleCalls))
	}
	if len(set.disputeCalls) != 0 {
		t.Errorf("dispute should not have been called on success, got %d calls", len(set.disputeCalls))
	}
	got := set.settleCalls[0]
	if got.ContractID != "contract-1" {
		t.Errorf("settle contract_id=%q want contract-1", got.ContractID)
	}
	if got.AgentDID != "did:tp:agent-a" {
		t.Errorf("settle agent_did=%q want did:tp:agent-a", got.AgentDID)
	}
	if !strings.Contains(got.Note, "criteria passed") {
		t.Errorf("settle note=%q missing criteria-passed summary", got.Note)
	}
}

func TestVerifyAndSettle_FailingCriterion_CallsDispute(t *testing.T) {
	set := &mockSettlement{}
	h := &Hirer{Settlement: set}

	// Custom extra criterion that always fails so we exercise the
	// Extra slice AND guarantee the baseline review doesn't decide.
	extra := DeliveryCriterion{
		ID:          "custom-format-ok",
		Description: "deliverable must be valid JSON",
		Check: func(_ context.Context, _ string, _ []byte) (bool, string) {
			return false, "custom check failed: JSON parse error at offset 0"
		},
	}

	spec := "produce a JSON report of all lint errors in the repo"
	delivery := []byte("JSON report of lint errors with 3 issues and 2 warnings")

	err := h.VerifyAndSettle(
		context.Background(),
		"contract-42",
		"did:tp:agent-b",
		delivery,
		spec,
		VerifyAndSettleOpts{Extra: []DeliveryCriterion{extra}},
	)
	if err == nil {
		t.Fatal("expected error on failing criterion")
	}
	if !errors.Is(err, ErrVerificationFailed) {
		t.Errorf("want ErrVerificationFailed wrap, got %v", err)
	}
	if !strings.Contains(err.Error(), "custom-format-ok") {
		t.Errorf("error should name the failed AC; got %v", err)
	}
	if len(set.disputeCalls) != 1 {
		t.Fatalf("dispute calls=%d, want 1", len(set.disputeCalls))
	}
	if len(set.settleCalls) != 0 {
		t.Errorf("settle should not have been called on failure, got %d calls", len(set.settleCalls))
	}
	ev := set.disputeCalls[0]
	if ev.FailedCriterionID != "custom-format-ok" {
		t.Errorf("dispute FailedCriterionID=%q want custom-format-ok", ev.FailedCriterionID)
	}
	if ev.Spec != spec {
		t.Errorf("dispute Spec missing; got %q", ev.Spec)
	}
	if !bytes.Equal(ev.DeliverySample, delivery) {
		// delivery is well under the default 4KB sample cap.
		t.Errorf("dispute DeliverySample should include full delivery when under cap; got %d bytes want %d", len(ev.DeliverySample), len(delivery))
	}
	// The two baseline ACs passed before the extra failed, so we
	// expect 3 verdicts total (delivery-complete, delivery-matches-spec, custom-format-ok).
	if len(ev.Verdicts) != 3 {
		t.Errorf("dispute Verdicts len=%d want 3", len(ev.Verdicts))
	}
	if ev.Verdicts[len(ev.Verdicts)-1].Pass {
		t.Error("last verdict should be the failing one (pass=false)")
	}
}

func TestVerifyAndSettle_EmitsEvents(t *testing.T) {
	set := &mockSettlement{}
	buf := &bytes.Buffer{}
	em := streamjson.New(buf, true)
	h := &Hirer{Settlement: set, Emitter: em}

	spec := "summarize these three log files with bullet points"
	delivery := []byte("- log files summarized\n- three bullet points covering each\n- root cause identified")

	if err := h.VerifyAndSettle(context.Background(), "contract-7", "did:tp:agent-c", delivery, spec); err != nil {
		t.Fatalf("VerifyAndSettle: %v", err)
	}

	events := parseEmittedEvents(buf)
	verifyCount := countSubtypes(events, "stoke.delegation.verify")
	settleCount := countSubtypes(events, "stoke.delegation.settle")
	disputeCount := countSubtypes(events, "stoke.delegation.dispute")

	// Two baseline criteria → two verify events.
	if verifyCount != 2 {
		t.Errorf("stoke.delegation.verify count=%d want 2", verifyCount)
	}
	if settleCount != 1 {
		t.Errorf("stoke.delegation.settle count=%d want 1", settleCount)
	}
	if disputeCount != 0 {
		t.Errorf("stoke.delegation.dispute should be 0 on success; got %d", disputeCount)
	}
	// Assert settle event carries the expected contract ID and verdicts.
	for _, e := range events {
		if e["subtype"] == "stoke.delegation.settle" {
			if got := e["contract_id"]; got != "contract-7" {
				t.Errorf("settle event contract_id=%v want contract-7", got)
			}
			vs, ok := e["verdicts"].([]any)
			if !ok {
				t.Errorf("settle event verdicts not []any; got %T", e["verdicts"])
			} else if len(vs) != 2 {
				t.Errorf("settle event verdicts len=%d want 2", len(vs))
			}
		}
	}
}

func TestVerifyAndSettle_EmitsDisputeEventOnFailure(t *testing.T) {
	set := &mockSettlement{}
	buf := &bytes.Buffer{}
	em := streamjson.New(buf, true)
	h := &Hirer{Settlement: set, Emitter: em}

	// Zero-length delivery trips delivery-complete immediately.
	err := h.VerifyAndSettle(context.Background(), "contract-9", "did:tp:agent-d", nil, "any spec will do")
	if err == nil {
		t.Fatal("expected failure on empty delivery")
	}

	events := parseEmittedEvents(buf)
	if got := countSubtypes(events, "stoke.delegation.verify"); got != 1 {
		t.Errorf("verify count=%d want 1 (first AC fails, short-circuits)", got)
	}
	if got := countSubtypes(events, "stoke.delegation.dispute"); got != 1 {
		t.Errorf("dispute count=%d want 1", got)
	}
	if got := countSubtypes(events, "stoke.delegation.settle"); got != 0 {
		t.Errorf("settle count=%d want 0 on failure", got)
	}
}

func TestBuildDeliveryCriteria_EmptyDelivery_Fails(t *testing.T) {
	h := &Hirer{}
	crits := h.buildDeliveryCriteria("contract-x", "write something", nil)
	if len(crits) < 2 {
		t.Fatalf("expected >= 2 baseline criteria, got %d", len(crits))
	}
	first := crits[0]
	if first.ID != "delivery-complete" {
		t.Errorf("first criterion id=%q want delivery-complete", first.ID)
	}
	pass, reason := first.Check(context.Background(), "write something", nil)
	if pass {
		t.Error("empty delivery should trip delivery-complete")
	}
	if !strings.Contains(reason, "empty") {
		t.Errorf("reason should mention empty; got %q", reason)
	}

	// whitespace-only delivery also fails.
	pass, _ = first.Check(context.Background(), "x", []byte("   \n\t  "))
	if pass {
		t.Error("whitespace-only delivery should trip delivery-complete")
	}

	// non-empty delivery passes.
	pass, _ = first.Check(context.Background(), "x", []byte("ok"))
	if !pass {
		t.Error("non-empty delivery should pass delivery-complete")
	}
}

func TestReviewDeliverableFallback(t *testing.T) {
	cases := []struct {
		name     string
		spec     string
		delivery []byte
		wantPass bool
	}{
		{
			name:     "empty spec passes",
			spec:     "",
			delivery: []byte("anything"),
			wantPass: true,
		},
		{
			name:     "empty delivery fails",
			spec:     "produce a report about lint",
			delivery: nil,
			wantPass: false,
		},
		{
			name:     "whitespace-only delivery fails",
			spec:     "produce a report about lint",
			delivery: []byte("   \n   "),
			wantPass: false,
		},
		{
			name:     "too-short delivery fails length floor",
			spec:     "produce a comprehensive architectural review of the authentication subsystem",
			delivery: []byte("ok."),
			wantPass: false,
		},
		{
			name:     "zero keyword overlap fails",
			spec:     "produce a lint report covering JavaScript ESLint rule violations",
			delivery: []byte("completely unrelated text about fishing boats and the open sea surface"),
			wantPass: false,
		},
		{
			name:     "one keyword overlap passes",
			spec:     "produce a lint report covering JavaScript errors",
			delivery: []byte("the lint report is ready and includes the results of the JavaScript analysis"),
			wantPass: true,
		},
		{
			name:     "unicode keyword overlap passes",
			spec:     "traduire le README en français",
			delivery: []byte("voici la traduction du README en français, complète et vérifiée"),
			wantPass: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, reason := ReviewDeliverableFallback(tc.spec, tc.delivery)
			if pass != tc.wantPass {
				t.Errorf("pass=%v want %v (reason=%q)", pass, tc.wantPass, reason)
			}
			if reason == "" {
				t.Error("reason should always be populated")
			}
		})
	}
}

func TestVerifyAndSettle_NoSettlementClient_ReturnsSentinel(t *testing.T) {
	h := &Hirer{}
	err := h.VerifyAndSettle(context.Background(), "c", "a", []byte("ok"), "spec")
	if !errors.Is(err, ErrNoSettlementClient) {
		t.Errorf("want ErrNoSettlementClient, got %v", err)
	}
}

func TestVerifyAndSettle_UsesCustomReviewHook(t *testing.T) {
	set := &mockSettlement{}
	called := 0
	h := &Hirer{
		Settlement: set,
		Review: func(_ context.Context, _ string, _ []byte) (bool, string) {
			called++
			return false, "custom LLM reviewer said no: format mismatch"
		},
	}
	err := h.VerifyAndSettle(context.Background(), "c", "a", []byte("some delivery"), "any spec")
	if err == nil {
		t.Fatal("expected failure from custom review")
	}
	if called != 1 {
		t.Errorf("custom review called %d times, want 1", called)
	}
	if len(set.disputeCalls) != 1 {
		t.Errorf("expected dispute from failed review; got %d disputes", len(set.disputeCalls))
	}
	if !strings.Contains(set.disputeCalls[0].FailedReason, "format mismatch") {
		t.Errorf("dispute FailedReason should carry review reason; got %q", set.disputeCalls[0].FailedReason)
	}
}

func TestVerifyAndSettle_DisputeFilingError_WrappedInResult(t *testing.T) {
	set := &mockSettlement{disputeErr: errors.New("trustplane: 503 service unavailable")}
	h := &Hirer{Settlement: set}
	err := h.VerifyAndSettle(context.Background(), "c", "a", nil, "spec")
	if err == nil {
		t.Fatal("expected error on failed verification")
	}
	if !errors.Is(err, ErrVerificationFailed) {
		t.Errorf("want ErrVerificationFailed wrap, got %v", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should include dispute filing error; got %v", err)
	}
}

func TestVerifyAndSettle_SampleTruncation(t *testing.T) {
	set := &mockSettlement{}
	h := &Hirer{Settlement: set}

	// Build a large delivery that will fail review but exceeds the
	// default 4096-byte dispute cap.
	big := bytes.Repeat([]byte("unrelated filler content "), 500) // ~12500 bytes
	spec := "produce a lint report covering JavaScript ESLint violations in the codebase"

	err := h.VerifyAndSettle(context.Background(), "c", "a", big, spec)
	if err == nil {
		t.Fatal("expected zero-overlap failure")
	}
	if len(set.disputeCalls) != 1 {
		t.Fatalf("disputes=%d want 1", len(set.disputeCalls))
	}
	sample := set.disputeCalls[0].DeliverySample
	if len(sample) != 4096 {
		t.Errorf("dispute sample len=%d want 4096 (default cap)", len(sample))
	}
	if !bytes.Equal(sample, big[:4096]) {
		t.Error("dispute sample should be the leading 4096 bytes of the delivery")
	}
}

func TestTokenize_StripsStopwordsAndShortTokens(t *testing.T) {
	got := tokenize("The quick and the dead go to the store")
	// Stopwords removed: the, and, to. Short removed: "go" (2 chars is min, "go" passes).
	// Actually tokens length >= 2, so "go" passes. Let's just check that
	// "the" and "and" are gone.
	for _, tok := range got {
		if tok == "the" || tok == "and" {
			t.Errorf("stopword %q leaked through filter", tok)
		}
	}
	// "quick" and "dead" and "store" should be in.
	want := map[string]bool{"quick": false, "dead": false, "store": false}
	for _, tok := range got {
		if _, ok := want[tok]; ok {
			want[tok] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("expected token %q not in tokenized output %v", k, got)
		}
	}
}
