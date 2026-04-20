# R02-rust — CLI binary that echoes args

Small Rust binary crate that reads argv, deduplicates, and prints the
result. Exercises `std::env`, `Vec`, `HashSet`, and integration tests.

## Scope

Create a binary crate `uniq-args` whose `src/main.rs` reads
`std::env::args()`, skips the program name (first element), preserves
first-seen order, drops duplicate strings, then prints each remaining
argument on its own line to stdout. If zero args are passed, it prints
nothing and exits 0.

Add an integration test in `tests/cli.rs` that spawns the binary via
`std::process::Command` on the built target and asserts output matches
expectation for at least:
- no args → empty output, exit 0
- `a b a c b` → `a\nb\nc\n`, exit 0

## Acceptance

- `Cargo.toml` declares `[package] name = "uniq-args"` with a
  `[[bin]]` named `uniq-args`.
- `src/main.rs` exists with the described behavior.
- `tests/cli.rs` exists with the two cases above.
- `cargo build --release` exits 0.
- `cargo test` exits 0; both cases pass.

## What NOT to do

- No third-party crates. Stdlib only (`std::env`, `std::process`,
  `std::collections::HashSet`).
- No subcommands, flags, or `clap`.
- No trimming/lowercasing of args — exact string match dedup.
- No printing to stderr unless the test needs it.
