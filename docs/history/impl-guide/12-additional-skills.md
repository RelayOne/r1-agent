# 12 — Additional Engineering Skills (from research bundle 02)

This is an addendum to `08-skill-library-extraction.md`. Six additional engineering content files were added in research bundle 02 that should be converted to skill content following the same procedure. One of them (`observability`) is already in SKILL.md format and only needs minor adaptation.

## The six new files

| Source title | Target skill name | Status | Notes |
|---|---|---|---|
| Observability SKILL.md: The Complete Agent Ruleset | `observability-enforcer` | **Ready as-is** | Already in proper SKILL.md format with frontmatter, activation rules, body, references. Just copy to `~/.stoke/skills/observability-enforcer/SKILL.md` and validate. |
| The Kubernetes operations field guide for platform engineers | `kubernetes-platform` | Convert | Operational guide for teams operating Kubernetes — node pools, autoscaling, RBAC, network policies, secrets management |
| The deep Kubernetes operations guide for teams that ship | `kubernetes-deploy` | Convert | More tactical: deployment strategies, PDBs, GitOps with ArgoCD/Flux, cost optimization |
| Production file handling in Go: R2, GCS, and Cloudflare CDN | `go-file-storage` | Convert | Multipart uploads, streaming, signed URLs, cache invalidation, Cloudflare R2 vs GCS tradeoffs |
| Production-grade IaC across GCP, Cloudflare, Fly.io, and DigitalOcean | `terraform-multicloud` | Convert | Terraform module composition, state management, GCP/Cloudflare/Fly/DO providers, Atlantis GitOps |
| Building the central nervous system for AI coding agents | (split into multiple) | Convert | This file is itself architectural research about LSP/DAP/SSE/OpenTelemetry integration patterns. It's relevant to **building** Stoke, not as a skill for agents using Stoke. **Do not extract as a skill.** Reference it for any future Stoke integrations work. |
| High-performance IPC in Go for event bus systems | (used in Phase 3) | — | Already used to inform the hub design in `05-phase3-hub.md`. Not a skill. |

So **5 new skill files** to add to the library, plus the central nervous system file as Stoke architecture reference (not a skill).

## Special handling: observability-enforcer

The observability file ships in proper SKILL.md format. Procedure:

1. Copy the entire content of `compass_artifact_wf-85a8d777-71a7-47fb-97cc-79730369e6b7_text_markdown.md` to `~/.stoke/skills/observability-enforcer/SKILL.md`
2. Verify the frontmatter parses (the `>` block scalar in description is valid YAML)
3. If the file body exceeds 500 lines, split the deepest reference sections out into `~/.stoke/skills/observability-enforcer/references/`:
   - `references/opentelemetry.md` for the OTel-specific deep dives
   - `references/alerting.md` for the SLO/burn-rate content
   - `references/dashboards.md` for the Grafana dashboard patterns
   - `references/cost-control.md` for the observability cost management content
4. Run the standard validation checklist from `08-skill-library-extraction.md`
5. Verify with `stoke skill show observability-enforcer`

This is the highest-quality starting skill in the library. It also serves as a reference example for the conversion procedure on the other files.

## Conversion procedure for the other 4 skills

Same procedure as `08-skill-library-extraction.md`:

1. Read the source file in full
2. Tag content as Behavioral / Decision / Gotcha / Background / Reference
3. Drop background, move reference to `references/`, keep behavioral + decisions + gotchas in `SKILL.md`
4. Write SKILL.md following the Trail of Bits template
5. Validate against the checklist

## Skill-specific guidance

### kubernetes-platform vs kubernetes-deploy

These two source files overlap heavily. Don't make two skills with duplicated content. Instead:

- **`kubernetes-platform`** is for the **platform engineer** persona — capacity planning, multi-tenant cluster design, resource quotas, network policies, RBAC, secrets architecture. Triggers: "set up cluster", "design k8s namespace", "configure node pool".
- **`kubernetes-deploy`** is for the **application engineer** persona — Deployment manifests, PDBs, HPAs, services, ingress, GitOps pull-based deploys. Triggers: "deploy to k8s", "kubernetes manifest", "helm chart".

If 80%+ of the gotchas are the same, merge into a single `kubernetes` skill instead. Use your judgment.

The most important shared gotchas to capture:
- **`requests == limits` for Guaranteed QoS** — without this, the scheduler overcommits and pods get OOMKilled in production
- **Default-deny network policies** — without an explicit default-deny, network policies do nothing because they're additive-only
- **PDB minAvailable on 1-replica deployments** is a footgun — it blocks every cluster maintenance operation forever
- **`failurePolicy: Fail` on validating webhooks** without monitoring kills the whole cluster when the webhook is unavailable
- **Cloud LB healthchecks bypass `terminationGracePeriodSeconds`** — pods receive SIGTERM before being de-registered from the LB, causing connection resets during deploy
- **`kubectl apply --prune` is dangerous** without label selectors — can delete unrelated resources
- **`hostPath` volumes are a security exit** — they let pods read host filesystem
- **Secrets in env vars leak via `kubectl describe pod` and process listings** — use volume mounts instead
- **HPA targets must be set on the deployment itself**, not the replicaset, or scaling fights happen
- **`emptyDir` volumes count against ephemeral storage limits** — easy to OOM-kill nodes by accident

### go-file-storage gotchas worth capturing

From the file handling research:
- **Multipart uploads need cleanup of orphaned parts** — use lifecycle policies or you accumulate cost forever
- **R2 vs S3 API differences** — R2 doesn't support all S3 metadata; some SDKs panic on missing fields
- **Signed URL expiry must account for clock skew** — use 5+ minutes minimum
- **GCS HMAC vs OAuth credentials** have different IAM semantics — GCS service accounts can do things HMAC keys can't
- **Cloudflare CDN cache invalidation has eventual consistency** — `cache: no-store` is the only way to guarantee freshness
- **Streaming uploads with `io.Pipe` can deadlock** if the writer goroutine errors before the reader starts
- **`http.MaxBytesReader` doesn't apply to multipart parts** — each part can be arbitrarily large
- **Range requests need server-side support** — if you proxy through your own server, you must handle them or videos won't seek

### terraform-multicloud gotchas worth capturing

From the IaC research:
- **`terraform workspaces` is NOT for environment separation** — use directories. The HashiCorp docs explicitly say so. An engineer thinking they're in `dev` while actually in `prod` finds no structural safeguard.
- **State files contain plaintext secrets** — `sensitive = true` only hides values from CLI output. Restrict bucket access via IAM.
- **GCS backend has automatic state locking, S3 needs `use_lockfile = true`** (Terraform 1.10+) or DynamoDB
- **Cloudflare provider v5 broke many resources** — pin to v4 unless you've audited every resource
- **Fly.io Terraform provider is archived** — flyctl is the only supported path
- **Pinning by digest** (`image@sha256:...`) instead of tag prevents silent drift
- **`for_each` over a `data` source** can cause refresh loops if the data source is dynamic
- **Imports must be reviewed before applying** — `terraform import` writes to state immediately, no plan step
- **Modules should NOT contain provider blocks** — they pollute the parent's provider configuration
- **HCP Terraform pricing is per-resource and includes things you don't expect** (security group rules, IAM policy attachments) — teams discover 30-50% more resources than expected

### Skipping the central-nervous-system file

The "Building the central nervous system" file (`compass_artifact_wf-ce5fb4c8-...`) is architectural research about how an AI coding tool with an event bus can integrate with LSP, DAP, GitHub Checks, OpenTelemetry, Prometheus, etc. It's about **building Stoke**, not about giving Stoke skills to use.

Do not extract it as a skill. Instead, save it as a reference document at `docs/architecture/integrations.md` and use it when implementing future Stoke integration work (e.g., a Stoke LSP server, a Stoke VS Code extension, a Stoke GitHub App).

## Updated extraction checklist

Add these 5 new entries to the skill extraction tracking in `STOKE-IMPL-NOTES.md`:

```markdown
## Skill Library — Round 2 (research bundle 02)

- [ ] observability-enforcer (READY AS-IS, just validate frontmatter and copy)
- [ ] kubernetes-platform OR merged kubernetes
- [ ] kubernetes-deploy OR merged kubernetes
- [ ] go-file-storage
- [ ] terraform-multicloud

Architecture references (NOT skills):
- [ ] docs/architecture/integrations.md (from "Building the central nervous system" file)
```

## Validation

After conversion:

```bash
stoke skill list | grep -E "observability|kubernetes|go-file-storage|terraform-multicloud"
```

Should show all 5 (or 4 if you merged the kubernetes skills).

```bash
# Test detection in a Kubernetes-using repo:
cd /path/to/k8s-project
stoke skill select
```

Should rank the kubernetes skill near the top.

```bash
# Test detection in a Terraform repo:
cd /path/to/tf-project
stoke skill select
```

Should rank `terraform-multicloud` near the top.
