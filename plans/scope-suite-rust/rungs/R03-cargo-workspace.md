# R03-rust — Cargo workspace with two crates

Split a trivial math library across a cargo workspace. Exercises
workspace manifests, cross-crate imports, and per-crate tests.

## Scope

Workspace layout:
- `crates/math-core/` — library crate exporting `pub fn add(a: i64, b: i64) -> i64`
  and `pub fn mul(a: i64, b: i64) -> i64`. Internal tests in `src/lib.rs`.
- `crates/math-cli/` — binary crate depending on `math-core`.
  Reads two i64 args from argv, prints `add`, newline, `mul`.

Root `Cargo.toml`:
```
[workspace]
members = ["crates/math-core", "crates/math-cli"]
resolver = "2"
```

## Acceptance

- Root `Cargo.toml` declares the workspace.
- `crates/math-core/Cargo.toml` has `[package] name = "math-core"`.
- `crates/math-cli/Cargo.toml` depends on `math-core` via
  `{ path = "../math-core" }`.
- `cargo build --workspace` exits 0.
- `cargo test --workspace` passes at least one test from each crate.
- Integration test in `crates/math-cli/tests/cli.rs` runs the built
  binary with `3 4`, asserts output is exactly `7\n12\n`.

## What NOT to do

- No third-party crates.
- No features, examples, or benches.
- No floating-point math.
