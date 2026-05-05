.PHONY: all build test vet lint lint-chdir ci bench bench-cache docker release clean check-pkg-count agent-features agent-features-update agent-features-drift-check storybook-mcp-validate lint-views docs-agentic

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
	go build -o ./bin/r1 ./cmd/r1
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

# Run the bench corpus
bench:
	go run ./bench/cmd/bench

# Print the prompt-cache savings projection.
# Pricing-model only — no API calls. Produces the table published at
# docs/benchmarks/prompt-cache.md.
bench-cache:
	go run ./bench/prompt_cache

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
# Pinned to ^9 per the §10a "Playwright/Storybook MCP version churn"
# mitigation. STATUS: BLOCKED on spec 6 merge — this target prints a
# notice and exits 0 until web/src/components/*.tsx exists.
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

storybook-mcp-validate:
	@if [ -d web/src/components ] && [ -n "$$(find web/src/components -name '*.tsx' -print -quit)" ]; then \
	    cd web && npx storybook-mcp@^9 validate .storybook/mcp.config.ts --fail-on-missing-a11y; \
	else \
	    echo "storybook-mcp-validate: SKIP — web/src/components/*.tsx not present (spec 6 web-chat-ui not merged)"; \
	    echo "  see web/.storybook/STATUS-BLOCKED-item-33.md for resolution path"; \
	fi

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
