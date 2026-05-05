# Deterministic Skills

R1 now has an additive deterministic skill substrate under
`internal/r1skill/`. It does not replace the markdown skill system yet;
it sits beside it during migration.

## What ships now

- Canonical JSON IR in `internal/r1skill/ir`
- 8-stage analyzer in `internal/r1skill/analyze`
- Compile proof generation via `cmd/r1-skill-compile`
- Minimal deterministic runtime in `internal/r1skill/interp`
- Opt-in execution path in `cmd/r1-mcp/backends.go` for manifests
  with `useIR=true`

## Dual-stack migration

- Markdown skills still load through `internal/skill/registry.go`
- Deterministic skills load through `internal/r1skill/registry`
- Capability manifests opt into deterministic execution with:
  - `useIR: true`
  - `irRef: <path to .r1.json>`
  - `compileProofRef: <path to .proof.json>`

## Compile flow

Compile a skill:

```bash
go run ./cmd/r1-skill-compile ./skills/deterministic-echo/skill.r1.json
```

Check-only mode:

```bash
go run ./cmd/r1-skill-compile --check ./skills/deterministic-echo/skill.r1.json
```

The compiler emits a sibling `.proof.json` file. Runtime execution is
allowed only when the manifest references both the IR and its proof.

## Runtime flow

`stoke_invoke` consults `skillmfr.Manifest`. When `useIR=true`, the MCP
backend:

1. loads the IR from `irRef`
2. loads the compile proof from `compileProofRef`
3. executes the skill through `internal/r1skill/interp`
4. returns deterministic output in the MCP response

## Example

The repo ships one deterministic example:

- `skills/deterministic-echo/skill.r1.json`
- `skills/deterministic-echo/skill.r1.proof.json`

It uses the `stdlib:echo` pure function and demonstrates the minimal
compile-and-run path without depending on external effects.

## Current limitations

- HCL parsing is not implemented in this pass; the executable path uses
  canonical JSON IR.
- The interpreter currently supports `pure_fn` and replay-cached
  `llm_call`. Additional node kinds should be added incrementally.
- Markdown remains the default worker-loop skill path; deterministic
  execution is manifest-gated.

## Author guidance

- Keep new deterministic skills in canonical JSON IR for now.
- Generate and commit the `.proof.json` artifact with the skill.
- Use deterministic skills for bounded effectful logic; keep prose
  skills for prompt-time guidance until the migration window closes.
