<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-06-03 -->
<!-- CREATED: 2026-06-03 -->
<!-- DEPENDS_ON: (none) -->
<!-- BUILD_ORDER: 4 -->
<!-- REVIEW: Codex (GPT) cross-model PASS first-pass, 0 findings. Full gate: go build + vet + go test ./... all exit 0. Go path byte-identical (7 existing tests pass); Python + TS fixtures prove non-empty ranked maps. Single commit R1 (cb21d792). -->
<!-- HOLISTIC: production-readiness/collision/playwright N/A — internal Go library. -->

# Language-Agnostic Repo Map (audit B3) — Implementation Spec

## Overview

`internal/repomap.Build()` is Go-only: its sole data source is
`goast.AnalyzeDir(root)`, which ingests **only** `.go` files. For a non-Go repo
(Python / TS / JS / Rust / etc.) `AnalyzeDir` returns a non-nil `*Analysis` with
an empty `Files` slice, so `rm.Files` stays empty, `Render`/`RenderRelevant`
emit only the `# Repository Map\n\n` header, and the repomap injected into
execute prompts at `internal/workflow/workflow.go:2054` is dropped by the
`mapContent != ""` guard (`workflow.go:2055`). Result: zero structural context
for every non-Go repo.

This spec adds a **fallback seam inside `Build()`**: when the goast loop yields
no files (`len(rm.Files) == 0`), populate the `FileNode` graph from
`symindex.Build` (multi-language symbols, AST for Go + regex for others) and
`depgraph.Build` (multi-language imports), then run the **unchanged**
`buildReverseEdges` → `rankFiles` → `collectSymbols` pipeline. The PageRank/render
path is already language-neutral; the only new code is the fallback population
method plus a symbol-kind string shim. **No public API change** (`Build`,
`Render`, `RenderRelevant` signatures untouched); **no call-site edits**; the Go
path stays byte-identical because the fallback only fires when there are zero Go
files. Consumer: every author/agent whose target repo is not Go.

## Stack & Versions

- Go (repo standard toolchain; module `github.com/RelayOne/r1`).
- No new third-party dependencies. New internal imports only:
  `github.com/RelayOne/r1/internal/symindex`,
  `github.com/RelayOne/r1/internal/depgraph`.
- CI gate (repo CLAUDE.md): `go build ./cmd/r1`, `go test ./...`, `go vet ./...`.

## Existing Patterns to Follow

- Build seam / pipeline: `internal/repomap/repomap.go`
  - `Build(root)` `repomap.go:55-118` (goast loop `:67-106`; trailing
    `buildReverseEdges`/`rankFiles`/`collectSymbols` `:109/:112/:115`).
  - `goastKindToString(goast.SymbolKind) string` `repomap.go:120-140` — mirror
    its switch shape for the new `symKindToString`.
  - `buildReverseEdges()` `repomap.go:340-360`, `rankFiles()` `repomap.go:364-393`,
    `collectSymbols()` `repomap.go:395-408`, `Render(budget)` `repomap.go:216-284`,
    `RenderRelevant(files,budget)` `repomap.go:287-323` — all language-neutral,
    **reuse unchanged**.
- Multi-language symbols: `internal/symindex/index.go`
  - `Build(root) (*Index, error)` `index.go:225-277` (Go via AST, others via
    regex; skips `.`/`node_modules`/`vendor`/`__pycache__`).
  - `idx.Files() []string` `index.go:475-482` (sorted relative paths).
  - `idx.InFile(file) []Symbol` `index.go:432-439`.
  - `symindex.Symbol` `index.go:43-54` (`Name, Kind, File, Line, Parent,
    Signature, Exported`).
  - `SymbolKind` constants `index.go:29-40`
    (`function/method/type/interface/class/variable/constant/import/package/field`).
- Multi-language imports: `internal/depgraph/graph.go`
  - `Build(root, extensions []string) (*Graph, error)` `graph.go:43-86`
    (nil extensions = all files; skips vendor/node_modules/.git/target/__pycache__).
  - `Graph.Nodes map[string]*Node` `graph.go:37-40`; `Node{Path, Imports}`
    `graph.go:25-28` (Path is relative).
  - Import regexes `graph.go:250-259`; dispatch `extractImports` `graph.go:261-274`.
- Tests: `internal/repomap/repomap_test.go`
  - `setupTestRepo(t)` `:10-72`, `TestBuild` `:74-101`, `TestRender` `:103-117`,
    `TestRenderRelevant` `:131-140` — copy structure (`t.TempDir`,
    `os.MkdirAll(..., 0755)`, `os.WriteFile(..., 0o600)`, `filepath.Join`).
  - `internal/symindex/index_test.go` `TestPythonSymbols` `:225-251`,
    `TestTypeScriptSymbols` `:253-281` — copy the `.py`/`.ts` fixture bodies.

## Library Preferences

- Symbols: `internal/symindex` (do NOT re-implement extraction).
- Imports: `internal/depgraph` (do NOT inline regexes; patterns already exist).
- Language decision: the `len(rm.Files) == 0` post-goast predicate (do NOT add
  `skillselect` — it adds a dependency without improving correctness; see
  Boundaries).

## Data Models

No new exported types. Reuse `repomap.FileNode` (`repomap.go:34-44`) and
`repomap.Symbol` (`repomap.go:24-31`). The fallback populates these existing
fields per file:

### FileNode (fields the fallback fills)
| Field | Source | Notes |
|-------|--------|-------|
| `Path` | `symindex.Symbol.File` (relative) | map key in `rm.Files` |
| `Package` | `pkgKey(rel)` = `filepath.Base(filepath.Dir(rel))` | non-empty key that lets `buildReverseEdges`/`RenderRelevant` *attempt* to match imports. **Best-effort only:** `buildReverseEdges` (`repomap.go:350-351`) matches the import's last `/`-segment against `node.Package`; for Python dotted imports (e.g. `import app.util`, last segment `app.util`) and most TS relative imports this will NOT equal the dir-base package key, so zero import edges form and ranking falls back to the symbol-count bonus (`rankFiles` `:384`). This is acceptable and matches the Acceptance Criteria (ranking via symbol count, not import edges). |
| `Symbols` | `idx.InFile(f)`, exported only, `KindField` skipped | kind mapped via `symKindToString` |
| `Imports` | `depgraph.Build(root, nil).Nodes[path].Imports` | language-specific import strings |
| `ImportedBy` | filled by unchanged `buildReverseEdges()` | reverse edges |
| `Rank` | filled by unchanged `rankFiles()` | PageRank + symbol-count bonus |
| `CalledBy` | left `0` (no call graph for regex langs) | symbol-count bonus still ranks |

### Symbol (per fallback symbol)
| Field | Value |
|-------|-------|
| `Name` | `symindex.Symbol.Name` |
| `Kind` | `symKindToString(s.Kind)` → one of `func/method/type/interface/var/const` |
| `File` | relative path `f` |
| `Line` | `symindex.Symbol.Line` |
| `Package` | `node.Package` |
| `Signature` | `symindex.Symbol.Signature` **only** when kind is `func`/`method`; else `""` |

## API Endpoints

N/A — library change, no HTTP surface.

## Business Logic

### Fallback population (`buildFromSymindex`)
1. **Validate / guard:** only invoked from `Build()` when `len(rm.Files) == 0`
   after the goast loop. (Caller-side guard; method assumes empty `rm.Files`.)
2. **Execute:**
   - `idx, err := symindex.Build(root)` — on error, return without populating
     (degrade to empty map; do not propagate, since goast already succeeded and
     `Build` should not start erroring for non-Go where it previously returned a
     valid empty map). Same best-effort posture for `dg, _ := depgraph.Build(root, nil)`.
   - For each `f := range idx.Files()`: get/create `node := rm.Files[f]`
     (`&FileNode{Path: f, Package: pkgKey(f)}`); for each `s := idx.InFile(f)`
     skip when `!s.Exported || s.Kind == symindex.KindField`; append
     `Symbol{Name, Kind: symKindToString(s.Kind), File: f, Line: s.Line,
     Package: node.Package, Signature: sigFor(s)}` where `sigFor` returns
     `s.Signature` only for func/method kinds, else `""`.
   - For each `path, n := range dg.Nodes`: if `node, ok := rm.Files[path]; ok`
     set `node.Imports = n.Imports`. (depgraph may include files symindex did
     not index, e.g. importless modules — only attach to nodes that exist.)
3. **Side effects:** mutates `rm.Files` in place. No I/O beyond the two Build
   scans.
4. **Return:** nothing; `Build` then runs `buildReverseEdges`/`rankFiles`/
   `collectSymbols` (`repomap.go:109/:112/:115`) on the populated graph.

### symKindToString shim
Map `symindex.SymbolKind` → repomap string kind expected by `Render`'s switch
(`repomap.go:264-277`): `KindFunction→"func"`, `KindMethod→"method"`,
`KindType→"type"`, `KindClass→"type"` (repomap has no "class" string; "type"
renders cleanly), `KindInterface→"interface"`, `KindVariable→"var"`,
`KindConstant→"const"`, default `"var"`.

## Error Handling

| Failure | Strategy | Caller Sees |
|---------|----------|-------------|
| `symindex.Build(root)` returns error | best-effort: skip symbol population, leave `rm.Files` empty, do not propagate | `Build` returns valid (possibly empty) `*RepoMap`, `nil` error — same as today for non-Go |
| `depgraph.Build(root, nil)` returns error | best-effort: skip import attach (`dg, _ :=`); symbol-count bonus still ranks | non-empty map (symbols only, zero import edges) |
| File in `dg.Nodes` not in `idx.Files()` | guard `if node, ok := rm.Files[path]; ok` before attaching imports | no panic; orphan import file ignored |
| `filepath.Dir(rel)` is `"."` (root-level file) | `filepath.Base(".")` → `"."`; acceptable non-empty package key | files in same dir still group; cross-dir matching best-effort |

## Boundaries — What NOT To Do

- Do NOT change the signatures of `Build`, `Render`, or `RenderRelevant`
  (`repomap.go:55,216,287`). The fix is internal to `Build`.
- Do NOT modify the goast block (`repomap.go:67-106`), `buildCallGraphEdges`
  (`:143-167`), `buildReverseEdges` (`:340-360`), `rankFiles` (`:364-393`),
  `collectSymbols` (`:395-408`), `Render` (`:216-284`), or `RenderRelevant`
  (`:287-323`). They are already language-neutral.
- Do NOT regress the Go path: the fallback fires **only** when
  `len(rm.Files) == 0`, so any repo with ≥1 parseable Go file behaves exactly as
  before (verified by the unchanged `TestBuild`/`TestRender`/`TestRenderRelevant`).
- Do NOT edit any `repomap.Build` call site
  (`cmd/r1/main.go:355,1075,1382,2755`) or the injection site
  (`internal/workflow/workflow.go:2054`). The fix is transparent to callers.
- Do NOT add heavy parsers or new third-party deps. Symbols come from
  `symindex` (regex/AST), imports from `depgraph` (regex). Nothing else.
- Do NOT inline the per-language import regexes — call `depgraph.Build(root, nil)`.
  (Inline patterns are catalogued in RT §"Exact regex import patterns" only as a
  fallback if a future cycle drops the depgraph dependency; not for this spec.)
- Do NOT add `skillselect.DetectStack` language detection — the `len==0` seam is
  simpler, dependency-free, and triggers exactly when goast yielded nothing.
- Do NOT touch `parseGoFile` (`repomap.go:171-212`) — test-only back-compat, not
  on the Build path.

## Testing

### `internal/repomap/repomap_test.go` — Python fixture + assertions
- [ ] Helper `setupPythonRepo(t) string`: `t.TempDir()`; `os.MkdirAll(dir/"app",
  0755)`; write `app/main.py` containing `import app.util`, `class App:` with a
  `    def run(self):` method, and a top-level `def main():`; write `app/util.py`
  containing several top-level `def`s (e.g. `def helper():`, `def parse():`,
  `def load():`, `def save():`) so its symbol-count bonus ranks it high. Use
  `os.WriteFile(..., 0o600)` and `filepath.Join` exactly as `setupTestRepo`.
- [ ] Happy `TestBuildPythonRepo`: `rm, err := Build(dir)` → no error;
  `len(rm.Files) > 0` (proves fallback fired); `len(rm.Symbols) > 0`; a `found`
  name set contains `App` and `helper` (Python symbols extracted via fallback).
- [ ] Happy `TestRenderPython`: `out := rm.Render(0)` contains `"Repository Map"`
  **and** a Python symbol name (`App` and/or `helper`).
- [ ] Happy `TestRenderRelevantPython`: `out := rm.RenderRelevant(
  []string{"app/main.py"}, 0)` is non-empty and contains an expected symbol
  (`App`); the top-ranked file (most symbols) is `app/util.py` — assert its
  symbols (`helper`) appear in `out`.
- [ ] Edge (regression guard): `len(out) > len("# Repository Map\n\n")` for the
  `RenderRelevant` output — this is the exact empty-header defect B3 fixes.

### `internal/repomap/repomap_test.go` — TypeScript fixture (second regex lang)
- [ ] Helper `setupTSRepo(t) string`: `t.TempDir()`; write `src/app.ts` with
  `import { Helper } from "./util"`, `export interface Config {}`,
  `export class App {}`, and `src/util.ts` with multiple
  `export function`/`export const` decls. Mirror `symindex` `TestTypeScriptSymbols`
  fixture bodies (`index_test.go:253-281`).
- [ ] Happy `TestBuildTSRepo`: `rm, _ := Build(dir)`; `len(rm.Files) > 0`;
  symbol set contains `Config` and `App`.

### Go path non-regression (already covered, re-run unchanged)
- [ ] `TestBuild` (`:74-101`) still asserts `len(rm.Files) == 2` and finds
  Go symbols `Main, Config, NewConfig, Hello, FormatName, Helper, Process, Reset`
  — proves the Go path is byte-identical and the fallback did NOT fire.
- [ ] `TestSkipHiddenAndVendor` (`:216-233`) still asserts `len(rm.Files) == 1`
  (single Go file ⇒ fallback never reached).

## Acceptance Criteria

- WHEN `Build(root)` runs against a repo containing zero parseable Go files but
  ≥1 Python/TS/JS file, THE SYSTEM SHALL return `rm` with `len(rm.Files) > 0`
  and `len(rm.Symbols) > 0`.
- WHEN `RenderRelevant(relevantFiles, budget)` is called on such a non-Go map,
  THE SYSTEM SHALL return a string with `len(out) > len("# Repository Map\n\n")`
  (non-empty body), so `workflow.go:2055`'s `mapContent != ""` guard passes and
  the repomap reaches the LLM.
- WHEN a Python fixture has `app/util.py` (more symbols) and `app/main.py`,
  THE SYSTEM SHALL rank `app/util.py` at or above `app/main.py` (its symbols
  appear in the rendered output), demonstrating the symbol-count bonus
  (`rankFiles` `:384`) produces a non-uniform ranking.
- WHEN `Build(root)` runs against a repo with ≥1 parseable Go file,
  THE SYSTEM SHALL produce output identical to pre-change behavior
  (`TestBuild`/`TestRender`/`TestRenderRelevant` pass unchanged; fallback never
  invoked).
- WHEN the build/test/vet gate runs, THE SYSTEM SHALL pass
  `go build ./cmd/r1 && go test ./internal/repomap/... && go vet ./internal/repomap`.

## Implementation Checklist

1. [ ] **Add fallback branch in `Build()`** — `internal/repomap/repomap.go`,
   function `Build(root string) (*RepoMap, error)` (`:55-118`). Insert between
   the end of the goast block (`:106`) and `rm.buildReverseEdges()` (`:109`):
   ```go
   // Non-Go repos: goast yields no files. Fall back to language-agnostic
   // symbol/import extraction so the same ranking pipeline runs.
   if len(rm.Files) == 0 {
       rm.buildFromSymindex(root)
   }
   ```
   Do NOT alter the goast loop or the trailing
   `buildReverseEdges`/`rankFiles`/`collectSymbols` calls.
   **VERIFY:** `go build ./internal/repomap`.

2. [ ] **Implement `(*RepoMap).buildFromSymindex(root string)`** — new method in
   `internal/repomap/repomap.go` (place after `Build`, before `goastKindToString`).
   Recipe (RT §4): `idx, err := symindex.Build(root)`; on err `return`. For each
   `f := range idx.Files()` get/create `node := rm.Files[f]`
   (`&FileNode{Path: f, Package: pkgKey(f)}`, store in `rm.Files[f]`); for each
   `s := range idx.InFile(f)` skip when `!s.Exported || s.Kind ==
   symindex.KindField`, else append
   `Symbol{Name: s.Name, Kind: symKindToString(s.Kind), File: f, Line: s.Line,
   Package: node.Package, Signature: sigFor(s)}`. Then `dg, _ := depgraph.Build(
   root, nil)`; if `dg != nil` for each `path, n := range dg.Nodes` set
   `node.Imports = n.Imports` only `if node, ok := rm.Files[path]; ok`. Add
   helper `pkgKey(rel string) string { return filepath.Base(filepath.Dir(rel)) }`
   and `sigFor(s symindex.Symbol) string` returning `s.Signature` only for
   `KindFunction`/`KindMethod`, else `""`. Leave `node.CalledBy` at 0.
   **VERIFY:** `go build ./internal/repomap`.

3. [ ] **Add `symKindToString(symindex.SymbolKind) string` shim** —
   `internal/repomap/repomap.go`, mirror `goastKindToString` (`:120-140`):
   `KindFunction→"func"`, `KindMethod→"method"`, `KindType→"type"`,
   `KindClass→"type"`, `KindInterface→"interface"`, `KindVariable→"var"`,
   `KindConstant→"const"`, default `"var"`. These string kinds must match the
   `Render` switch cases (`repomap.go:264-277`).
   **VERIFY:** `go build ./internal/repomap`.

4. [ ] **Add imports** to `internal/repomap/repomap.go` import block (`:14-21`):
   `github.com/RelayOne/r1/internal/symindex`,
   `github.com/RelayOne/r1/internal/depgraph`, and `path/filepath` (for
   `pkgKey`). Keep the existing `goast` import. No import cycle: symindex imports
   only `goast`; depgraph imports only `logging`; neither imports repomap.
   **VERIFY:** `go vet ./internal/repomap` (cycle/unused-import check).

5. [ ] **Confirm pipeline functions untouched** — `internal/repomap/repomap.go`:
   `buildReverseEdges` (`:340-360`), `rankFiles` (`:364-393`), `collectSymbols`
   (`:395-408`), `Render` (`:216-284`), `RenderRelevant` (`:287-323`),
   `buildCallGraphEdges` (`:143-167`), `parseGoFile` (`:171-212`) must have
   **zero** diff. They are already language-neutral and consume only
   `ImportedBy/Imports/Symbols/CalledBy/Package`.
   **VERIFY:** `git diff internal/repomap/repomap.go` shows changes ONLY in
   `Build`, new `buildFromSymindex`/`symKindToString`/`pkgKey`/`sigFor`, and the
   import block.

6. [ ] **Confirm callers + injection site unchanged** — no edits to
   `cmd/r1/main.go:355,1075,1382,2755` (all `repomap.Build(absRepo)`) or
   `internal/workflow/workflow.go:2054` (`RenderRelevant`). The fix is internal
   to `Build`; every site benefits transparently.
   **VERIFY:** `git diff --stat` lists ONLY `internal/repomap/repomap.go` and
   `internal/repomap/repomap_test.go`.

7. [ ] **Add Python tests** — `internal/repomap/repomap_test.go`: `setupPythonRepo`,
   `TestBuildPythonRepo`, `TestRenderPython`, `TestRenderRelevantPython` (with the
   `len(out) > len("# Repository Map\n\n")` regression guard and the
   `app/util.py` top-rank assertion) per the Testing section, modeled on
   `setupTestRepo` (`:10-72`) / `TestRenderRelevant` (`:131-140`) and symindex
   `TestPythonSymbols` (`index_test.go:225-251`).
   **VERIFY:** `go test ./internal/repomap/... -run Python -v`.

8. [ ] **Add TypeScript tests** — `internal/repomap/repomap_test.go`:
   `setupTSRepo`, `TestBuildTSRepo` asserting `len(rm.Files) > 0` and symbols
   `Config`/`App`, modeled on symindex `TestTypeScriptSymbols`
   (`index_test.go:253-281`).
   **VERIFY:** `go test ./internal/repomap/... -run TS -v`.

9. [ ] **Full verification gate** (repo CLAUDE.md CI gate):
   **VERIFY:** `go build ./cmd/r1 && go test ./internal/repomap/... && go vet ./internal/repomap && go test ./...`.
