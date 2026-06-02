.PHONY: all build test vet lint lint-chdir ci bench bench-cache docker release clean check-pkg-count agent-features agent-features-update agent-features-drift-check lint-views docs-agentic smoke-coderadar
.PHONY: all build test vet lint lint-chdir ci bench bench-cache docker release clean check-pkg-count agent-features agent-features-update agent-features-drift-check lint-views docs-agentic test-oneshot-concurrent

# Default: run the CI gate
all: build test vet

# CI gate (per CLAUDE.md): build + test + vet + chdir-lint.
# lint-chdir is the r1d-server Phase A audit gate — see specs/r1d-server.md §10.
ci: build test vet lint-chdir

# Build all binaries. Primary is ./cmd/r1; ./cmd/r1-acp is
# the Agent Client Protocol adapter (S-U-002). Outputs land in
# ./bin/ so build artifacts do not clutter the repo root.
build:
	mkdir -p bin
	go build -tags sqlite_fts5 -o ./bin/r1 ./cmd/r1
	go build -o ./bin/r1-acp ./cmd/r1-acp

# Run all tests
test:
	go test ./... -count=1 -timeout=120s

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (requires golangci-lint installed)
lint:
	golangci-lint run ./...

# r1d-server Phase A audit gate — flags every unannotated cwd-mutating
# call (os.Chdir / os.Getwd / filepath.Abs("") / os.Open("./...")).
# Must be green before the multi-session daemon (Phase E) is enabled.
lint-chdir:
	./tools/lint-no-chdir.sh

# CodeRadar dogfood smoke (spec coderadar-dogfood.md T8).
# Requires ENV=dev|staging|prod and either CODERADAR_DSN already set or
# `gcloud` available to materialize the env-scoped secret. Builds only
# the coderadar package and runs the live `service_started` round-trip
# behind the coderadar_smoke build tag. Cloud Build invokes this step
# after deploy-coord-api per services/cloudbuild-deploy.yaml.
smoke-coderadar:
	@test -n "$(ENV)" || (echo "ENV=dev|staging|prod required"; exit 1)
	@if [ -z "$$CODERADAR_DSN" ]; then \
	  echo "Materializing CODERADAR_DSN from Secret Manager..."; \
	  export CODERADAR_DSN=$$(gcloud secrets versions access latest --secret=r1-$(ENV)-shared-CODERADAR_DSN); \
	fi; \
	CODERADAR_DSN=$${CODERADAR_DSN} R1_ENV=$(ENV) \
	  go test -tags=coderadar_smoke -count=1 -timeout=30s ./internal/coderadar/...

# Run the bench corpus
bench:
	go run ./bench/cmd/bench

# Print the prompt-cache savings projection.
# Pricing-model only — no API calls. Produces the table published at
# docs/benchmarks/prompt-cache.md.
bench-cache:
	go run ./bench/prompt_cache

# A3 — 1000-concurrent oneshot integration benchmark. Behind the
# `integration` build tag so it doesn't run on every `go test
# ./...`. Recipe prints the host's ulimits so capacity problems
# (nofile / nproc / RLIMIT_AS) are obvious.
#
# Override the size on a commodity host:
#   R1_BENCH_CONCURRENCY=100 R1_BENCH_WALL_BUDGET_S=10 make test-oneshot-concurrent
#
# Spec: specs/oneshot-production-hardening.md §T5.4.
test-oneshot-concurrent:
	@ulimit -n 65535 2>/dev/null || true; ulimit -u 4096 2>/dev/null || true; \
	echo "nofile=$$(ulimit -n) nproc=$$(ulimit -u) nproc(host)=$$(nproc)" && \
	go test -tags integration -timeout 5m -run TestOneShot_1000Concurrent -v ./internal/oneshot/

# Build Docker image
docker:
	docker build -t r1:latest .

# Build release artifacts via goreleaser
release:
	goreleaser release --clean

# Clean build artifacts
clean:
	rm -f ./bin/r1 ./bin/r1-acp
	rm -rf dist/
	rm -f coverage.out

# Run tests with race detector
test-race:
	go test ./... -race -count=1 -timeout=300s

# Run tests with coverage
test-cover:
	go test ./... -coverprofile=coverage.out -timeout=120s
	go tool cover -func=coverage.out

# Run security scanners
security:
	govulncheck ./...
	gosec ./...

# Run the agent feature meta-test (spec 8 §10/§12 item 20). Walks
# tests/agent/**/*.agent.feature.md and dispatches every scenario
# through the r1.* MCP catalog. Requires the r1d daemon (spec 5);
# until that merges this target prints parsed-step counts.
#
# The `|| true` swallows the runner's exit code while seed fixtures
# land in items 23-30; remove it once all 8 fixtures are committed
# AND spec 5 has merged.
agent-features:
	go run ./tools/agent-feature-runner --root tests/agent || true

# Re-record golden a11y snapshots (spec 8 §10a "Snapshot drift"
# mitigation, §12 item 21). Run when an intentional UI redesign means
# the prior snapshots no longer match. The resulting diff MUST be
# reviewed alongside the source-code diff in the same PR (the lint at
# §22 fails when source diff is empty + snapshot diff is non-empty).
agent-features-update:
	go run ./tools/agent-feature-runner --root tests/agent --update || true

# CI guard against accidental snapshot updates (spec 8 §10a + item 22).
# Fails when golden snapshots changed without any source change in
# web/src/, internal/tui/, or desktop/src-tauri/.
agent-features-drift-check:
	./tools/agent-feature-runner/snapshot_drift_check.sh

# Storybook MCP contract validator (spec 8 §7 + item 34).
# Pinned to a published `storybook-mcp` release so this fails only on
# real validation issues, not npm `notarget`.
storybook-mcp-validate:
	@if [ -d web/src/components ] && [ -n "$$(find web/src/components -name '*.tsx' -print -quit)" ]; then \
	    cd web && npx storybook-mcp@^0.5 validate .storybook/mcp.config.ts --fail-on-missing-a11y; \
	else \
	    echo "storybook-mcp-validate: SKIP — web/src/components/*.tsx not present (spec 6 web-chat-ui not merged)"; \
	    echo "  see web/.storybook/STATUS-BLOCKED-item-33.md for resolution path"; \
	fi

# Run the lint-view-without-api scanner (spec 8 §8 + item 37). Spawns
# `r1 mcp serve --print-tools` to load the catalog, then walks the
# React, Bubble Tea, and Tauri source trees per §8.1. Exits non-zero
# on any FAIL finding.
#
# Until specs 1-7 ship the UI surfaces, this target's output includes
# legitimate FAILs against legacy internal/tui/ models that do not
# yet implement A11yEmitter — those are real findings, not noise.
lint-views:
	go run ./tools/lint-view-without-api --root . --catalog <(go run ./cmd/r1 mcp serve --print-tools)

# Regenerate the tool-catalog section of docs/AGENTIC-API.md from the
# live r1.* catalog (spec 8 §9 + item 41). Writes the Markdown form
# emitted by `r1 mcp serve --print-tools --markdown` to
# docs/AGENTIC-API-CATALOG.md so reviewers can see the per-tool
# input-schema diff in PRs.
docs-agentic:
	go run ./cmd/r1 mcp serve --print-tools --markdown > docs/AGENTIC-API-CATALOG.md
	@echo "wrote docs/AGENTIC-API-CATALOG.md ($$(wc -l < docs/AGENTIC-API-CATALOG.md) lines)"
# Verify package count hasn't drifted (CI check)
check-pkg-count:
	@expected=180; \
	actual=$$(find . -path ./vendor -prune -o -name "*.go" -print | xargs grep -l "^package " | sed 's|/[^/]*$$||' | sort -u | grep "^./internal/" | wc -l | tr -d ' '); \
	if [ "$$actual" != "$$expected" ]; then \
		echo "ERROR: internal package count drifted: expected $$expected, got $$actual"; \
		echo "Update README.md, PACKAGE-AUDIT.md, and CLAUDE.md, then update this check."; \
		exit 1; \
	fi; \
	echo "OK: $$actual internal packages (expected $$expected)"
