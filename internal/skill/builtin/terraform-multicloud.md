# terraform-multicloud

> Infrastructure-as-Code with Terraform across GCP, Cloudflare, Fly.io, and DigitalOcean: module composition, state management, drift detection, and security scanning

<!-- keywords: terraform, iac, infrastructure, gcp, cloudflare, flyio, digitalocean, hcl, module, state, drift, pulumi -->

## When to Use
- Writing or reviewing Terraform configurations for any cloud provider
- Designing module composition, state management, or multi-environment layouts
- Importing existing infrastructure into Terraform
- Setting up CI/CD pipelines for infrastructure changes (Atlantis, Spacelift)
- Evaluating Terraform vs Pulumi for a specific use case

## When NOT to Use
- One-off manual cloud console changes that will not be repeated
- Fly.io-only deployments (provider is archived; use flyctl)
- Application-level configuration management (use Ansible, Chef, etc.)

## Behavioral Guidance

### Module Composition

Keep module trees flat -- one level of child modules composed in the root. Modules receive dependencies as inputs (dependency inversion), not creating them internally.

```hcl
module "network" {
  source          = "./modules/networking"
  base_cidr_block = "10.0.0.0/8"
}
module "database" {
  source     = "./modules/cloud-sql"
  vpc_id     = module.network.vpc_id
  subnet_ids = module.network.private_subnet_ids
}
```

**Do not create "wrapper modules" that switch providers based on a `cloud` variable.** This creates opaque configurations and debugging nightmares.

Version pinning: pessimistic constraints in staging (`~> 5.5.0`), exact pins in production (`= 5.5.1`).

### State Management

**GCS backend is simplest** -- locking is automatic with no extra resources:

```hcl
terraform {
  backend "gcs" {
    bucket             = "mycompany-terraform-state"
    prefix             = "terraform/prod"
    kms_encryption_key = "projects/.../cryptoKeys/key"
  }
}
```

S3: Terraform 1.10+ supports native locking via `use_lockfile = true` (no DynamoDB needed). Always enable bucket versioning.

### Environment Separation

**Use directories, not workspaces, for dev/staging/prod.** Workspaces share `.tf` files, credentials, and state context. An engineer running `terraform destroy` in the wrong workspace has no structural safeguard.

```
infrastructure/
  modules/
    networking/
    cloud-run/
    cloud-sql/
  environments/
    dev/       (main.tf, terraform.tfvars, backend.tf)
    staging/
    prod/
```

Workspaces are only appropriate for same-config-different-instances (regional deploys, ephemeral feature branches).

### Import and Refactoring

**Import blocks** (Terraform 1.5+) replace imperative `terraform import`:

```hcl
import {
  to = google_sql_database_instance.production
  id = "projects/myproject/instances/postgres-prod"
}
```

Use `terraform plan -generate-config-out=generated.tf` for auto-generation. Remove import blocks after successful apply.

**Moved blocks** handle renames, module moves, count-to-for_each conversions without destroy/recreate. Retain moved blocks indefinitely in shared modules.

### GCP Specifics

- Use `google_project_iam_member` (additive, safe). Avoid `google_project_iam_binding` (overwrites all members for a role) and `google_project_iam_policy` (can lock you out).
- Workload Identity Federation with GitHub Actions OIDC eliminates long-lived keys. Always include `attribute_condition`.
- Cloud Run: `cpu_idle = true` for cost savings; env vars with `secret_key_ref` using `latest` do NOT auto-update on new versions (must redeploy).
- Keep `hashicorp/google` and `hashicorp/google-beta` at the same version.

### Cloudflare Specifics

- Provider v5 is stabilizing but had ~15% resource issues at launch. Consider holding on v4 until the March 2026 migration tool.
- All WAF, rate limiting, redirects now use `cloudflare_ruleset` with appropriate phases.
- Use the `ref` field on ruleset rules for stable IDs -- without it, Terraform recreates rules on every change.
- API rate limit is 1,200 requests per 5 minutes. Split large configs into smaller root modules.

### Fly.io

**The Terraform provider is archived (March 2024).** Use flyctl + GitHub Actions or the Machines REST API. For cross-provider IaC, manage Fly.io separately via `local-exec` or a dedicated pipeline.

### Security Scanning

- **tfsec is dead** -- use `trivy config ./terraform-dir` or Checkov (750+ checks, graph-based cross-resource analysis)
- Scan both HCL files and `terraform plan` JSON output
- Use OPA/Conftest for custom policy enforcement, Sentinel for HCP Terraform
- Use Infracost for cost estimation in PR comments

### CI/CD Pipeline

Recommended flow: `terraform plan` -> security scan -> cost check -> CODEOWNERS review -> manual approval -> `terraform apply`. Use Atlantis for self-hosted GitOps, Spacelift for managed (predictable concurrency-based pricing).

## Gotchas
- **State file contains all secrets in plaintext.** `sensitive = true` only hides CLI output. Restrict state bucket access via IAM, never commit state to VCS, enable encryption at rest. Use `ephemeral = true` (Terraform 1.10+) to omit values from state entirely.
- **Provider version pinning is critical.** Google provider releases weekly. Use `~> 5.0` with `.terraform.lock.hcl` for reproducibility. Cloudflare v4 to v5 migration requires careful per-resource audit.
- **Module composition anti-pattern.** Deep nesting and wrapper modules that abstract multiple providers create opaque configs. Keep it flat with dependency inversion.
- **Drift detection only covers resources in state.** `terraform plan` cannot find resources created outside Terraform. Schedule plans every 4-6 hours, tag managed resources with `ManagedBy=Terraform`.
- **HCP Terraform bills per managed resource.** Security group rules, IAM policies, and lifecycle configs each count individually. Teams often find 30-50% more resources than expected.
- **Cloudflare API rate limiting.** Large configurations trigger 429 errors during refresh. Use `-parallelism=5` and split configs.
- **D1 database replacement destroys all data.** Ensure backups before any Terraform operation that might trigger resource replacement.
- **Workspaces for environments are dangerous.** All environments share the same code and credentials with only a workspace name differentiating them.
- **`google_project_iam_binding` overwrites.** It is authoritative per-role and will remove members added outside Terraform.
- **`for_each` over a data source causes refresh loops.** If the data source returns different results each plan (e.g., dynamic instance lists), resources are perpetually created/destroyed. Use a stable input like a local variable or tfvars list instead.
- **Modules should NOT contain provider blocks.** Provider configuration belongs in the root module only. Child modules with embedded provider blocks cannot be reused across regions or accounts and break when the caller passes a different provider alias.
- **Import blocks support `for_each` for bulk operations** but are one-time: remove after successful apply to avoid confusion.
