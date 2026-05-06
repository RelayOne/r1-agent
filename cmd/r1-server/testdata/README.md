# `cmd/r1-server/testdata/`

Test fixtures + golden files for the r1-server v2 dashboard test
suite. Three classes of tests share this directory:

| Class | Where it runs | What's stored here |
|---|---|---|
| Golden template tests | `go test ./cmd/r1-server/...` (default) | `golden/*.html` — byte-stable snapshots of every page template rendered against a deterministic fixture |
| Vendor freshness | `go test ./cmd/r1-server/...` (default) | nothing here — guards live in `cmd/r1-server/sri_test.go` + `cmd/r1-server/vendor_freshness_test.go`; the on-disk vendor blobs they check are at `cmd/r1-server/ui/vendor/` |
| Vitest worker fixture | `cd web && npx vitest run` | nothing here — the 50-node JSON fixture is at `web/src/test/testdata/graph-50.json` |
| Playwright E2E | `cd cmd/r1-server/e2e && go test -tags=e2e ./...` | nothing here — runner script is at `cmd/r1-server/e2e/e2e-fullflow.mjs` |

The non-Go fixtures intentionally sit outside this directory because
the runners that consume them live elsewhere (web/ vitest workspace,
e2e/ Go submodule). Keeping each fixture next to its consumer avoids
the cross-tree imports that vite + Playwright handle awkwardly.

## Golden suite

`golden/*.html` is the byte-stable rendered output of each page
template. The test harness in `cmd/r1-server/golden_test.go` parses
the templates fresh on each test run and renders them against a
fixed deterministic context (timestamps frozen at
`2026-01-01T12:00:00Z`, session ids like `sess-fixture`, faux SRI
values like `sha384-FAKEHTMX`).

### Updating goldens after an intentional template change

```bash
go test -run TestGolden ./cmd/r1-server/ -args -update
```

The `-args` separator is required because Go's test runner forwards
unknown flags to the test binary only after that boundary. If you
forget it, `go test` will print its own help text and exit non-zero
without running the tests.

After running with `-update`, review the diff carefully — golden
mismatches are usually intentional (a template was edited) but can
also be silent regressions (an HTML attribute slipped in upstream).
Commit the goldens in the same PR that ships the template change.

### Why goldens for HTML at all

Golden tests pin the *shape* of the rendered output: tag order,
attribute spelling, whitespace handling. They catch regressions that
unit tests miss — e.g., a CSS class being renamed in the template
but not in the JS that selects against it.

## Running each class locally

```bash
# Golden + vendor freshness + foundation tests (default lane).
go test ./cmd/r1-server/...

# Vitest worker convergence test.
cd web
npx vitest run src/test/graph-worker.test.ts ../cmd/r1-server/ui/js/graph-worker.test.js

# Playwright E2E (requires browser binaries — see prerequisites).
cd cmd/r1-server/e2e
R1_SERVER_UI_V2=1 R1_SERVER_SHARE_ENABLED=1 go test -tags=e2e ./...
```

### Playwright prerequisites

```bash
cd web
npm install            # picks up @axe-core/playwright + playwright via the workspace
npx playwright install --with-deps chromium
```

The `--with-deps` flag is required on Linux because Playwright's
chromium ships with system-package dependencies (libnss, libxcb,
etc.) that the install wizard prompts for. Cloud Build's
`services/cloudbuild-e2e.yaml` lane runs the install in a
release-rehearsal trigger; default-lane CI never installs Playwright.

## Cross-links

- Root project layout: `docs/ARCHITECTURE.md` § "Testing Architecture"
- Contributing guide: `CONTRIBUTING.md` § "Updating golden files"
- Deployment guide: `docs/DEPLOYMENT.md` § "Release-rehearsal lane"
