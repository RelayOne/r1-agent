# R07-go — Worker pool with goroutines + channels

Service that spawns N goroutine workers communicating via buffered
channels. Exercises concurrency, graceful shutdown, and context
cancellation.

## Scope

Module `github.com/example/workerpool-demo`.

Workers loop over a shared `<-chan Job`, compute `Job.Square()` (input
squared), write to `chan<- JobResult`. N workers spawned from env
`WORKERS` (default 4).

HTTP surface (chi):
- `POST /jobs` body `{"value": int}` → pushes to queue, returns 202
- `GET /results` → returns accumulated results as JSON array
- `POST /shutdown` closes the job channel, waits for all workers to
  finish, returns 200

`Job = struct { Value int }`; `JobResult = struct { Value, Squared int }`.

## Acceptance

- 50 submitted jobs produce 50 results (test via `httptest.NewServer`
  + goroutine submission).
- `/shutdown` blocks until every worker exits cleanly (no deadlocks).
- `go build` + `go test` + `go vet` all exit 0.
- No `-race` flag violations when running `go test -race ./...`
  (SHOULD also pass `-race`, not required by AC).

## What NOT to do

- No persistence.
- No retries on failed jobs.
- No context-cancellation timeout beyond what the spec requires.
