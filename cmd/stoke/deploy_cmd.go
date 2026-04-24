package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/deploy"

	// Side-effect imports register the Vercel and Cloudflare adapters
	// with the deploy registry via their init()s. Without these blank
	// imports `deploy.Names()` would report only "fly" and the
	// multi-provider flag surface added in DP2-10 would reject every
	// non-fly provider as unknown. The fly adapter self-registers from
	// the top-level internal/deploy package so it needs no import
	// here.
	_ "github.com/RelayOne/r1/internal/deploy/cloudflare"
	_ "github.com/RelayOne/r1/internal/deploy/vercel"
)

// deployCmd is the `stoke deploy` entry point. DP2-10 extends the
// Fly-only MVP from Track B Task 22 into a multi-provider surface
// (Fly / Vercel / Cloudflare) selected via --provider or --auto.
//
// Modes (selected by flag combination):
//
//   - default — run a real deploy. When --provider fly, shells out to
//     flyctl via the legacy deploy.Deploy path (byte-identical to the
//     pre-DP2-10 behavior). When --provider vercel or cloudflare,
//     dispatches through the registry and the Deployer interface.
//   - --dry-run — fly: render fly.toml preview, exit 0, no network.
//     vercel/cloudflare: print a preview line naming the adapter and
//     exit 0 without invoking the CLI.
//   - --verify-only — skip any deploy, run a single net/http.Get
//     against --health-url. Unchanged from the Fly MVP.
//   - --auto — consult deploy.Detect(--dir) to pick the provider from
//     on-disk signals; if the signals are ambiguous or absent, print
//     the DetectResult and exit 2 so the operator can disambiguate.
//
// Exit codes (unchanged):
//
//	0 — healthy deploy (or dry-run/verify-only success)
//	1 — deploy succeeded but health check failed
//	2 — usage error, unsupported provider, flyctl missing
//	3 — deploy subprocess itself failed (non-zero exit)
func deployCmd(args []string) {
	code := runDeployCmd(args, os.Stdout, os.Stderr)
	os.Exit(code)
}

// runDeployCmd is the testable core of deployCmd. Writers are injected
// so tests can capture stdout/stderr without touching process state.
func runDeployCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)

	// Multi-provider surface (DP2-10). --provider names the target
	// adapter; --auto asks the stack detector to pick. Exactly one of
	// the two must resolve to a registered provider.
	provider := fs.String("provider", "fly", "Target provider: fly | vercel | cloudflare.")
	auto := fs.Bool("auto", false, "Auto-detect provider from repo signals (deploy.Detect). Mutually exclusive with --provider.")

	// Environment selectors (DP2-10). --env names the deploy
	// environment (preview by default); --prod is shorthand for
	// production. --env and --prod are mutually exclusive unless
	// --env happens to resolve to "production", which the operator
	// may have typed explicitly for CI clarity.
	env := fs.String("env", "preview", "Environment name. Vercel: maps to VERCEL_ENV. Cloudflare: maps to CF_ENV.")
	prod := fs.Bool("prod", false, "Production shorthand. Sets VERCEL_PROD=1 (Vercel) or CF_ENV=production (Cloudflare).")

	// Cloudflare mode selector (DP2-10). Workers is the 2026+ default
	// per spec §Cloudflare Flag Churn Mitigation; pages is the legacy
	// path.
	cfMode := fs.String("cf-mode", "workers", "Cloudflare deploy mode: workers | pages.")

	// Config-file generation (DP2-10). When set, Stoke calls
	// deploy.WriteIfAbsent for the detected/selected provider + stack
	// so a fresh repo gets a starter vercel.json or wrangler.toml.
	// Never overwrites existing files.
	writeConfig := fs.Bool("write-config", false, "Write provider config file (vercel.json / wrangler.toml) if absent before deploy.")

	// Vercel --prebuilt (DP2-10). Skips the server build by uploading
	// .vercel/output/ — signalled to the adapter via VERCEL_PREBUILT.
	prebuilt := fs.Bool("prebuilt", false, "Vercel: upload .vercel/output/ (--prebuilt). No-op for other providers.")

	// Legacy Fly-only flags (unchanged from Track B Task 22).
	app := fs.String("app", "", "Fly.io app name (required unless --dry-run).")
	region := fs.String("region", "iad", "Fly.io primary region.")
	dir := fs.String("dir", "", "Working directory for the deploy subprocess (default: cwd).")
	image := fs.String("image", "", "Docker image tag override; skips local build (fly only).")
	dryRun := fs.Bool("dry-run", false, "Print preview and exit without invoking the provider CLI.")
	verifyOnly := fs.Bool("verify-only", false, "Skip the deploy; run a single health check against --health-url.")
	healthURL := fs.String("health-url", "", "Override auto-derived health URL.")
	expectedBody := fs.String("expected-body", "", "Optional substring expected in the health response body.")
	flyctlPath := fs.String("flyctl", "", "Override the flyctl binary path (default: PATH lookup).")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: stoke deploy [--provider fly|vercel|cloudflare | --auto] [--env NAME | --prod] [--cf-mode workers|pages] [--write-config] [--prebuilt] [--dry-run | --verify-only] ...\n\n")
		fmt.Fprintf(stderr, "Ship the current workspace to the selected provider. --provider picks a registered adapter (%s);\n", strings.Join(deploy.Names(), ", "))
		fmt.Fprintf(stderr, "--auto consults deploy.Detect on --dir to infer one from on-disk signals.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// --verify-only is a standalone mode: no deploy, just an HTTP
	// probe. It short-circuits before provider resolution so
	// "is prod healthy?" works without any adapter registration.
	if *verifyOnly {
		return runVerifyOnly(stdout, stderr, *healthURL, *app, *expectedBody)
	}

	// Mutual exclusion: --provider (non-default) + --auto is a
	// usage bug. We detect "user set --provider explicitly" by
	// comparing the parsed value to the default "fly"; when the
	// user actually types --provider fly alongside --auto we still
	// flag it — operators should pick one.
	providerSetExplicitly := flagWasSet(fs, "provider")
	if providerSetExplicitly && *auto {
		fmt.Fprintln(stderr, "stoke deploy: --provider and --auto are mutually exclusive; pick one")
		return 2
	}

	// Mutual exclusion: --prod + --env when env != "production".
	// Operators who type both should mean the same thing; a mismatch
	// is a usage bug (CI config likely wired two incompatible
	// settings) and we refuse to guess which one was intended.
	envSetExplicitly := flagWasSet(fs, "env")
	if *prod && envSetExplicitly && *env != "production" {
		fmt.Fprintf(stderr, "stoke deploy: --prod and --env %q are mutually exclusive; --prod implies --env production\n", *env)
		return 2
	}

	// Resolve provider. --auto beats the defaulted --provider fly
	// flag so operators who just type `stoke deploy --auto` get the
	// detector, not a silent fly dispatch.
	resolved := *provider
	if *auto {
		detect := deploy.Detect(*dir)
		if detect.Provider == "" || detect.Ambiguous {
			reason := detect.Note
			if reason == "" {
				reason = "no provider signals found"
			}
			fmt.Fprintf(stderr, "stoke deploy: --auto could not resolve a provider: %s\n", reason)
			fmt.Fprintf(stderr, "  signals: [%s]\n", strings.Join(detect.Signals, ", "))
			fmt.Fprintf(stderr, "  retry with --provider <name> (%s)\n", strings.Join(deploy.Names(), ", "))
			return 2
		}
		resolved = detect.Provider
	}

	// Validate provider is registered. Unknown providers exit 2 with
	// a message listing every name in deploy.Names() so operators see
	// exactly what typo they made.
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	known := deploy.Names()
	if !containsString(known, resolved) {
		fmt.Fprintf(stderr, "stoke deploy: unknown --provider %q (valid: %s)\n", resolved, strings.Join(known, ", "))
		return 2
	}

	// Fly preserves the exact pre-DP2-10 behavior so operators and CI
	// pipelines do not regress. New providers flow through the
	// registry-based dispatch below.
	if resolved == "fly" {
		return runFlyDeploy(stdout, stderr, flyDeployArgs{
			app:          *app,
			region:       *region,
			dir:          *dir,
			image:        *image,
			dryRun:       *dryRun,
			healthURL:    *healthURL,
			expectedBody: *expectedBody,
			flyctlPath:   *flyctlPath,
		})
	}

	// Non-Fly providers (Vercel, Cloudflare). Build the DeployConfig
	// with env keys derived from flags. The adapter owns the argv
	// shape; this layer only supplies intent via well-known env keys
	// so the Deployer interface stays unchanged.
	cfg := deploy.DeployConfig{
		AppName:        *app,
		Dir:            *dir,
		HealthCheckURL: *healthURL,
		ExpectedBody:   *expectedBody,
		DryRun:         *dryRun,
		Env:            map[string]string{},
	}

	// Map --env / --prod per provider. Vercel reads VERCEL_PROD=1 for
	// production; Cloudflare reads CF_ENV. We always populate the env
	// keys named in the spec's §Provider-Selection CLI table even
	// when the adapter only consumes a subset, so future adapter
	// extensions can widen their Env usage without a CLI change.
	switch resolved {
	case "vercel":
		if *prod || *env == "production" {
			cfg.Env["VERCEL_PROD"] = "1"
			cfg.Env["VERCEL_ENV"] = "production"
		} else {
			cfg.Env["VERCEL_ENV"] = *env
		}
		if *prebuilt {
			cfg.Env["VERCEL_PREBUILT"] = "1"
		}
	case "cloudflare":
		if *prod {
			cfg.Env["CF_ENV"] = "production"
		} else {
			cfg.Env["CF_ENV"] = *env
		}
		cfg.Env["CF_MODE"] = strings.ToLower(strings.TrimSpace(*cfMode))
	}

	// --write-config before deploy. The template pair is provider +
	// stack; we use a conservative default stack per provider so
	// operators get a working starter file without a dedicated flag.
	// Overwrite is impossible — WriteIfAbsent stats first.
	if *writeConfig {
		stack := defaultConfigStack(resolved, cfg.Env)
		target := *dir
		if target == "" {
			target = "."
		}
		path, wrote, err := deploy.WriteIfAbsent(target, resolved, stack, map[string]string{
			"NAME": defaultConfigName(*app, resolved),
		})
		if err != nil {
			fmt.Fprintf(stderr, "stoke deploy: --write-config: %v\n", err)
			return 2
		}
		if wrote {
			fmt.Fprintf(stdout, "wrote %s (provider=%s stack=%s)\n", path, resolved, stack)
		} else {
			fmt.Fprintf(stdout, "config already present at %s; not overwriting\n", path)
		}
	}

	// Dry-run for non-Fly providers is a client-side preview: print
	// the resolved provider + env map summary and exit 0 without
	// invoking the adapter. The adapter itself does not have a
	// dry-run hook yet; adding one requires a Deployer interface
	// change and DP2-10's scope rules forbid that.
	if *dryRun {
		fmt.Fprintf(stdout, "# stoke deploy preview (provider=%s)\n", resolved)
		for _, k := range sortedKeys(cfg.Env) {
			fmt.Fprintf(stdout, "%s=%s\n", k, cfg.Env[k])
		}
		fmt.Fprintf(stdout, "dir=%s\n", cfg.Dir)
		fmt.Fprintf(stderr, "dry-run: %s adapter not invoked\n", resolved)
		return 0
	}

	// Dispatch through the registry. Get returns a fresh Deployer
	// per call so concurrent deploys cannot share mutable state.
	dep, err := deploy.Get(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "stoke deploy: %v\n", err)
		return 2
	}
	res, derr := dep.Deploy(context.Background(), cfg)
	if derr != nil {
		fmt.Fprintf(stderr, "stoke deploy: %v\n", derr)
		if res.Stderr != "" {
			fmt.Fprintf(stderr, "--- %s stderr ---\n%s\n", resolved, res.Stderr)
		}
		return 3
	}
	fmt.Fprintf(stdout, "deployed %s → %s\n", resolved, res.URL)
	if res.Stdout != "" {
		fmt.Fprintf(stdout, "--- %s stdout ---\n%s\n", resolved, res.Stdout)
	}

	// Post-deploy health check mirrors the Fly path so operators get
	// the same exit-code taxonomy regardless of provider.
	probeURL := res.URL
	if *healthURL != "" {
		probeURL = *healthURL
	}
	if probeURL == "" {
		// No URL to probe: treat as success but warn. The adapter
		// should have populated res.URL; an empty URL is a bug we
		// surface rather than silently swallow.
		fmt.Fprintln(stderr, "stoke deploy: no URL to health-check; skipping probe")
		return 0
	}
	ok, detail := deploy.HealthCheck(context.Background(), probeURL, *expectedBody)
	fmt.Fprintf(stdout, "%s\n", detail)
	if !ok {
		return 1
	}
	return 0
}

// flyDeployArgs bundles the Fly-specific flag subset so runFlyDeploy's
// signature stays readable. Keeping the struct local (unexported) keeps
// the refactor surgical — no new exported types.
type flyDeployArgs struct {
	app, region, dir, image string
	dryRun                  bool
	healthURL, expectedBody string
	flyctlPath              string
}

// runFlyDeploy is the pre-DP2-10 Fly path, extracted verbatim so the
// byte-identical-behavior guarantee is easy to verify by diff. The
// only change is that --provider validation moved up into
// runDeployCmd; this function is invoked only after the caller has
// already decided "yes, provider=fly".
func runFlyDeploy(stdout, stderr io.Writer, a flyDeployArgs) int {
	if a.app == "" && !a.dryRun {
		fmt.Fprintf(stderr, "stoke deploy: --app is required unless --dry-run is set\n")
		return 2
	}

	cfg := deploy.DeployConfig{
		Provider:       deploy.ProviderFly,
		AppName:        a.app,
		Region:         a.region,
		Dir:            a.dir,
		DockerImage:    a.image,
		HealthCheckURL: a.healthURL,
		ExpectedBody:   a.expectedBody,
		DryRun:         a.dryRun,
		FlyctlPath:     a.flyctlPath,
	}

	ctx := context.Background()
	res, derr := deploy.Deploy(ctx, cfg)
	if derr != nil {
		if errors.Is(derr, deploy.ErrFlyctlNotFound) || errors.Is(derr, deploy.ErrAppNameMissing) || errors.Is(derr, deploy.ErrProviderUnsupported) {
			fmt.Fprintf(stderr, "stoke deploy: %v\n", derr)
			return 2
		}
		fmt.Fprintf(stderr, "stoke deploy: %v\n", derr)
		if res.Stderr != "" {
			fmt.Fprintf(stderr, "--- flyctl stderr ---\n%s\n", res.Stderr)
		}
		return 3
	}

	if res.DryRun {
		fmt.Fprintln(stdout, res.Stdout)
		fmt.Fprintf(stderr, "dry-run: fly.toml preview above; no flyctl invoked\n")
		return 0
	}

	fmt.Fprintf(stdout, "deployed %s → %s\n", res.AppName, res.URL)
	if res.Stdout != "" {
		fmt.Fprintf(stdout, "--- flyctl stdout ---\n%s\n", res.Stdout)
	}

	probeURL := res.URL
	if a.healthURL != "" {
		probeURL = a.healthURL
	}
	ok, detail := deploy.HealthCheck(ctx, probeURL, a.expectedBody)
	fmt.Fprintf(stdout, "%s\n", detail)
	if !ok {
		return 1
	}
	return 0
}

// runVerifyOnly runs a single health check against the given URL (or
// derived from app name) and returns an exit code. Unchanged from the
// Fly MVP — the verify-only mode is provider-agnostic.
func runVerifyOnly(stdout, stderr io.Writer, healthURL, app, expectedBody string) int {
	url := healthURL
	if url == "" && app != "" {
		url = fmt.Sprintf("https://%s.fly.dev", app)
	}
	if url == "" {
		fmt.Fprintf(stderr, "stoke deploy: --verify-only requires --health-url or --app\n")
		return 2
	}
	ok, detail := deploy.HealthCheck(context.Background(), url, expectedBody)
	fmt.Fprintf(stdout, "%s\n", detail)
	if !ok {
		return 1
	}
	return 0
}

// parseDeployProvider maps the legacy --provider flag to a Provider
// enum. Retained for backwards compatibility with callers that still
// go through the enum surface (TestParseDeployProvider). DP2-10's
// dispatch uses deploy.Get(string) instead, but removing this helper
// would break existing tests for no gain.
func parseDeployProvider(s string) (deploy.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "fly":
		return deploy.ProviderFly, nil
	case "vercel":
		return deploy.ProviderVercel, nil
	case "cloudflare":
		return deploy.ProviderCloudflare, nil
	case "docker":
		return deploy.ProviderDocker, fmt.Errorf("--provider docker is not wired (see specs/deploy-phase2.md)")
	case "kamal":
		return deploy.ProviderKamal, fmt.Errorf("--provider kamal is not wired (see specs/deploy-phase2.md)")
	default:
		return deploy.ProviderUnknown, fmt.Errorf("unknown --provider %q (valid: %s)", s, strings.Join(deploy.Names(), ", "))
	}
}

// flagWasSet reports whether the caller passed flag name on argv. Used
// to distinguish "operator typed --provider fly alongside --auto" (a
// usage bug worth flagging) from "operator typed --auto and the flag
// set carries the default for --provider" (fine).
func flagWasSet(fs *flag.FlagSet, name string) bool {
	var found bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// containsString is a tiny local helper so deploy_cmd.go does not grow
// a slices dependency for one membership check. Existing code in this
// file already prefers stdlib-only imports.
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// sortedKeys returns map keys in deterministic order. Used for
// dry-run output so tests can assert on byte-identical preview text.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defaultConfigStack picks a sensible stack label for --write-config.
// Vercel defaults to "next" (the most common Vercel project shape);
// Cloudflare honors the --cf-mode env key. Operators who need a
// different template can hand-edit the generated file or skip
// --write-config entirely.
func defaultConfigStack(provider string, env map[string]string) string {
	switch provider {
	case "vercel":
		return "next"
	case "cloudflare":
		mode := strings.ToLower(strings.TrimSpace(env["CF_MODE"]))
		if mode == "pages" {
			return "pages"
		}
		return "workers"
	default:
		return ""
	}
}

// defaultConfigName yields the {{NAME}} substitution for template
// rendering. We prefer --app when set and fall back to a
// provider-suffixed default so the generated file is immediately
// valid for wrangler/vercel to parse even before the operator edits
// it.
func defaultConfigName(app, provider string) string {
	if strings.TrimSpace(app) != "" {
		return app
	}
	return "stoke-" + provider + "-app"
}
