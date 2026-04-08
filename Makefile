.PHONY: all build test vet lint bench docker release clean check-pkg-count

# Default: run the CI gate
all: build test vet

# Build the stoke binary
build:
	go build ./cmd/stoke

# Run all tests
test:
	go test ./... -count=1 -timeout=120s

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (requires golangci-lint installed)
lint:
	golangci-lint run ./...

# Run the bench corpus
bench:
	go run ./bench/cmd/bench

# Build Docker image
docker:
	docker build -t stoke:latest .

# Build release artifacts via goreleaser
release:
	goreleaser release --clean

# Clean build artifacts
clean:
	rm -f stoke
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

# Verify package count hasn't drifted (CI check)
check-pkg-count:
	@expected=132; \
	actual=$$(find . -path ./vendor -prune -o -name "*.go" -print | xargs grep -l "^package " | sed 's|/[^/]*$$||' | sort -u | grep "^./internal/" | wc -l | tr -d ' '); \
	if [ "$$actual" != "$$expected" ]; then \
		echo "ERROR: internal package count drifted: expected $$expected, got $$actual"; \
		echo "Update README.md, PACKAGE-AUDIT.md, and CLAUDE.md, then update this check."; \
		exit 1; \
	fi; \
	echo "OK: $$actual internal packages (expected $$expected)"
