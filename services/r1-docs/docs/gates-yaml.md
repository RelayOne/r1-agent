# `.stoke/gates.yaml` — gates preset schema

R1's verify pipeline decides whether a task may merge by running
it through a preset bundle of per-gate thresholds and composite
weights. The R1 site's gates demo drags a single "strictness"
slider — under the hood that slider switches between three
committed preset files in `.stoke/gates.d/`.

## Layout

```
.stoke/
  gates.d/
    default.yaml   # balanced, ship everyday changes
    strict.yaml    # regulated / safety-critical
    fast.yaml      # experimental spikes
  gates.yaml       # (optional) symlink or override; loader reads this
                   # first, falls back to gates.d/<name>.yaml by preset name
```

The directory name `.stoke/` is preserved during the R1 rename;
`.r1/` is resolved as a dual-accept alias per the rename plan.

## Loader

Go callers use the exported helpers in `internal/verify`:

```go
preset, err := verify.LoadGatesYAML(".stoke/gates.d/default.yaml")
if err != nil { /* missing file, malformed yaml, or invalid shape */ }

all, err := verify.LoadGatesPresetDir(".stoke/gates.d")
// all["default"], all["strict"], all["fast"]
```

`LoadGatesYAML` reports three failure modes distinctly:

1. **Missing file** — `os.IsNotExist(err)` returns true on the
   wrapped error.
2. **Malformed YAML** — the error message contains the YAML parser
   diagnostic and the offending path.
3. **Semantically invalid preset** — empty preset name, no gates,
   duplicate gate IDs, thresholds outside `[0,1]`, or negative
   weights.

Empty-gate presets are rejected explicitly: a preset with no gates
would silently pass every artifact, which is exactly the failure
mode the preset system exists to prevent.

## Schema

| Field | Type | Required | Meaning |
|---|---|---|---|
| `preset` | string | yes | Short preset name (e.g. `default`, `strict`). Unique within a directory; case-insensitive for lookup. |
| `description` | string | no | Free-form prose shown in pickers and help output. |
| `composite.threshold` | float in `[0,1]` | yes | Overall composite score the preset must clear to pass. |
| `composite.weights` | map[gate_id]float | no | Per-axis weights used in the weighted average. Unlisted gate IDs default to `1.0`. Negatives rejected. |
| `gates[].id` | string | yes | Gate identifier (`build`, `tests`, `lint`, `review`, `scope`). Unique within a preset. |
| `gates[].threshold` | float in `[0,1]` | yes | Per-gate score required for this gate to pass. For pass/fail gates use `1.0`. |
| `gates[].blocker` | bool | no | Hard gate: failure fails the preset regardless of composite. Default `false`. |

## Canonical gate IDs

The R1 gates demo UI advertises five axes. Presets are free to
declare more, but these five are the canonical set:

- `build` — compile / build succeeds end-to-end.
- `tests` — every declared test runs to completion and passes;
  any test flagged as not-executed counts against this gate
  unless annotated with an explicit reason.
- `lint` — lint output clean, or only contains warnings that
  predate the change under verification.
- `review` — adversarial cross-family reviewer signs off.
- `scope` — modified files stay within the declared task scope.

A preset may omit any of these if the caller's pipeline doesn't
run the underlying gate. Unknown IDs are accepted at load time —
enforcement is the caller's concern.

## Example

```yaml
preset: default
description: Balanced defaults for general-purpose code tasks.
composite:
  threshold: 0.80
  weights:
    build: 2.0
    tests: 2.0
    lint: 1.0
    review: 1.5
    scope: 1.0
gates:
  - id: build
    threshold: 1.0
    blocker: true
  - id: tests
    threshold: 1.0
    blocker: true
  - id: lint
    threshold: 0.9
    blocker: false
  - id: review
    threshold: 0.8
    blocker: true
  - id: scope
    threshold: 1.0
    blocker: true
```

A task passes this preset when (a) every blocker gate clears its
per-gate threshold AND (b) the weighted composite score across all
listed gates clears `0.80`.

## Precedence

1. If `.stoke/gates.yaml` exists and points at a preset (by `preset:`
   key or symlink to a file under `gates.d/`), the loader uses it.
2. Otherwise callers select by name: `LoadGatesPresetDir(".stoke/gates.d")`
   then lookup `all[strings.ToLower(name)]`.
3. If nothing matches, R1 falls back to the in-code `CodeRubric`
   in `internal/verify/rubrics.go` — the pre-preset-era default.

## Integration

The loader is a self-contained library. Callers use
`LoadGatesYAML` / `LoadGatesPresetDir` to read a preset, then
enforce the thresholds via whatever mechanism fits their code
path — a preset-aware verification renderer, a CLI flag
(`--gates <name>`), a rubric-threshold override layer on top of
`verify.Pipeline`, or a composite-score report formatter. The
preset schema is stable on its own; different integration points
can adopt it independently.
