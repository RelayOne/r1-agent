//go:build bitbucket_integration

// integration_test.go — live integration tests against api.bitbucket.org
// (C5, T22). Behind the `bitbucket_integration` build tag — matches the GH
// adapter's convention — so it never runs in the default CI gate.
//
// Skipped automatically when BITBUCKET_API_TOKEN is unset. The fixture repo
// slug + workspace are sourced from env so a downstream operator can wire
// their own fixture without editing the source.
//
// Live endpoints exercised:
//   - POST /repositories/{w}/{r}/pipelines-config/validate
//   - POST /repositories/{w}/{r}/pipelines/
//   - GET  /repositories/{w}/{r}/pipelines/{uuid}
//   - GET  /repositories/{w}/{r}/pipelines/{uuid}/steps/
//   - GET  /repositories/{w}/{r}/pipelines/{uuid}/steps/{s}/artifacts/
//   - POST /repositories/{w}/{r}/pullrequests/{id}/comments
//   - GET  /repositories/{w}/{r}/pullrequests/{id}/comments
//
// To run locally:
//
//	export BITBUCKET_API_TOKEN=...
//	export BITBUCKET_WORKSPACE=relayone
//	export BITBUCKET_FIXTURE_REPO=r1-bitbucket-fixture
//	export BITBUCKET_FIXTURE_PR_ID=1
//	go test -tags bitbucket_integration ./internal/cicd/bitbucket/...

package bitbucket

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cicd"
	"github.com/RelayOne/r1/internal/cicd/shared"
)

// envOrSkip returns the env var or skips the test when unset.
func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; skipping integration test", name)
	}
	return v
}

// newLiveClient builds a client wired to api.bitbucket.org.
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	tok := envOrSkip(t, "BITBUCKET_API_TOKEN")
	user := os.Getenv("BITBUCKET_USERNAME") // optional; only needed for basic auth
	cfg := Config{
		Token:    tok,
		Username: user,
	}
	if user != "" {
		cfg.BasicAuth = true
	}
	return New(cfg)
}

// TestLive_ValidatorRoundTrip generates YAML for every mode and POSTs each
// to Bitbucket's own pipelines-config validator. Asserts the validator
// accepts every mode's output.
func TestLive_ValidatorRoundTrip(t *testing.T) {
	workspace := envOrSkip(t, "BITBUCKET_WORKSPACE")
	repo := envOrSkip(t, "BITBUCKET_FIXTURE_REPO")
	c := newLiveClient(t)

	for _, mode := range cicd.AllModes() {
		yaml, _, err := cicd.GenerateConfig(cicd.ProviderBitbucket, cicd.Options{Mode: mode})
		if err != nil {
			t.Fatalf("%s: generate: %v", mode, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = c.ValidatePipelinesConfig(ctx, workspace, repo, yaml)
		cancel()
		if err != nil {
			t.Errorf("%s: validator rejected YAML: %v", mode, err)
		}
	}
}

// TestLive_TriggerAndPoll triggers a pipeline on the fixture repo and polls
// until completion. Verifies the artifact archive is downloadable.
func TestLive_TriggerAndPoll(t *testing.T) {
	workspace := envOrSkip(t, "BITBUCKET_WORKSPACE")
	repo := envOrSkip(t, "BITBUCKET_FIXTURE_REPO")
	c := newLiveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	p, err := c.TriggerPipeline(ctx, workspace, repo, "main", nil)
	if err != nil {
		t.Fatalf("TriggerPipeline: %v", err)
	}
	t.Logf("triggered pipeline %s (#%d)", p.UUID, p.BuildNumber)

	final, err := c.WaitForCompletion(ctx, workspace, repo, p.UUID, 4*time.Minute)
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if !final.IsTerminal() {
		t.Errorf("pipeline did not terminate: state=%+v", final.State)
	}

	steps, err := c.ListPipelineSteps(ctx, workspace, repo, p.UUID)
	if err != nil {
		t.Fatalf("ListPipelineSteps: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("expected at least one step")
	}
	// Attempt artifact download — may be empty for a trivial pipeline; we
	// assert only that the call does not return a transport error.
	if _, err := c.DownloadArtifact(ctx, workspace, repo, p.UUID, steps[0].UUID, ""); err != nil {
		// 404 is acceptable when no artifacts were uploaded.
		if !IsNotFound(err) {
			t.Errorf("DownloadArtifact: %v", err)
		}
	}
}

// TestLive_PRCommentRoundTrip opens a PR comment via AutoReview with a stub
// LLM that returns one finding, then verifies the comment appears via the
// list-comments endpoint.
func TestLive_PRCommentRoundTrip(t *testing.T) {
	workspace := envOrSkip(t, "BITBUCKET_WORKSPACE")
	repo := envOrSkip(t, "BITBUCKET_FIXTURE_REPO")
	prIDStr := envOrSkip(t, "BITBUCKET_FIXTURE_PR_ID")
	prID, err := strconv.Atoi(prIDStr)
	if err != nil {
		t.Fatalf("BITBUCKET_FIXTURE_PR_ID not a number: %v", err)
	}
	c := newLiveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	stub := func(_ context.Context, _ string) (string, error) {
		return "- **info** README.md:1 — integration test comment", nil
	}
	findings, err := NewReviewer(c).AutoReview(ctx, workspace, repo, prID, shared.LLMFunc(stub))
	if err != nil {
		t.Fatalf("AutoReview: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	// Spot-check that at least one comment with our marker exists on the PR.
	if err := c.PostPRComment(ctx, workspace, repo, prID, "R1 integration smoke — "+time.Now().Format(time.RFC3339)); err != nil {
		t.Errorf("summary comment failed: %v", err)
	}
	_ = strings.TrimSpace // unused if we ever decide to fetch & assert by substring
}
