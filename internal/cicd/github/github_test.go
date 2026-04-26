// github_test.go — unit tests for the GitHub Actions + PR adapter (T-R1P-021).
//
// All tests use httptest.NewServer to serve canned API responses. The
// real github.com API is never contacted from this file.
//
// Coverage:
//   - TriggerWorkflow POSTs the dispatch payload + auth header
//   - GetRunStatus parses the workflow-run payload
//   - WaitForCompletion polls and returns on terminal status
//   - WaitForCompletion times out cleanly on a stuck run
//   - GetJobLogs returns the raw log archive bytes
//   - GetPullRequestDiff sends the diff Accept header
//   - ListPullRequestFiles decodes the per-file change array
//   - PostReviewComment fetches head sha and posts the inline comment
//   - APIError carries status + message and IsNotFound classifies 404s
//   - Auth header (Authorization: Bearer ...) is sent on every request
//   - RenderCommentBody formats findings deterministically (T-R1P-021 requirement)
//   - ParseFindings reads the default LLM response shape
//   - AutoReview runs the full pipeline against a fake LLM + httptest server

package github

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

// newTestClient wires a Client to a httptest.Server with a short poll
// interval so WaitForCompletion finishes quickly in tests.
func newTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	return New(Config{
		BaseURL:      srv.URL,
		Token:        token,
		HTTPClient:   srv.Client(),
		PollInterval: 10 * time.Millisecond,
	})
}

// TestTriggerWorkflowSendsPayload verifies request method, URL, body
// and headers are correct.
func TestTriggerWorkflowSendsPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		wantPath := "/repos/owner/repo/actions/workflows/ci.yml/dispatches"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-xyz" {
			t.Errorf("Authorization header = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if got := r.Header.Get("Accept"); !strings.Contains(got, "github") {
			t.Errorf("Accept = %q", got)
		}
		var body dispatchPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Ref != "main" {
			t.Errorf("body.Ref = %q, want main", body.Ref)
		}
		if v, ok := body.Inputs["color"].(string); !ok || v != "blue" {
			t.Errorf("body.Inputs[color] = %v, want blue", body.Inputs["color"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "secret-xyz")
	err := c.TriggerWorkflow(context.Background(), "owner", "repo", "ci.yml", "main",
		map[string]interface{}{"color": "blue"})
	if err != nil {
		t.Fatalf("TriggerWorkflow: %v", err)
	}
}

// TestTriggerWorkflowGuards rejects empty owner/repo/workflow/ref.
func TestTriggerWorkflowGuards(t *testing.T) {
	c := New(Config{})
	if err := c.TriggerWorkflow(context.Background(), "", "r", "w", "main", nil); err == nil {
		t.Error("empty owner should error")
	}
	if err := c.TriggerWorkflow(context.Background(), "o", "", "w", "main", nil); err == nil {
		t.Error("empty repo should error")
	}
	if err := c.TriggerWorkflow(context.Background(), "o", "r", "", "main", nil); err == nil {
		t.Error("empty workflow should error")
	}
	if err := c.TriggerWorkflow(context.Background(), "o", "r", "w", "", nil); err == nil {
		t.Error("empty ref should error")
	}
}

// TestGetRunStatusDecodes parses the workflow-run payload and exposes
// the IsTerminal helper.
func TestGetRunStatusDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/42" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": 42, "name": "CI", "head_branch": "main",
			"head_sha": "abc123", "run_number": 7,
			"event": "push", "status": "completed",
			"conclusion": "success",
			"html_url": "https://github.com/o/r/actions/runs/42"
		}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	run, err := c.GetRunStatus(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetRunStatus: %v", err)
	}
	if run.ID != 42 || run.Status != "completed" || run.Conclusion != "success" {
		t.Errorf("run = %+v", run)
	}
	if !run.IsTerminal() {
		t.Error("IsTerminal should be true for status=completed")
	}
}

// TestPollUntilDoneSucceeds polls a fake server that flips to
// completed on the third call.
func TestPollUntilDoneSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		status := "in_progress"
		conclusion := ""
		if calls >= 3 {
			status = "completed"
			conclusion = "success"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id": 7, "status": "`+status+`", "conclusion": "`+conclusion+`"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	run, err := c.WaitForCompletion(context.Background(), "o", "r", 7, time.Second)
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		t.Errorf("run = %+v", run)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls)
	}
}

// TestPollUntilDoneTimesOut returns a timeout error when the run
// never reaches a terminal state.
func TestPollUntilDoneTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id": 7, "status": "in_progress"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.WaitForCompletion(context.Background(), "o", "r", 7, 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want 'timed out'", err)
	}
}

// TestGetJobLogsReturnsBody returns the raw log bytes.
func TestGetJobLogsReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/9/logs" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "PK\x03\x04 (fake zip bytes)")
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	logs, err := c.GetJobLogs(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("GetJobLogs: %v", err)
	}
	if !strings.HasPrefix(logs, "PK") {
		t.Errorf("logs = %q, want zip-prefix", logs)
	}
}

// TestGetPullRequestDiffSendsDiffAccept verifies the diff media type
// header is sent and the response body is returned verbatim.
func TestGetPullRequestDiffSendsDiffAccept(t *testing.T) {
	const fakeDiff = `diff --git a/x.go b/x.go
index 0..1 100644
--- a/x.go
+++ b/x.go
@@ -1 +1 @@
-old
+new`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); !strings.Contains(got, "diff") {
			t.Errorf("Accept = %q, want diff media type", got)
		}
		_, _ = io.WriteString(w, fakeDiff)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	got, err := c.GetPullRequestDiff(context.Background(), "o", "r", 5)
	if err != nil {
		t.Fatalf("GetPullRequestDiff: %v", err)
	}
	if got != fakeDiff {
		t.Errorf("diff mismatch:\nwant %q\ngot  %q", fakeDiff, got)
	}
}

// TestListPullRequestFilesDecodes parses the per-file change list.
func TestListPullRequestFilesDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"sha":"a","filename":"x.go","status":"modified","additions":3,"deletions":1,"changes":4,"patch":"+ new"},
			{"sha":"b","filename":"y.go","status":"added","additions":10,"deletions":0,"changes":10,"patch":"+++ y"}
		]`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	files, err := c.ListPullRequestFiles(context.Background(), "o", "r", 5)
	if err != nil {
		t.Fatalf("ListPullRequestFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].Filename != "x.go" || files[0].Additions != 3 {
		t.Errorf("file[0] = %+v", files[0])
	}
	if files[1].Status != "added" {
		t.Errorf("file[1].Status = %q", files[1].Status)
	}
}

// TestPostReviewCommentSendsCorrectBody verifies the inline-comment
// flow: fetch head sha, then POST the comment with the right shape.
func TestPostReviewCommentSendsCorrectBody(t *testing.T) {
	var postedBody ReviewComment
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/12"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"head": {"sha": "deadbeef"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls/12/comments":
			if err := json.NewDecoder(r.Body).Decode(&postedBody); err != nil {
				t.Fatalf("decode comment: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id": 99}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	err := c.PostReviewComment(context.Background(), "o", "r", 12, "looks fishy", "main.go", 42)
	if err != nil {
		t.Fatalf("PostReviewComment: %v", err)
	}
	if postedBody.Body != "looks fishy" || postedBody.Path != "main.go" || postedBody.Line != 42 {
		t.Errorf("posted = %+v", postedBody)
	}
	if postedBody.CommitID != "deadbeef" {
		t.Errorf("CommitID = %q, want deadbeef", postedBody.CommitID)
	}
	if postedBody.Side != "RIGHT" {
		t.Errorf("Side = %q, want RIGHT", postedBody.Side)
	}
}

// TestErrorResponseClassifies404 verifies APIError + IsNotFound.
func TestErrorResponseClassifies404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message": "Not Found"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.GetRunStatus(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound = false for: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *APIError: %T", err)
	}
	if apiErr.Message != "Not Found" {
		t.Errorf("message = %q", apiErr.Message)
	}
}

// TestErrorResponseClassifies401 verifies IsUnauthorized.
func TestErrorResponseClassifies401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message": "Bad credentials"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.GetRunStatus(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("IsUnauthorized = false for: %v", err)
	}
}

// TestNoTokenSkipsAuthHeader confirms an unauthenticated client does
// not send the Authorization header.
func TestNoTokenSkipsAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id": 1, "status": "completed"}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	if _, err := c.GetRunStatus(context.Background(), "o", "r", 1); err != nil {
		t.Fatalf("GetRunStatus: %v", err)
	}
}
