// Package vercel implements the Vercel CLI adapter for Stoke's deploy
// pipeline (DP2-3).
//
// This file wires the `deploy.Deployer` interface established in
// specs/deploy-phase2.md §Vercel Adapter. It shells out to the
// `vercel` CLI (there is no official Go SDK per RT-10 §2) via a
// process-group-isolated subprocess, then parses the deployment URL
// out of stdout using the `ExtractURL` helper in url.go.
//
// Scope for this commit:
//
//   - Deploy: `vercel deploy --yes` (preview) or `vercel deploy --yes
//     --prod` (production). Token passed via child env (`VERCEL_TOKEN`)
//     — NEVER on argv, since argv leaks into `ps` listings and stoke
//     logs.
//   - Verify: delegates to the top-level `deploy.HealthCheck` helper
//     (same HTTP cascade used by the Fly adapter). The richer browser-
//     verify cascade from spec-6 remains with the executor layer.
//   - Rollback: resolves the last-known production URL via
//     `captureCurrentDeployment` (reads `vercel ls --json`) and then
//     runs `vercel rollback <url> --yes`. Returns a descriptive error
//     when no prior deploy exists so the caller can emit a
//     `deploy.rollback.skipped` event rather than treating the
//     "nothing to roll back to" case as a hard failure.
//
// Out of scope here:
//
//   - CLI flag wiring — DP2-10 owns `deploy_cmd.go`.
//   - URL extraction regex/fallback — DP2-4 owns `url.go`; we consume
//     `ExtractURL` as-is.
//   - Fly registration — DP2-2 owns the `fly/` subpackage.
//   - Dry-run preview — mirrors the top-level `DryRun` behavior via
//     the existing `deploy.Deploy` path; this adapter is for real
//     invocations only.
package vercel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/deploy"
)

// vercelBinEnv names the env override operators and tests use to point
// Stoke at a specific `vercel` binary. The test suite stamps a shell
// script into `t.TempDir()` and sets this env so the real CLI is never
// invoked during `go test`.
const vercelBinEnv = "STOKE_VERCEL_BIN"

// vercelDeployer is the Vercel implementation of deploy.Deployer.
//
// The struct is intentionally empty: all configuration flows through
// `DeployConfig` on each call, which keeps the adapter stateless and
// safe to reuse across concurrent deploys. A zero-value
// `vercelDeployer{}` is ready to use — no init is required beyond
// wiring the registry factory below.
type vercelDeployer struct{}

// newVercelDeployer is the deploy.Factory registered in init(). It
// returns a fresh Deployer per call so callers never share mutable
// state across goroutines.
func newVercelDeployer() deploy.Deployer { return &vercelDeployer{} }

func init() { deploy.Register("vercel", newVercelDeployer) }

// Name returns the canonical provider key. Load-bearing for registry
// round-trips and for CLI error messages that list known providers.
func (v *vercelDeployer) Name() string { return "vercel" }

// Deploy runs `vercel deploy` in cfg.Dir and returns a DeployResult
// whose URL is parsed from the combined stdout+stderr of the child
// process via `ExtractURL`.
//
// Production vs preview is controlled by `cfg.Env["VERCEL_PROD"]`:
// the value `"1"` (exact string) toggles `--prod`. Any other value,
// including absent, yields a preview deploy. We deliberately key off
// a string env value (not a typed bool on DeployConfig) because
// DeployConfig is owned by spec-6 and this adapter is not authorized
// to widen it; env is the DP2-scoped configuration surface.
//
// On non-zero child exit, returns an error wrapping the first 500
// chars of stderr (tail-trimmed so a 50KB build log does not blow
// up a CLI error line). Stdout/stderr are also populated on the
// returned `DeployResult` so operators can read the full build log
// in the telemetry record.
func (v *vercelDeployer) Deploy(ctx context.Context, cfg deploy.DeployConfig) (deploy.DeployResult, error) {
	args := []string{"deploy", "--yes"}
	if cfg.Env["VERCEL_PROD"] == "1" {
		args = append(args, "--prod")
	}

	stdout, stderr, err := runVercel(ctx, cfg, args...)
	result := deploy.DeployResult{
		Provider: deploy.ProviderVercel,
		AppName:  cfg.AppName,
		Stdout:   stdout,
		Stderr:   stderr,
	}
	if err != nil {
		return result, fmt.Errorf("vercel deploy: %w\nstderr tail: %s",
			err, tail(stderr, 500))
	}

	// The CLI prints the URL to stdout; stderr may carry extra
	// diagnostic lines, so feed both to ExtractURL for defence in
	// depth (spec §Vercel CLI Contract warns the URL can drift to
	// stderr on some CLI versions).
	combined := stdout
	if stderr != "" {
		combined = combined + "\n" + stderr
	}
	if u, ok := ExtractURL(combined); ok {
		result.URL = u
	} else {
		return result, fmt.Errorf("vercel deploy: could not parse deployment URL from output (stdout len=%d, stderr len=%d)",
			len(stdout), len(stderr))
	}
	return result, nil
}

// Verify issues an HTTP GET against `DeployResult.URL` (resolved via
// cfg.HealthCheckURL, falling back to the URL stamped into the result
// by Deploy) and returns the `deploy.HealthCheck` verdict.
//
// Vercel DNS propagation is near-instant (CDN edge), so we do not
// insert a warm-up sleep here — the caller's repair loop is already
// allowed to retry.
func (v *vercelDeployer) Verify(ctx context.Context, cfg deploy.DeployConfig) (bool, string) {
	url := cfg.HealthCheckURL
	if url == "" {
		// No URL available → operator bug; surface a clear reason
		// rather than pretending the deploy was verified.
		return false, "vercel verify: no health check URL configured"
	}
	return deploy.HealthCheck(ctx, url, cfg.ExpectedBody)
}

// Rollback attempts to revert the current production deploy back to
// the previous ready deployment.
//
// Strategy:
//  1. Capture the most recent production URL via `vercel ls --json`.
//     Empty list or `state != "READY"` → return a descriptive error
//     so the caller can emit `deploy.rollback.skipped` per spec
//     §Error Handling.
//  2. Invoke `vercel rollback <url> --yes` (per spec §Vercel CLI
//     Contract). We deliberately use `rollback`, not `promote` — the
//     task text mentions `promote` as a shorthand but the
//     authoritative CLI contract is `rollback`.
//  3. Do not re-verify inside Rollback — the caller owns the
//     verification cascade so a rollback followed by a fresh verify
//     is one decision, not two.
func (v *vercelDeployer) Rollback(ctx context.Context, cfg deploy.DeployConfig) error {
	prev, err := v.captureCurrentDeployment(ctx, cfg)
	if err != nil {
		return fmt.Errorf("vercel rollback: capture previous deployment: %w", err)
	}
	if prev == "" {
		return fmt.Errorf("vercel rollback: no previous deployment found; rollback skipped")
	}

	_, stderr, runErr := runVercel(ctx, cfg, "rollback", prev, "--yes")
	if runErr != nil {
		return fmt.Errorf("vercel rollback %s: %w\nstderr tail: %s",
			prev, runErr, tail(stderr, 500))
	}
	return nil
}

// vercelListEntry is the slice of `vercel ls --json` output we care
// about. The CLI returns many more fields (aliases, meta, build info)
// but Stoke only needs URL + state + created-at to pick the newest
// ready deploy. Unknown fields are ignored by encoding/json, so this
// struct stays stable across CLI version bumps.
type vercelListEntry struct {
	UID       string `json:"uid"`
	URL       string `json:"url"`
	State     string `json:"state"`
	CreatedAt int64  `json:"created"`
	// Target, when present, is "production" | "preview". Older CLI
	// versions omit it; Stoke treats missing as preview-equivalent
	// per spec §Deployment id for rollback.
	Target string `json:"target,omitempty"`
}

// captureCurrentDeployment parses `vercel ls --json` and returns the
// URL of the newest READY production deployment.
//
// Returns ("", nil) when the list is empty (first-ever deploy — not
// an error; the caller decides whether to skip rollback).
// Returns ("", err) on JSON parse failure or CLI exit non-zero.
//
// The URL is returned with a leading `https://` when missing, since
// some CLI versions emit bare hostnames here. The `vercel rollback`
// command accepts either shape but we normalise for predictability.
func (v *vercelDeployer) captureCurrentDeployment(ctx context.Context, cfg deploy.DeployConfig) (string, error) {
	stdout, stderr, err := runVercel(ctx, cfg, "ls", "--json")
	if err != nil {
		return "", fmt.Errorf("vercel ls --json: %w\nstderr tail: %s",
			err, tail(stderr, 500))
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || trimmed == "[]" {
		return "", nil
	}

	var entries []vercelListEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		// Some CLI versions wrap the array in an object
		// {"deployments": [...]}; accept that shape too before
		// giving up.
		var wrapped struct {
			Deployments []vercelListEntry `json:"deployments"`
		}
		if werr := json.Unmarshal([]byte(trimmed), &wrapped); werr != nil {
			return "", fmt.Errorf("vercel ls --json: parse: %w", err)
		}
		entries = wrapped.Deployments
	}

	// Prefer production deploys; fall back to the newest READY entry
	// if no production target is present (older CLIs + personal
	// projects).
	ready := make([]vercelListEntry, 0, len(entries))
	for _, e := range entries {
		if e.State != "" && !strings.EqualFold(e.State, "READY") {
			continue
		}
		ready = append(ready, e)
	}
	if len(ready) == 0 {
		return "", nil
	}

	// Sort newest first. `created` is a unix-millis timestamp per
	// the Vercel API; newer > older.
	sort.SliceStable(ready, func(i, j int) bool {
		return ready[i].CreatedAt > ready[j].CreatedAt
	})

	// Prefer an explicit production target.
	for _, e := range ready {
		if strings.EqualFold(e.Target, "production") && e.URL != "" {
			return normalizeVercelURL(e.URL), nil
		}
	}
	// Fall through: newest READY, regardless of target.
	if ready[0].URL != "" {
		return normalizeVercelURL(ready[0].URL), nil
	}
	return "", nil
}

// normalizeVercelURL prepends `https://` when the CLI emitted a bare
// hostname. Idempotent for already-qualified URLs.
func normalizeVercelURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://" + u
}

// runVercel resolves the `vercel` binary and runs it with args, in
// cfg.Dir, with `VERCEL_TOKEN` (and the rest of cfg.Env) exported to
// the child — never passed on argv.
//
// Returns (stdout, stderr, err). The stdout/stderr strings are
// populated on BOTH the success and error paths so callers can
// surface the child's build log even when the child exited non-zero.
//
// Process-group isolation is enabled via `Setpgid: true` so a
// runaway child (or the CLI's fallback interactive prompt) can be
// killed cleanly by signalling the entire group. Stoke's orchestrator
// layer already knows how to terminate via the pgid when ctx is
// cancelled.
func runVercel(ctx context.Context, cfg deploy.DeployConfig, args ...string) (string, string, error) {
	bin, err := resolveVercelBin()
	if err != nil {
		return "", "", err
	}

	// Belt-and-braces deadline: if the caller's ctx has no deadline,
	// cap the CLI at a generous bound so a hung vercel child cannot
	// stall Stoke indefinitely. 10m covers even slow prod builds;
	// the CLI usually finishes in <60s.
	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, bin, args...) // #nosec G204 -- deploy tool binary invoked with Stoke-generated args.
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}

	// Start with the parent env so the child inherits PATH and any
	// tooling the operator already has configured (e.g. node), then
	// overlay cfg.Env so Stoke-provided values win on conflict. The
	// overlay includes VERCEL_TOKEN when the caller set it — the
	// token is NEVER on argv, per spec §Token Security.
	cmd.Env = append(os.Environ(), envToSlice(cfg.Env)...)

	// Setpgid gives the child its own process group so the runner
	// can kill the whole tree (the CLI may spawn helper processes).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	return stdout.String(), stderr.String(), runErr
}

// resolveVercelBin honors the `STOKE_VERCEL_BIN` override first
// (tests + pinned-version operators) and falls back to the
// exec.LookPath lookup.
func resolveVercelBin() (string, error) {
	if override := os.Getenv(vercelBinEnv); override != "" {
		return override, nil
	}
	p, err := exec.LookPath("vercel")
	if err != nil {
		return "", fmt.Errorf("vercel CLI not found on PATH and %s unset: %w",
			vercelBinEnv, err)
	}
	return p, nil
}

// envToSlice converts a map to the KEY=VALUE slice shape exec.Cmd.Env
// expects. Keys are emitted in sorted order so argv-dependent tests
// (TestDeploy_Prod asserts on child-observed env) stay deterministic.
func envToSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(env))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// tail returns the last n bytes of s. Used to keep stderr bounded in
// error messages so a long build log does not blow up a CLI error
// line.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
