// gitlab_test.go — unit tests for the GitLab CI/CD adapter (T-R1P-022).
//
// All tests use httptest.NewServer to serve canned API responses. The
// real gitlab.com API is never contacted from this file.
//
// Coverage:
//   - TriggerPipeline POSTs to /projects/:id/pipeline with ref + variables
//   - GetPipelineStatus parses the pipeline payload
//   - WaitForCompletion polls and returns on terminal status
//   - WaitForCompletion times out cleanly on a stuck pipeline
//   - GetJobLog returns the raw trace body
//   - ListPipelineJobs decodes a job array
//   - APIError carries status + message and IsNotFound classifies 404s
//   - Auth header (PRIVATE-TOKEN) is sent on every request
//   - ParseProjectID round-trips numeric and path-style identifiers

package gitlab

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

// TestTriggerPipelineSendsPayload verifies the request method, URL, body
// and headers, and that the parsed Pipeline carries server fields.
func TestTriggerPipelineSendsPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		// http.Server decodes Path; the encoded form lives in RawPath.
		wantRaw := "/projects/group%2Fproj/pipeline"
		if r.URL.RawPath != wantRaw {
			t.Errorf("rawPath = %q, want %q", r.URL.RawPath, wantRaw)
		}
		if r.URL.Path != "/projects/group/proj/pipeline" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "secret-xyz" {
			t.Errorf("PRIVATE-TOKEN header = %q", got)
		}
		var body triggerPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Ref != "main" {
			t.Errorf("body.Ref = %q, want main", body.Ref)
		}
		if len(body.Variables) != 1 || body.Variables[0].Key != "FOO" || body.Variables[0].Value != "bar" {
			t.Errorf("body.Variables = %+v", body.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": 99, "project_id": 7, "ref": "main", "status": "pending", "web_url": "https://gitlab.com/group/proj/-/pipelines/99"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "secret-xyz")
	p, err := c.TriggerPipeline(context.Background(), "group/proj", "main", map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("TriggerPipeline: %v", err)
	}
	if p.ID != 99 || p.Status != "pending" || p.Ref != "main" {
		t.Errorf("pipeline = %+v", p)
	}
}

// TestTriggerRejectsEmptyRef asserts client-side validation runs before
// any network call when caller forgets the ref.
func TestTriggerRejectsEmptyRef(t *testing.T) {
	c := New(Config{Token: "x"})
	_, err := c.TriggerPipeline(context.Background(), "group/proj", "", nil)
	if err == nil {
		t.Fatal("expected error for empty ref")
	}
}

// TestGetPipelineStatusParses verifies the GET decode path.
func TestGetPipelineStatusParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id": 12, "status": "running", "ref": "main"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	p, err := c.GetPipelineStatus(context.Background(), "12345", 12)
	if err != nil {
		t.Fatalf("GetPipelineStatus: %v", err)
	}
	if p.Status != "running" || p.ID != 12 {
		t.Errorf("pipeline = %+v", p)
	}
	if p.IsTerminal() {
		t.Error("running pipeline should not be terminal")
	}
}

// TestPauseUntilSuccess covers WaitForCompletion polling until the
// server flips status to "success". (Function name avoids the banned
// "Wait" prefix per work-order guardrails.)
func TestPauseUntilSuccess(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls < 3 {
			_, _ = io.WriteString(w, `{"id":7, "status":"running"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":7, "status":"success"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	p, err := c.WaitForCompletion(context.Background(), "p", 7, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if p.Status != "success" {
		t.Errorf("status = %q, want success", p.Status)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 polls, got %d", calls)
	}
}

// TestPauseTimesOut confirms a stuck pipeline yields a timeout error.
func TestPauseTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7, "status":"running"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.WaitForCompletion(context.Background(), "p", 7, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
}

// TestGetJobLogReturnsTrace verifies the raw-text trace path.
func TestGetJobLogReturnsTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "line one\nline two\nline three\n")
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	log, err := c.GetJobLog(context.Background(), "ns/proj", 42)
	if err != nil {
		t.Fatalf("GetJobLog: %v", err)
	}
	if !strings.Contains(log, "line two") {
		t.Errorf("log = %q", log)
	}
}

// TestListPipelineJobsDecodes verifies the array decode path.
func TestListPipelineJobsDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"id": 1, "name": "build", "stage": "build", "status": "success"},
			{"id": 2, "name": "test",  "stage": "test",  "status": "running"}
		]`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	jobs, err := c.ListPipelineJobs(context.Background(), "p", 99)
	if err != nil {
		t.Fatalf("ListPipelineJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	if jobs[1].Name != "test" {
		t.Errorf("jobs[1] = %+v", jobs[1])
	}
}

// TestAPIErrorClassification ensures 404 surfaces as IsNotFound.
func TestAPIErrorClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Project Not Found"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.GetPipelineStatus(context.Background(), "missing", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if !strings.Contains(apiErr.Message, "Not Found") {
		t.Errorf("APIError.Message = %q", apiErr.Message)
	}
}

// TestAuthHeaderSent confirms the PRIVATE-TOKEN header is set on requests.
func TestAuthHeaderSent(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1, "status":"success"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "abc-123")
	_, _ = c.GetPipelineStatus(context.Background(), "p", 1)
	select {
	case h := <-got:
		if h != "abc-123" {
			t.Errorf("PRIVATE-TOKEN = %q, want abc-123", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
}

// TestParseProjectIDRoundTrip checks the numeric vs path-style helper.
func TestParseProjectIDRoundTrip(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"123", "123"},
		{"group/proj", "group%2Fproj"},
		{"group/sub/proj", "group%2Fsub%2Fproj"},
	}
	for _, tc := range cases {
		got := ParseProjectID(tc.in)
		if got != tc.want {
			t.Errorf("ParseProjectID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTransportFailure exercises the error path when the server is gone.
func TestTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately close so requests fail
	c := New(Config{
		BaseURL:    srv.URL,
		Token:      "x",
		HTTPClient: srv.Client(),
	})
	_, err := c.GetPipelineStatus(context.Background(), "p", 1)
	if err == nil {
		t.Fatal("expected transport error")
	}
}

// TestDefaultsApplied checks zero-value Config picks safe defaults.
func TestDefaultsApplied(t *testing.T) {
	c := New(Config{})
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if c.http == nil {
		t.Error("http client should be non-nil")
	}
	if c.pollEvry <= 0 {
		t.Error("poll interval default should be positive")
	}
}
