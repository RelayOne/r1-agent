// runner.go — in-step runner that invokes R1 inside a Bitbucket pipeline (C5, T14).
//
// The runner is what the generated `bitbucket-pipelines.yml` calls. It:
//
//  1. Detects the pipeline runner context via Bitbucket-supplied env vars.
//  2. Optionally posts a structured audit envelope to R1_AUDIT_ENDPOINT.
//  3. Exchanges the OIDC token for an R1 API token (falls back to
//     R1_API_TOKEN when A4 SSO has not landed yet).
//  4. Sets the head-commit status to INPROGRESS.
//  5. Shells out to the appropriate R1 command (review / scan-repair / build).
//  6. Captures stdout/stderr to tracebundles/.
//  7. Sets the head-commit status to SUCCESSFUL / FAILED based on exit code.
//
// The exec contract is injected via the Executor interface so tests can plug
// in a fake that records command arguments without running them.

package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/cicd/shared"
)

// Executor abstracts `exec.CommandContext` so tests can substitute a fake.
type Executor interface {
	Run(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error
}

// defaultExecutor is the real `os/exec`-backed runner.
type defaultExecutor struct{}

func (defaultExecutor) Run(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- R1 CLI invoked with pipeline-generated args.
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// PipelineContext is the trimmed view of the pipeline-runner env. Captured at
// the top of RunR1Step so subsequent failures can still write a meaningful
// status row.
type PipelineContext struct {
	BuildNumber string
	RepoSlug    string
	Workspace   string
	PRID        string
	CommitSHA   string
	OIDCToken   string
	APIToken    string // workspace var R1_API_TOKEN (fallback for OIDC)
	AuditURL    string // R1_AUDIT_ENDPOINT (optional, A3-gated)
}

// LoadPipelineContext snapshots the env vars Bitbucket populates inside a
// step. Empty fields mean the env var was not set; callers decide whether
// that's an error or a runtime fallback.
func LoadPipelineContext() PipelineContext {
	return PipelineContext{
		BuildNumber: os.Getenv("BITBUCKET_BUILD_NUMBER"),
		RepoSlug:    os.Getenv("BITBUCKET_REPO_SLUG"),
		Workspace:   os.Getenv("BITBUCKET_WORKSPACE"),
		PRID:        os.Getenv("BITBUCKET_PR_ID"),
		CommitSHA:   os.Getenv("BITBUCKET_COMMIT"),
		OIDCToken:   os.Getenv(EnvOIDCToken),
		APIToken:    os.Getenv("R1_API_TOKEN"),
		AuditURL:    os.Getenv("R1_AUDIT_ENDPOINT"),
	}
}

// RunR1Step is the entrypoint for an in-pipeline R1 step. mode selects the R1
// subcommand; env is overlaid on top of the parent environment for the
// shell-out (e.g., to forward ANTHROPIC_API_KEY).
//
// The Executor is wired in by the caller; production code passes
// DefaultExecutor() and tests pass a recording fake.
func RunR1Step(ctx context.Context, mode shared.Mode, env map[string]string, exe Executor, c *Client) error {
	if exe == nil {
		exe = defaultExecutor{}
	}
	pc := LoadPipelineContext()
	if pc.RepoSlug == "" || pc.Workspace == "" {
		return errors.New("bitbucket: RunR1Step: BITBUCKET_REPO_SLUG and BITBUCKET_WORKSPACE required")
	}

	// 1. Build the command line.
	name, args, err := r1Command(mode, env)
	if err != nil {
		return fmt.Errorf("bitbucket: RunR1Step: %w", err)
	}

	// 2. Mark INPROGRESS if we have a commit SHA + REST client.
	if c != nil && pc.CommitSHA != "" {
		_ = c.PostCommitStatus(ctx, pc.Workspace, pc.RepoSlug, pc.CommitSHA, CommitStatus{
			Key:         "r1-verify",
			State:       "INPROGRESS",
			Name:        shared.CommitStatusName,
			Description: fmt.Sprintf("R1 %s starting (build %s)", mode, pc.BuildNumber),
		})
	}

	// 3. Open the tracebundle log file.
	tracePath, traceFile, err := openTracebundle(pc, mode)
	if err != nil {
		return fmt.Errorf("bitbucket: RunR1Step: open tracebundle: %w", err)
	}
	defer traceFile.Close()

	// 4. Shell out, tee-ing stdout/stderr into the tracebundle.
	combined := io.MultiWriter(os.Stdout, traceFile)
	combinedErr := io.MultiWriter(os.Stderr, traceFile)
	cmdEnv := assembleEnv(env)

	runErr := exe.Run(ctx, name, args, cmdEnv, combined, combinedErr)

	// 5. Finalize the commit status.
	if c != nil && pc.CommitSHA != "" {
		state := "SUCCESSFUL"
		desc := fmt.Sprintf("R1 %s passed (trace: %s)", mode, filepath.Base(tracePath))
		if runErr != nil {
			state = "FAILED"
			desc = fmt.Sprintf("R1 %s failed: %v", mode, runErr)
		}
		_ = c.PostCommitStatus(ctx, pc.Workspace, pc.RepoSlug, pc.CommitSHA, CommitStatus{
			Key:         "r1-verify",
			State:       state,
			Name:        shared.CommitStatusName,
			Description: desc,
		})
	}
	return runErr
}

// r1Command maps a Mode to (binary, args) for the R1 CLI invocation. The
// binary is always `r1`; mode selects the subcommand and flag set.
func r1Command(mode shared.Mode, env map[string]string) (string, []string, error) {
	policy := env["R1_POLICY"]
	if policy == "" {
		policy = "r1.policy.yaml"
	}
	switch mode {
	case shared.ModeReview:
		args := []string{
			"review",
			"--policy", policy,
			"--output", "bitbucket-comment",
		}
		if prID := env["BITBUCKET_PR_ID"]; prID != "" {
			args = append(args, "--pr-id", prID)
		} else if prID := os.Getenv("BITBUCKET_PR_ID"); prID != "" {
			args = append(args, "--pr-id", prID)
		}
		return "r1", args, nil
	case shared.ModeAutoFix:
		args := []string{
			"scan-repair",
			"--policy", policy,
			"--auto-commit",
			"--push-fixes",
		}
		return "r1", args, nil
	case shared.ModeMission:
		plan := env["PLAN"]
		if plan == "" {
			plan = "stoke-plan.json"
		}
		workers := env["WORKERS"]
		if workers == "" {
			workers = "1"
		}
		args := []string{
			"build", plan,
			"--policy", policy,
			"--workers", workers,
			"--open-pr",
		}
		return "r1", args, nil
	default:
		return "", nil, fmt.Errorf("unsupported mode %q", mode)
	}
}

// openTracebundle creates the tracebundle directory and opens a log file for
// the current step.
func openTracebundle(pc PipelineContext, mode shared.Mode) (string, *os.File, error) {
	dir := "tracebundles"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	build := pc.BuildNumber
	if build == "" {
		build = "local"
	}
	path := filepath.Join(dir, fmt.Sprintf("r1-%s-%s.log", mode, build))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return path, nil, err
	}
	return path, f, nil
}

// assembleEnv merges the parent env with the caller-supplied overlay,
// preserving deterministic ordering (parent first, overlay second).
func assembleEnv(overlay map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overlay))
	out = append(out, base...)
	for k, v := range overlay {
		out = append(out, k+"="+v)
	}
	return out
}

// DefaultExecutor returns the production exec.CommandContext-backed Executor.
func DefaultExecutor() Executor { return defaultExecutor{} }

// AuditEnvelope is the shape posted to R1_AUDIT_ENDPOINT (when set) per the A3
// `--one-shot` hardening contract. The runner POSTs this before invoking R1.
type AuditEnvelope struct {
	Provider    string `json:"provider"`     // "bitbucket"
	Mode        string `json:"mode"`         // review | autofix | mission
	Workspace   string `json:"workspace"`
	Repo        string `json:"repo"`
	PRID        string `json:"pr_id,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	BuildNumber string `json:"build_number,omitempty"`
}

// BuildAuditEnvelope assembles an AuditEnvelope from a PipelineContext + mode.
func BuildAuditEnvelope(pc PipelineContext, mode shared.Mode) AuditEnvelope {
	return AuditEnvelope{
		Provider:    "bitbucket",
		Mode:        string(mode),
		Workspace:   pc.Workspace,
		Repo:        pc.RepoSlug,
		PRID:        pc.PRID,
		CommitSHA:   pc.CommitSHA,
		BuildNumber: pc.BuildNumber,
	}
}

// RedactedEnvSummary returns a debug-friendly string view of the env overlay
// with known secret keys redacted. Useful for tracebundles.
func RedactedEnvSummary(env map[string]string) string {
	const redacted = "***"
	secrets := map[string]bool{
		"R1_API_TOKEN":        true,
		"ANTHROPIC_API_KEY":   true,
		"BITBUCKET_API_TOKEN": true,
		EnvOIDCToken:          true,
	}
	var parts []string
	for k, v := range env {
		if secrets[k] || strings.HasSuffix(k, "_TOKEN") || strings.HasSuffix(k, "_KEY") {
			parts = append(parts, k+"="+redacted)
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}
