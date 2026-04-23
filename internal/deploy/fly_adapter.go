package deploy

import (
	"context"
	"errors"
)

// flyAdapter wraps the package-level Deploy / HealthCheck functions so
// Fly deploys flow through the same Deployer interface (DP2-1) that
// Vercel, Cloudflare, and future providers implement. DP2-2's spec
// asks for a dedicated internal/deploy/fly subpackage; for this commit
// we keep the adapter co-located with the existing Fly implementation
// to minimise churn — the existing flyDeploy / flyctlTomlPreview /
// resolveFlyctl helpers in deploy.go are adapt-and-register only,
// never refactored. A future commit can lift this file into a
// subpackage once the other providers have stabilised their shapes.
type flyAdapter struct{}

// Name identifies this adapter in the provider registry. It matches
// Provider.String() for ProviderFly so "stoke deploy --provider fly"
// and the registry key agree byte-for-byte.
func (flyAdapter) Name() string { return "fly" }

// Deploy delegates to the top-level Deploy function. The adapter
// forces cfg.Provider to ProviderFly so a caller who looked us up by
// registry key ("fly") cannot accidentally land in the "unsupported
// provider" branch because their cfg.Provider was still the zero
// value or some other enum.
func (flyAdapter) Deploy(ctx context.Context, cfg DeployConfig) (DeployResult, error) {
	cfg.Provider = ProviderFly
	return Deploy(ctx, cfg)
}

// Verify runs the package-level HealthCheck against the configured
// URL (or the auto-derived https://<app>.fly.dev when the caller did
// not override). Returning the same (ok, detail) tuple keeps parity
// with the other Deployers so a caller draining verify results over
// a channel does not need per-provider branching.
func (flyAdapter) Verify(ctx context.Context, cfg DeployConfig) (bool, string) {
	url := cfg.HealthCheckURL
	if url == "" && cfg.AppName != "" {
		url = "https://" + cfg.AppName + ".fly.dev"
	}
	return HealthCheck(ctx, url, cfg.ExpectedBody)
}

// ErrFlyRollbackUnsupported reports that flyctl has no single-command
// rollback path without a pre-recorded release_command. We surface
// this as a typed error rather than silently succeed so the repair
// loop can escalate to the operator ("redeploy previous image
// manually") instead of assuming the deploy was safely rolled back.
var ErrFlyRollbackUnsupported = errors.New("deploy/fly: rollback is not implemented (flyctl requires a pre-recorded release to revert; use `flyctl releases rollback` manually)")

// Rollback is a deliberate no-op that returns ErrFlyRollbackUnsupported.
// Fly.io does not expose a one-shot rollback via flyctl without a
// release_command already configured on the app, and faking success
// here would let the descent engine believe a bad deploy was reverted
// when it wasn't. Honest failure is strictly better than silent
// no-op on a rollback path.
func (flyAdapter) Rollback(_ context.Context, _ DeployConfig) error {
	return ErrFlyRollbackUnsupported
}

// newFlyDeployer is the Factory registered under "fly". It returns a
// zero-value flyAdapter on each call — the adapter carries no mutable
// state, so a fresh instance per Get() is cheap and keeps the factory
// contract (no shared state) intact.
func newFlyDeployer() Deployer { return flyAdapter{} }

// registerBuiltins registers adapters that ship in-tree with the
// deploy package. Currently that is just Fly; future commits add
// Vercel and Cloudflare as they move out of their dedicated
// subpackages. Factored out of init() so resetRegistryForTest can
// reinstate the built-ins after a clean slate without duplicating
// the registration call sites.
func registerBuiltins() {
	Register("fly", newFlyDeployer)
}

// init registers the built-in adapters at package load so
// deploy.Get("fly") / deploy.Names() work without an explicit wire-up
// in main. Keeping the registration in this file (rather than a
// separate subpackage) matches the minimal-churn decision documented
// on flyAdapter above.
func init() {
	registerBuiltins()
}
