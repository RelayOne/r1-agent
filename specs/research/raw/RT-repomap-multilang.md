# RT: Language-Agnostic Repo Map (audit finding B3)

Integration map for making `internal/repomap` produce a real ranked map for
non-Go repos (Python / TS / JS / etc.). All paths absolute; all line numbers
verified against the working tree at investigation time.

**Root cause (confirmed):** `repomap.Build()` at
`/home/eric/repos/r1-agent/internal/repomap/repomap.go:62` calls
`goast.AnalyzeDir(root)`. `goast.AnalyzeDir`
(`/home/eric/repos/r1-agent/internal/goast/goast.go:177-211`) walks the tree
but **only** ingests files where `strings.HasSuffix(path, ".go")` (line 195) —
non-`.go` files are skipped. It returns a **non-nil** `*Analysis` whose `Files`
slice is empty for a non-Go repo (it is constructed `a := &Analysis{}` at
:178 and only appended to for Go files). Therefore in `Build()` the guard
`if analysis != nil` (`repomap.go:67`) is **true**, but the `for _, fa := range
analysis.Files` loop (`:68`) iterates zero times, so `rm.Files` stays empty,
`rm.Symbols` stays empty, and `Render`/`RenderRelevant` emit only the
`# Repository Map` header. Net effect: the repomap injected into execute
prompts at `workflow.go:2054` is empty for every non-Go repo, and the
`if mapContent != ""` guard at `workflow.go:2055` drops it entirely.

---

## 1. CURRENT repomap internals

File: `/home/eric/repos/r1-agent/internal/repomap/repomap.go` (410 lines).

### Structs

- `Symbol` (`:24-31`): `Name, Kind string` (kind is the *string* form: `func`,
  `type`, `method`, `const`, `var`, `interface`), `File string` (relative path),
  `Line int`, `Package string`, `Signature string` (functions/methods only).
- `FileNode` (`:34-44`): `Path, Package string`, `Symbols []Symbol`,
  `Imports []string`, `ImportedBy []string` (reverse edges), `Rank float64`,
  `CalledBy int` (count of cross-file calls into this file). **These are the
  exact fields a language-agnostic ranker must populate.**
- `RepoMap` (`:47-51`): `Root string`, `Files map[string]*FileNode` (keyed by
  relative path), `Symbols []Symbol` (all symbols, rank-sorted).

### `Build(root string) (*RepoMap, error)` — `:55-118`

Flow:
1. `:56-59` allocate `RepoMap{Root, Files: map}`.
2. `:62` `analysis, err := goast.AnalyzeDir(root)` — **the only data source.
   Go-specific.**
3. `:67-106` if `analysis != nil`: per `fa` in `analysis.Files`, build a
   `FileNode` from `fa.Path`, `fa.Package`; copy imports from `fa.Imports`
   (`imp.ImportPath`, `:75-77`); copy **exported** symbols only (`:80-83`,
   skips `goast.KindField` at `:85`); map kind via `goastKindToString` (`:90`);
   set `Signature` for func/method (`:95-97`). Store node in `rm.Files[fa.Path]`.
4. `:105` `rm.buildCallGraphEdges(analysis)` — Go-AST call graph.
5. `:109` `rm.buildReverseEdges()` — language-neutral (operates on `node.Imports`).
6. `:112` `rm.rankFiles()` — language-neutral PageRank.
7. `:115` `rm.collectSymbols()` — language-neutral sort.

### Go-specificity locations (exact)

- `repomap.go:62` — `goast.AnalyzeDir(root)` (sole ingest).
- `repomap.go:90,95` — `goastKindToString`, `goast.KindFunction/KindMethod`.
- `repomap.go:120-140` — `goastKindToString(k goast.SymbolKind) string`.
- `repomap.go:143-167` — `buildCallGraphEdges(*goast.Analysis)` consumes
  `analysis.AllSymbols` (with `s.Exported`, `s.Receiver`) and `analysis.AllCalls`
  (`c.Callee`, `c.File`) to increment `node.CalledBy`.
- `repomap.go:171-212` — `parseGoFile` (test-only back-compat; delegates to
  `goast.AnalyzeSource`). Not on the Build path; leave as-is.

### `rankFiles()` — `:364-393` (language-neutral; reuse unchanged)

- `:365-367` init every `node.Rank = 1.0`.
- `:370` **3 iterations** of propagation.
- Per node per iteration (`:372-388`): `rank := 0.15` (damping/base);
  for each `importer` in `node.ImportedBy`, add
  `0.85 * impNode.Rank / outDegree` where `outDegree = len(impNode.Imports)`
  (floored to 1) (`:374-382`); **symbol bonus** `+= len(node.Symbols) * 0.1`
  (`:384`); **call-graph bonus** `+= node.CalledBy * 0.15` (`:386`).
- `:389-391` commit `newRanks`.

This function reads **only** `ImportedBy`, `Imports`, `Symbols`, `CalledBy` —
all fields a non-Go ranker can fill. No changes needed.

### `buildReverseEdges()` — `:340-360` (language-neutral; reuse unchanged)

Builds `pkgFiles map[pkgName][]path` from `node.Package` (`:342-345`); then for
each `node`'s import, takes the **last path segment** as the package name
(`parts[len(parts)-1]`, `:350-351`) and appends the importer to every matching
target file's `ImportedBy` (`:352-357`). **Caveat for non-Go:** this match keys
on `node.Package` equalling the import's last segment. For Python/TS the import
string is a module path, not a Go package; the fallback ranker should set
`FileNode.Package` to a value that lets this matcher connect importers to
importees (see §4).

### `buildCallGraphEdges()` — `:143-167` (Go-AST-only)

Consumes `*goast.Analysis`. For non-Go there is no call graph, so `CalledBy`
stays 0 — acceptable; PageRank still runs on imports + symbol-count bonus.

### `collectSymbols()` — `:395-408` (language-neutral)

Flattens all `node.Symbols`, sorts by owning file's `Rank` desc, then `Line` asc.

### `Render(budget int) string` — `:216-284`

Groups symbols by file, sorts file groups by `rank` desc (`:250-252`), emits
`# Repository Map`, then `## <path>` sections with per-kind formatting
(`:262-278`). `budget <= 0` → 100000. Language-neutral; works on whatever
symbols/ranks exist. Note kind-formatting switch at `:264-277` keys on the
**string** kinds (`func`, `method`, `type`, `interface`, default) — the fallback
must emit those same string kinds.

### `RenderRelevant(relevantFiles []string, budget int) string` — `:287-323`

1. `:289-305` builds `boosted` set: the relevant files, files providing their
   imports (matched via `strings.HasSuffix(imp, n.Package)`, `:296`), and their
   `ImportedBy`.
2. `:308-319` temporarily multiplies `node.Rank *= 3.0` for boosted files, with
   a `defer` restore.
3. `:321-322` re-`collectSymbols()` then `Render(budget)`.

This is the function `workflow.go:2054` calls. It returns `Render()`'s output,
which is empty when `rm.Files` is empty. Language-neutral once `rm.Files` is
populated.

---

## 2. SYMINDEX — the multi-language source to feed in

File: `/home/eric/repos/r1-agent/internal/symindex/index.go` (643 lines).
Doc comment explicitly states: Go via AST, **other languages via regex**
(`:10-12`).

### Public API

- `Build(root string) (*Index, error)` — `:225-277`. Pass 1: `goast.AnalyzeDir`
  → `idx.ingestGoAST` (`:234-237`). Pass 2: `filepath.Walk` (`:240-274`) skips
  dirs starting with `.`, `node_modules`, `vendor`, `__pycache__` (`:246-248`);
  **skips `.go`** (handled by AST, `:254`); for other extensions calls
  `findLang(ext)` (`:258`) and `extractSymbolsRegex` (`:269`). **This is the
  exact multi-language symbol source.**
- `BuildFromFiles(root string, files []string) (*Index, error)` — `:280-328`.
  Same split (Go via AST, rest via regex) for an explicit file list.
- `Symbol` struct — `:43-54`: `Name`, `Kind SymbolKind`, `File`, `Line`,
  `EndLine`, `Parent` (receiver/class), `Signature` (full decl line for regex
  matches, `:607`), `Exported bool`, `TypeName`, `Doc`.
- `SymbolKind` constants — `:29-40`: `function, method, type, interface, class,
  variable, constant, import, package, field`. **Note:** symindex uses
  `KindClass` and `KindType`; repomap's string kinds are `func/method/type/
  interface/var/const`. A mapping is required (see §4 + §recommendations).

### Per-file symbol enumeration (what the ranker uses)

- `idx.Files() []string` — `:475-482`, sorted relative paths.
- `idx.InFile(file string) []Symbol` — `:432-439`, all symbols in one file.
- `idx.AllSymbols() []Symbol` — `:463-467`.
- `idx.ByKind(kind) []Symbol`, `idx.Exported() []Symbol` (`:442-460`).

The cleanest feed: `for _, f := range idx.Files() { syms := idx.InFile(f) }`.
Each `Symbol.File` is already the **relative path** (set from `filepath.Rel`
at `index.go:268`), matching the key convention repomap expects.

### Language ExtractorSpec / regex patterns — `languages []langPattern` `:95-221`

`langPattern{Extensions []string; Patterns []symbolPattern}` (`:83-86`);
`symbolPattern{Kind SymbolKind; Regex *regexp.Regexp; Parent int; Name int}`
(`:88-93`). Coverage relevant to B3:

- `.py` (`:96-104`): func `^def\s+(\w+)\s*\(`; class `^class\s+(\w+)`;
  method `^\s+def\s+(\w+)\s*\(self`; var `^(\w+)\s*=`.
- `.ts/.tsx/.js/.jsx` (`:105-115`): func
  `^(?:export\s+)?(?:async\s+)?function\s+(\w+)`; class
  `^(?:export\s+)?class\s+(\w+)`; interface
  `^(?:export\s+)?interface\s+(\w+)`; type `^(?:export\s+)?type\s+(\w+)\s*=`;
  var `^(?:export\s+)?(?:const|let|var)\s+(\w+)`; method
  `^\s+(?:async\s+)?(\w+)\s*\(`.
- Also `.rs, .java, .kt, .swift, .rb, .php, .cs, .ex, .c/.h, .cpp, .scala`
  (`:116-220`).

`extractSymbolsRegex` (`:577-616`) is line-by-line, first-match-wins per line
(`:611`), sets `Exported` via `isExported` (`:618-631`: `.py` → not
`_`-prefixed; default → true). `findLang(ext)` (`:633-642`) maps extension →
`*langPattern`.

**Key insight:** symindex already produces exported, kind-tagged, line-numbered
symbols per relative path for all target languages. The ranker does **not** need
new symbol extraction — only a kind-string mapping and a feed loop.

---

## 3. IMPORT HEURISTICS for non-Go — ALREADY EXISTS in depgraph

File: `/home/eric/repos/r1-agent/internal/depgraph/graph.go` (377 lines). Package
doc: "Supports Go, Python, TypeScript, and Rust import patterns" (`:10`).

### Available API

- `Build(root string, extensions []string) (*Graph, error)` — `:43-86`. Walks
  tree, `shouldSkip` (vendor/node_modules/.git/target/__pycache__, `:344-352`),
  `matchExt` (empty list = all, `:354-364`), `extractImports(content, ext)`
  (`:72`), stores `Node{Path rel, Imports []string}` and `Edge{From, To}`.
- `Graph{Nodes map[string]*Node, Edges []Edge}` — `:37-40`;
  `Node{Path, Imports []string}` — `:25-28`.
- Helpers: `Dependents(path)` (`:89-98`), `Dependencies(path)` (`:101-110`).

### Existing import regexes (`:250-259`) — **reuse, do not re-derive**

- Python `pyImport = ^(?:from\s+(\S+)\s+)?import\s+(.+)` (`:254`), parsed in
  `extractPyImports` (`:296-315`): group 1 = `from X` module; else split
  group 2 on `,` and take first token of each (`:304-309`).
- TS/JS `tsImport = (?:import|from)\s+['"]([^'"]+)['"]` (`:255`) plus
  `tsRequire = require\s*\(\s*['"]([^'"]+)['"]\s*\)` (`:256`); merged in
  `extractTSImports` (`:317-328`).
- Go block/single (`:251-253`); Rust `use`/`mod` (`:257-258`).
- Dispatch `extractImports` (`:261-274`): `.py`→py, `.ts/.tsx/.js/.jsx`→ts,
  `.rs`→rust, default→nil.

**Conclusion:** the lightweight import extraction the task anticipated needing
**already exists** in depgraph with exactly the patterns required. The spec
should *reuse `depgraph.Build`* rather than add new regexes. The patterns are
catalogued in §Spec-recommendations for self-containment in case the spec author
prefers an inlined helper to avoid the new package dependency.

### chunker (`/home/eric/repos/r1-agent/internal/chunker/chunker.go`)

Multi-language by extension (`DetectLanguage` `:54-66`: go/py/ts-tsx-js-jsx/rs/
java). Provides semantic chunks, not imports — **not needed** for B3, but
confirms the repo's "regex per extension" convention. No import extraction here.

---

## 4. THE GRAPH MERGE — how the non-Go ranker plugs into existing rankFiles

**Goal:** populate `FileNode.{Path, Package, Symbols, Imports, ImportedBy,
CalledBy}` from symindex + depgraph instead of goast, so the **unchanged**
`buildReverseEdges` → `rankFiles` → `collectSymbols` pipeline runs.

### Cleanest seam: a fallback branch inside `Build()`

After the existing goast block (`repomap.go:67-106`), add: **if `rm.Files` is
empty** (i.e., goast found no Go files), run a `buildFromSymindex(root)` path
that fills `rm.Files`, then fall through to the **same**
`buildReverseEdges()` / `rankFiles()` / `collectSymbols()` calls
(`:109/:112/:115`). This requires **zero** signature changes to `Build`,
`Render`, `RenderRelevant`, or any caller (§5).

Recommended decision predicate: `len(rm.Files) == 0` *after* the goast loop.
This is more robust than language-detecting up front because `goast.AnalyzeDir`
already returns empty `Files` for non-Go repos, and it also covers mixed repos
that happen to have zero parseable Go (degenerate). For mixed Go+Python repos
where Go files exist, the Go path wins (acceptable for v1; B3 scope is the
"empty for non-Go" defect).

### Population recipe (`buildFromSymindex`)

```
idx, _ := symindex.Build(root)            // multi-lang symbols
dg, _  := depgraph.Build(root, nil)       // multi-lang imports (nil = all exts)

for _, f := range idx.Files() {           // f is relative path
    node := rm.Files[f]
    if node == nil {
        node = &FileNode{Path: f, Package: pkgKey(f)}
        rm.Files[f] = node
    }
    for _, s := range idx.InFile(f) {
        if !s.Exported || s.Kind == symindex.KindField { continue }
        node.Symbols = append(node.Symbols, Symbol{
            Name: s.Name, Kind: symKindToString(s.Kind),
            File: f, Line: s.Line, Package: node.Package,
            Signature: maybeSig(s),       // only func/method
        })
    }
}
for path, n := range dg.Nodes {           // attach imports
    if node, ok := rm.Files[path]; ok { node.Imports = n.Imports }
}
```

- `node.CalledBy` stays 0 (no cross-file call graph for regex langs) — fine.
- `buildReverseEdges` (`:340`) then links importers→importees and `rankFiles`
  (`:364`) ranks via imports + symbol-count bonus (`+= len(Symbols)*0.1`,
  `:384`). Even with zero import edges, the symbol-count bonus yields a
  non-uniform, meaningful ranking (more-symbol files rank higher).

### Two mapping shims the spec must add

1. **`symKindToString(symindex.SymbolKind) string`** — bridge symindex kinds to
   repomap's string kinds expected by `Render` (`:264-277`):
   `KindFunction→"func"`, `KindMethod→"method"`, `KindType→"type"`,
   `KindClass→"type"` (repomap has no "class" string; "type" renders cleanly),
   `KindInterface→"interface"`, `KindVariable→"var"`, `KindConstant→"const"`,
   else `"var"`. (Mirror of existing `goastKindToString` at `repomap.go:120`.)
2. **`pkgKey(relPath) string` for `FileNode.Package`** — `buildReverseEdges`
   (`:350-351`) matches an import's last path segment against `node.Package`, and
   `RenderRelevant` (`:296`) matches `strings.HasSuffix(imp, n.Package)`. For
   non-Go, set `Package` to the file's directory base (e.g.
   `filepath.Base(filepath.Dir(rel))`) or the filename stem. This gives
   `buildReverseEdges` a non-empty key so intra-directory imports can connect.
   Exact heuristic is a spec decision; the ranking still works (via symbol
   bonus) even if package-matching yields no edges, so this is best-effort.

### Functions to refactor (exact)

- `repomap.go:Build` (`:55-118`): add the `len(rm.Files)==0` fallback branch
  between `:106` and `:109`.
- **New** `buildFromSymindex(root string)` method on `*RepoMap` in `repomap.go`.
- **New** `symKindToString` helper in `repomap.go`.
- New imports in `repomap.go`: `github.com/RelayOne/r1/internal/symindex`,
  `github.com/RelayOne/r1/internal/depgraph`. (Verify no import cycle: symindex
  imports only `goast`; depgraph imports only `logging`; neither imports
  repomap — **no cycle**.)

No change to `rankFiles`, `buildReverseEdges`, `collectSymbols`, `Render`,
`RenderRelevant`.

---

## 5. WIRING / INJECTION — no API change required

### Primary injection (the one that matters for B3)

`/home/eric/repos/r1-agent/internal/workflow/workflow.go:2049-2061`:
```
if e.RepoMap != nil {
    budget := e.RepoMapBudget; if budget <= 0 { budget = 2000 }
    mapContent := e.RepoMap.RenderRelevant(e.AllowedFiles, budget)
    if mapContent != "" { contextItems = append(..., ctxpack.Item{ID:"repomap", ...}) }
}
```
`e.RepoMap *repomap.RepoMap` and `e.RepoMapBudget int` declared at
`workflow.go:138-139`. The fallback makes `RenderRelevant` non-empty for non-Go
repos, so the `mapContent != ""` guard (`:2055`) passes and the item is packed
into `ctxpack.Pack` (`:2062`) and reaches the LLM. **No change to workflow.go.**

### All `repomap.Build` call sites (`cmd/r1/main.go`) — none need changes

- `:355` `repoMap, repoMapErr := repomap.Build(absRepo)` → assigned to executor
  `RepoMap` at `:438`.
- `:1075` `runRepoMap, _ := repomap.Build(absRepo)` → `RepoMap:` at `:1122`.
- `:1382` `tuiRepoMap, _ := repomap.Build(absRepo)` → `:1388`.
- `:2753-2756` `repomap.Build(absRepo)` → `sowRepoMap` → `:3085` `RepoMap:`,
  `:3086` `RepoMapBudget`.
- Executor opts plumb through `cmd/r1/main.go:6546` (`RepoMap *repomap.RepoMap`)
  and `:6593` (`cfg.RepoMap = opts.RepoMap`).

All call `Build(root)` and consume `*RepoMap`; the fallback is internal to
`Build`, so every site benefits with **zero** call-site edits.

---

## 6. LANGUAGE DETECTION — reuse skillselect (optional, not required)

File: `/home/eric/repos/r1-agent/internal/skillselect/detect.go`.

- `DetectStack(repoRoot string) (*StackInfo, error)` — `:75-89`. Never returns
  nil StackInfo.
- `StackInfo.Languages []string` (`:22-28`), populated in `layer1` (`:92-145`):
  `go.mod`→go (`:94`), `package.json`→javascript + `tsconfig.json`→typescript
  (`:97-102`), `Cargo.toml`→rust (`:103`), `requirements.txt`/`pyproject.toml`/
  `setup.py`→python (`:106`), `pom.xml`/`build.gradle*`→java (`:109`).
- `StackInfo.HasLanguage(lang string) bool` — `:31-33`.

**Recommendation:** the `len(rm.Files)==0` predicate (§4) is simpler and more
direct than language detection — it triggers exactly when goast yielded nothing,
regardless of how the language is declared, and needs no extra package import.
`skillselect.DetectStack` is available as a fallback if the spec author prefers
an explicit "is this a Go repo?" check, but it adds a dependency without
improving correctness. Document both; default to the `len==0` seam.

---

## 7. TEST PATTERN

### Existing fixtures to mirror

- `repomap_test.go` (`/home/eric/repos/r1-agent/internal/repomap/repomap_test.go`):
  `setupTestRepo` (`:10-72`) builds a `t.TempDir()` Go project with `cmd/main.go`
  + `pkg/util/util.go`; `TestBuild` (`:74-101`) asserts `len(rm.Files)` and
  presence of named symbols; `TestRenderRelevant` (`:131-140`) calls
  `rm.RenderRelevant([]string{"pkg/util/util.go"}, 0)` and asserts a symbol
  appears.
- `symindex/index_test.go`: `TestPythonSymbols` (`:225-251`) writes `app.py` to
  `t.TempDir()` and asserts `idx.ByKind(KindClass)` non-empty;
  `TestTypeScriptSymbols` (`:253-281`) writes `app.ts` and asserts
  `idx.ByKind(KindInterface)`. **Copy these fixtures verbatim** for the new
  repomap multilang test.

### New test to add (in `repomap_test.go`)

`setupPythonRepo(t)`: `t.TempDir()` with e.g. `app/main.py` (`import app.util`,
a `class App` + `def run(self)`) and `app/util.py` (`def helper()`, several
`def`s so its symbol-count bonus ranks it). Assertions:
1. `rm, _ := Build(dir)` → `len(rm.Files) > 0` (proves fallback fired).
2. `rm.Render(0)` contains the header **and** a Python symbol name (`App`,
   `helper`).
3. `out := rm.RenderRelevant([]string{"app/main.py"}, 0)` is non-empty and
   contains the expected top file's symbols — the exact behavior `workflow.go`
   relies on.
4. (Edge) `len(out) > len("# Repository Map\n\n")` — guards the empty-header
   regression that B3 is fixing.

A TS variant (`.ts`/`.tsx` fixture) is cheap to add and exercises the second
regex language. Use `t.TempDir()`, `os.WriteFile(..., 0o600)`, and
`filepath.Join` exactly as the existing tests do (no external fixtures).

---

## Spec recommendations (concrete checklist items)

1. **[repomap.go] Add fallback branch in `Build()`** between `:106` and `:109`:
   `if len(rm.Files) == 0 { rm.buildFromSymindex(root) }`. Do not alter the
   goast block or the trailing `buildReverseEdges`/`rankFiles`/`collectSymbols`
   calls.
2. **[repomap.go] Implement `(*RepoMap).buildFromSymindex(root string)`** per
   §4 recipe: feed `symindex.Build(root)` symbols (skip non-exported and
   `KindField`) into `FileNode.Symbols`, and `depgraph.Build(root, nil)` imports
   into `FileNode.Imports`. Set `FileNode.Package` via a dir-base heuristic.
3. **[repomap.go] Add `symKindToString(symindex.SymbolKind) string`** mapping:
   `function→func, method→method, type→type, class→type, interface→interface,
   variable→var, constant→const, default→var`. Set `Signature` only for
   func/method.
4. **[repomap.go] Add imports** `internal/symindex` and `internal/depgraph`
   (verified acyclic). Keep `goast` import.
5. **[no change] Confirm `rankFiles`, `buildReverseEdges`, `collectSymbols`,
   `Render`, `RenderRelevant` are untouched** — they are already
   language-neutral.
6. **[no change] Confirm all `repomap.Build` callers** (`cmd/r1/main.go:355,
   1075,1382,2753`) and the injection site (`workflow.go:2054`) need no edits;
   the fix is internal to `Build`.
7. **[repomap_test.go] Add `TestBuildPythonRepo` + `TestRenderRelevantPython`**
   (and optionally TS), modeled on `setupTestRepo`/`TestRenderRelevant` and
   `symindex` `TestPythonSymbols`/`TestTypeScriptSymbols`. Assert `len(rm.Files)
   > 0`, non-empty `RenderRelevant`, and expected top-file symbol present.
8. **Verification gate:** `go build ./... && go test ./internal/repomap/... &&
   go vet ./...` (per repo CLAUDE.md CI gate).

### Exact regex import patterns per language (from depgraph; for self-containment)

If the spec inlines extraction instead of importing depgraph, use these
(verbatim from `depgraph/graph.go:250-259`):

- **Python:** `^(?:from\s+(\S+)\s+)?import\s+(.+)` — group 1 = `from X` module;
  else split group 2 on `,`, take first whitespace-token of each part
  (handles `import a, b as c`). (`graph.go:254`, `extractPyImports:296-315`.)
- **TS/JS (`.ts/.tsx/.js/.jsx`):**
  `(?:import|from)\s+['"]([^'"]+)['"]` (ES imports) **and**
  `require\s*\(\s*['"]([^'"]+)['"]\s*\)` (CommonJS). (`graph.go:255-256`,
  `extractTSImports:317-328`.)
- **Go (existing, for reference):** single `import\s+"([^"]+)"`; block
  `import\s*\(\s*((?:[^)]+))\)` then per-line `"([^"]+)"`. (`graph.go:251-253`.)
- **Rust:** `^use\s+(\w+(?:::\w+)*)` and `^mod\s+(\w+)\s*;`. (`graph.go:257-258`.)

**Preferred approach:** call `depgraph.Build(root, nil)` and read `Node.Imports`
— it already applies these patterns plus `vendor`/`node_modules`/`__pycache__`
skipping (`shouldSkip:344-352`) and dedup (`dedup:366-376`). Inlining is only
needed if the spec author wants to avoid the new package dependency.
