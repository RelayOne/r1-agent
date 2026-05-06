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

# 2. Seed a 3000-node fixture session (TODO — fixture seeder lands in
#    a follow-up; until then the runner skips with the e2e Go test
#    surfacing the missing fixture).

# 3. Run the e2e Go test.
R1_SERVER_UI_V2=1 go test -tags=e2e -run TestGraph3kFPS ./cmd/r1-server/...
```

## CI lane

The `cloudbuild-e2e.yaml` step (release-rehearsal trigger) runs the
above sequence with the fixture seeder enabled and posts the FPS
result + p99 frame time to a build-summary annotation. A regression
below `meanFps = 30` blocks the release.
