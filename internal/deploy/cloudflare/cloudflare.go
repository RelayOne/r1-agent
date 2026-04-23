// Package cloudflare implements the Wrangler-backed deploy.Deployer for
// Cloudflare Workers (primary) and Cloudflare Pages (legacy behind
// CF_MODE=pages).
//
// DP2-6 lands the Deployer itself on top of the DP2-5 NDJSON tailer
// (ndjson.go in this package). The shape follows the spec's Cloudflare
// Adapter section verbatim:
//
//   - Version-gate on `wrangler --version`; refuse wrangler <3.x
//     because that line is where the NDJSON output path stabilized
//     (spec §Cloudflare Flag Churn Mitigation). The spec's
//     "recommended" target is 4.x+, but we only hard-fail below 3.x
//     so the adapter keeps working through the Pages→Workers
//     consolidation churn Cloudflare announced in April 2026.
//
//   - Spawn `wrangler deploy` (Workers default) or
//     `wrangler pages deploy <dir>` (CF_MODE=pages) with
//     WRANGLER_OUTPUT_FILE_PATH set to a temp file. The NDJSON tailer
//     runs concurrently and fans documented progress events onto the
//     bus under the "deploy.progress" subtype (nil-safe — a nil bus
//     skips the publish).
//
//   - URL extraction: primary is the final `deploy-complete` NDJSON
//     event's payload; fallback is a Wrangler stdout regex for a
//     workers.dev URL (spec §URL extraction). Stdout is secondary,
//     not authoritative.
//
//   - Rollback: `wrangler rollback` for Workers,
//     `wrangler pages deployment rollback` for Pages. Flag surface
//     whitelisted (spec §Cloudflare Flag Churn Mitigation #4) —
//     no config passthrough, just the documented set as of 2026-04.
//
// Scope boundaries (per DP2-6):
//
//   - Does NOT modify ndjson.go (DP2-5's helper is consumed as-is).
//   - Does NOT add CLI wiring (DP2-10 owns `cmd/stoke deploy` flags).
//   - Does NOT import any third-party SDK (spec §Library Preferences).
//     Uses stdlib + internal/bus + internal/deploy + internal/logging.
package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/deploy"
	"github.com/ericmacdougall/stoke/internal/logging"
)

// wranglerMinMajor is the minimum Wrangler major version the adapter
// will drive. Wrangler 3 stabilized WRANGLER_OUTPUT_FILE_PATH; older
// releases emit a non-NDJSON human log we cannot reliably parse
// (spec §Cloudflare Flag Churn Mitigation: "< 3.x: warn + refuse").
//
// This is exposed as a var (not a const) so future consolidation-era
// tests can bump the floor without rippling through callers.
var wranglerMinMajor = 3

// workersDevURLRe is the stdout fallback URL shape for `wrangler deploy`.
// Wrangler 3/4 consistently prints a `https://<subdomain>.workers.dev`
// line after a successful deploy even when NDJSON is plumbed, so it
// makes a cheap second source of truth when the NDJSON deploy-complete
// event is absent or malformed (spec §URL extraction fallback cascade
// step #1).
var workersDevURLRe = regexp.MustCompile(`https://[a-z0-9][a-z0-9.-]*\.workers\.dev[^\s"']*`)

// wranglerVersionRe pulls the major.minor.patch from `wrangler --version`.
// As of wrangler 3.x the output is literally `⛅️ wrangler 3.78.0`; the
// 4.x line dropped the emoji in some shells but keeps "wrangler X.Y.Z"
// as a substring. We only look for the first version token to stay
// tolerant of banner/emoji churn.
var wranglerVersionRe = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// progressTypes is the closed set of NDJSON event types the adapter
// fans out onto the bus as deploy.progress events. Any other types
// (unknown/future) stay in the tailer log and are not republished —
// we surface only the operator-meaningful progression markers here.
var progressTypes = map[string]struct{}{
	"build-start":      {},
	"build-complete":   {},
	"upload-progress":  {},
	"deploy-complete":  {},
}

// EvtDeployProgress is the bus event type emitted for each wrangler
// progress marker. The string is the canonical cross-provider subtype
// (spec §Post-Deploy Verification Parity: "event taxonomy identical").
const EvtDeployProgress bus.EventType = "deploy.progress"

// packageBus is the optional *bus.Bus the adapter publishes
// deploy.progress events on. It is nil by default; callers that want
// to observe progression register a bus via SetBus(). Keeping the bus
// as package state (rather than threading it through DeployConfig)
// preserves the existing deploy.Deployer interface from DP2-1 — we
// cannot add fields without breaking the Fly adapter's shape.
//
// Guarded by busMu so SetBus calls during initialization don't race
// the Deploy goroutine that reads the pointer.
var (
	busMu      sync.RWMutex
	packageBus *bus.Bus
)

// SetBus registers the bus on which the cloudflare adapter should
// publish deploy.progress events. Pass nil to disable publication.
//
// Intended to be called exactly once from the process main (or test
// setup) before the first Deploy. Calling concurrently with Deploy is
// safe but produces a documented race on *which* events land on the
// old bus vs the new one; no hang, no crash.
func SetBus(b *bus.Bus) {
	busMu.Lock()
	packageBus = b
	busMu.Unlock()
}

// getBus returns the currently registered bus, or nil if none.
// Readers never block on writers because the lock is held for the
// pointer load only.
func getBus() *bus.Bus {
	busMu.RLock()
	b := packageBus
	busMu.RUnlock()
	return b
}

// cloudflareDeployer is the deploy.Deployer implementation for
// Cloudflare. The zero value is ready to use; all per-call state
// (temp paths, argv, child procs) lives in local variables inside
// Deploy so concurrent Deploy calls do not share state.
type cloudflareDeployer struct{}

// newCloudflareDeployer is the factory registered with the deploy
// package's registry via init(). Kept unexported — external callers
// resolve a *cloudflareDeployer through deploy.Get("cloudflare").
func newCloudflareDeployer() deploy.Deployer {
	return &cloudflareDeployer{}
}

// Name satisfies deploy.Deployer. The returned value must match the
// key passed to deploy.Register so registry round-trips are
// reflexive.
func (*cloudflareDeployer) Name() string { return "cloudflare" }

// Deploy runs `wrangler deploy` (or `wrangler pages deploy <dir>`
// when cfg.Env["CF_MODE"]=="pages"), tailing the NDJSON output file
// for progress events.
//
// The returned DeployResult has URL populated from the NDJSON
// deploy-complete payload when present, falling back to the
// workers.dev URL in stdout. Stdout/Stderr are always populated so
// operators can read the build log on both success and failure.
func (*cloudflareDeployer) Deploy(ctx context.Context, cfg deploy.DeployConfig) (deploy.DeployResult, error) {
	if cfg.DryRun {
		// DP2-10 will wire --dry-run through the CLI layer. For now
		// keep a minimal, network-free preview so unit tests that
		// exercise the DryRun branch do not need a fake wrangler.
		return deploy.DeployResult{
			Provider: deploy.ProviderCloudflare,
			DryRun:   true,
			Stdout:   fmt.Sprintf("# wrangler dry-run preview (mode=%s)\n", cfMode(cfg)),
		}, nil
	}

	wranglerBin, err := resolveWrangler()
	if err != nil {
		return deploy.DeployResult{}, err
	}

	if err := assertWranglerVersion(ctx, wranglerBin); err != nil {
		return deploy.DeployResult{}, err
	}

	mode := cfMode(cfg)
	args, err := buildWranglerArgs(mode, cfg)
	if err != nil {
		return deploy.DeployResult{}, err
	}

	// WRANGLER_OUTPUT_FILE_PATH is where wrangler writes the NDJSON
	// stream. We pre-create the file (empty) so the tailer's
	// openWhenReady succeeds immediately rather than racing the
	// child's first write.
	tmpDir, err := os.MkdirTemp("", "stoke-wrangler-*")
	if err != nil {
		return deploy.DeployResult{}, fmt.Errorf("cloudflare.Deploy: mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	ndjsonPath := filepath.Join(tmpDir, "wrangler.ndjson")
	if err := os.WriteFile(ndjsonPath, nil, 0o644); err != nil {
		return deploy.DeployResult{}, fmt.Errorf("cloudflare.Deploy: pre-create ndjson: %w", err)
	}

	// Tailer runs on its own goroutine with a derived ctx we cancel
	// after the child exits. completeCh captures the deploy-complete
	// payload the moment we see it; the tailer must not block, so we
	// use a non-blocking send with a 1-buffered channel (only the
	// first deploy-complete matters).
	log := logging.Component("cloudflare-deployer")
	tailCtx, tailCancel := context.WithCancel(ctx)
	defer tailCancel()

	completeCh := make(chan deployComplete, 1)
	tailDone := make(chan struct{})
	b := getBus()

	onEvent := func(evt Event) {
		if _, ok := progressTypes[evt.Type]; ok {
			publishProgress(b, evt, cfg)
		}
		if evt.Type == "deploy-complete" {
			if dc, ok := decodeDeployComplete(evt.Raw); ok {
				select {
				case completeCh <- dc:
				default:
				}
			}
		}
	}

	go func() {
		defer close(tailDone)
		if err := TailNDJSON(tailCtx, ndjsonPath, onEvent); err != nil &&
			!errors.Is(err, context.Canceled) {
			log.Debug("ndjson tailer exited with error",
				slog.String("err", err.Error()))
		}
	}()

	// Build the child command. We use CommandContext so the parent's
	// ctx cancel propagates to the child via SIGKILL; Setpgid=true
	// puts the wrangler tree in its own process group so a botched
	// deploy cannot leave zombie build workers behind (spec §Existing
	// Patterns to Follow: "process spawn + stream parsing").
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, wranglerBin, args...)
	cmd.Dir = cfg.Dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = buildChildEnv(cfg, ndjsonPath)

	runErr := cmd.Run()

	// Drain any remaining NDJSON lines before we stop the tailer:
	// small files, fast poll, 100ms is plenty in practice and bounded
	// so a wedged tailer cannot hang the deploy path.
	select {
	case dc := <-completeCh:
		completeCh <- dc // put it back for the post-cancel read below
	case <-time.After(100 * time.Millisecond):
	}
	tailCancel()
	<-tailDone

	// Pick up any deploy-complete that landed during the drain.
	var dc deployComplete
	select {
	case dc = <-completeCh:
	default:
	}

	url := dc.URL
	if url == "" {
		if m := workersDevURLRe.FindString(stdout.String()); m != "" {
			url = m
		}
	}

	result := deploy.DeployResult{
		Provider: deploy.ProviderCloudflare,
		AppName:  cfg.AppName,
		URL:      url,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	if runErr != nil {
		return result, fmt.Errorf("cloudflare.Deploy: wrangler %s: %v\nstderr tail: %s",
			strings.Join(args, " "), runErr, tailString(stderr.String(), 500))
	}
	return result, nil
}

// Verify performs a post-deploy HTTP health check on the deployed
// URL. Parity with Fly/Vercel is intentional (spec §Post-Deploy
// Verification Parity): same HealthCheck helper, same 200-or-fail
// contract, no retry inside Verify.
//
// When cfg.HealthCheckURL is empty, Verify falls back to the URL
// captured on the most recent Deploy call for the same cfg; but since
// DeployResult does not round-trip through cfg, the caller is
// expected to plumb HealthCheckURL when they want a non-default
// target. Empty URL → (false, "...url is empty...").
func (*cloudflareDeployer) Verify(ctx context.Context, cfg deploy.DeployConfig) (bool, string) {
	url := cfg.HealthCheckURL
	return deploy.HealthCheck(ctx, url, cfg.ExpectedBody)
}

// Rollback reverts to the previous Worker (or Pages deployment) for
// cfg. Argument surface is the spec's whitelisted set as of 2026-04:
//
//   - Workers: `wrangler rollback --message "stoke auto-rollback" --yes`
//     (version id is resolved by wrangler from the most recent
//     deployment when omitted; explicit id would require the Pages
//     adapter to plumb `versions list --json` which DP2-6 does not
//     own).
//   - Pages: `wrangler pages deployment rollback <deployment-id>
//     --project-name <name>` — Pages has no implicit "previous"
//     selector, so callers must pass the deployment id via
//     cfg.Env["CF_PAGES_DEPLOYMENT_ID"].
//
// Rollback does NOT re-verify; the caller's repair loop decides
// whether to run Verify afterward.
func (*cloudflareDeployer) Rollback(ctx context.Context, cfg deploy.DeployConfig) error {
	wranglerBin, err := resolveWrangler()
	if err != nil {
		return err
	}

	mode := cfMode(cfg)
	var args []string
	switch mode {
	case "pages":
		deployID := cfg.Env["CF_PAGES_DEPLOYMENT_ID"]
		projectName := cfg.AppName
		if projectName == "" {
			projectName = cfg.Env["CF_PAGES_PROJECT"]
		}
		if projectName == "" {
			return errors.New("cloudflare.Rollback: pages mode requires AppName or CF_PAGES_PROJECT")
		}
		if deployID == "" {
			return errors.New("cloudflare.Rollback: pages mode requires CF_PAGES_DEPLOYMENT_ID")
		}
		args = []string{
			"pages", "deployment", "rollback", deployID,
			"--project-name", projectName,
		}
	default: // workers
		args = []string{
			"rollback",
			"--message", "stoke auto-rollback",
			"--yes",
		}
	}

	cmd := exec.CommandContext(ctx, wranglerBin, args...)
	cmd.Dir = cfg.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = buildChildEnv(cfg, "") // no NDJSON tail for rollback

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloudflare.Rollback: wrangler %s: %v\nstderr tail: %s",
			strings.Join(args, " "), err, tailString(stderr.String(), 500))
	}
	return nil
}

// cfMode reads CF_MODE from cfg.Env; default is "workers". The string
// is lower-cased so operators who pass "Pages" don't trip up the
// switch statements downstream.
func cfMode(cfg deploy.DeployConfig) string {
	m := strings.ToLower(strings.TrimSpace(cfg.Env["CF_MODE"]))
	if m == "" {
		return "workers"
	}
	return m
}

// buildWranglerArgs constructs the argv for `wrangler deploy` or
// `wrangler pages deploy`. Honors the spec's flag whitelist
// (§Cloudflare Flag Churn Mitigation #4) — no unknown flag
// passthrough.
//
// Pages mode reads the output directory from cfg.Env["CF_PAGES_DIR"]
// (required); workers mode honors cfg.Env["CF_WORKER_ENV"] as
// --env when present.
func buildWranglerArgs(mode string, cfg deploy.DeployConfig) ([]string, error) {
	switch mode {
	case "pages":
		dir := strings.TrimSpace(cfg.Env["CF_PAGES_DIR"])
		if dir == "" {
			return nil, errors.New("cloudflare.Deploy: pages mode requires CF_PAGES_DIR")
		}
		args := []string{"pages", "deploy", dir}
		if name := strings.TrimSpace(cfg.AppName); name != "" {
			args = append(args, "--project-name", name)
		}
		if branch := strings.TrimSpace(cfg.Env["CF_PAGES_BRANCH"]); branch != "" {
			args = append(args, "--branch", branch)
		}
		return args, nil
	case "workers", "":
		args := []string{"deploy"}
		if env := strings.TrimSpace(cfg.Env["CF_WORKER_ENV"]); env != "" {
			args = append(args, "--env", env)
		}
		if name := strings.TrimSpace(cfg.AppName); name != "" {
			args = append(args, "--name", name)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("cloudflare.Deploy: unknown CF_MODE %q (want \"workers\" or \"pages\")", mode)
	}
}

// buildChildEnv layers cfg.Env onto the parent environment and injects
// WRANGLER_OUTPUT_FILE_PATH. When ndjsonPath is empty (rollback path)
// the env var is omitted so wrangler does not spam the parent's FS.
//
// We start from os.Environ rather than scrubbing because wrangler
// relies on the operator's home-dir OAuth cache (~/.wrangler) for
// non-token auth (spec §Gotchas implicitly). Scrubbing would force
// every deploy to re-authenticate.
func buildChildEnv(cfg deploy.DeployConfig, ndjsonPath string) []string {
	env := os.Environ()
	if ndjsonPath != "" {
		env = append(env, "WRANGLER_OUTPUT_FILE_PATH="+ndjsonPath)
	}
	for k, v := range cfg.Env {
		// Skip our internal CF_MODE/CF_PAGES_* markers — they are
		// Stoke-side routing hints, not wrangler env vars.
		if strings.HasPrefix(k, "CF_MODE") || strings.HasPrefix(k, "CF_PAGES_") || strings.HasPrefix(k, "CF_WORKER_") {
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

// resolveWrangler returns the wrangler binary path. STOKE_WRANGLER_BIN
// overrides exec.LookPath so tests can stamp a shell script at a
// known location without touching PATH; operators use it to pin a
// specific wrangler release for reproducible deploys.
func resolveWrangler() (string, error) {
	if override := strings.TrimSpace(os.Getenv("STOKE_WRANGLER_BIN")); override != "" {
		return override, nil
	}
	p, err := exec.LookPath("wrangler")
	if err != nil {
		return "", errors.New("cloudflare.Deploy: wrangler not found on PATH (install wrangler or set STOKE_WRANGLER_BIN)")
	}
	return p, nil
}

// assertWranglerVersion runs `wrangler --version` and confirms the
// reported major version is >= wranglerMinMajor. Any parse failure is
// a hard error — we would rather refuse to deploy than assume a
// version we cannot verify.
//
// Uses a 10s timeout inside the caller's ctx; wrangler has no reason
// to take longer than that to print a version banner.
func assertWranglerVersion(parent context.Context, binPath string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	var out strings.Builder
	cmd := exec.CommandContext(ctx, binPath, "--version")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloudflare.Deploy: wrangler --version failed: %w", err)
	}

	m := wranglerVersionRe.FindStringSubmatch(out.String())
	if len(m) < 4 {
		return fmt.Errorf("cloudflare.Deploy: cannot parse wrangler version from %q", tailString(out.String(), 120))
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return fmt.Errorf("cloudflare.Deploy: invalid wrangler major %q: %w", m[1], err)
	}
	if major < wranglerMinMajor {
		return fmt.Errorf("cloudflare.Deploy: wrangler %s.%s.%s detected; Stoke requires wrangler v%d+ (install a newer wrangler)",
			m[1], m[2], m[3], wranglerMinMajor)
	}
	return nil
}

// deployComplete is the narrow shape of the NDJSON deploy-complete
// event we actually consume. The spec's §Wrangler NDJSON Contract
// lists more payload keys; we only decode the ones Stoke needs so
// wrangler field additions don't break the parser.
type deployComplete struct {
	URL          string `json:"url"`
	VersionID    string `json:"version_id"`
	DeploymentID string `json:"deployment_id"`
}

// decodeDeployComplete tries to pull the deploy-complete payload out
// of the raw JSON line. Returns ok=false on any decode error; the
// caller then falls back to the stdout regex URL source.
func decodeDeployComplete(raw json.RawMessage) (deployComplete, bool) {
	var dc deployComplete
	if err := json.Unmarshal(raw, &dc); err != nil {
		return deployComplete{}, false
	}
	return dc, true
}

// publishProgress emits a deploy.progress event onto the bus. Nil-safe
// — when no bus is registered this is a no-op.
//
// The payload includes the wrangler event type (so subscribers can
// distinguish build-start from deploy-complete) plus the raw wrangler
// JSON so richer downstream consumers (e.g. the TUI) can decode
// wrangler-specific fields without a second round trip through
// the ledger.
func publishProgress(b *bus.Bus, evt Event, cfg deploy.DeployConfig) {
	if b == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"provider":      "cloudflare",
		"wrangler_type": evt.Type,
		"message":       evt.Message,
		"app_name":      cfg.AppName,
		"raw":           evt.Raw,
	})
	if err != nil {
		return
	}
	_ = b.Publish(bus.Event{
		Type:      EvtDeployProgress,
		EmitterID: "cloudflare-deployer",
		Payload:   payload,
	})
}

// tailString returns the last n bytes of s with an ellipsis prefix
// when truncated. Used to keep stderr tails in error messages
// bounded.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// init registers the cloudflare adapter with the deploy registry so
// callers can resolve it via deploy.Get("cloudflare"). Register panics
// on duplicate, which is intentional — two init() registrations for
// the same name almost always indicate a mis-linked binary.
func init() {
	deploy.Register("cloudflare", newCloudflareDeployer)
}
