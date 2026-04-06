# dependency-supply-chain

> Dependency management, supply chain security, vulnerability scanning, and license compliance

<!-- keywords: dependency, supply chain, sbom, renovate, dependabot, govulncheck, cargo-audit, npm audit, license, cve, trivy, scorecard, sigstore -->

## Supply Chain Security Layers

1. **Detection:** Scan dependencies for known CVEs continuously
2. **Prevention:** Pin versions, verify checksums, restrict registries
3. **Provenance:** Verify artifact origin and build integrity
4. **Licensing:** Automated license compliance checks in CI

## Vulnerability Scanning by Language

### Go
```bash
# Built-in vulnerability database (uses govulncheck.golang.org)
govulncheck ./...                    # analyzes call graphs, not just imports
go mod verify                        # verify checksums match go.sum
```
`govulncheck` is superior to generic scanners because it checks whether vulnerable code paths are actually reachable in your binary.

### Rust
```bash
cargo audit                          # check Cargo.lock against RustSec DB
cargo deny check                     # licenses + bans + advisories + sources
cargo auditable                      # embed dependency info in compiled binaries
```
`cargo-deny` is the all-in-one gate: license allowlists, banned crates, advisory checks, and source restrictions in a single `deny.toml`.

### JavaScript/TypeScript
```bash
npm audit --audit-level=high         # check package-lock.json
npx better-npm-audit audit           # better formatting, CI-friendly exit codes
```

### Container Images
```bash
trivy image myapp:latest             # CVEs, secrets, misconfigs in one scan
trivy fs --security-checks vuln .    # scan local filesystem
```

## Automated Updates

### Renovate (recommended over Dependabot)
- Groups related updates (e.g., all `@testing-library/*` packages together)
- Respects monorepo structure with per-package update rules
- Supports auto-merge for patch updates with passing CI
- Schedule-aware: run updates during business hours for faster review
- `extends: ["config:recommended"]` covers sensible defaults

### Dependabot
- GitHub-native, zero setup for basic scanning
- Limited grouping and customization compared to Renovate
- `dependabot.yml` in `.github/` with update schedules per ecosystem

### Key Patterns
- **Pin Actions by SHA**, not tag: `uses: actions/checkout@a5ac7e51b41094c92402da3b24376905380afc29` prevents tag hijacking
- **Lock files must be committed:** `Cargo.lock`, `package-lock.json`, `go.sum`
- **Auto-merge patches:** Low risk, high volume. Let CI be the gate.
- **Group major updates:** Review together for breaking change assessment.

## SBOM Generation

Software Bill of Materials is increasingly required for compliance (US EO 14028, EU CRA).

```bash
# Syft: comprehensive multi-format SBOM generation
syft packages dir:. -o spdx-json > sbom.spdx.json
syft packages dir:. -o cyclonedx-json > sbom.cdx.json

# Trivy can also generate SBOMs
trivy fs --format cyclonedx --output sbom.json .
```

Generate SBOMs in CI on every release. Store alongside release artifacts. CycloneDX and SPDX are the two standard formats.

## License Compliance

Automate license checking in CI to catch problems before they reach legal review:

```toml
# Rust: deny.toml
[licenses]
allow = ["MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC"]
deny = ["GPL-3.0"]
copyleft = "deny"
```

For Go, use `go-licenses` to scan and report. For Node.js, `license-checker` or `licensee`. Flag any AGPL/GPL dependencies in MIT/Apache-licensed projects.

## GOPRIVATE and Private Modules

`GOPRIVATE=github.com/mycompany/*` skips both module proxy and checksum database for private paths. Without it, `go mod download` fails with 410 Gone. In CI, use Docker BuildKit secrets for credentials:
```dockerfile
RUN --mount=type=secret,id=NETRC,dst=/root/.netrc go mod download
```

## Build Provenance and Signing

- **Sigstore/cosign:** Sign container images with keyless signing via OIDC identity
- **SLSA (Supply-chain Levels for Software Artifacts):** Framework for build integrity guarantees. GitHub Actions has built-in SLSA 3 provenance generation.
- **OpenSSF Scorecard:** Automated security health check for open-source dependencies. Run `scorecard --repo=github.com/org/repo` in CI.

## CI Pipeline Pattern

```yaml
# Order: fast checks first, expensive checks last
- govulncheck ./...           # Go vulnerability check
- cargo audit                 # Rust vulnerability check
- npm audit --audit-level=high # Node vulnerability check
- trivy fs .                  # Comprehensive scan
- license-check               # License compliance
- sbom-generate               # SBOM for release artifacts
```

Gate deployments on critical/high CVEs. Warn on medium. Track mean-time-to-remediate by severity. Never set aggressive remediation SLAs you cannot meet -- auditors check compliance rates against your stated policy.
