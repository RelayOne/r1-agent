# R07-rust — background worker pool with mpsc channels

Small async service that offloads CPU/IO work to a pool of workers
communicating via tokio mpsc channels. Exercises task joinsets,
channel backpressure, and graceful shutdown.

## Scope

Binary crate `workerpool-demo`. Main spawns N workers (from env
`WORKERS`, default 4). Each worker loops receiving `Job` messages from
a shared `mpsc::Receiver<Job>` and writes results to a shared
`mpsc::Sender<JobResult>`.

Job type is `Job::Compute(u32)` — returns `JobResult::Computed(u32, u64)`
where `u64` is `(value as u64).pow(2)`. Plus `Job::Shutdown` which
causes the worker to break the loop after draining pending messages.

HTTP surface: Axum with:
- `POST /jobs { value }` pushes to channel, returns 202.
- `GET /results` returns all accumulated results as JSON array.
- `POST /shutdown` sends `Job::Shutdown` to all workers, awaits their
  JoinHandle completion, returns 200.

## Acceptance

- `Cargo.toml`: axum, tokio full, serde/serde_json, dev reqwest.
- Test submits 50 compute jobs, polls `/results` until array size is
  50, asserts every result's square matches expectation.
- Test shows `/shutdown` blocks until all workers exit (response only
  arrives after join).
- `cargo build --release` + `cargo test` exit 0.
- No deadlocks or panics under the test load.

## What NOT to do

- No persistence.
- No `tokio::spawn` in the HTTP handler beyond what's needed to send
  on the channel.
- No rayon or std-thread workers — tokio tasks only.
