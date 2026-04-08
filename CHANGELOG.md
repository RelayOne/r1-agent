# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### V2 Governance Layer
- Append-only content-addressed ledger with 13+ node types and 7 edge types
- Durable WAL-backed event bus with 30+ event types, hooks, and delayed events
- 30 deterministic supervisor rules across 10 categories (trust, consensus, snapshot, hierarchy, drift, research, skill, SDM, cross-team)
- 7-state consensus loop tracker for structured agreement
- 11 stance roles with per-role tool authorization via harness
- Skill manufacturing pipeline with 4 workflows and confidence ladder
- V1-to-V2 bridge adapters (cost, verify, wisdom, audit)
- Content-addressed ID generation (SHA256, 16 prefixes)
- Structured error taxonomy with 10 error codes
- Snapshot protection for baseline manifests
- First-time configuration wizard with presets (minimal/balanced/strict)
- Concern field projection with 10 sections and 9 role templates

#### Execution Engine
- Native agentic tool-use loop via Anthropic Messages API
- 5-provider fallback chain: Claude -> Codex -> OpenRouter -> DirectAPI -> LintOnly
- Cross-model review gate (Claude implements, Codex reviews)
- GRPW priority scheduling with file-scope conflict detection
- Speculative parallel execution (4 strategies, pick winner)
- 10 failure classes with language-specific parsers and fingerprint dedup
- Dependency-aware test selection via import graph
- Adversarial self-audit convergence checks
- Pre-commit AST-aware critic (secrets, injection, debug prints)
- 17 review personas for multi-perspective audit
- Ember integration: managed AI routing, burst compute, remote progress (Phases 1-3)

#### Productionization (S1-S12)
- SECURITY.md, CONTRIBUTING.md, CHANGELOG.md, Makefile
- .goreleaser.yml for cross-platform builds (linux/darwin x amd64/arm64)
- GitHub Actions CI: build/vet/test, race detector, golangci-lint, govulncheck, gosec
- GitHub Actions release: goreleaser, cosign signing, Docker push to ghcr.io
- Dockerfile (multi-stage, distroless) and Dockerfile.pool (worker image)
- install.sh: platform detection, checksum verification, cosign signature check
- Container pool runtime for macOS Keychain isolation (Docker volume-based)
- stoke doctor --providers: checks all 5 fallback chain providers
- OAuth usage endpoint contract test with forward-compatibility validation
- 11-layer policy engine documented with layer-to-package mapping
- Negative test suite for enforcer hooks (12 attack payloads, all blocked)
- Self-scan dogfooding: 18+ rules over 327 Go source files, zero blocking
- Bench corpus of 20 tasks across 5 categories (security, correctness, refactoring, features, testing)
- Golden mission baseline test with regression detection
- SWE-bench Pro evaluation path documented
- Nightly bench workflow with HTML report artifacts
- 12 MCP memory tools exposing ledger, wisdom, research, and skill stores
- Temporal validity for wisdom: ValidFrom/ValidUntil, AsOf() query, Invalidate()
- L0-L3 context budget framing (Identity, Critical, Topical, Deep)
- Auto-infer task dependencies from file scope overlap
- Shared dependency symlinks in worktrees (node_modules, vendor, .venv, etc.)
- Architecture documentation: v2 overview, ledger, bus, supervisor rules, harness/stances, env backends, bridge, failure recovery, policy engine, providers, OAuth endpoint, bare mode, context budget
- docs/README.md navigable index
- Historical design documents preserved in docs/history/
- Bubble Tea interactive TUI (Dashboard/Focus/Detail views)

### Changed

- Version variable settable via ldflags (was hardcoded const)
- Package count reconciled to 132 internal + 1 cmd + 9 bench across all docs
- docs/ROADMAP.md updated to reflect implemented Ember integration (Phases 1-3)
- .gitignore updated to exclude all *.zip patterns
- Research store: added RWMutex for vector index thread safety (race fix)

### Removed

- Seven embedded zip files (guide, impl-guide, research, trio-main) from repo root

### Fixed

- Race condition in research.Store.indexEntry() during concurrent Add() calls
