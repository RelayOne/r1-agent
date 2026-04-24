package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/research"
)

// TestResearchExecutor_Execute_ReturnsDeliverable exercises the happy
// path: a query with candidate URLs yields a ResearchDeliverable with
// Claims and Sources populated from the stub fetcher.
func TestResearchExecutor_Execute_ReturnsDeliverable(t *testing.T) {
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://docs.example.com/go": "Go is a compiled programming language designed at Google in 2009. " +
			"Go is compiled and statically typed. Go has garbage collection. " +
			"Go is used for distributed systems and cloud infrastructure.",
	}}
	ex := NewResearchExecutor(stub)
	p := Plan{
		ID:    "P-1",
		Query: "What is Go programming language?",
		Extra: map[string]any{
			"urls": []string{"https://docs.example.com/go"},
		},
	}
	d, err := ex.Execute(context.Background(), p, EffortMedium)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("want ResearchDeliverable, got %T", d)
	}
	if len(rd.Report.Claims) == 0 {
		t.Fatal("want at least one claim, got zero")
	}
	if len(rd.Sources) == 0 {
		t.Fatal("want at least one source, got zero")
	}
	if rd.Report.Query != p.Query {
		t.Errorf("query not propagated: got %q want %q", rd.Report.Query, p.Query)
	}
	if rd.Summary() == "" {
		t.Error("Summary must not be empty")
	}
	if rd.Size() == 0 {
		t.Error("Size should reflect body length")
	}
}

// TestResearchExecutor_Execute_NoURLs_EmptyClaims exercises the
// degenerate-no-search path: without candidate URLs, Execute still
// returns a Deliverable but the AC set is empty.
func TestResearchExecutor_Execute_NoURLs_EmptyClaims(t *testing.T) {
	ex := NewResearchExecutor(&research.StubFetcher{})
	d, err := ex.Execute(context.Background(), Plan{Query: "anything"}, EffortLow)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	if len(rd.Report.Claims) != 0 {
		t.Errorf("want 0 claims without URLs, got %d", len(rd.Report.Claims))
	}
}

// TestResearchExecutor_BuildCriteria_OneACPerClaim verifies the
// AC-per-claim invariant.
func TestResearchExecutor_BuildCriteria_OneACPerClaim(t *testing.T) {
	stub := &research.StubFetcher{Pages: map[string]string{
		"https://example/a": "Sentence one matches a claim. " +
			"Sentence two matches another claim. " +
			"Sentence three matches yet one more claim.",
	}}
	ex := NewResearchExecutor(stub)
	d, err := ex.Execute(context.Background(),
		Plan{Query: "sentence claim matches", Extra: map[string]any{"urls": []string{"https://example/a"}}},
		EffortMedium)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	acs := ex.BuildCriteria(Task{ID: "T-1"}, rd)
	if len(acs) != len(rd.Report.Claims) {
		t.Fatalf("AC count %d != claim count %d", len(acs), len(rd.Report.Claims))
	}
	for i, ac := range acs {
		if ac.ID != rd.Report.Claims[i].ID {
			t.Errorf("AC[%d].ID = %q, want %q", i, ac.ID, rd.Report.Claims[i].ID)
		}
		if ac.VerifyFunc == nil {
			t.Errorf("AC[%d] has nil VerifyFunc", i)
		}
		if ac.Command != "" {
			t.Errorf("AC[%d] should not have Command, got %q", i, ac.Command)
		}
	}
}

// TestResearchExecutor_VerifyFunc_Passes ensures that when the stub
// fetcher returns a body containing the claim tokens + a 3-word
// phrase from the claim, VerifyFunc reports passing.
func TestResearchExecutor_VerifyFunc_Passes(t *testing.T) {
	url := "https://example/pass"
	body := "Goroutines enable lightweight concurrency in Go. " +
		"Each goroutine uses small stacks that grow on demand. " +
		"Scheduling is cooperative via the runtime."
	stub := &research.StubFetcher{Pages: map[string]string{url: body}}
	ex := NewResearchExecutor(stub)
	d, err := ex.Execute(context.Background(),
		Plan{Query: "goroutines lightweight concurrency", Extra: map[string]any{"urls": []string{url}}},
		EffortLow)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	if len(rd.Report.Claims) == 0 {
		t.Fatal("no claims extracted")
	}
	acs := ex.BuildCriteria(Task{ID: "T-1"}, rd)
	anyPassed := false
	for _, ac := range acs {
		ok, reason := ac.VerifyFunc(context.Background())
		if ok {
			anyPassed = true
			if !strings.Contains(reason, "supported") {
				t.Errorf("passing AC reason should say supported, got %q", reason)
			}
		}
	}
	if !anyPassed {
		t.Error("want at least one passing VerifyFunc, none passed")
	}
}

// TestResearchExecutor_VerifyFunc_Fails builds claims from one page
// then has VerifyFunc re-fetch — but with the Fetcher swapped to a
// stub returning an unrelated body. The AC must report not-supported.
func TestResearchExecutor_VerifyFunc_Fails(t *testing.T) {
	// Build claims from one body.
	sourceURL := "https://example/source"
	truth := "Go is a compiled programming language. Goroutines enable concurrency."
	seedStub := &research.StubFetcher{Pages: map[string]string{sourceURL: truth}}
	ex := NewResearchExecutor(seedStub)
	d, _ := ex.Execute(context.Background(),
		Plan{Query: "Go programming language", Extra: map[string]any{"urls": []string{sourceURL}}},
		EffortLow)
	rd, ok := d.(ResearchDeliverable)
	if !ok {
		t.Fatalf("deliverable: unexpected type: %T", d)
	}
	if len(rd.Report.Claims) == 0 {
		t.Fatal("no claims extracted from seed body")
	}
	// Now swap the fetcher so the verify step sees unrelated content.
	ex.Fetcher = &research.StubFetcher{Pages: map[string]string{
		sourceURL: "Lasagna layering requires patience and a hot oven.",
	}}
	acs := ex.BuildCriteria(Task{ID: "T-1"}, rd)
	for _, ac := range acs {
		ok, reason := ac.VerifyFunc(context.Background())
		if ok {
			t.Errorf("AC %s unexpectedly passed against unrelated body (%s)", ac.ID, reason)
		}
	}
}

// TestResearchExecutor_BuildEnvFixFunc_Transient verifies the env-fix
// heuristic correctly classifies common transient-network failures.
func TestResearchExecutor_BuildEnvFixFunc_Transient(t *testing.T) {
	ex := NewResearchExecutor(&research.StubFetcher{})
	fix := ex.BuildEnvFixFunc()
	cases := []struct {
		cause   string
		stderr  string
		wantOK  bool
	}{
		{"", "i/o timeout", true},
		{"", "connection refused", true},
		{" 503 Service Unavailable ", "", true},
		{"", "Temporary failure in name resolution", true},
		{"HTTP 404", "", false},
		{"unauthorized", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got := fix(context.Background(), tc.cause, tc.stderr)
		if got != tc.wantOK {
			t.Errorf("EnvFix(%q, %q) = %v; want %v", tc.cause, tc.stderr, got, tc.wantOK)
		}
	}
}

// TestResearchExecutor_TaskType pins the task-type discriminator.
func TestResearchExecutor_TaskType(t *testing.T) {
	ex := NewResearchExecutor(&research.StubFetcher{})
	if ex.TaskType() != TaskResearch {
		t.Errorf("want TaskResearch, got %q", ex.TaskType())
	}
}

// TestResearchExecutor_NilFetcher_Errors confirms Execute refuses to
// run without a fetcher.
func TestResearchExecutor_NilFetcher_Errors(t *testing.T) {
	ex := &ResearchExecutor{}
	_, err := ex.Execute(context.Background(), Plan{Query: "anything"}, EffortLow)
	if err == nil {
		t.Fatal("want error when Fetcher is nil, got nil")
	}
	if !strings.Contains(err.Error(), "Fetcher") {
		t.Errorf("error should mention Fetcher, got %v", err)
	}
}
