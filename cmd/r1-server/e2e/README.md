# `cmd/r1-server/e2e/`

End-to-end Playwright runners invoked by `//go:build e2e` Go tests in
`cmd/r1-server/`. Skipped from the default `go test ./...` lane;
exercised in the release-rehearsal CI lane.

## Files

- `graph-fps.mjs` — Spec 2 §6 (TASK-11). Drives headless Chromium
  through `/session/{id}/graph` with a 3000-node fixture and samples
  per-frame timing for 5 seconds. Emits the FPS summary on the last
  stdout line for `graph_e2e_test.go` to parse.

## Running locally

```bash
# 1. Install Playwright + browser binaries (one-time per dev machine).
cd web
npm install --save-dev playwright
npx playwright install chromium
cd ..

# 2. Run the e2e Go test. The 3000-node fixture session is seeded
#    automatically by graph_e2e_fixture.go (build tag e2e) into a
#    temp R1_DATA_DIR — no manual seeding step.
#    Note: `node` must resolve `playwright` from an ancestor
#    node_modules of cmd/r1-server/ (repo-root node_modules works;
#    web/node_modules does not — symlink it if needed).
R1_SERVER_UI_V2=1 go test -tags=e2e -run TestGraph3kFPS ./cmd/r1-server
```

## CI lane

`services/cloudbuild-e2e.yaml`'s e2e-test step (release-rehearsal
trigger) runs two invocations: the e2e submodule suite
(`cd cmd/r1-server/e2e && go test -tags=e2e ./...`) and then, from the
repo root, `go test -tags=e2e -run 'TestGraph3kFPS|TestSeedGraphFixture'
./cmd/r1-server` with `R1_SERVER_UI_V2=1` — graph_e2e_test.go lives in
package main one level above this submodule, so the submodule run alone
never reaches it (this README previously claimed otherwise; audit
A050). The FPS result + p99 frame time land in the test log; a
regression below `meanFps = 30` fails the step and therefore the
rehearsal lane.
