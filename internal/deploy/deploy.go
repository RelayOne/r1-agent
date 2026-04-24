// Package deploy implements Stoke's Track B Task 22 deploy adapter.
//
// Scope for this MVP commit:
//
//   - Fly.io only. The broader spec (specs/deploy-executor.md,
//     specs/deploy-phase2.md) covers Vercel, Cloudflare, Docker,
//     and Kamal, all deliberately deferred so this commit stays
//     surgical.
//
//   - Shell out to flyctl rather than importing superfly/fly-go.
//     Operators already have flyctl configured on their workstations;
//     Stoke has no business pulling the Fly API directly when a
//     first-party CLI is available. The package accepts an override
//     path so tests can stub it, and refuses to execute real deploys
//     unless the binary is present.
//
//   - Dry-run mode renders a fly.toml preview string without any
//     subprocess or network calls. Operators use --dry-run to preview
//     what Stoke would ship before committing to a real deploy.
//
//   - Post-deploy verification is a single net/http.Get against the
//     deployed URL (auto-derived https://<app>.fly.dev by default, or
//     overridden via HealthCheckURL). Optional substring match lets
//     callers assert "the homepage contains 'Sentinel'" without a
//     full browser step — the richer browser-based verify cascade
//     described in the spec is follow-up work.
//
// This package is consumed by internal/executor/deploy.go, which
// wraps Deploy/HealthCheck into an Executor with two acceptance
// criteria (DEPLOY-COMMIT-MATCH, DEPLOY-HEALTH-200) so the descent
// engine treats deploy failures the same way it treats code failures.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Provider is the target platform for a deploy. Only ProviderFly is
// wired for this MVP; the rest are reserved constants so the package
// map compiles and future commits can add them without touching
// call sites.
type Provider int

const (
	// ProviderUnknown is the zero value. Callers that leave it
	// unset get a clear validation error rather than a silent
	// dispatch to some default provider.
	ProviderUnknown Provider = iota

	// ProviderFly targets fly.io. Wired in this commit.
	ProviderFly

	// ProviderVercel targets vercel.com. Reserved name only;
	// the adapter lands in specs/deploy-phase2.md.
	ProviderVercel

	// ProviderCloudflare targets Cloudflare Workers/Pages.
	// Reserved name only; the adapter lands in
	// specs/deploy-phase2.md.
	ProviderCloudflare

	// ProviderDocker targets a plain Docker registry push +
	// remote pull/restart. Reserved name only; the adapter
	// lands in specs/deploy-phase2.md.
	ProviderDocker

	// ProviderKamal targets Kamal-managed hosts. Reserved name
	// only; the adapter lands in specs/deploy-phase2.md.
	ProviderKamal
)

// String returns the lower-case canonical name. Used in logs and in
// the fly.toml preview header.
func (p Provider) String() string {
	switch p {
	case ProviderFly:
		return "fly"
	case ProviderVercel:
		return "vercel"
	case ProviderCloudflare:
		return "cloudflare"
	case ProviderDocker:
		return "docker"
	case ProviderKamal:
		return "kamal"
	case ProviderUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// DeployConfig is the input to Deploy and HealthCheck. Zero values
// are valid for most fields; AppName is the one required setting on
// a real (non-DryRun) deploy.
type DeployConfig struct {
	// Provider selects the target platform. Only ProviderFly is
	// honored in this MVP.
	Provider Provider

	// AppName is the fly.io app name. Required on non-dry-run.
	AppName string

	// Region is the fly.io primary region (e.g. "iad"). Defaults
	// to "iad" when empty.
	Region string

	// DockerImage, if set, overrides the dockerfile build. Stoke
	// passes it as --image to flyctl deploy so the child uses a
	// prebuilt registry image instead of performing a local build.
	DockerImage string

	// Dir is the working directory the flyctl subprocess runs in.
	// Must contain fly.toml (and, for image-less deploys, the
	// Dockerfile or buildpack-compatible source). Empty means the
	// caller's current working directory.
	Dir string

	// HealthCheckURL overrides the auto-derived
	// https://<AppName>.fly.dev. Use this when the app is
	// fronted by a custom domain, or for --verify-only against an
	// existing deploy whose AppName the caller does not know.
	HealthCheckURL string

	// ExpectedBody is an optional substring that must appear in
	// the health check response body. Empty means "any non-empty
	// 2xx body passes".
	ExpectedBody string

	// DryRun, when true, skips flyctl invocation and network I/O.
	// Deploy returns a DeployResult with DryRun=true and a populated
	// Stdout containing the fly.toml preview.
	DryRun bool

	// FlyctlPath overrides exec.LookPath("flyctl"). Tests stamp a
	// fake binary; operators can point at a pinned version.
	FlyctlPath string

	// Env is a map of extra environment variables passed to the
	// flyctl subprocess. FLY_API_TOKEN is the common entry here.
	// The parent process env is preserved; Env overlays on top.
	Env map[string]string
}

// DeployResult summarises what Deploy produced. DryRun runs populate
// Stdout with the fly.toml preview and leave CommitHash, URL, etc.
// empty.
type DeployResult struct {
	// Provider echoes the configured provider.
	Provider Provider

	// AppName echoes cfg.AppName.
	AppName string

	// URL is the deployed URL. Empty on dry run. On a real deploy
	// it defaults to https://<app>.fly.dev, or HealthCheckURL if
	// the caller overrode it.
	URL string

	// CommitHash is the git HEAD at deploy time, captured by the
	// executor layer (BuildCriteria reads git HEAD separately for
	// the DEPLOY-COMMIT-MATCH AC). Empty when Deploy runs outside
	// a git repo.
	CommitHash string

	// DryRun mirrors cfg.DryRun. True means no network calls were
	// made; Stdout holds the preview.
	DryRun bool

	// Stdout is the flyctl stdout (real deploy) or the generated
	// fly.toml preview (dry run).
	Stdout string

	// Stderr is the flyctl stderr. Populated on both success and
	// failure paths so operators can read the build log.
	Stderr string
}

// Sentinel errors the executor layer keys off to pick the right
// repair/env-fix path.
var (
	// ErrFlyctlNotFound means flyctl is not on PATH and no
	// override was supplied. Env-fix class: operator installs
	// flyctl (or sets FlyctlPath) and retries.
	ErrFlyctlNotFound = errors.New("deploy: flyctl not found on PATH (install flyctl or set FlyctlPath)")

	// ErrAppNameMissing means cfg.AppName was empty on a
	// non-dry-run deploy. Validation class: caller bug.
	ErrAppNameMissing = errors.New("deploy: AppName is required (set cfg.AppName)")

	// ErrProviderUnsupported means the configured provider is
	// not ProviderFly. The other constants are reserved names for
	// specs/deploy-phase2.md; callers that set them today get
	// this error.
	ErrProviderUnsupported = errors.New("deploy: only ProviderFly is wired in this MVP (see specs/deploy-phase2.md)")
)

// Deploy executes the configured deployment.
//
// DryRun branch: renders a fly.toml preview via flyctlTomlPreview
// and returns immediately. No subprocess, no network, no filesystem
// writes — safe to call from any test.
//
// Real branch: resolves flyctl (cfg.FlyctlPath → exec.LookPath),
// invokes `flyctl deploy` in cfg.Dir with --app / --region /
// optional --image, and captures stdout+stderr. On non-zero exit,
// returns an error that wraps the first 500 chars of stderr so the
// operator sees exactly what flyctl complained about.
//
// Deploy does NOT perform the post-deploy health check — callers
// (or the executor's DEPLOY-HEALTH-200 AC) invoke HealthCheck
// separately so a failed health check is a distinct, actionable
// failure class rather than being folded into the deploy error.
func Deploy(ctx context.Context, cfg DeployConfig) (DeployResult, error) {
	if cfg.Provider == ProviderUnknown {
		cfg.Provider = ProviderFly
	}
	if cfg.Provider != ProviderFly {
		return DeployResult{}, ErrProviderUnsupported
	}

	if cfg.DryRun {
		preview := flyctlTomlPreview(cfg)
		return DeployResult{
			Provider: cfg.Provider,
			AppName:  cfg.AppName,
			DryRun:   true,
			Stdout:   preview,
		}, nil
	}

	if cfg.AppName == "" {
		return DeployResult{}, ErrAppNameMissing
	}

	return flyDeploy(ctx, cfg)
}

// HealthCheck fetches url and returns (ok, detail).
//
// ok semantics:
//   - Transport error / DNS / timeout → ok=false, detail names the
//     underlying error.
//   - Non-200 status → ok=false, detail includes the status code so
//     operators can spot a 502 vs a 404 at a glance.
//   - 200 with empty body → ok=false (an empty 200 from a fly.io
//     edge usually means the machine is still cold-starting).
//   - 200 with ExpectedBody set and body lacks the substring →
//     ok=false with a "body missing <expected>" detail.
//   - 200 with non-empty body and (optional) substring match →
//     ok=true, detail is "200 OK (<n> bytes)".
//
// HealthCheck uses a 15-second context timeout derived from ctx. It
// does NOT retry; the descent engine's repair loop decides whether
// a transient failure is worth another attempt.
func HealthCheck(ctx context.Context, url, expectedBody string) (bool, string) {
	if url == "" {
		return false, "health check: url is empty"
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Sprintf("health check: build request: %v", err)
	}
	req.Header.Set("User-Agent", "stoke-deploy/1 (+health)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("health check: GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, fmt.Sprintf("health check: read body: %v", readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("health check: got HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if len(body) == 0 {
		return false, "health check: 200 but empty body"
	}
	if expectedBody != "" && !strings.Contains(string(body), expectedBody) {
		return false, fmt.Sprintf("health check: body missing expected substring %q", expectedBody)
	}
	return true, fmt.Sprintf("health check: 200 OK (%d bytes)", len(body))
}

// flyDeploy runs `flyctl deploy` in cfg.Dir and returns the result.
func flyDeploy(ctx context.Context, cfg DeployConfig) (DeployResult, error) {
	flyctl, err := resolveFlyctl(cfg.FlyctlPath)
	if err != nil {
		return DeployResult{}, err
	}

	region := cfg.Region
	if region == "" {
		region = defaultRegion
	}

	args := []string{"deploy", "--app", cfg.AppName, "--region", region}
	if cfg.DockerImage != "" {
		args = append(args, "--image", cfg.DockerImage)
	}

	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, flyctl, args...)
	cmd.Dir = cfg.Dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Overlay cfg.Env on top of the parent environment. We do NOT
	// scrub the parent env — flyctl may rely on FLY_API_TOKEN or
	// FLY_ACCESS_TOKEN already exported by the operator's shell.
	if len(cfg.Env) > 0 {
		cmd.Env = append(cmd.Env, envToSlice(cfg.Env)...)
	}

	runErr := cmd.Run()

	url := cfg.HealthCheckURL
	if url == "" {
		url = fmt.Sprintf("https://%s.fly.dev", cfg.AppName)
	}

	result := DeployResult{
		Provider: cfg.Provider,
		AppName:  cfg.AppName,
		URL:      url,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	if runErr != nil {
		return result, fmt.Errorf("deploy: flyctl %s: %v\nstderr tail: %s",
			strings.Join(args, " "), runErr, tail(stderr.String(), 500))
	}
	return result, nil
}

// flyctlTomlPreview renders a fly.toml body from cfg without any
// network or subprocess calls. Used by DryRun=true and by the CLI's
// --dry-run flag.
//
// The preview is intentionally minimal — matches what a human
// operator would check into a fresh repo — because the executor's
// job is to deploy, not to design application config. Operators
// with existing fly.toml files should leave them in place; Stoke's
// flyctl subprocess reads the on-disk file, not this preview.
func flyctlTomlPreview(cfg DeployConfig) string {
	app := cfg.AppName
	if app == "" {
		app = "<app-name-required>"
	}
	region := cfg.Region
	if region == "" {
		region = defaultRegion
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# fly.toml preview (provider=%s, dry-run)\n", cfg.Provider)
	fmt.Fprintf(&b, "app = %q\n", app)
	fmt.Fprintf(&b, "primary_region = %q\n\n", region)
	if cfg.DockerImage != "" {
		fmt.Fprintf(&b, "[build]\n  image = %q\n\n", cfg.DockerImage)
	} else {
		b.WriteString("[build]\n  dockerfile = \"Dockerfile\"\n\n")
	}
	b.WriteString("[http_service]\n")
	b.WriteString("  internal_port = 8080\n")
	b.WriteString("  force_https = true\n")
	b.WriteString("  auto_stop_machines = \"stop\"\n")
	b.WriteString("  auto_start_machines = true\n")
	b.WriteString("  min_machines_running = 0\n\n")
	b.WriteString("[[http_service.checks]]\n")
	b.WriteString("  grace_period = \"10s\"\n")
	b.WriteString("  interval = \"30s\"\n")
	b.WriteString("  method = \"GET\"\n")
	b.WriteString("  path = \"/\"\n")
	b.WriteString("  timeout = \"5s\"\n")
	return b.String()
}

// resolveFlyctl returns the flyctl binary path, honoring an explicit
// override before falling back to exec.LookPath.
func resolveFlyctl(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	p, err := exec.LookPath("flyctl")
	if err != nil {
		return "", ErrFlyctlNotFound
	}
	return p, nil
}

// envToSlice converts a map to the KEY=VALUE slice shape
// exec.Cmd.Env expects. Keys are emitted in arbitrary order; flyctl
// does not care.
func envToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// tail returns the last n bytes of s. Used to keep flyctl stderr
// bounded in the error message so a 50KB build log does not blow up
// a CLI error line.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// defaultRegion matches flyctl's own iad default. Changing this
// would shift where greenfield apps launch; leave as-is.
const defaultRegion = "iad"
