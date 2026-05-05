# Contributing to R1

Thank you for your interest in contributing to R1. The canonical
source tree is `github.com/RelayOne/r1` post work-r1-rename.md §S2-2;
GitHub preserves redirects from the legacy `github.com/ericmacdougall/stoke`
path. The primary binary is still named `stoke` pending the §S2-3
binary rename — paths and binary invocations below reflect the current
on-disk layout.

## Development Setup

### Prerequisites

- Go 1.22 or later
- Git

### Build, Test, Vet

These three commands are the CI gate. All must pass before submitting a PR.

```bash
go build ./cmd/r1
go test ./...
go vet ./...
```

Or use the Makefile:

```bash
make          # runs build, test, vet
make lint     # runs golangci-lint (requires golangci-lint installed)
```

### Anti-truncation git hooks (recommended)

R1 ships a layered defense against LLM self-truncation. One layer is
a post-commit git hook that scans commit bodies for false-completion
phrases (e.g. "spec 9 done", "all items complete"). Install it with:

```bash
bash scripts/install-hooks.sh           # install
bash scripts/install-hooks.sh --check   # report current state
bash scripts/install-hooks.sh --uninstall
```

The hook is non-blocking — it writes warnings to `audit/antitrunc/`
but never fails a commit. The full layered defense is documented in
`docs/ANTI-TRUNCATION.md`.

## Submitting Changes

### Branch Naming

- `feature/<short-description>` for new features
- `fix/<short-description>` for bug fixes
- `docs/<short-description>` for documentation changes
- `refactor/<short-description>` for refactoring

### Pull Request Process

1. Fork the repository and create your branch from `main`.
2. Ensure `go build ./cmd/r1`, `go test ./...`, and `go vet ./...` all pass.
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

- [ ] `go build ./cmd/r1` passes
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
- **Bugs and feature requests:** Open a GitHub issue. Templates are provided under `.github/ISSUE_TEMPLATE/` for bug reports, feature requests, and harness regressions.

## Code of Conduct

This project follows a [Code of Conduct](CODE_OF_CONDUCT.md) adapted from
Contributor Covenant 2.1. By participating, you agree to uphold it. Report
concerns privately to `conduct@goodventures.dev`.

## License

By contributing, you agree that your contributions will be licensed under the
MIT License.
