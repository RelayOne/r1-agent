# R01-rust — Hello world crate

The smallest possible SOW in Rust. One library crate, one function, one
unit test. Designed to converge in 1-2 rounds.

## Scope

Create a Rust library crate with a `Cargo.toml` naming it `greet`.

Create `src/lib.rs` that exports:

```rust
pub fn greet(name: &str) -> String {
    format!("Hello, {}!", name)
}
```

Write at least one `#[test]` inside a `#[cfg(test)] mod tests { … }`
block that asserts `greet("world")` contains the literal `"world"`.

## Acceptance

- `Cargo.toml` exists at repo root with `[package] name = "greet"`.
- `src/lib.rs` exists and exports a function `greet`.
- `src/lib.rs` contains at least one `#[test]` function calling `greet`.
- `cargo build` completes with exit code 0.
- `cargo test` completes with exit code 0 and reports at least one
  passing test.
- No other files added. No binaries, no extra dependencies, no
  `tests/` directory, no CI config, no README extras.

## What NOT to do

- Do not add `[dependencies]` — stdlib is enough.
- Do not create a `main.rs` or split into multiple crates.
- Do not add workspace members, features, or examples.
- Do not use `#![deny(…)]` or other repo-wide lints; keep it
  conventional and minimal.
