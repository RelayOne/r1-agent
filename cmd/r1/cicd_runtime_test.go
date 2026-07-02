package main

// cicd_runtime_test.go — integration tests for the `r1 cicd
// trigger|status|logs` runtime verbs (audit A056). Each provider
// adapter is exercised end-to-end against an httptest server via
// --base-url; env tokens come from t.Setenv. No real CI provider is
// contacted.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runCicd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCicdRuntime(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCicdRuntimeGitHubTriggerStatusLogs(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	var dispatched struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/o/r/actions/workflows/ci.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &dispatched)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/42", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"status":"completed","conclusion":"success","html_url":"http://x/run/42"}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/42/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://logs.example/archive.zip", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errb := runCicd(t, "trigger", "--provider", "github", "--repo", "o/r",
		"--workflow", "ci.yml", "--ref", "main", "--base-url", srv.URL, "--var", "FOO=bar")
	if code != 0 {
		t.Fatalf("trigger exit = %d\nstderr: %s", code, errb)
	}
	if dispatched.Ref != "main" || dispatched.Inputs["FOO"] != "bar" {
		t.Errorf("dispatch payload = %+v, want ref=main inputs[FOO]=bar", dispatched)
	}
	if !strings.Contains(out, `"triggered": true`) {
		t.Errorf("trigger output missing confirmation: %s", out)
	}

	code, out, errb = runCicd(t, "status", "--provider", "github", "--repo", "o/r",
		"--run-id", "42", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("status exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, `"conclusion": "success"`) {
		t.Errorf("status output missing conclusion: %s", out)
	}

	// --wait on an already-terminal successful run returns 0 fast.
	code, _, errb = runCicd(t, "status", "--provider", "github", "--repo", "o/r",
		"--run-id", "42", "--wait", "--timeout", "5s", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("status --wait exit = %d\nstderr: %s", code, errb)
	}

	code, out, errb = runCicd(t, "logs", "--provider", "github", "--repo", "o/r",
		"--run-id", "42", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("logs exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, "logs.example/archive.zip") {
		t.Errorf("logs output should carry the signed archive URL, got: %s", out)
	}
}

func TestCicdRuntimeGitHubWaitFailureExitsNonZero(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/actions/runs/7", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"status":"completed","conclusion":"failure"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, _ := runCicd(t, "status", "--provider", "github", "--repo", "o/r",
		"--run-id", "7", "--wait", "--timeout", "5s", "--base-url", srv.URL)
	if code != 1 {
		t.Fatalf("failed run with --wait should exit 1, got %d (out: %s)", code, out)
	}
	if !strings.Contains(out, `"conclusion": "failure"`) {
		t.Errorf("output should still carry the run JSON: %s", out)
	}
}

func TestCicdRuntimeGitLabTriggerAndLogs(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-test")
	mux := http.NewServeMux()
	var gotToken string
	mux.HandleFunc("POST /projects/123/pipeline", func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`{"id":9001,"status":"pending","ref":"main","web_url":"http://gl/x"}`))
	})
	mux.HandleFunc("GET /projects/123/jobs/9/trace", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("job log line 1\njob log line 2\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errb := runCicd(t, "trigger", "--provider", "gitlab", "--project", "123",
		"--ref", "main", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("trigger exit = %d\nstderr: %s", code, errb)
	}
	if gotToken != "glpat-test" {
		t.Errorf("PRIVATE-TOKEN = %q, want env token", gotToken)
	}
	if !strings.Contains(out, `"id": 9001`) {
		t.Errorf("trigger output missing pipeline id: %s", out)
	}

	code, out, errb = runCicd(t, "logs", "--provider", "gitlab", "--project", "123",
		"--job-id", "9", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("logs exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, "job log line 2") {
		t.Errorf("logs output missing trace: %s", out)
	}
}

func TestCicdRuntimeBitbucketTriggerStatusLogs(t *testing.T) {
	t.Setenv("BITBUCKET_API_TOKEN", "bb-test")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repositories/ws/slug/pipelines/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"uuid":"p-1","build_number":3,"state":{"name":"PENDING"}}`))
	})
	mux.HandleFunc("GET /repositories/ws/slug/pipelines/p-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"uuid":"p-1","build_number":3,"state":{"name":"IN_PROGRESS"}}`))
	})
	mux.HandleFunc("GET /repositories/ws/slug/pipelines/p-1/steps/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"uuid":"s-1","name":"build","state":{"name":"COMPLETED"}}]}`))
	})
	mux.HandleFunc("GET /repositories/ws/slug/pipelines/p-1/steps/s-1/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bb step log\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errb := runCicd(t, "trigger", "--provider", "bitbucket", "--workspace", "ws",
		"--repo", "slug", "--ref", "main", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("trigger exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, `"uuid": "p-1"`) {
		t.Errorf("trigger output missing pipeline uuid: %s", out)
	}

	code, out, errb = runCicd(t, "status", "--provider", "bitbucket", "--workspace", "ws",
		"--repo", "slug", "--uuid", "p-1", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("status exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, "IN_PROGRESS") {
		t.Errorf("status output missing state: %s", out)
	}

	code, out, errb = runCicd(t, "logs", "--provider", "bitbucket", "--workspace", "ws",
		"--repo", "slug", "--uuid", "p-1", "--base-url", srv.URL)
	if code != 0 {
		t.Fatalf("logs exit = %d\nstderr: %s", code, errb)
	}
	if !strings.Contains(out, "bb step log") {
		t.Errorf("logs output missing step log: %s", out)
	}
}

func TestCicdRuntimeUsageErrors(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "x")
	if code, _, _ := runCicd(t, "trigger", "--provider", "github"); code != 2 {
		t.Errorf("github trigger without --repo should exit 2, got %d", code)
	}
	if code, _, _ := runCicd(t, "status", "--provider", "gitlab"); code != 2 {
		t.Errorf("gitlab status without --project should exit 2, got %d", code)
	}
	if code, _, _ := runCicd(t, "logs", "--provider", "bitbucket"); code != 2 {
		t.Errorf("bitbucket logs without workspace/repo should exit 2, got %d", code)
	}
	if code, _, _ := runCicd(t, "trigger", "--provider", "teamcity"); code != 2 {
		t.Errorf("unknown provider should exit 2, got %d", code)
	}
}
