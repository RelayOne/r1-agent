// reviewer_test.go — tests for the auto code-review pipeline (T-R1P-021).
//
// Coverage:
//   - RenderCommentBody formats the body deterministically
//   - ParseFindings reads the default LLM bullet shape
//   - ParseFindings handles "NO FINDINGS" sentinel
//   - AutoReview runs the full pipeline (diff fetch → LLM → comment post)
//   - AutoReview rejects nil llm and zero pr number
//   - Custom prompt template propagates {{DIFF}} substitution

package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRenderCommentBodyFormat is the explicit review-comment
// formatting test required by T-R1P-021. Asserts exact byte output
// for a known Finding so downstream consumers (PR readers, dashboards)
// can rely on the layout.
func TestRenderCommentBodyFormat(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want string
	}{
		{
			name: "warning with body",
			f:    Finding{Severity: "warning", Body: "unchecked error"},
			want: "**[r1-review · WARNING]** unchecked error",
		},
		{
			name: "error severity uppercased",
			f:    Finding{Severity: "error", Body: "nil deref risk"},
			want: "**[r1-review · ERROR]** nil deref risk",
		},
		{
			name: "missing severity defaults to INFO",
			f:    Finding{Body: "consider extracting helper"},
			want: "**[r1-review · INFO]** consider extracting helper",
		},
		{
			name: "body trimmed",
			f:    Finding{Severity: "info", Body: "  spaces around  \n"},
			want: "**[r1-review · INFO]** spaces around",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderCommentBody(tc.f)
			if got != tc.want {
				t.Errorf("\nwant: %q\ngot:  %q", tc.want, got)
			}
		})
	}
}

// TestParseFindingsReadsBullets exercises the default bullet parser.
func TestParseFindingsReadsBullets(t *testing.T) {
	response := `Here are the findings I see:

- **warning** main.go:42 — handle the error returned from os.Open
- **error** util.go:7 — possible nil dereference on *cfg
- **info** util.go:99 — consider extracting this into a helper
- not a finding line, ignored
- **bogus** x.go:1 — unknown severity becomes info

That's it.`

	got := ParseFindings(response)
	if len(got) != 4 {
		t.Fatalf("findings = %d, want 4: %+v", len(got), got)
	}

	check := func(i int, path string, line int, sev string) {
		t.Helper()
		if got[i].Path != path {
			t.Errorf("got[%d].Path = %q, want %q", i, got[i].Path, path)
		}
		if got[i].Line != line {
			t.Errorf("got[%d].Line = %d, want %d", i, got[i].Line, line)
		}
		if got[i].Severity != sev {
			t.Errorf("got[%d].Severity = %q, want %q", i, got[i].Severity, sev)
		}
	}
	check(0, "main.go", 42, "warning")
	check(1, "util.go", 7, "error")
	check(2, "util.go", 99, "info")
	check(3, "x.go", 1, "info") // unknown severity normalized
}

// TestParseFindingsNoneSentinel returns nil for the "NO FINDINGS" shape.
func TestParseFindingsNoneSentinel(t *testing.T) {
	for _, response := range []string{"NO FINDINGS", "Looks good. NO FINDINGS to report."} {
		if got := ParseFindings(response); len(got) != 0 {
			t.Errorf("response %q produced findings: %+v", response, got)
		}
	}
}

// TestFindingValidityGate confirms IsValid blocks empty fields.
func TestFindingValidityGate(t *testing.T) {
	if (Finding{Path: "x", Line: 1, Body: "ok"}).IsValid() != true {
		t.Error("valid finding should pass IsValid")
	}
	bad := []Finding{
		{Line: 1, Body: "ok"},                  // missing path
		{Path: "x", Body: "ok"},                // missing line
		{Path: "x", Line: -1, Body: "ok"},      // negative line
		{Path: "x", Line: 1},                   // missing body
		{Path: "x", Line: 1, Body: "   \t\n "}, // whitespace-only body
	}
	for i, f := range bad {
		if f.IsValid() {
			t.Errorf("case %d: %+v should be invalid", i, f)
		}
	}
}

// TestAutoReviewFullPipeline runs end-to-end against a fake server +
// fake LLM. Verifies the diff is fetched, the prompt is rendered with
// the diff, and findings are posted as inline comments.
func TestAutoReviewFullPipeline(t *testing.T) {
	const diff = "diff --git a/main.go b/main.go\n+missing error check"
	posted := struct {
		mu       sync.Mutex
		comments []ReviewComment
	}{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptDiff := strings.Contains(r.Header.Get("Accept"), "diff")
		switch {
		case r.Method == http.MethodGet && acceptDiff &&
			r.URL.Path == "/repos/o/r/pulls/3":
			_, _ = io.WriteString(w, diff)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/3":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"head": {"sha": "cafef00d"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls/3/comments":
			var body ReviewComment
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			posted.mu.Lock()
			posted.comments = append(posted.comments, body)
			posted.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id": 1}`)
		default:
			t.Errorf("unexpected request: %s %s (Accept=%q)", r.Method, r.URL.Path, r.Header.Get("Accept"))
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
	if posted.comments[0].CommitID != "cafef00d" {
		t.Errorf("CommitID = %q", posted.comments[0].CommitID)
	}
	if !strings.Contains(posted.comments[0].Body, "missing error handling") {
		t.Errorf("body missing message: %q", posted.comments[0].Body)
	}
	if !strings.HasPrefix(posted.comments[0].Body, "**[r1-review · WARNING]**") {
		t.Errorf("body missing prefix: %q", posted.comments[0].Body)
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
		t.Error("empty owner should error")
	}
	if _, err := rev.AutoReview(context.Background(), "o", "r", 0, llm); err == nil {
		t.Error("zero prNumber should error")
	}
}

// TestAutoReviewCustomPromptSubstitutes confirms SetPrompt overrides
// the template and {{DIFF}} substitution still works.
func TestAutoReviewCustomPromptSubstitutes(t *testing.T) {
	const customTpl = "REVIEW THIS:\n{{DIFF}}\nDONE."
	const diff = "fake-diff-content"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			_, _ = io.WriteString(w, diff)
		case r.URL.Path == "/repos/o/r/pulls/1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"head": {"sha": "abc"}}`)
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
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			_, _ = io.WriteString(w, "diff")
		case r.URL.Path == "/repos/o/r/pulls/1" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"head": {"sha": "abc"}}`)
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
	rev := NewReviewer(c).SetParser(func(_ string) []Finding {
		return []Finding{
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
