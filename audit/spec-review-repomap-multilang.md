# Spec Review — repomap-multilang (audit B3)

Spec under review: `/home/eric/repos/r1-agent/specs/repomap-multilang.md`
Source map (ground truth): `/home/eric/repos/r1-agent/specs/research/raw/RT-repomap-multilang.md`
Reviewer model: opus, effort: high. Date: 2026-06-03.

## Result: 10/10 PASS — READY (after 1 critical citation fix + 1 clarity hardening)

---

## CRITICAL ASSUMPTION CHECK — depgraph multi-language (highest-risk)

**VERDICT: CONFIRMED. `depgraph.Build` IS genuinely multi-language. The spec's
import-edge step is correct as written; no regex-inlining fallback is needed.**

Evidence (`/home/eric/repos/r1-agent/internal/depgraph/graph.go`):
- Package doc line 10: "Supports Go, Python, TypeScript, and Rust import patterns."
- `extractImports(content, ext)` dispatch (`graph.go:261-274`): `.go`→Go,
  `.py`→`extractPyImports`, `.ts/.tsx/.js/.jsx`→`extractTSImports`, `.rs`→`extractRustImports`.
- Real per-language extractors exist and are non-trivial:
  - `extractPyImports` `graph.go:296-315` (regex `pyImport` `:254`).
  - `extractTSImports` `graph.go:317-328` (regexes `tsImport` `:255`, `tsRequire` `:256`).
  - `extractRustImports` `graph.go:330-342` (`rustUse` `:257`, `rustMod` `:258`).
- `Build(root, extensions []string)` `graph.go:43-86`; `matchExt` with empty list
  = all extensions (`graph.go:354-364`); `Node{Path, Imports}` `graph.go:25-28`.

The spec's instruction to call `depgraph.Build(root, nil)` and read `Node.Imports`
is correct. The RT's inline-regex catalogue is correctly scoped by the spec as a
do-NOT-do (Boundaries) fallback for a future cycle only. **No spec change required
on this axis.** This was the load-bearing assumption and it holds.

Caveat (already handled by spec, see Failure Mode 5 below): although depgraph
*produces* the imports, the existing `buildReverseEdges` matcher keys on
`node.Package == import-last-segment`, which will NOT connect Python dotted
imports (`app.util`) to a dir-base package key (`app`). Ranking therefore relies
on the symbol-count bonus, which the spec's Acceptance Criteria already state.
Hardened the FileNode `Package` table row to make this explicit (see Criticals).

---

## Citations verified (all against the working tree)

| Citation in spec | Verified | Status |
|---|---|---|
| `repomap.Build` `:55-118` (goast loop `:67-106`; trailing calls `:109/:112/:115`) | repomap.go:55,67,109,112,115,118 | OK |
| `goastKindToString` `:120-140` | repomap.go:120-140 | OK |
| `buildReverseEdges` `:340-360`, `rankFiles` `:364-393`, `collectSymbols` `:395-408` | repomap.go:340,364,395 | OK |
| `Render` `:216-284`, kind switch `:264-277` | repomap.go:216,264-277 | OK |
| `RenderRelevant` `:287-323`, HasSuffix match `:296` | repomap.go:287,296 | OK |
| `buildCallGraphEdges` `:143-167`, `parseGoFile` `:171-212` | repomap.go:143,171 | OK |
| symbol-count bonus `rankFiles` `:384` | repomap.go:384 (`+= len(node.Symbols)*0.1`) | OK |
| `symindex.Build(root)` `:225-277` | index.go:225 | OK |
| `idx.Files()` `:475-482`, `idx.InFile()` `:432-439` | index.go:475,432 | OK |
| `symindex.Symbol` `:43-54`, `SymbolKind` consts `:29-40` | index.go:43-54,29-40 | OK |
| symindex kinds incl. `KindClass`/`KindField` | index.go:30-40 (`function/method/type/interface/class/variable/constant/import/package/field`) | OK |
| `depgraph.Build(root, extensions)` `:43-86`; `Node{Path,Imports}` `:25-28`; `Graph.Nodes` `:37-40` | graph.go:43,25-28,37-40 | OK |
| depgraph import regexes `:250-259`; `extractImports` `:261-274` | graph.go:250-259,261-274 | OK |
| `repomap.Build` call sites `cmd/r1/main.go:355,1075,1382` | main.go:355,1075,1382 | OK |
| 4th call site cited as `2753` | **ACTUAL: main.go:2755** (`if rm, rmErr := repomap.Build(absRepo)`) | **STALE → FIXED** |
| `workflow.go:2054` RenderRelevant + `:2055` `mapContent != ""` guard | workflow.go:2054,2055 | OK |
| `e.RepoMapBudget` `workflow.go:138-139`; default 2000 | workflow.go:139, default `budget = 2000` at :2052 | OK |
| `setupTestRepo` `:10-72`, `TestBuild` `:74-101`, `TestRender` `:103-117`, `TestRenderRelevant` `:131-140` | repomap_test.go:10,74,103,131 | OK |
| `TestBuild` asserts `len(rm.Files)==2` + symbols `Main,Config,NewConfig,Hello,FormatName,Helper,Process,Reset` | repomap_test.go:82,96 | OK |
| `TestSkipHiddenAndVendor` `:216-233` asserts `len(rm.Files)==1` | repomap_test.go:216,230 | OK |
| `TestPythonSymbols` `:225-251`, `TestTypeScriptSymbols` `:253-281` | index_test.go:225,253 | OK |
| Import-cycle claim (symindex→goast only; depgraph→logging only; neither→repomap) | grep confirmed acyclic; module `github.com/RelayOne/r1` | OK |

**Stale citations found: 1** (the `2753` call site, in 2 locations). Fixed to `2755`.

---

## Per-failure-mode verdict

### 1. Self-contained checklist items — PASS
Each of the 9 checklist items carries its own file path, function, line range,
recipe, and VERIFY command. Item 2 inlines the full `buildFromSymindex` recipe;
item 3 inlines the full kind map; item 7/8 name the fixtures and model files.
A subagent could implement any single item from that item alone.

### 2. Library ambiguity — PASS
Every package is fully qualified: `github.com/RelayOne/r1/internal/symindex`,
`.../internal/depgraph`, `path/filepath`. The Library Preferences section names
symindex for symbols, depgraph for imports, and explicitly bans `skillselect`.
No bare "the symbol library".

### 3. Pattern references — PASS
All referenced files exist and were read: `repomap.go`, `symindex/index.go`,
`depgraph/graph.go`, `repomap_test.go`, `symindex/index_test.go`. Mirror targets
(`goastKindToString`, `setupTestRepo`, `TestPythonSymbols`,
`TestTypeScriptSymbols`) all resolve.

### 4. Missing error responses — PASS (N/A HTTP; library error matrix present)
No HTTP surface (correctly marked N/A). The Error Handling table enumerates 4
failure modes with exact strategy + caller-visible result: `symindex.Build` error
(best-effort skip, nil error preserved), `depgraph.Build` error (`dg, _ :=`),
file-in-dg-not-in-idx (`if node, ok := rm.Files[path]; ok`), and `filepath.Dir`
== `"."` root-level case. All match real code behavior (depgraph/symindex both
best-effort, swallow per-file read errors).

### 5. Vague acceptance criteria — PASS
Acceptance criteria use concrete, test-verifiable predicates: `len(rm.Files) > 0`,
`len(rm.Symbols) > 0`, `len(out) > len("# Repository Map\n\n")`, "rank
`app/util.py` at or above `app/main.py`", and the exact CI gate command. No
"appropriate"/"proper"/"handle correctly". The one nuance — that Python import
edges do NOT actually connect and ranking is via symbol-count bonus — is now
stated explicitly in both the Acceptance Criteria (already present) and the
FileNode `Package` table row (hardened this pass).

### 6. Missing boundaries — PASS
Boundaries section is thorough: exact functions that must have zero diff
(`buildReverseEdges/rankFiles/collectSymbols/Render/RenderRelevant/
buildCallGraphEdges/parseGoFile`), no signature changes to `Build/Render/
RenderRelevant`, no call-site edits, no new third-party deps, no inline regexes,
no `skillselect`. Edit surface is named: only `Build` + new
`buildFromSymindex/symKindToString/pkgKey/sigFor` + import block + test file.

### 7. Dependency ordering — PASS
Order is correct and buildable top-down: item 1 (seam) → 2 (method) → 3 (shim) →
4 (imports) → 5/6 (no-change confirmations) → 7/8 (tests) → 9 (full gate). Item 1
calls `buildFromSymindex` which item 2 defines — but both land in the same commit
and item 1's VERIFY is `go build` which only passes once item 2 exists; the
intent (build incrementally) is sound and the items are not circularly dependent.
No item references a later item's output as a precondition.

### 8. Verification specificity — PASS
Every item has a concrete VERIFY command: `go build ./internal/repomap`,
`go vet ./internal/repomap`, `go test ./internal/repomap/... -run Python -v`,
`-run TS -v`, and the final CI gate. Each is a runnable command checking a
specific package/file.

### 9. Unresearched assumptions — PASS
The single highest-risk assumption (depgraph multi-language) is now hard-verified
above and is correct. Import regexes, symbol kinds, line numbers, and the
acyclic-import claim are all backed by the RT and re-verified here against the
tree. No unconfirmed library versions or speculative API shapes.

### 10. Missing specific values — PASS
Concrete values throughout: PageRank 3 iterations / damping 0.15 / symbol bonus
0.1 / call bonus 0.15 (cited from `rankFiles`), budget default 2000
(`workflow.go:2052`), file perms `0755`/`0o600`, the exact kind-map table, the
exact empty-header sentinel `"# Repository Map\n\n"`. No "recommended settings".

---

## Bundled-item check (failure mode 9 variant) — PASS
No item bundles unrelated work. Items 5 and 6 are pure confirmation/no-op guards
(intentional, cheap, and verifiable), not hidden implementation. Tests are split
Python (item 7) / TypeScript (item 8) rather than bundled.

## Go-path-no-regression safety — PASS (explicitly stated)
The fallback fires only when `len(rm.Files) == 0` after the goast loop, so any
repo with >=1 parseable Go file is byte-identical. Asserted by unchanged
`TestBuild` (`len==2`) and `TestSkipHiddenAndVendor` (`len==1`), both of which
reach >=1 Go file and therefore never trigger the fallback. No public signature
change (`Build/Render/RenderRelevant` untouched). Verified the seam insertion
point (after repomap.go:106 `}`, before :109 `buildReverseEdges`) is correct.

## Synthetic non-Go fixture test — concrete (PASS)
Python fixture (`setupPythonRepo`) and TS fixture (`setupTSRepo`) are specified
with exact file contents, directory layout, perms, and assertions, modeled on
real existing fixtures. Cross-checked against symindex behavior:
- Python `class App` + `def run(self)` + `def helper/main` are all non-underscore
  → `isExported` (`index.go:624-625`) marks them exported → survive the
  `!s.Exported` filter. `class App` emits `KindClass` → mapped to `"type"` by
  `symKindToString` → renders as `type App`. Assertions for `App`/`helper` hold.
- TS `export interface Config` / `export class App` → `isExported` default branch
  (`index.go:628-629`) returns true → exported. Assertions for `Config`/`App` hold.
  NOTE: symindex `isExported` for TS/JS returns true unconditionally (does not
  check the `export` keyword), so the exported-only filter never drops TS symbols.
  Harmless for the fixtures; flagged for implementer awareness.
The regression-guard assertion `len(out) > len("# Repository Map\n\n")` precisely
targets the B3 empty-header defect.

---

## Criticals found / fixed (this pass)

1. **[STALE CITATION — critical]** 4th `repomap.Build` call site cited as
   `cmd/r1/main.go:2753` in two places (Boundaries + checklist item 6). Actual
   line is **2755** (`if rm, rmErr := repomap.Build(absRepo)`). An implementer
   running the item-6 grep/no-edit confirmation against `:2753` would inspect the
   wrong line. **FIXED** → `2755` in both locations.

2. **[CLARITY HARDENING — non-blocking, fixed]** The FileNode `Package` table row
   implied `buildReverseEdges`/`RenderRelevant` "can match imports". In reality,
   for Python dotted imports and most TS relative imports the dir-base package key
   will NOT equal the import's last segment, so no import edges form and ranking
   relies solely on the symbol-count bonus. The Acceptance Criteria already stated
   this correctly; hardened the data-model row to remove the misleading
   implication and cite `repomap.go:350-351` + `:384`. **FIXED.**

**Criticals fixed: 1** (the stale citation). Plus 1 clarity hardening.

---

## Final verdict: READY

The spec is implementable as written. Core technical assumption (depgraph is
multi-language) is verified true. All but one citation resolved exactly; the one
stale call-site line was corrected inline. Go-path non-regression, no-API-change,
and the synthetic non-Go fixtures are all concrete and test-verifiable. Scope was
not weakened.
