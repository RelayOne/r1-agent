<!-- STATUS: done -->
<!-- BUILD_STARTED: 2026-04-22 -->
<!-- BUILD_COMPLETED: 2026-04-23 -->

# OSS-hub (in-repo addendum) — Implementation Spec

## Overview

Ships the in-repo tractable portion of the OSS-hub addendum from
`/home/eric/repos/plans/work-orders/work-stoke.md` §1685 (OSS-1..OSS-4).
The cross-repo portfolio items (r1.dev landing page, Cloudsmith
apt/yum hosting, Stripe billing, GHCR org rename) are out of scope for
this spec — they live in sibling work orders / decisions and cannot be
shipped from this repo alone.

**In scope here:**
- OSS-2: Homebrew tap auto-publish via goreleaser `brews:` config.
- OSS-2: Fix install.sh cosign keyless verification (currently a no-op
  because the required `--certificate-identity` /
  `--certificate-oidc-issuer` flags are missing; cosign v2+ rejects
  keyless verify-blob without them).
- OSS-3: `GOVERNANCE.md` documenting maintainer roles + decision process.
- OSS-3: CLA Assistant GitHub Action workflow + `CLA.md` text.
- README install-section: mention `brew install` option once the tap
  is live.

**Out of scope (flagged BLOCKED with reason):**
- r1.dev landing page — lives in goodventures-sites work order.
- Cloudsmith apt/yum hosting — requires org account + budget decision.
- GHCR image org rename (`ericmacdougall` → `RelayOne`) — blocked on
  portfolio org-transfer decision; install paths still point at the
  existing org.
- Stripe billing / R1-Pro pricing — separate product surface.

## Stack & Versions

- Go 1.23 (matches `.github/workflows/release.yml:22`).
- goreleaser v2 (already pinned in `.goreleaser.yml:1`).
- cosign v3 (installed via `sigstore/cosign-installer@v3` in
  `release.yml:55`). Keyless verify-blob API changed in v2; flags are
  mandatory.
- `contributor-assistant/github-action@v2.4.0` for CLA Assistant
  (GitHub Action, signed CLAs stored in a project-owned gist).

## Existing Patterns to Follow

- goreleaser config: `.goreleaser.yml` (single source of truth for
  build + archive + checksum + sboms).
- Release workflow: `.github/workflows/release.yml` (separate `test`,
  `release`, `docker` jobs; cosign runs in `release` after goreleaser).
- Install script: `install.sh` (platform detect, checksum verify,
  optional cosign verify, install both `stoke` and `stoke-acp`).
- Repo docs: `CONTRIBUTING.md`, `STEWARDSHIP.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md` (top-level markdown, plain prose).

## Library Preferences

- Signing: sigstore/cosign (already wired). Do NOT introduce gpg/pgp
  or a second signer.
- CLA: contributor-assistant/github-action — free, SAP-backed, signed
  CLAs stored in a repo-scoped gist. Do NOT introduce a paid CLA
  service.
- Homebrew: goreleaser `brews:` block — do NOT hand-author or commit
  `Formula/stoke.rb`; let goreleaser regenerate it on every tag.

## Changes

### 1. `.goreleaser.yml` — Homebrew tap publishing

Append a `brews:` section that produces `Formula/stoke.rb` and pushes
it to a dedicated `homebrew-stoke` tap repo on each release tag.

```yaml
brews:
  - name: stoke
    repository:
      owner: ericmacdougall
      name: homebrew-stoke
      branch: main
    directory: Formula
    homepage: https://github.com/ericmacdougall/Stoke
    description: "Stoke — agentic coding orchestrator (R1)"
    license: "Apache-2.0"
    test: |
      system "#{bin}/stoke", "--version"
    install: |
      bin.install "stoke"
      bin.install "stoke-acp" if File.exist?("stoke-acp")
    commit_author:
      name: goreleaserbot
      email: bot@goreleaser.com
```

Target tap lives at `ericmacdougall/homebrew-stoke` (matches current
GHCR owner). Rename tracks the GHCR rename when that ships.

### 2. `install.sh` — cosign verify-blob keyless flags

The current cosign invocation silently fails on every release because
cosign v2+ requires `--certificate-identity-regexp` +
`--certificate-oidc-issuer` for keyless verification. The signature
file downloads, the verify call fails with `missing required flags`,
and the script exits non-zero on any release that published a `.sig`.

Fix: use the `cosign-bundle` format if available (preferred — one
file, embedded cert + rekor proof), or pass the required flags:

```bash
cosign verify-blob \
    --certificate-identity-regexp "https://github\.com/ericmacdougall/Stoke/\.github/workflows/release\.yml@refs/tags/.*" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    --signature "${tmp_dir}/${archive_name}.sig" \
    "${tmp_dir}/${archive_name}"
```

The identity pattern pins the signer to this repo's `release.yml`
workflow on any tag ref — narrow enough to block a sibling-repo
signer, wide enough to match new tags without script edits.

### 3. `GOVERNANCE.md` (new)

Sections:
- **Roles**: Maintainers (write access), Contributors (PR authors),
  BDFL (tie-breaker = repo owner).
- **Decision process**: Lazy consensus on PRs; 1 maintainer approval
  merges small changes; 2 maintainer approvals for architecture
  changes; RFC via GitHub Discussion for breaking/public-API changes.
- **Becoming a maintainer**: Sustained high-quality contributions +
  nominated by existing maintainer + lazy consensus (no objection in
  7 days).
- **Code owners**: `.github/CODEOWNERS` is authoritative for required
  reviewers.
- **Release cadence**: Tag-driven; maintainers cut tags; no fixed
  cadence.

### 4. `.github/workflows/cla.yml` (new) + `CLA.md` (new)

`cla.yml`:
```yaml
name: CLA Assistant
on:
  issue_comment:
    types: [created]
  pull_request_target:
    types: [opened, closed, synchronize]

permissions:
  actions: write
  contents: write
  pull-requests: write
  statuses: write

jobs:
  CLAAssistant:
    runs-on: ubuntu-latest
    steps:
      - name: "CLA Assistant"
        if: (github.event.comment.body == 'recheck' || github.event.comment.body == 'I have read the CLA Document and I hereby sign the CLA') || github.event_name == 'pull_request_target'
        uses: contributor-assistant/github-action@v2.4.0
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          PERSONAL_ACCESS_TOKEN: ${{ secrets.CLA_GIST_TOKEN }}
        with:
          path-to-signatures: "signatures/v1/cla.json"
          path-to-document: "https://github.com/ericmacdougall/Stoke/blob/main/CLA.md"
          branch: "main"
          allowlist: "dependabot[bot],goreleaserbot"
          remote-organization-name: ericmacdougall
          remote-repository-name: cla-signatures
```

`CLA.md`: SAP-style individual contributor CLA text, scoped to
"the Project Owner" = ericmacdougall, granting copyright + patent
license for contributions. Based on the public apache-style ICLA —
plain English, single page.

### 5. `README.md` — install section

Add a brew line to the install section once the tap repo is public:

```bash
# Homebrew (macOS + Linux)
brew install ericmacdougall/stoke/stoke

# One-line (with cosign verification)
curl -fsSL https://raw.githubusercontent.com/ericmacdougall/Stoke/main/install.sh | bash
```

## Boundaries — What NOT To Do

- Do NOT rename the `stoke` binary to `r1`. Binary keeps the `stoke`
  name; OSS-hub markets it as "R1" externally (decision from
  work-stoke.md §OSS-1).
- Do NOT modify the GHCR image path. Any rename is a separate PR
  gated on the org-transfer decision.
- Do NOT introduce DCO — CLA Assistant is the chosen contributor
  agreement (work-stoke.md §OSS-3 rationale).
- Do NOT self-host apt/yum (Cloudsmith or PackageCloud is the v1
  choice; neither is in scope for this spec).
- Do NOT commit generated Formula/*.rb — goreleaser regenerates on
  every tag.

## Testing

### `.goreleaser.yml`
- [x] `goreleaser check` passes with new `brews:` block.
- [x] `goreleaser release --snapshot --clean --skip=publish` produces
  a formula in `dist/` without attempting a tap push.

### `install.sh`
- [x] Shellcheck clean on modified script.
- [x] Dry-run with `VERSION=vTESTFAKE` and mocked curl exits early on
  missing release (existing behavior) — no regression.

### `cla.yml`
- [x] `act` or GitHub-Actions syntax validator (`actionlint`) passes.

### `GOVERNANCE.md`, `CLA.md`
- [x] Files exist and render as valid GitHub markdown.

## Acceptance Criteria

- WHEN a release tag is pushed THE goreleaser job SHALL produce a
  Homebrew formula and push it to the tap repo.
- WHEN a user runs `install.sh` against a signed release THE cosign
  verification SHALL pass (not silently succeed via a no-op) with
  valid keyless identity pinning.
- WHEN a new PR is opened against `main` THE CLA Assistant SHALL
  either auto-pass (allowlisted author) or post a sign-request
  comment.
- WHEN a contributor asks "who can approve what?" THE GOVERNANCE.md
  document SHALL answer without ambiguity.

## Implementation Checklist

1. [x] Add `brews:` section to `.goreleaser.yml`; YAML validated (commit ed6459f).
2. [x] Fix install.sh cosign verify-blob flags (commit aced842).
3. [x] Write `GOVERNANCE.md` with roles, decision process, and code-owner rules (commit f1c578a).
4. [x] Write `CLA.md` + `.github/workflows/cla.yml` (commit 11519c4).
5. [x] Update README install section with brew option.
6. [x] `go build ./cmd/stoke` — green. `go vet ./...` — green. `go test ./...` — one pre-existing failure in `internal/scan/selfscan_test.go` flagging two `//nolint:nilerr` directives at `cmd/r1-server/import.go:254` and `cmd/stoke/export_cmd.go:455`. Both directives predate this branch (added in PR #3 T16 commits 9b8428a + 9802367 on main). Not a regression from OSS-hub work. BLOCKED for separate triage — user decides whether to refactor the nolint call-sites or extend the selfscan false-positive allowlist.
7. [x] Mark spec STATUS: done.
