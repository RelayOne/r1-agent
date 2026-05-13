// runner_test.go — tests for the in-step R1 runner (C5, T14).
package bitbucket

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// fakeExecutor records every Run call without invoking real processes.
type fakeExecutor struct {
	mu      sync.Mutex
	calls   []fakeCall
	wantErr error
	stdout  string
}

type fakeCall struct {
	Name string
	Args []string
}

func (f *fakeExecutor) Run(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{Name: name, Args: append([]string(nil), args...)})
	if f.stdout != "" {
		_, _ = io.WriteString(stdout, f.stdout)
	}
	return f.wantErr
}

// TestRunR1Step_Review confirms the runner shells out with r1 review args.
func TestRunR1Step_Review(t *testing.T) {
	// Bitbucket env required.
	t.Setenv("BITBUCKET_WORKSPACE", "ws")
	t.Setenv("BITBUCKET_REPO_SLUG", "r")
	t.Setenv("BITBUCKET_COMMIT", "abc")
	t.Setenv("BITBUCKET_BUILD_NUMBER", "42")
	t.Setenv("BITBUCKET_PR_ID", "5")
	// Run from a tempdir so tracebundles/ lands in a fresh place.
	dir := t.TempDir()
	// LINT-ALLOW chdir-test: BitBucket adapter integration test; restores cwd via t.Cleanup.
	wd, _ := os.Getwd()
	// LINT-ALLOW chdir-test: matched cleanup for the os.Getwd above.
	t.Cleanup(func() { _ = os.Chdir(wd) })
	// LINT-ALLOW chdir-test: switch to per-test tempdir; cleanup above.
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var statusBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		statusBodies = append(statusBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})
	fx := &fakeExecutor{stdout: "ok"}

	err := RunR1Step(context.Background(), shared.ModeReview, map[string]string{
		"R1_POLICY": "custom.yaml",
	}, fx, c)
	if err != nil {
		t.Fatalf("RunR1Step: %v", err)
	}
	if len(fx.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(fx.calls))
	}
	got := fx.calls[0]
	if got.Name != "r1" {
		t.Errorf("binary = %q, want r1", got.Name)
	}
	if got.Args[0] != "review" {
		t.Errorf("args[0] = %q, want review", got.Args[0])
	}
	if !contains(got.Args, "--policy") || !contains(got.Args, "custom.yaml") {
		t.Errorf("args missing --policy custom.yaml: %v", got.Args)
	}
	if !contains(got.Args, "bitbucket-comment") {
		t.Errorf("args missing bitbucket-comment output: %v", got.Args)
	}
	// Two status calls — INPROGRESS then SUCCESSFUL.
	if len(statusBodies) != 2 {
		t.Errorf("expected 2 status posts, got %d", len(statusBodies))
	}
	if !strings.Contains(statusBodies[0], "INPROGRESS") {
		t.Errorf("first status not INPROGRESS: %q", statusBodies[0])
	}
	if !strings.Contains(statusBodies[1], "SUCCESSFUL") {
		t.Errorf("final status not SUCCESSFUL: %q", statusBodies[1])
	}
	// Tracebundle file should exist.
	tracePath := filepath.Join(dir, "tracebundles", "r1-review-42.log")
	if _, err := os.Stat(tracePath); err != nil {
		t.Errorf("tracebundle missing at %s: %v", tracePath, err)
	}
}

// TestRunR1Step_MissionFailureFlagsStatus covers the failure path: the
// runner writes a FAILED commit-status row when r1 build exits non-zero.
func TestRunR1Step_MissionFailureFlagsStatus(t *testing.T) {
	t.Setenv("BITBUCKET_WORKSPACE", "ws")
	t.Setenv("BITBUCKET_REPO_SLUG", "r")
	t.Setenv("BITBUCKET_COMMIT", "abc")
	t.Setenv("BITBUCKET_BUILD_NUMBER", "7")
	dir := t.TempDir()
	// LINT-ALLOW chdir-test: BitBucket adapter integration test; restores cwd via t.Cleanup.
	wd, _ := os.Getwd()
	// LINT-ALLOW chdir-test: matched cleanup for the os.Getwd above.
	t.Cleanup(func() { _ = os.Chdir(wd) })
	// LINT-ALLOW chdir-test: switch to per-test tempdir; cleanup above.
	_ = os.Chdir(dir)

	var statusStates []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "INPROGRESS"):
			statusStates = append(statusStates, "INPROGRESS")
		case strings.Contains(string(body), "FAILED"):
			statusStates = append(statusStates, "FAILED")
		case strings.Contains(string(body), "SUCCESSFUL"):
			statusStates = append(statusStates, "SUCCESSFUL")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()})
	fx := &fakeExecutor{wantErr: errors.New("boom")}

	err := RunR1Step(context.Background(), shared.ModeMission, map[string]string{
		"PLAN":    "plans/x.json",
		"WORKERS": "2",
	}, fx, c)
	if err == nil {
		t.Fatal("expected error from failing exec")
	}
	if len(statusStates) != 2 || statusStates[0] != "INPROGRESS" || statusStates[1] != "FAILED" {
		t.Errorf("status flow = %v, want [INPROGRESS FAILED]", statusStates)
	}
	if len(fx.calls) != 1 {
		t.Fatalf("expected 1 exec call")
	}
	got := fx.calls[0]
	if got.Args[0] != "build" {
		t.Errorf("args[0] = %q, want build", got.Args[0])
	}
	if got.Args[1] != "plans/x.json" {
		t.Errorf("plan arg = %q", got.Args[1])
	}
	if !contains(got.Args, "--workers") || !contains(got.Args, "2") {
		t.Errorf("workers flag missing: %v", got.Args)
	}
}

// TestLoadPipelineContextReadsEnv.
func TestLoadPipelineContextReadsEnv(t *testing.T) {
	t.Setenv("BITBUCKET_WORKSPACE", "ws")
	t.Setenv("BITBUCKET_REPO_SLUG", "r")
	t.Setenv("BITBUCKET_BUILD_NUMBER", "100")
	t.Setenv("BITBUCKET_PR_ID", "9")
	t.Setenv("BITBUCKET_COMMIT", "deadbeef")
	t.Setenv(EnvOIDCToken, "jwt-x")
	t.Setenv("R1_API_TOKEN", "r1-tok")
	t.Setenv("R1_AUDIT_ENDPOINT", "https://audit")

	pc := LoadPipelineContext()
	if pc.Workspace != "ws" || pc.RepoSlug != "r" || pc.BuildNumber != "100" {
		t.Errorf("ctx = %+v", pc)
	}
	if pc.PRID != "9" || pc.CommitSHA != "deadbeef" {
		t.Errorf("ctx = %+v", pc)
	}
	if pc.OIDCToken != "jwt-x" || pc.APIToken != "r1-tok" || pc.AuditURL != "https://audit" {
		t.Errorf("ctx = %+v", pc)
	}
}

// TestBuildAuditEnvelope.
func TestBuildAuditEnvelope(t *testing.T) {
	env := BuildAuditEnvelope(PipelineContext{
		Workspace: "ws", RepoSlug: "r", PRID: "1", CommitSHA: "abc", BuildNumber: "42",
	}, shared.ModeReview)
	if env.Provider != "bitbucket" {
		t.Errorf("provider = %q", env.Provider)
	}
	if env.Mode != "review" {
		t.Errorf("mode = %q", env.Mode)
	}
	if env.Workspace != "ws" || env.Repo != "r" {
		t.Errorf("envelope = %+v", env)
	}
}

// TestRedactedEnvSummaryRedactsSecrets.
func TestRedactedEnvSummaryRedactsSecrets(t *testing.T) {
	got := RedactedEnvSummary(map[string]string{
		"R1_API_TOKEN":      "shhh",
		"ANTHROPIC_API_KEY": "shhh",
		"BUILD_NUMBER":      "42",
	})
	if strings.Contains(got, "shhh") {
		t.Errorf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "BUILD_NUMBER=42") {
		t.Errorf("non-secret missing: %q", got)
	}
}

// TestRunR1Step_RejectsMissingContext.
func TestRunR1Step_RejectsMissingContext(t *testing.T) {
	t.Setenv("BITBUCKET_WORKSPACE", "")
	t.Setenv("BITBUCKET_REPO_SLUG", "")
	fx := &fakeExecutor{}
	if err := RunR1Step(context.Background(), shared.ModeReview, nil, fx, nil); err == nil {
		t.Error("expected error for missing BITBUCKET_WORKSPACE / REPO_SLUG")
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
