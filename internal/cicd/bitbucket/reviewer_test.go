// reviewer_test.go — tests for the Bitbucket auto-review pipeline (C5).
package bitbucket

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// TestAutoReviewFullPipeline runs end-to-end against a fake server + fake LLM.
func TestAutoReviewFullPipeline(t *testing.T) {
	const diff = "diff --git a/main.go b/main.go\n+missing error check"
	posted := struct {
		mu       sync.Mutex
		comments []PRComment
	}{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repositories/o/r/pullrequests/3/diff":
			_, _ = io.WriteString(w, diff)
		case r.Method == http.MethodGet && r.URL.Path == "/repositories/o/r/pullrequests/3":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":3,"source":{"commit":{"hash":"cafef00d"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repositories/o/r/pullrequests/3/comments":
			var body PRComment
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			posted.mu.Lock()
			posted.comments = append(posted.comments, body)
			posted.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id": 1}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	rev := NewReviewer(c)

	var capturedPrompt string
	llm := func(_ context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return `Found issues:

- **warning** main.go:7 — missing error handling
- **info** main.go:9 — variable could be const`, nil
	}

	findings, err := rev.AutoReview(context.Background(), "o", "r", 3, llm)
	if err != nil {
		t.Fatalf("AutoReview: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if !strings.Contains(capturedPrompt, diff) {
		t.Errorf("prompt does not contain diff; prompt = %q", capturedPrompt)
	}

	posted.mu.Lock()
	defer posted.mu.Unlock()
	if len(posted.comments) != 2 {
		t.Fatalf("comments posted = %d, want 2", len(posted.comments))
	}
	if posted.comments[0].Inline == nil {
		t.Fatalf("comments[0] should be inline")
	}
	if posted.comments[0].Inline.Path != "main.go" || posted.comments[0].Inline.To != 7 {
		t.Errorf("comments[0].Inline = %+v", posted.comments[0].Inline)
	}
	if !strings.HasPrefix(posted.comments[0].Content.Raw, "**[r1-review · WARNING]**") {
		t.Errorf("body missing prefix: %q", posted.comments[0].Content.Raw)
	}
}

// TestAutoReviewGuards rejects nil llm + zero pr.
func TestAutoReviewGuards(t *testing.T) {
	c := New(Config{})
	rev := NewReviewer(c)

	if _, err := rev.AutoReview(context.Background(), "o", "r", 1, nil); err == nil {
		t.Error("nil llm should error")
	}
	llm := func(_ context.Context, _ string) (string, error) { return "", nil }
	if _, err := rev.AutoReview(context.Background(), "", "r", 1, llm); err == nil {
		t.Error("empty workspace should error")
	}
	if _, err := rev.AutoReview(context.Background(), "o", "r", 0, llm); err == nil {
		t.Error("zero prID should error")
	}
}

// TestAutoReviewCustomPromptSubstitutes confirms SetPrompt overrides the
// template and {{DIFF}} substitution still works.
func TestAutoReviewCustomPromptSubstitutes(t *testing.T) {
	const customTpl = "REVIEW THIS:\n{{DIFF}}\nDONE."
	const diff = "fake-diff-content"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repositories/o/r/pullrequests/1/diff":
			_, _ = io.WriteString(w, diff)
		case r.URL.Path == "/repositories/o/r/pullrequests/1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":1,"source":{"commit":{"hash":"abc"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	rev := NewReviewer(c).SetPrompt(customTpl)

	var capturedPrompt string
	llm := func(_ context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "NO FINDINGS", nil
	}

	if _, err := rev.AutoReview(context.Background(), "o", "r", 1, llm); err != nil {
		t.Fatalf("AutoReview: %v", err)
	}
	wantPrompt := "REVIEW THIS:\nfake-diff-content\nDONE."
	if capturedPrompt != wantPrompt {
		t.Errorf("prompt = %q, want %q", capturedPrompt, wantPrompt)
	}
}

// TestAutoReviewSkipsInvalidFindings drops findings missing path/line.
func TestAutoReviewSkipsInvalidFindings(t *testing.T) {
	postCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repositories/o/r/pullrequests/1/diff":
			_, _ = io.WriteString(w, "diff")
		case r.URL.Path == "/repositories/o/r/pullrequests/1" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":1,"source":{"commit":{"hash":"abc"}}}`)
		case r.Method == http.MethodPost:
			postCount++
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id": 1}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	rev := NewReviewer(c).SetParser(func(_ string) []shared.Finding {
		return []shared.Finding{
			{Path: "ok.go", Line: 1, Body: "valid", Severity: "info"},
			{Path: "", Line: 1, Body: "missing path"},
			{Path: "ok.go", Body: "missing line"},
			{Path: "ok.go", Line: 2, Body: "  "},
		}
	})

	llm := func(_ context.Context, _ string) (string, error) { return "ignored", nil }
	findings, err := rev.AutoReview(context.Background(), "o", "r", 1, llm)
	if err != nil {
		t.Fatalf("AutoReview: %v", err)
	}
	if len(findings) != 4 {
		t.Errorf("findings = %d, want 4 (parser returned all)", len(findings))
	}
	if postCount != 1 {
		t.Errorf("postCount = %d, want 1 (only one valid finding)", postCount)
	}
}

// TestBuildSummaryBodyEmpty + Populated.
func TestBuildSummaryBody(t *testing.T) {
	empty := BuildSummaryBody(nil, "")
	if !strings.Contains(empty, "no findings") {
		t.Errorf("empty body = %q", empty)
	}

	full := BuildSummaryBody([]shared.Finding{
		{Path: "main.go", Line: 42, Severity: "warning", Body: "unchecked error"},
	}, "https://example/artifact.zip")
	if !strings.Contains(full, "main.go:42") {
		t.Errorf("full body missing anchor: %q", full)
	}
	if !strings.Contains(full, "https://example/artifact.zip") {
		t.Errorf("full body missing artifact url: %q", full)
	}
	if !strings.Contains(full, "WARNING") {
		t.Errorf("full body missing severity: %q", full)
	}
}
