# secrets-management

> Workload identity, secret rotation, vault patterns, and credential lifecycle management

<!-- keywords: secret, vault, rotation, workload identity, oidc, credential, api key, certificate, hashicorp, infisical, doppler, gcp, aws -->

## The Hierarchy: Eliminate > Rotate > Manage

1. **Eliminate secrets entirely** with workload identity federation (best)
2. **Use dynamic/ephemeral credentials** with short TTLs (better)
3. **Rotate static credentials** on a schedule with dual-acceptance windows (acceptable)
4. **Store static long-lived credentials** in a secrets manager (last resort)

## Workload Identity Federation

GCP, AWS, and Azure all support exchanging OIDC tokens for short-lived cloud credentials, eliminating stored keys entirely.

**CI/CD (GitHub Actions):** Exchange platform OIDC tokens for cloud credentials. Always set attribute conditions (e.g., `assertion.repository_owner == 'my-org'`) -- without them, any repository could authenticate.

```yaml
permissions:
  id-token: write  # Required for OIDC
steps:
  - uses: google-github-actions/auth@v2
    with:
      workload_identity_provider: "projects/PROJECT/locations/global/..."
      service_account: "github-sa@project.iam.gserviceaccount.com"
```

**Kubernetes (GKE):** Workload Identity maps K8s service accounts to cloud service accounts. No keys to manage.

**Fly.io:** Native OIDC tokens via local Unix socket. Can federate to GCP/AWS for keyless access.

## Zero-Downtime Rotation: Dual-Acceptance Window

Every rotation must follow: generate new credential -> deploy to target (both old and new valid) -> verify new works -> revoke old. Skipping this sequence guarantees auth failures.

**AWS alternating-users strategy:** Create two DB users (`appuser` and `appuser_clone`), rotate passwords alternately. The un-rotated user always has valid credentials.

**Vault dynamic secrets (gold standard):** Instead of rotating, create brand-new database users on-demand with 1-hour TTLs. Each app instance gets unique credentials with per-instance audit trails.

## Application Secret Access Patterns

| Pattern | Security | Latency | Availability |
|---------|----------|---------|--------------|
| Environment variables | Low (visible in /proc, logs, dumps) | None | High |
| Mounted files on tmpfs | Medium (POSIX perms, RAM-backed) | None | High |
| Direct API to secrets manager | High (audit trail, always fresh) | ~50ms | Dependent |
| Vault Agent sidecar | High (auto-renewal, file-based) | None after init | High |

OWASP recommends against environment variables for sensitive values. Prefer mounted files or Vault Agent for production workloads.

## Secret Detection Pipeline

Layer pre-commit speed with CI depth:

1. **Pre-commit:** Gitleaks (millisecond-scale, regex-based, 88% recall)
2. **CI:** TruffleHog (800+ detectors, active verification against live APIs)
3. **History:** `gitleaks detect --source /path/to/repo` or `trufflehog git` for full history
4. **PR-scoped:** `trufflehog git file://. --since-commit=$BASE_SHA --fail`

**Cardinal rule:** Rotate first, clean history second. Once pushed, assume compromised -- even in a private repo. 64% of secrets discovered in 2022 were still active four years later.

## API Key Lifecycle

- **Generate:** 256+ bits of entropy via `crypto/rand` (Go), `crypto.randomBytes(32)` (Node)
- **Hash before storage:** SHA-256 (not bcrypt -- high entropy keys don't need slow hashing)
- **Prefix for identification:** `sk_live_`, `pk_test_` pattern (enables leak detection)
- **Scope:** Per-key permissions limiting blast radius
- **Expire:** Mandatory `expires_at` timestamp on every key
- **Revoke:** Short TTL caches (60-300s) on validation bound the window of revoked-but-functional keys

## Database Credentials

**Vault database secrets engine:** Configure a privileged connection, define roles with SQL templates and TTLs. Each `vault read database/creds/readonly` creates a unique DB user that Vault drops when the lease expires.

**Cloud SQL Auth Proxy / RDS Proxy:** Applications connect to localhost; the proxy handles TLS, token refresh, and credential lifecycle transparently.

**Store connection components separately** (host, port, user, password, database) not as monolithic connection strings. This enables rotating passwords without touching other components.

## Connection Pool Invalidation During Rotation

Set `maxLifetime` on connections shorter than the rotation interval. Enable pre-ping health checks. Implement error-triggered credential re-fetch. Use credential proxies (Cloud SQL Auth Proxy, RDS Proxy) to abstract all credential management from the application.

## Build-Time Secrets

Docker `ARG` and `ENV` bake secrets into image layers, extractable via `docker history`. Use BuildKit `RUN --mount=type=secret` instead -- the secret exists only during that RUN instruction and never persists in any layer.
