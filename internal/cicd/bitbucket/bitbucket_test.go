// bitbucket_test.go — REST client unit tests for the Bitbucket adapter (C5).
//
// All tests use httptest.NewServer; no live API calls. Mirrors gitlab_test.go.

package bitbucket

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

func newTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	return New(Config{
		BaseURL:      srv.URL,
		Token:        token,
		HTTPClient:   srv.Client(),
		PollInterval: 10 * time.Millisecond,
	})
}

// TestTriggerPipelineSendsPayload verifies the request method, URL, body and
// headers, and that the parsed Pipeline carries server fields.
func TestTriggerPipelineSendsPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		want := "/repositories/myws/myrepo/pipelines/"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-xyz" {
			t.Errorf("Authorization header = %q", got)
		}
		var body triggerPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Target.RefName != "main" {
			t.Errorf("target.ref_name = %q, want main", body.Target.RefName)
		}
		if body.Target.RefType != "branch" {
			t.Errorf("target.ref_type = %q, want branch", body.Target.RefType)
		}
		if len(body.Variables) != 1 || body.Variables[0].Key != "FOO" || body.Variables[0].Value != "bar" {
			t.Errorf("body.Variables = %+v", body.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"uuid": "{abc-123}", "build_number": 7, "state": {"name": "PENDING"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "secret-xyz")
	p, err := c.TriggerPipeline(context.Background(), "myws", "myrepo", "main", map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("TriggerPipeline: %v", err)
	}
	if p.UUID != "{abc-123}" || p.BuildNumber != 7 || p.State.Name != "PENDING" {
		t.Errorf("pipeline = %+v", p)
	}
}

// TestTriggerRejectsEmptyRef asserts client-side validation runs before any
// network call when the caller forgets the ref.
func TestTriggerRejectsEmptyRef(t *testing.T) {
	c := New(Config{Token: "x"})
	if _, err := c.TriggerPipeline(context.Background(), "ws", "repo", "", nil); err == nil {
		t.Fatal("expected error for empty ref")
	}
}

// TestTriggerCustomPipeline covers the workflow-dispatch analogue.
func TestTriggerCustomPipeline(t *testing.T) {
	var gotBody triggerPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"uuid":"u","build_number":1,"state":{"name":"PENDING"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "t")
	if _, err := c.TriggerCustomPipeline(context.Background(), "ws", "r", "main", "r1-mission", nil); err != nil {
		t.Fatalf("TriggerCustomPipeline: %v", err)
	}
	if gotBody.Target.Selector.Type != "custom" || gotBody.Target.Selector.Pattern != "r1-mission" {
		t.Errorf("custom selector wrong: %+v", gotBody.Target.Selector)
	}
}

// TestGetPipelineStatusParses verifies the GET decode path.
func TestGetPipelineStatusParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"uuid":"u-9","state":{"name":"IN_PROGRESS"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	p, err := c.GetPipelineStatus(context.Background(), "ws", "r", "u-9")
	if err != nil {
		t.Fatalf("GetPipelineStatus: %v", err)
	}
	if p.UUID != "u-9" || p.State.Name != "IN_PROGRESS" {
		t.Errorf("pipeline = %+v", p)
	}
	if p.IsTerminal() {
		t.Error("IN_PROGRESS pipeline should not be terminal")
	}
}

// TestPauseUntilSuccess covers WaitForCompletion polling until the server
// flips state to COMPLETED.
func TestPauseUntilSuccess(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls < 3 {
			_, _ = io.WriteString(w, `{"uuid":"u","state":{"name":"IN_PROGRESS"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"uuid":"u","state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	p, err := c.WaitForCompletion(context.Background(), "ws", "r", "u", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if p.State.Name != "COMPLETED" || p.State.Result.Name != "SUCCESSFUL" {
		t.Errorf("state = %+v", p.State)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 polls, got %d", calls)
	}
}

// TestPauseTimesOut confirms a stuck pipeline yields a timeout error.
func TestPauseTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"uuid":"u","state":{"name":"IN_PROGRESS"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.WaitForCompletion(context.Background(), "ws", "r", "u", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
}

// TestGetStepLogReturnsTrace verifies the raw-text trace path.
func TestGetStepLogReturnsTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "line one\nline two\nline three\n")
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	log, err := c.GetStepLog(context.Background(), "ws", "r", "p-1", "s-1")
	if err != nil {
		t.Fatalf("GetStepLog: %v", err)
	}
	if !strings.Contains(log, "line two") {
		t.Errorf("log = %q", log)
	}
}

// TestListPipelineStepsDecodes verifies the paged-array decode path.
func TestListPipelineStepsDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[
			{"uuid":"s-1","name":"build","state":{"name":"COMPLETED"}},
			{"uuid":"s-2","name":"test","state":{"name":"IN_PROGRESS"}}
		]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	steps, err := c.ListPipelineSteps(context.Background(), "ws", "r", "p-1")
	if err != nil {
		t.Fatalf("ListPipelineSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[1].Name != "test" {
		t.Errorf("steps[1] = %+v", steps[1])
	}
}

// TestPostCommitStatusSendsName verifies the name + key + truncation.
func TestPostCommitStatusSendsName(t *testing.T) {
	var got CommitStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "t")
	longDesc := strings.Repeat("x", 200)
	err := c.PostCommitStatus(context.Background(), "ws", "r", "abc", CommitStatus{
		Key:         "r1-verify",
		State:       "SUCCESSFUL",
		Description: longDesc,
	})
	if err != nil {
		t.Fatalf("PostCommitStatus: %v", err)
	}
	if got.Name != "R1 Verify" {
		t.Errorf("got.Name = %q, want %q", got.Name, "R1 Verify")
	}
	if got.Key != "r1-verify" {
		t.Errorf("got.Key = %q", got.Key)
	}
	if len(got.Description) != commitStatusMaxDescLen {
		t.Errorf("description not truncated: len=%d, want %d", len(got.Description), commitStatusMaxDescLen)
	}
}

// TestPostCommitStatusGuards rejects missing fields.
func TestPostCommitStatusGuards(t *testing.T) {
	c := New(Config{})
	if err := c.PostCommitStatus(context.Background(), "ws", "r", "", CommitStatus{Key: "k"}); err == nil {
		t.Error("empty commitSHA should error")
	}
	if err := c.PostCommitStatus(context.Background(), "ws", "r", "abc", CommitStatus{}); err == nil {
		t.Error("empty status.Key should error")
	}
}

// TestAPIErrorClassification ensures 404 surfaces as IsNotFound and 401 as
// IsUnauthorized.
func TestAPIErrorClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"repo not found"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.GetPipelineStatus(context.Background(), "missing", "r", "u")
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
	if !strings.Contains(apiErr.Message, "not found") {
		t.Errorf("APIError.Message = %q", apiErr.Message)
	}
}

func TestUnauthorizedClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad token"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	_, err := c.GetPipelineStatus(context.Background(), "ws", "r", "u")
	if !IsUnauthorized(err) {
		t.Errorf("expected IsUnauthorized, got %v", err)
	}
}

// TestAuthHeaderSentBearer confirms Authorization: Bearer ... is set on requests.
func TestAuthHeaderSentBearer(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"uuid":"u","state":{"name":"COMPLETED"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "abc-123")
	_, _ = c.GetPipelineStatus(context.Background(), "ws", "r", "u")
	select {
	case h := <-got:
		if h != "Bearer abc-123" {
			t.Errorf("Authorization = %q, want %q", h, "Bearer abc-123")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
}

// TestAuthHeaderSentBasic confirms basic-auth pairing when configured.
func TestAuthHeaderSentBasic(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"uuid":"u","state":{"name":"COMPLETED"}}`)
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL:    srv.URL,
		Token:      "tok",
		Username:   "alice@example.com",
		BasicAuth:  true,
		HTTPClient: srv.Client(),
	})
	_, _ = c.GetPipelineStatus(context.Background(), "ws", "r", "u")
	select {
	case h := <-got:
		if !strings.HasPrefix(h, "Basic ") {
			t.Errorf("Authorization = %q, want Basic prefix", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
}

// TestParseWorkspaceRepoRoundTrip checks the path-parse helper.
func TestParseWorkspaceRepoRoundTrip(t *testing.T) {
	cases := []struct {
		in           string
		wantWs       string
		wantRepo     string
		wantOK       bool
	}{
		{"ws/repo", "ws", "repo", true},
		{"alice/my-proj", "alice", "my-proj", true},
		{"justone", "", "", false},
		{"/leading", "", "", false},
		{"trailing/", "", "", false},
	}
	for _, tc := range cases {
		ws, repo, ok := ParseWorkspaceRepo(tc.in)
		if ok != tc.wantOK || ws != tc.wantWs || repo != tc.wantRepo {
			t.Errorf("ParseWorkspaceRepo(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, ws, repo, ok, tc.wantWs, tc.wantRepo, tc.wantOK)
		}
	}
}

// TestDefaultsApplied checks zero-value Config picks safe defaults.
func TestDefaultsApplied(t *testing.T) {
	c := New(Config{})
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.EditionValue() != EditionCloud {
		t.Errorf("edition = %q, want %q", c.EditionValue(), EditionCloud)
	}
	if c.http == nil {
		t.Error("http client should be non-nil")
	}
	if c.pollEvry <= 0 {
		t.Error("poll interval default should be positive")
	}
}

// TestTransportFailure exercises the error path when the server is gone.
func TestTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	c := New(Config{
		BaseURL:    srv.URL,
		Token:      "x",
		HTTPClient: srv.Client(),
	})
	_, err := c.GetPipelineStatus(context.Background(), "ws", "r", "u")
	if err == nil {
		t.Fatal("expected transport error")
	}
}

// TestGetPullRequestHeadParses verifies HEAD-commit extraction.
func TestGetPullRequestHeadParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":3,"source":{"commit":{"hash":"deadbeef"}}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	sha, err := c.GetPullRequestHead(context.Background(), "ws", "r", 3)
	if err != nil {
		t.Fatalf("GetPullRequestHead: %v", err)
	}
	if sha != "deadbeef" {
		t.Errorf("sha = %q", sha)
	}
}

// TestValidatePipelinesConfigSendsYAML ensures the validator helper POSTs
// to the right endpoint with the yaml body shape.
func TestValidatePipelinesConfigSendsYAML(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok")
	if err := c.ValidatePipelinesConfig(context.Background(), "ws", "r", "image: x"); err != nil {
		t.Fatalf("ValidatePipelinesConfig: %v", err)
	}
	if gotPath != "/repositories/ws/r/pipelines-config/validate" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["yaml"] != "image: x" {
		t.Errorf("body yaml = %q", gotBody["yaml"])
	}
}
