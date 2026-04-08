# Contributing to Stoke

Thank you for your interest in contributing to Stoke.

## Development Setup

### Prerequisites

- Go 1.22 or later
- Git

### Build, Test, Vet

These three commands are the CI gate. All must pass before submitting a PR.

```bash
go build ./cmd/stoke
go test ./...
go vet ./...
```

Or use the Makefile:

```bash
make          # runs build, test, vet
make lint     # runs golangci-lint (requires golangci-lint installed)
```

## Submitting Changes

### Branch Naming

- `feature/<short-description>` for new features
- `fix/<short-description>` for bug fixes
- `docs/<short-description>` for documentation changes
- `refactor/<short-description>` for refactoring

### Pull Request Process

1. Fork the repository and create your branch from `main`.
2. Ensure `go build ./cmd/stoke`, `go test ./...`, and `go vet ./...` all pass.
3. Add tests for any new functionality.
4. Update documentation if your change affects the public API or user-facing behavior.
5. Write a clear PR description explaining what changed and why.

### PR Template

```
## Summary

Brief description of the change.

## Test Plan

How was this tested?

## Checklist

- [ ] `go build ./cmd/stoke` passes
- [ ] `go test ./...` passes
- [ ] `go vet ./...` passes
- [ ] New tests added for new functionality
- [ ] Documentation updated if needed
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Keep packages focused. See `PACKAGE-AUDIT.md` for the current package map.
- Avoid adding dependencies without discussion.
- Prefer table-driven tests.

## Developer Certificate of Origin

By contributing to this project, you certify that your contribution was created
in whole or in part by you and that you have the right to submit it under the
MIT license. This is the [Developer Certificate of Origin (DCO)](https://developercertificate.org/).

Sign your commits with `git commit -s` to add the `Signed-off-by` trailer.

## Reporting Issues

- **Security vulnerabilities:** See [SECURITY.md](SECURITY.md).
- **Bugs and feature requests:** Open a GitHub issue.

## License

By contributing, you agree that your contributions will be licensed under the
MIT License.
