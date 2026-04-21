<!-- STATUS: ready -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-3 (Executor interface, VerifyFunc, router) -->
<!-- BUILD_ORDER: 4 -->

# Browser + Research Executors — Implementation Spec

## Overview

Adds two capabilities to Stoke: (1) a headless-browser tool package `internal/browser/` powered by `github.com/go-rod/rod`, exposed as worker agentloop tools and as a Go client for research/deploy verification; and (2) `internal/executor/research.go`, a multi-agent research executor following Anthropic's orchestrator-worker pattern (Opus 4.7 Lead + Sonnet 4.5 subagents + CitationAgent), with a 4-stage claim-verification pipeline and per-claim `AcceptanceCriterion` output that plugs directly into the spec-3 `Executor` interface and the spec-1 descent engine (T4 repair / T5 env-fix / T8 soft-pass).

## Stack & Versions

- Go 1.22+ (existing module)
- `github.com/go-rod/rod` — main-branch SHA (pinned). Last tagged release v0.116.2 (Jul 2024) predates 2026 commits; pin to a specific 2026 SHA with a comment in `go.mod`. Example: `github.com/go-rod/rod v0.116.2-0.20260401xxxxxx-abcdef123456`.
- `github.com/go-rod/rod/lib/launcher` — Chromium auto-downloader; used only when `STOKE_BROWSER_AUTO_DOWNLOAD=1`.
- Provider routing via existing `provider.Provider`: `claude-opus-4-7` (Lead), `claude-sonnet-4-5` (Subagent + Judge).
- SQLite (existing `internal/eventlog/`, `internal/session/`) for verification-cache + ledger nodes.
- Existing `internal/vecindex/` for top-k passage retrieval. Existing `internal/research/store.go` for FTS5 persistence (no executor yet — this spec adds it).
- Existing `internal/agentloop/` — browser tools registered via its tool registry.

## Existing Patterns to Follow

- Executor interface: `spec-3` (defines `Executor`, `AcceptanceCriterion.VerifyFunc`, router). This spec implements against it.
- Descent integration: `/home/eric/repos/stoke/internal/plan/verification_descent.go` (T0..T8 ladder). Research executor supplies `BuildRepairFunc`, `BuildEnvFixFunc`, `BuildCriteria` exactly like `CodeExecutor` (spec-3 D12).
- Agentloop tools: `/home/eric/repos/stoke/internal/agentloop/loop.go` + tool registry. New browser tools register here.
- Research storage: `/home/eric/repos/stoke/internal/research/store.go` (FTS5). Research executor writes facts here via existing API.
- Event emission: `/home/eric/repos/stoke/internal/streamjson/emitter.go` (C1). New subtypes go under `_stoke.dev/*`.
- Event log: spec-3's `internal/eventlog/` (C2) — all `research.*` and `browser.*` events persist here.
- Ledger: `/home/eric/repos/stoke/ledger/nodes/` — four new node types added here.
- Cost: `internal/costtrack/` — each subagent + Lead + Judge call updates the tracker; `OverBudget()` is a hard stop.

## Library Preferences

- Headless browser: **go-rod** (D15, RT-01). Reject chromedp/Playwright/Puppeteer — go-rod is the only pool-native pure-Go option that preserves Stoke's single-binary distribution.
- HTTP for non-browser fetches: existing `internal/apiclient/` (SSE/JSON) — do not add `net/http` directly.
- JSON judge output: existing `internal/jsonutil/` (tolerant parser) + `internal/schemaval/` (strict validation).
- Vector search: existing `internal/vecindex/`. No new embedding library.
- Claim extraction / synthesis: existing `provider.Provider` — no new LLM SDK.

---

## Part 1 — `internal/browser/` (headless browser tool)

### 1.1 Package layout

```
internal/browser/
  browser.go          # Browser interface + rodDriver impl
  pool.go             # Pool interface + rodPool impl wrapping rod.NewBrowserPool
  tools.go            # agentloop tool definitions + auth predicates
  console.go          # per-page console ring buffer
  extract.go          # readable-text extraction (body + article/main preference)
  install.go          # Chromium presence check + STOKE_BROWSER_AUTO_DOWNLOAD gate
  browser_test.go     # mock-driver tests; no Chromium required in CI
  integration_test.go # build tag `browser`; CI-gated on opt-in
```

### 1.2 `Browser` interface (copy of RT-01)

```go
package browser

import (
    "context"
    "time"
)

type Browser interface {
    Navigate(ctx context.Context, url string) error
    Screenshot(ctx context.Context, path string) error    // full-page PNG
    ExtractText(ctx context.Context) (string, error)      // readable body text
    ConsoleErrors(ctx context.Context) ([]string, error)  // drains since Navigate
    WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error
    Close() error
}

type Pool interface {
    Acquire(ctx context.Context) (Browser, error)
    Release(b Browser)
    Close() error
}

func NewPool(size int) (Pool, error) // wraps rod.NewBrowserPool(size)
```

Implementation notes:
- One `*rod.Browser` per pool slot; `Acquire` checks out a browser and allocates a fresh `*rod.Page` per call. On `Release`, the page is closed; the browser is returned to the pool.
- Console capture: immediately after `Navigate`, register `page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) {...})` that appends to a per-page ring buffer (capacity 500). `ConsoleErrors` drains and returns only entries with type `error` or exception frames.
- `ExtractText`: prefer `page.MustElement("article, main").MustText()`; fall back to `page.MustElement("body").MustText()`; if both fail, return rendered innerText of `html`. Trim to 200 KB and log a truncation warning.
- `WaitForSelector`: pass-through to `page.Timeout(timeout).MustElement(selector)`; convert rod panics to errors.
- `Close()`: close the current page, return browser to pool. Pool `Close()` shuts down all browsers and removes the Chromium user-data-dir.

### 1.3 Chromium acquisition

go-rod auto-downloads Chromium on first run to `$HOME/.cache/rod/browser`. To preserve airgapped/deterministic builds:

1. `install.go` checks `STOKE_BROWSER_AUTO_DOWNLOAD` (default `0`). When `0`, require either (a) `CHROMIUM_BIN` env var pointing to an existing binary, or (b) a cached download at the default rod path. Error message explicitly lists the three remediation paths.
2. **Offline workflow — operator path:** `rod-manager install` (shipped as a side binary under `cmd/rod-manager/` is out of scope; document the upstream `go run github.com/go-rod/rod/lib/launcher/rod-manager install` invocation instead).
3. **Docker image sketch** (document in spec, do not add a Dockerfile this round):
   ```dockerfile
   FROM golang:1.22 AS build
   WORKDIR /src
   COPY . .
   RUN go build -o /stoke ./cmd/stoke

   FROM chromedp/headless-shell:latest
   COPY --from=build /stoke /usr/local/bin/stoke
   ENV CHROMIUM_BIN=/headless-shell/headless-shell
   ENV STOKE_BROWSER_AUTO_DOWNLOAD=0
   ENTRYPOINT ["/usr/local/bin/stoke"]
   ```

### 1.4 Worker tools (agentloop registration)

Four tools registered with the agentloop tool registry, gated by trust level (`harness/tools` auth predicate):

| Tool name | Input schema (JSON Schema) | Trust gate |
|---|---|---|
| `tools/browser_navigate` | `{"url": string (https://*)}` | TrustDev+ OR url matches `STOKE_BROWSER_ALLOWLIST` |
| `tools/browser_screenshot` | `{"path": string (relative, ends .png)}` | same session that called navigate |
| `tools/browser_extract_text` | `{}` | same session that called navigate |
| `tools/browser_console_errors` | `{}` | same session that called navigate |

**Auth predicate** (`tools.go`):
```go
func authBrowserNavigate(sess Session, input map[string]any) error {
    u := input["url"].(string)
    if sess.TrustLevel >= TrustDev { return nil }
    for _, re := range sess.BrowserAllowlist {
        if re.MatchString(u) { return nil }
    }
    return errors.New("browser_navigate: untrusted URL for worker trust level")
}
```

Allowlist comes from `config.BrowserAllowlist []string` (regex) merged with `STOKE_BROWSER_ALLOWLIST` (comma-separated). Research executor sets this dynamically per run (only URLs from `sources.jsonl` are allowed during verification).

**Tool schemas** are published to the worker model in the `tools` array alongside existing tools; responses are plain JSON (`{"ok": true}` or `{"ok":false,"error":"..."}`) or structured payloads (`{"text": "...", "truncated": false}`).

### 1.5 Tests (no Chromium in CI)

- `browser_test.go`: unit tests against a `mockDriver` implementing `Browser`. Covers `Pool` acquire/release/close, console buffer wraparound, allowlist auth predicate, text-extraction fallback order.
- `integration_test.go`: build tag `//go:build browser` — runs against real rod + local fileserver on :0. Excluded from `go test ./...` default path. CI matrix can opt in via `-tags browser`.
- `TestNavigateScreenshot` specifically: starts a `net/http/httptest.Server` with a 2-section HTML page, navigates, screenshots to a temp file, asserts PNG magic bytes + >1 KB size, extracts text, asserts expected substring. Build-tag-gated.

---

## Part 2 — `internal/executor/research.go` (Research executor)

### 2.1 Orchestrator-worker architecture (RT-07 §1)

```go
package executor

import (
    "context"
    "github.com/stoke/stoke/internal/browser"
    "github.com/stoke/stoke/internal/provider"
    "github.com/stoke/stoke/internal/research"
    "github.com/stoke/stoke/ledger"
)

type ResearchExecutor struct {
    LeadProvider   provider.Provider // claude-opus-4-7
    SubProvider    provider.Provider // claude-sonnet-4-5
    JudgeProvider  provider.Provider // claude-sonnet-4-5 (or claude-haiku-4-5 at scale)
    Browser        browser.Pool
    Search         SearchClient     // Brave primary, Tavily/Bing fallback (2.5)
    Store          research.Store   // FTS5 persistence of accepted facts
    Ledger         ledger.Writer
    RunRoot        string           // ".stoke/research/<run-id>"
    MaxParallel    int              // default 5 (D17)
    Clock          func() time.Time
}
```

**Roles:**
- **Lead (Opus 4.7)** receives the query, writes `plan.md` (decomposition), spawns N subagents, then synthesizes.
- **Subagents (Sonnet 4.5)** each own a single objective + its own tool budget; they write `findings.md` and `sources.jsonl`, returning only a reference path to the Lead.
- **CitationAgent (Sonnet 4.5)** ingests `synthesis.md` + subagent sources, attaches `[^N]` citation spans, emits `claims.jsonl`.
- **Verifier (Sonnet 4.5)** runs the 4-stage pipeline per claim, emits `verifications.jsonl`.

### 2.2 Effort scaling (RT-07 §1.3 + D17)

`--effort` flag on `stoke research`:

| Flag value | Effort enum | Subagents | Tool calls / subagent | Lead wall clock (max) |
|---|---|---|---|---|
| `minimal` | `EffortFact` | 1 | 3-10 | 2 min |
| `standard` | `EffortCompare` | 2-4 | 10-15 | 10 min |
| `thorough` | `EffortDeep` | up to `MaxParallel` (default 5, Anthropic recommends 10+ but bounded by search-API quotas) | 15+ | 30 min |
| `critical` | `EffortDeep` + cross-model judge | same as thorough | 15+ | 60 min |

`critical` additionally routes the Judge through a second model (e.g., Opus 4.7 or Codex) for cross-model verification; otherwise identical to `thorough`.

**Enforcement:** the Lead's system prompt contains the table verbatim and a hard instruction: *"Overinvestment is the dominant failure mode. Use the smallest tier consistent with the query. Do not escalate without explicit user flag."* Additionally, `Execute` ceilings `subagentCount <= max(table_max, MaxParallel)` deterministically.

### 2.3 Filesystem-as-communication layout (RT-07 §1.5, §4)

```
.stoke/research/<run-id>/
  plan.md                 # Lead's decomposition (also mirrored as ResearchPlan ledger node)
  memory.json             # Lead's persistent scratchpad (survives truncation/resume)
  subagent-1/
    objective.md          # exactly the 4-field prompt from 2.4
    findings.md           # subagent writeup (returned to Lead by reference)
    sources.jsonl         # {url, fetched_at, excerpt, content_hash} per source
    transcript.jsonl      # tool-call log (replay + cost accounting)
  subagent-2/ ...
  synthesis.md            # Lead's final report with inline [^N] citation markers
  claims.jsonl            # {id, text, source_urls[], group_id} — atomic claims
  verifications.jsonl     # {claim_id, label, confidence, passage, judge_model}
  cost.json               # tokens + USD per stage (lead / each subagent / citation / verify)
  report.json             # final Deliverable envelope
```

Example `subagent-1/objective.md`:
```markdown
# Objective
Find the three most widely adopted open-source embedding models in 2026
and their licenses, parameter counts, and primary training corpora.

# Output format
One row per model, as a markdown table with columns:
| Model | License | Params | Training corpus | Primary source URL |
Plus a one-paragraph summary with [^N] citations.

# Tool guidance
Use web_search first; prefer arxiv.org, huggingface.co, and model cards.
Do not consult Reddit, blog aggregators, or SEO spam sites.

# Task boundaries
Do NOT investigate closed-source models (OpenAI, Anthropic, Cohere).
Do NOT benchmark quality — only catalog the three models.
```

### 2.4 Subagent instruction template (RT-07 §1.4)

Every subagent spawn carries exactly 4 sections rendered by `renderSubagentPrompt(objective SubObjective) string`:

```
{ .Objective }        — single concrete research question
{ .OutputFormat }     — e.g., "bulleted findings with source URLs and 1-2 sentence quoted excerpts"
{ .ToolGuidance }     — which tools + preferred source types
{ .TaskBoundaries }   — explicit negative scope ("do NOT do X")
```

`SubObjective` is a Go struct written by the Lead. It is also persisted to the ledger as a `SubagentRun` node for replay.

### 2.5 `Executor` interface implementation (spec-3)

```go
func (e *ResearchExecutor) Execute(ctx context.Context, plan TaskPlan, effort Effort) (Deliverable, error)
func (e *ResearchExecutor) BuildCriteria(task Task, d Deliverable) []AcceptanceCriterion
func (e *ResearchExecutor) BuildRepairFunc(plan TaskPlan) RepairFunc
func (e *ResearchExecutor) BuildEnvFixFunc() EnvFixFunc
```

**Execute flow:**
1. Create `RunRoot`; write initial `memory.json`.
2. Lead call (Opus 4.7) — "decompose the query into N subagent objectives"; parse JSON response; persist `plan.md`; emit `research.plan.ready` event (eventlog + streamjson).
3. Spawn subagents via `errgroup.WithContext(ctx)` with `errgroup.SetLimit(MaxParallel)`. Each runs its own agentloop with the subagent tool set (web_search, browser_extract_text, url_fetch).
4. Collect `findings.md` references; Lead synthesizes (second Opus call) → `synthesis.md`.
5. CitationAgent pass (Sonnet 4.5) → `claims.jsonl`.
6. Verifier pass — 4-stage pipeline per claim (§2.6) → `verifications.jsonl`.
7. Write `report.json` (final `Deliverable`): path to synthesis, claim count, verified fraction, cost.
8. Emit `research.completed` event with rollup.

**BuildCriteria output (RT-07 §5.1):**

Hard criteria (block T8 soft-pass):
```go
{ID:"report.exists",     VerifyFunc: fileExistsAndNonEmpty(RunRoot+"/synthesis.md")}
{ID:"claims.count",       VerifyFunc: jsonlCountAtLeast(RunRoot+"/claims.jsonl", 1)}
{ID:"claims.sourced",     VerifyFunc: everyClaimHasSource(RunRoot+"/claims.jsonl")}
{ID:"claims.no_contradiction", VerifyFunc: noClaimWithLabel(RunRoot+"/verifications.jsonl", "contradicted")}
```

Per-claim criteria (soft-passable):
```go
for claim in claims:
  {ID: "claim."+claim.ID+".verified",
   VerifyFunc: claimVerified(claim.ID, minConfidence=0.7),
   GroupID: claim.GroupID}
```

Coverage criterion (soft-passable):
```go
{ID:"coverage",
 VerifyFunc: verifiedFractionAtLeast(0.9),  // soft-passes at 0.8 if unverifiable claims are annotated [unverified: reason] in synthesis.md
 SoftPassThreshold: 0.8}
```

Verdict rollup per group: `min(child confidences)`. UI collapses under `GroupID`.

**BuildRepairFunc** (T4 in descent):
```go
func (e *ResearchExecutor) BuildRepairFunc(plan TaskPlan) RepairFunc {
    return func(ctx context.Context, directive RepairDirective) error {
        // directive.ACID == "claim.<id>.verified"
        claim := loadClaim(e.RunRoot, directive.ACID)
        obj := SubObjective{
            Objective:     "Re-verify claim: " + claim.Text,
            OutputFormat:  "JSON verdict {supported|contradicted|unrelated, confidence, quote}",
            ToolGuidance:  "Use browser_extract_text on each source URL; use web_search only if source URLs are unreachable.",
            TaskBoundaries: "Do NOT broaden the investigation beyond this single claim.",
        }
        // One targeted subagent, 10-15 tool calls (EffortCompare slice).
        return e.runSingleSubagent(ctx, obj, 15)
    }
}
```
Writes a new `verifications.jsonl` row; never mutates existing rows (append-only; matches ledger semantics).

**BuildEnvFixFunc** (T5 in descent):
```go
func (e *ResearchExecutor) BuildEnvFixFunc() EnvFixFunc {
    return func(ctx context.Context, cause FailureCause, stderr string) bool {
        switch cause {
        case SearchRateLimit:   // HTTP 429
            return e.Search.RotateProvider() // Brave -> Tavily -> Bing
        case FetchBlocked:      // HTTP 403 / robots-blocked
            return e.Search.EnableWayback()  // switch to Internet Archive snapshot
        case NetworkDNS:
            time.Sleep(backoffJitter(stderr))
            return true
        default:
            return false
        }
    }
}
```
If all providers exhausted → mark source `unreachable` (NOT `unverified`) in `verifications.jsonl`; the per-claim AC fails at T4 rather than silently passing.

### 2.6 4-stage claim verification pipeline (RT-07 §3)

Implemented in `internal/executor/research_verify.go`:

```go
func (e *ResearchExecutor) verifyClaim(ctx context.Context, c Claim) (Verdict, error) {
    // Stage 1: Retrieve cited page(s) via browser.
    pages := make([]PageContent, 0, len(c.SourceURLs))
    for _, u := range c.SourceURLs {
        if v := e.cacheLookup(c.Hash, hashURL(u)); v != nil { return *v, nil }
        b, err := e.Browser.Acquire(ctx); if err != nil { return Verdict{Label:"unreachable"}, nil }
        defer e.Browser.Release(b)
        if err := b.Navigate(ctx, u); err != nil { pages = append(pages, PageContent{URL:u, Err:err}); continue }
        txt, _ := b.ExtractText(ctx)
        pages = append(pages, PageContent{URL:u, Text:txt, Hash:hashStr(txt)})
    }

    // Stage 2: Chunk + embed; top-3 passage retrieval against claim.
    idx := vecindex.BuildFromPages(pages, chunkSize=1000)
    top3 := idx.TopK(c.Text, 3)

    // Stage 3: Judge (Sonnet 4.5) — JSON verdict.
    resp, err := e.JudgeProvider.Complete(ctx, provider.Request{
        System: judgePrompt,
        User:   fmt.Sprintf("CLAIM: %s\n\nPASSAGES:\n%s\n\nReply as JSON.", c.Text, renderPassages(top3)),
        MaxTokens: 400,
        ResponseFormat: provider.JSON,
    })
    var v Verdict
    if err := schemaval.UnmarshalVerdict(resp.Text, &v); err != nil { return Verdict{Label:"unreachable"}, nil }

    // Stage 4: Cross-reference cache (append-only) — ledger node + sqlite cache row.
    e.cacheWrite(c.Hash, pages[0].Hash, v)
    e.Ledger.Write(ledger.VerificationNode{ClaimID: c.ID, Verdict: v, JudgeModel: e.JudgeProvider.Name()})
    return v, nil
}
```

Judge prompt skeleton (deterministic, `temperature=0`):
```
You are a citation verifier. For the CLAIM and PASSAGES below, answer whether
any single passage directly supports the claim.

Respond as JSON only:
{"label": "supported"|"contradicted"|"unrelated"|"unreachable",
 "confidence": 0.0-1.0,
 "quote": "verbatim span <= 400 chars that drove the verdict, empty if label != supported"}
```

Cache table (sqlite, co-located with eventlog DB):
```sql
CREATE TABLE IF NOT EXISTS verification_cache(
  claim_hash TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  verdict_json TEXT NOT NULL,
  ts INTEGER NOT NULL,
  PRIMARY KEY(claim_hash, source_hash)
);
```
T4 repair hits this cache first; only re-verifies if `claim.Hash` or `source.Hash` changed.

### 2.7 Descent tier mapping (RT-07 §6)

| Tier | Research-specific behavior |
|---|---|
| T0-T3 | N/A — research produces markdown, not compiled code. Trivially pass. |
| T4 Repair | `BuildRepairFunc` re-spawns one subagent for the failing claim. |
| T5 EnvFix | `BuildEnvFixFunc` rotates Brave→Tavily→Bing or Wayback on 403/429/DNS. |
| T6-T7 | Fetch-cache (keyed by `content_hash`) prevents refetching identical URLs. T7 refactor is a no-op for research. |
| T8 Soft-pass | Granted when `verified_fraction >= 0.8` AND every unverified claim carries `[unverified: <reason>]` in `synthesis.md` AND no claim is `contradicted`. Writes a `SoftPassGrant` ledger node citing `verifications.jsonl`. |

### 2.8 Ledger node types (add to `ledger/nodes/`)

```go
type ResearchPlan struct { ID string; PlanHash string; SubagentCount int; Effort string; Query string; StartedAt time.Time }
type SubagentRun    struct { ID string; ParentPlanID string; ObjectiveHash string; FindingsHash string; ToolCalls int; TokensIn int; TokensOut int; CostUSD float64 }
type ClaimNode      struct { ID string; Text string; SourceURLs []string; GroupID string; PlanID string }
type VerificationNode struct { ID string; ClaimID string; Label string; Confidence float64; PassageHash string; JudgeModel string; SourceHash string }
```

All four satisfy the existing `NodeTyper` interface. Emitted via `ledger.Writer` at the corresponding stage.

### 2.9 Cost estimates (RT-07 §1.7: multi-agent ≈ 15x chat tokens)

Budget envelope before kickoff; checked against `costtrack.OverBudget()` per stage.

| Effort | Lead tokens | Subagent tokens (N × per) | Judge tokens | Est. $/task (2026 pricing) |
|---|---|---|---|---|
| `minimal` | ~3k in / 2k out | 1 × (10k in / 3k out) | ~2k in / 500 out | ~$0.08 |
| `standard` | ~8k in / 4k out | 3 × (20k in / 5k out) | ~5k in / 1k out | ~$0.45 |
| `thorough` | ~15k in / 8k out | 5 × (40k in / 10k out) | ~15k in / 3k out | ~$1.80 |
| `critical` | same as thorough | same | 2× judge (cross-model) | ~$2.60 |

`cost.json` records actuals per stage. Hard stop: `costtrack.OverBudget()` returns true → Execute returns `ErrBudgetExhausted` (exit code 2 at CLI per spec-2 D11).

---

## Part 3 — CLI (`cmd/stoke/research_cmd.go`)

New cobra subcommand:

```
stoke research [flags] "query text"
  --effort {minimal|standard|thorough|critical}   (default: standard)
  --deep                                          (alias for --effort thorough)
  --verify <path.md>                              (verify an existing markdown report rather than run fresh research)
  --run-id <id>                                   (resume an existing run by ID; defaults to new ULID)
  --output {text|markdown|json|stream-json}       (default: markdown)
  --budget-usd <float>                            (hard stop; 0 = use config default)
```

Semantics:
- `stoke research "query"` — one-shot; writes to `.stoke/research/<ulid>/` and prints synthesis.md + verification rollup to stdout. Exit 0 on all-pass or soft-pass; exit 1 on hard-fail.
- `stoke research --verify path.md` — skips Lead+subagent stages; extracts claims from the given markdown, runs the 4-stage verifier, and writes only `claims.jsonl` + `verifications.jsonl`. Useful as a post-hoc audit.
- `stoke research --deep "query" --effort thorough` — `--deep` is sugar for `--effort thorough` (both accepted; `--effort` wins if both set).
- Output `--output stream-json` emits agentloop-style NDJSON events (research.plan.ready, research.subagent.started, research.subagent.completed, research.claim.verified, result). Subtypes live under `_stoke.dev/research/*` per C1.

Router integration (spec-3): `router.Classify("research X")` returns `TaskResearch` and dispatches to `ResearchExecutor.Execute`. The CLI entrypoint bypasses the router by construction (explicit subcommand).

---

## Part 4 — Integration points

- **Browser consumers:** Research executor (claim verification, this spec), spec-6 Deploy (URL smoke + console check), spec-3 Code executor (optional UI smoketest after task completion).
- **Research consumer:** spec-3 router (`"research X"` → TaskResearch), operator CLI (direct invocation), spec-5 delegation (a hireable agent may emit a research subtask).
- **Eventlog:** `research.plan.ready`, `research.subagent.started`, `research.subagent.completed`, `research.synthesis.ready`, `research.claim.verified`, `research.completed`, `research.budget_exhausted`. All persisted via spec-3 `internal/eventlog/`.
- **Descent:** Research executor passes `BuildCriteria`, `BuildRepairFunc`, `BuildEnvFixFunc` into the descent `DescentConfig` exactly like `CodeExecutor`. `IntentCheckFunc` for research is "reviewer stance reads synthesis.md and confirms it answers the query"; default-true if no reviewer is present.

---

## Data Models

### `Claim`
| Field | Type | Constraints |
|---|---|---|
| ID | string | ULID |
| Text | string | non-empty, ≤1000 chars |
| SourceURLs | []string | ≥1 entry |
| GroupID | string | paragraph cluster for UI rollup |
| Hash | string | sha256 of Text |

### `Verdict`
| Field | Type | Constraints |
|---|---|---|
| Label | enum | supported \| contradicted \| unrelated \| unreachable |
| Confidence | float | 0.0-1.0 |
| Passage | string | ≤400 chars, verbatim from source |
| SourceURL | string | from the page that drove the verdict |
| JudgeModel | string | provider+model name |

### `SubObjective`
| Field | Type | Constraints |
|---|---|---|
| Objective | string | single question |
| OutputFormat | string | format spec |
| ToolGuidance | string | allowed/preferred tools |
| TaskBoundaries | string | negative scope |

---

## Error Handling

| Failure | Strategy | Resulting state |
|---|---|---|
| Chromium missing + `STOKE_BROWSER_AUTO_DOWNLOAD=0` | Hard error with 3 remediation paths | Exit 1, no partial artifacts |
| Chromium crash mid-session | Pool drops browser, retries once with fresh browser | Retry or mark source `unreachable` |
| Search provider 429 | `EnvFixFunc` rotates provider (Brave→Tavily→Bing) | Resume same claim |
| URL 403/robots-blocked | Switch to Wayback snapshot | Resume same claim |
| Judge returns malformed JSON | `jsonutil` tolerant parse; on failure, mark Verdict label=`unreachable` | Claim fails AC; T4 repair attempts once |
| Budget exceeded mid-run | Abort at next stage boundary; write partial report.json; exit 2 | Partial artifacts preserved under `RunRoot/` |
| Ctx canceled (SIGINT) | Flush current subagent, write partial artifacts, exit 130 | Resumable via `--run-id` |
| Ghost-verify (cache hit but underlying source changed) | `verification_cache` keys on `(claim_hash, source_hash)`; cache miss forces re-verify | Correct by construction |
| Contradiction detected | Hard criterion `claims.no_contradiction` fails; cannot soft-pass | Requires T4 repair or manual review |

---

## Boundaries — What NOT To Do

- Do NOT define the `Executor` interface or `VerifyFunc` field — that lives in spec-3.
- Do NOT build a router — spec-3 owns `Classify`.
- Do NOT implement Deploy verification — that's spec-6; it consumes `internal/browser/` but lives in its own package.
- Do NOT add payment/escrow — that's spec-5.
- Do NOT introduce a new event bus; reuse `internal/streamjson/` + spec-3's `internal/eventlog/`.
- Do NOT add embeddings via a new library; reuse `internal/vecindex/`.
- Do NOT create `cmd/rod-manager/` — document the upstream go-rod tool path instead.
- Do NOT default `STOKE_BROWSER_AUTO_DOWNLOAD=1` in CI; gate behind explicit opt-in.
- Do NOT touch `/home/eric/repos/stoke/internal/plan/verification_descent.go` — the executor supplies funcs; descent core stays stable (H-91).

---

## Testing

### `internal/browser/`
- [ ] Happy: `Pool.Acquire` returns distinct `Browser` instances up to size N; N+1th blocks on ctx.
- [ ] Happy: `Navigate`→`Screenshot` on local httptest server produces valid PNG >1KB (build tag `browser`).
- [ ] Happy: `ExtractText` prefers `<article>` over `<body>` when both exist.
- [ ] Error: `Navigate` with unreachable URL returns non-nil error; `ConsoleErrors` still returns empty slice (not nil).
- [ ] Error: `authBrowserNavigate` with `TrustMinimal` + off-allowlist URL returns auth error.
- [ ] Edge: Console ring buffer of 500 entries wraps on 501st entry without dropping the last 500.
- [ ] Edge: `WaitForSelector` with expired ctx returns ctx.Err().

### `internal/executor/research.go`
- [ ] Happy: `Execute(ctx, plan, EffortFact)` against mock providers writes full filesystem tree (plan.md, subagent-1/*, synthesis.md, claims.jsonl, verifications.jsonl, report.json).
- [ ] Happy: `BuildCriteria` returns ≥4 hard criteria + N per-claim + 1 coverage for a 3-claim synthesis.
- [ ] Happy: `verifyClaim` with supported passage returns `Verdict{Label:"supported", Confidence>0.7}`.
- [ ] Happy: Cache hit on `(claim_hash, source_hash)` skips the Judge call (assert via mock call-counter).
- [ ] Happy: Cross-reference rule accepts claim when ≥2 independent sources `supported`.
- [ ] Error: Search provider 429 triggers `EnvFixFunc` rotation; second attempt succeeds.
- [ ] Error: All providers exhausted → Verdict `unreachable`, not `unverified`.
- [ ] Error: Judge returns malformed JSON → Verdict `unreachable`; test asserts no panic.
- [ ] Error: `BuildRepairFunc` on `ac_bug`-category failure is NEVER invoked (descent T8 hard-rule; verified via descent integration stub).
- [ ] Edge: `MaxParallel=1` with `EffortDeep` deterministically serializes subagents.
- [ ] Edge: `--verify path.md` on a report with zero claims fails `claims.count >= 1` hard criterion.
- [ ] Edge: Contradicted claim cannot soft-pass (hard criterion `claims.no_contradiction`).
- [ ] Edge: Soft-pass granted at verified_fraction=0.85 with `[unverified: rate-limited]` annotation present.

### CLI
- [ ] Happy: `stoke research --help` contains `effort` word.
- [ ] Happy: `stoke research --effort minimal "capital of France"` prints `[verified]`-marked claim on a mock run.
- [ ] Happy: `stoke research --verify synthesis.md` skips Lead+subagent stages (assert no subagent directories created).
- [ ] Edge: `--effort` + `--deep` both set: `--effort` wins.

---

## Acceptance Criteria (bash)

```
# Build
go build ./cmd/stoke
go vet ./...

# Browser
go test ./internal/browser/... -run TestNavigateScreenshot
go test ./internal/browser/... -run TestPoolAcquireRelease
go test ./internal/browser/... -run TestAuthBrowserNavigate

# Research executor
go test ./internal/executor/... -run TestResearchExecutor
go test ./internal/executor/... -run TestBuildCriteria
go test ./internal/executor/... -run TestVerifyClaim4Stage
go test ./internal/executor/... -run TestEnvFixProviderRotation

# CLI
./stoke research --help | grep -q 'effort'
./stoke research --effort minimal "what is the capital of France" | grep -q '\[verified\]'
./stoke research --verify testdata/report.md | grep -qE '(verified|unreachable|contradicted)'

# Filesystem artifacts
ls -la .stoke/research/*/subagent-1/findings.md
ls -la .stoke/research/*/synthesis.md
ls -la .stoke/research/*/claims.jsonl

# Event emission
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type LIKE "research.%"'
```

---

## Implementation Checklist

Each item is self-contained: what, where, which patterns to follow, what to test, what error path to cover.

1. [ ] **Pin go-rod SHA in `go.mod`.** Run `go get github.com/go-rod/rod@<2026-SHA>`; add comment `// pinned: upstream v0.116.2 predates 2026 main; TODO: drop pin when new tag cut`. Verify with `go build ./...`. No code yet.

2. [ ] **Create `internal/browser/browser.go` with `Browser` interface + `rodDriver` implementation.** Copy interface verbatim from RT-01. Implement `Navigate` (rod page creation + console listener registration), `Screenshot` (FullScreenshot → PNG), `ExtractText` (article/main/body fallback), `ConsoleErrors` (drain ring buffer), `WaitForSelector` (ctx-scoped), `Close`. Thread ctx into every op. Return errors for rod panics via `defer recover()`.

3. [ ] **Create `internal/browser/pool.go` with `Pool` interface + `rodPool` backed by `rod.NewBrowserPool(size)`.** `Acquire`/`Release`/`Close`. Concurrent safe by rod construction; verify with `-race` test.

4. [ ] **Create `internal/browser/console.go` — per-page ring buffer (capacity 500, FIFO wrap).** Test wraparound at 501 entries.

5. [ ] **Create `internal/browser/extract.go` — readable-text extraction.** Preference order: `article, main` → `body` → `html`. Truncate at 200KB with log warning.

6. [ ] **Create `internal/browser/install.go` — Chromium presence check.** Check `CHROMIUM_BIN` → rod default cache → fail with 3-path remediation message if `STOKE_BROWSER_AUTO_DOWNLOAD=0`. Invoke `launcher.NewBrowser().MustGet()` only when auto-download is on.

7. [ ] **Create `internal/browser/tools.go` — four agentloop tool definitions.** `browser_navigate`, `browser_screenshot`, `browser_extract_text`, `browser_console_errors`. JSON Schema for each. Auth predicate enforces `TrustLevel` + `BrowserAllowlist` regex match. Register with agentloop tool registry.

8. [ ] **Add `BrowserAllowlist []*regexp.Regexp` to `config.Config` + `harness.Session`.** Read `STOKE_BROWSER_ALLOWLIST` env var (comma-separated regex) merged with config. Research executor sets per-run from `sources.jsonl`.

9. [ ] **Create `internal/browser/browser_test.go` — unit tests with mock driver.** No Chromium required. Covers Pool (3), console wrap (1), extract fallback (1), auth predicate (2). ≥7 tests.

10. [ ] **Create `internal/browser/integration_test.go` with `//go:build browser` tag.** Local `httptest.Server` on :0, 2-section HTML. Navigate + screenshot + extract + console. Assert PNG magic bytes. Excluded from default `go test ./...`.

11. [ ] **Create `internal/executor/research.go` skeleton — struct + constructor + `Executor` interface stubs.** Returns `ErrNotImplemented` for `Execute`; other methods return minimal valid values. Compiles against spec-3's `Executor`.

12. [ ] **Implement `ResearchExecutor.Execute`.** Write plan.md via Lead provider; spawn subagents via errgroup with `SetLimit(MaxParallel)`; collect findings; synthesize; cite; verify. Emit eventlog events at each stage (`research.plan.ready` etc.). Write `report.json`.

13. [ ] **Implement subagent loop in `internal/executor/research_subagent.go`.** Uses agentloop with subagent-scoped tool set (web_search + browser_extract_text + url_fetch). Writes `objective.md`, `findings.md`, `sources.jsonl`, `transcript.jsonl`. Enforces tool-call budget per tier.

14. [ ] **Implement `renderSubagentPrompt` — exact 4-field template from RT-07 §1.4.** Test renders expected string for golden `SubObjective`.

15. [ ] **Implement CitationAgent pass in `internal/executor/research_cite.go`.** Sonnet 4.5. Reads synthesis + sources; emits `claims.jsonl` with `[^N]` attachments. Deterministic prompt (temperature=0).

16. [ ] **Implement 4-stage verifier in `internal/executor/research_verify.go`.** Exactly the code sketch in §2.6. Cache table DDL in `schema.sql`. Verdict schema validated via `schemaval`.

17. [ ] **Implement `BuildCriteria` per §2.5.** Hard criteria (4) + per-claim (N) + coverage (1). Each with `VerifyFunc` — no `Command` field used. Group IDs populated from `claims.jsonl`.

18. [ ] **Implement `BuildRepairFunc` per §2.5.** Re-spawns single subagent for failing claim; appends to `verifications.jsonl`.

19. [ ] **Implement `BuildEnvFixFunc` per §2.5.** Handles `SearchRateLimit`, `FetchBlocked`, `NetworkDNS`. Returns `false` for other causes (descent falls through).

20. [ ] **Create `internal/executor/search.go` — `SearchClient` interface + Brave/Tavily/Bing impls.** `RotateProvider()` + `EnableWayback()` methods. Provider order from config. Returns typed errors (`ErrRateLimited`, `ErrBlocked`, `ErrUnreachable`).

21. [ ] **Add 4 ledger node types to `ledger/nodes/`.** `ResearchPlan`, `SubagentRun`, `ClaimNode`, `VerificationNode`. Each implements `NodeTyper`. Tests assert roundtrip marshaling.

22. [ ] **Wire research events into `internal/streamjson/`.** New subtypes under `_stoke.dev/research/*`: `plan.ready`, `subagent.started`, `subagent.completed`, `synthesis.ready`, `claim.verified`, `completed`, `budget_exhausted`. No schema break.

23. [ ] **Create `cmd/stoke/research_cmd.go` cobra subcommand.** Flags per Part 3. Handles `--verify` mode (skip Lead+subagents, only run verifier). Resolves `--run-id` to existing directory on resume. Respects `--budget-usd` via `costtrack.OverBudget`.

24. [ ] **Register `TaskResearch` → `ResearchExecutor` with spec-3 router.** Single line at router init; do not extend router logic.

25. [ ] **Write `internal/executor/research_test.go` covering §Testing list.** ≥12 tests. Use mock providers implementing `provider.Provider` with canned responses. No network calls in CI.

26. [ ] **Document offline Chromium workflow in `docs/browser.md`** — 3 install paths (auto-download, `CHROMIUM_BIN`, Docker image sketch from §1.3). NOT a README. Include the exact remediation message shown on install failure.

27. [ ] **Add cost estimates table (§2.9) to `docs/research.md`.** Also include the budget-check integration point. NOT a README.

28. [ ] **Update `cmd/stoke/main.go` help output to include `stoke research` in the command list.** One-liner: `research    Run multi-agent research with claim verification`.

29. [ ] **Verify the full AC block (§Acceptance Criteria) passes locally.** `go build`, `go vet`, `go test ./internal/browser/...`, `go test ./internal/executor/...`, and the CLI/filesystem/sqlite checks. Fix any reds; do not suppress.
