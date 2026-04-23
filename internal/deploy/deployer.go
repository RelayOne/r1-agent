package deploy

import "context"

// Deployer is the per-provider deployment adapter abstraction.
//
// DP2-1 introduces this interface so multiple providers (Fly, Vercel,
// Cloudflare Workers, …) can implement a common contract and be
// selected at runtime via the provider registry in registry.go. The
// top-level Deploy function in deploy.go remains as the Fly-only v1
// entry point; DP2-2 will move the Fly implementation into a
// subpackage that registers itself at init() time.
//
// Method contract:
//
//   - Deploy executes the provider-specific deployment for cfg and
//     returns the resulting DeployResult. Implementations shell out
//     to the provider's CLI (flyctl, vercel, wrangler, …) and honor
//     cfg.DryRun by returning a preview without subprocess I/O.
//
//   - Verify performs a provider-appropriate post-deploy health
//     check. It returns (true, "200 OK …") on healthy responses and
//     (false, "<reason>") on any failure. Implementations do NOT
//     retry; the caller's repair loop decides whether to try again.
//
//   - Rollback reverts the most recent deploy for cfg. Implementations
//     must be safe to call after a failed Deploy (e.g. missing
//     previous image → return a well-typed error rather than panic).
//
//   - Name returns the lower-case canonical provider name used in
//     registry lookups ("fly", "vercel", "cloudflare", …). The value
//     must be stable and match the key passed to Register so that
//     registry round-trips are reflexive.
type Deployer interface {
	Deploy(ctx context.Context, cfg DeployConfig) (DeployResult, error)
	Verify(ctx context.Context, cfg DeployConfig) (ok bool, detail string)
	Rollback(ctx context.Context, cfg DeployConfig) error
	Name() string
}
