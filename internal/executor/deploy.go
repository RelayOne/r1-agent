package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ericmacdougall/stoke/internal/deploy"
	// Side-effect imports: each provider subpackage's init() registers
	// its factory in deploy.registry so NewDeployExecutor can resolve
	// "fly" / "vercel" / "cloudflare" via deploy.Get. Without these
	// blank imports the executor would only see the fly adapter
	// (registered in the deploy package itself), leaving Provider=
	// Vercel / Cloudflare unresolvable even after DP2-3 / DP2-6 landed
	// their Deployer implementations.
	_ "github.com/ericmacdougall/stoke/internal/deploy/cloudflare"
	_ "github.com/ericmacdougall/stoke/internal/deploy/vercel"
	"github.com/ericmacdougall/stoke/internal/plan"
)

// DeployExecutor wraps internal/deploy's Fly.io adapter behind the
// Executor interface. Track B Task 22 — MVP scope, fly.io only.
// Vercel / Cloudflare / Docker / Kamal drivers land in
// specs/deploy-phase2.md.
//
// Design choices documented on Deploy itself:
//
//   - DryRun first. `stoke deploy --dry-run` renders a fly.toml
//     preview without any subprocess or network calls, so operators
//     can review what Stoke would ship before committing.
//
//   - flyctl subprocess, not fly-go. Operators already have flyctl
//     configured; importing superfly/fly-go would create a second,
//     weaker surface that could drift out of sync with the CLI.
//
//   - Health check is a single net/http.Get against the deployed
//     URL. Wired through BuildCriteria as VerifyFunc on an
//     AcceptanceCriterion so the descent engine's repair/env-fix
//     tiers engage the same way they do for CodeExecutor.
type DeployExecutor struct {
	// Config is the deploy configuration. Callers populate it from
	// CLI flags or a plan annotation; the executor treats it as
	// immutable for the lifetime of an Execute call.
	Config deploy.DeployConfig

	// deployer is the provider-specific adapter resolved at
	// construction time via deploy.Get(cfg.Provider.String()). For
	// ProviderFly this is the flyAdapter (which delegates to the
	// package-level deploy.Deploy, preserving byte-identical behavior
	// with the pre-DP2-11 path). For Vercel / Cloudflare it is the
	// adapter registered by their respective subpackages (blank-
	// imported above).
	deployer deploy.Deployer

	// deployerErr captures any error returned by deploy.Get during
	// construction so Execute can surface it unchanged (per DP2-11
	// requirement #5: "descent downstream sees it"). We stash it here
	// rather than failing the constructor because the Executor
	// interface has no error-returning constructor shape and callers
	// already treat Execute as the failure-surface.
	deployerErr error
}

// NewDeployExecutor constructs an executor bound to cfg. Kept as a
// constructor (not a struct literal) so future fields — a logger,
// an event emitter — can be added without churning call sites.
//
// The deployer is resolved from the registry using cfg.Provider's
// canonical string. An unknown provider (or ProviderUnknown, whose
// String() is "unknown") does not fail here — the error is retained
// on the executor and surfaced by Execute, which matches how the
// descent engine expects per-task failures to arrive.
func NewDeployExecutor(cfg deploy.DeployConfig) *DeployExecutor {
	e := &DeployExecutor{Config: cfg}
	e.deployer, e.deployerErr = deploy.Get(cfg.Provider.String())
	return e
}

// TaskType reports TaskDeploy.
func (e *DeployExecutor) TaskType() TaskType { return TaskDeploy }

// Execute dispatches to the provider adapter selected at construction
// time and wraps the result in a DeployDeliverable. Errors bubble up
// unwrapped so callers can inspect sentinels (deploy.ErrFlyctlNotFound,
// ErrAppNameMissing, and the adapter-specific errors the Vercel /
// Cloudflare packages return).
//
// If the registry lookup in NewDeployExecutor failed (unknown provider,
// typically ProviderUnknown) the stashed error is returned here so the
// descent engine's task-result path handles it like any other Execute
// failure — no panic, no silent dispatch to the Fly default.
func (e *DeployExecutor) Execute(ctx context.Context, _ Plan, _ EffortLevel) (Deliverable, error) {
	if e.deployerErr != nil {
		return nil, e.deployerErr
	}
	res, err := e.deployer.Deploy(ctx, e.Config)
	if err != nil {
		return nil, err
	}
	return DeployDeliverable{Result: res}, nil
}

// BuildCriteria returns two acceptance criteria — both using
// VerifyFunc rather than shell Command, so the descent engine
// handles them via the same programmatic gate the research/browser
// executors use.
//
//  1. DEPLOY-COMMIT-MATCH — local git HEAD agrees with
//     result.CommitHash. On a dry run (no CommitHash captured) the
//     criterion soft-passes to keep --dry-run CLI invocations
//     non-destructive.
//  2. DEPLOY-HEALTH-200 — GET against the deployed URL returns 200
//     with a non-empty body (and optional ExpectedBody substring
//     match). This is the user-visible "the deploy actually works"
//     signal.
func (e *DeployExecutor) BuildCriteria(_ Task, d Deliverable) []plan.AcceptanceCriterion {
	dd, ok := d.(DeployDeliverable)
	if !ok {
		return nil
	}
	cfg := e.Config
	url := firstNonEmpty(cfg.HealthCheckURL, dd.Result.URL)
	return []plan.AcceptanceCriterion{
		{
			ID:          "DEPLOY-COMMIT-MATCH",
			Description: "deployed commit matches local HEAD",
			VerifyFunc: func(ctx context.Context) (bool, string) {
				// Dry-run soft-pass: no deploy happened, so there's
				// no remote commit to compare against. Returning
				// true here lets --dry-run exit cleanly through the
				// descent engine without triggering a repair loop
				// over a commit hash Stoke never captured.
				if dd.Result.DryRun {
					return true, "dry run: commit match skipped"
				}
				head, err := gitHead(ctx, cfg.Dir)
				if err != nil {
					return false, fmt.Sprintf("commit match: %v", err)
				}
				if dd.Result.CommitHash == "" {
					return true, fmt.Sprintf("commit match: local HEAD=%s (no deploy commit captured)", head)
				}
				if !strings.HasPrefix(head, dd.Result.CommitHash) && !strings.HasPrefix(dd.Result.CommitHash, head) {
					return false, fmt.Sprintf("commit match: local HEAD=%s but deploy commit=%s", head, dd.Result.CommitHash)
				}
				return true, fmt.Sprintf("commit match: %s", head)
			},
		},
		{
			ID:          "DEPLOY-HEALTH-200",
			Description: fmt.Sprintf("GET %s returns 200 with expected body", url),
			VerifyFunc: func(ctx context.Context) (bool, string) {
				if dd.Result.DryRun {
					return true, "dry run: health check skipped"
				}
				return deploy.HealthCheck(ctx, url, cfg.ExpectedBody)
			},
		},
	}
}

// BuildRepairFunc returns a T4 repair primitive that re-runs Deploy
// with DryRun forced off. The descent engine calls this when
// BuildCriteria reports a failing AC; the directive argument is
// ignored for deploy (repair is always "retry the same deploy"),
// but the signature matches the interface so the engine does not
// special-case TaskDeploy.
func (e *DeployExecutor) BuildRepairFunc(_ Plan) func(context.Context, string) error {
	return func(ctx context.Context, _ string) error {
		if e.deployerErr != nil {
			return e.deployerErr
		}
		cfg := e.Config
		cfg.DryRun = false
		_, err := e.deployer.Deploy(ctx, cfg)
		return err
	}
}

// BuildEnvFixFunc returns a T5 env-fix primitive. Returns true when
// the failure looks transient (timeout, 5xx, DNS temporary failure)
// so the descent engine retries; false for permanent failures
// (auth, 4xx other than 408/429, config errors) so the engine
// surfaces the error to the operator instead of burning budget on
// a hopeless retry.
func (e *DeployExecutor) BuildEnvFixFunc() func(context.Context, string, string) bool {
	return func(_ context.Context, rootCause, stderr string) bool {
		low := strings.ToLower(rootCause + " " + stderr)
		for _, transient := range []string{
			"timeout",
			"502",
			"503",
			"504",
			"temporary failure",
			"i/o timeout",
			"connection reset",
			"no such host",
		} {
			if strings.Contains(low, transient) {
				return true
			}
		}
		return false
	}
}

// DeployDeliverable is the Deliverable shape for TaskDeploy.
type DeployDeliverable struct {
	// Result is the raw output of deploy.Deploy.
	Result deploy.DeployResult
}

// Summary renders a one-line summary for logs and the TUI.
func (d DeployDeliverable) Summary() string {
	mode := "live"
	if d.Result.DryRun {
		mode = "dry-run"
	}
	return fmt.Sprintf("deploy(%s/%s): %s @ %s",
		d.Result.Provider, mode, d.Result.AppName, d.Result.URL)
}

// Size returns the combined stdout+stderr byte count. Convergence
// uses this as a sanity check — a zero-size deliverable on a
// non-dry-run deploy means flyctl produced no output, which is
// unusual enough to merit investigation.
func (d DeployDeliverable) Size() int {
	return len(d.Result.Stdout) + len(d.Result.Stderr)
}

// gitHead returns the short HEAD SHA for dir via `git rev-parse
// HEAD`. Empty dir means the parent process's cwd — matches what
// the deploy subprocess would see.
func gitHead(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// firstNonEmpty returns the first non-empty string, or "" if all
// are empty. Used to pick HealthCheckURL override over the
// auto-derived fly.dev URL.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Compile-time assertions.
var (
	_ Executor    = (*DeployExecutor)(nil)
	_ Deliverable = DeployDeliverable{}
)
