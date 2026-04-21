# RT-07: Multi-Agent Research Executor (Research Notes)

Status: raw research notes for the `ResearchExecutor` task type.
Audience: implementers integrating into `harness/`, `concern/`, `verify/`, and the descent engine.

## 1. Anthropic's multi-agent research system (June 2025)

Primary source: Anthropic engineering blog post "How we built our multi-agent research system" [1]. Secondary: Simon Willison summary [2], ByteByteGo deep dive [3], ZenML LLMOps writeup [4].

### 1.1 Orchestrator-worker architecture

- **LeadResearcher** (Opus 4 originally; upgrade target below): receives the user query, thinks, and writes a *plan* to Memory. It then spawns N specialized **Subagents** with distinct tasks, each running its own tool loop (web search, URL fetch, interleaved thinking). Subagents return condensed findings. The Lead iterates (re-spawn more subagents, or exit the loop) until the question is resolved.
- **Subagents** (Sonnet 4 originally): narrow objective, own 200k context, own tool budget. They do not talk to each other; the Lead is the only coordinator.
- **CitationAgent**: a terminal post-processing stage. Once the Lead has written a synthesis, the CitationAgent ingests both the synthesis and the collected source documents and attaches citation spans — it identifies the exact passage in each source that supports each claim.

Benchmark: lead+subagents beat single-agent Opus 4 by **90.2%** on Anthropic's internal research eval [1].

### 1.2 Model recommendations (2026 refresh)

The original post used Opus 4 + Sonnet 4. For Stoke in April 2026:

- **Lead**: Opus 4.7 (best MCP-Atlas tool orchestration, 77.3%) [5][6]. Opus 4.5 remains the documented floor where "multi-agent orchestration felt reliable rather than fragile" [5].
- **Subagents**: Sonnet 4.5 (cheap, fast, strong search-and-summarize). Haiku tier is usable for pure fact-retrieval subagents where reasoning is minimal.
- **CitationAgent**: Sonnet 4.5 is sufficient (deterministic passage-matching workload); Haiku acceptable at scale.

### 1.3 Effort scaling (verbatim from Anthropic)

The Lead decides parallelism based on query complexity [1][3]:

| Query type            | Subagents | Tool calls per subagent |
|-----------------------|-----------|-------------------------|
| Simple fact-check     | 1         | 3-10                    |
| Direct comparison     | 2-4       | 10-15                   |
| Complex research      | 10+       | 15+                     |

"Overinvestment in simple queries" is called out as the common failure mode, so the scaling ladder must be enforced by explicit instructions to the Lead, not left to model judgment alone.

### 1.4 Subagent instruction format

Each spawn must carry four fields [1]:
1. **Objective** — single concrete research question.
2. **Output format** — e.g. "bulleted findings with source URLs and 1-2 sentence quoted excerpts".
3. **Tool guidance** — which tools to use and which sources to prefer (e.g. "primary sources only, no Reddit").
4. **Task boundaries** — what NOT to investigate; scope fence to prevent subagent drift and duplicate work.

### 1.5 Filesystem-as-communication

Subagents write findings to external storage and return **lightweight references** to the Lead — not full payloads. Rationale from the post [1][4]:

- Avoids the "game of telephone" where each copy through the Lead loses fidelity.
- Keeps large excerpts out of the Lead's context (Lead's 200k window is precious).
- Lets the CitationAgent read raw sources directly at the end, rather than reconstruct them from Lead chatter.

### 1.6 Memory persistence across truncation

The Lead's plan is written to a Memory tool at spawn time because its context will almost certainly exceed 200k tokens before synthesis. On truncation or restart, the plan is reloaded — this is the single most important durability point [1].

### 1.7 Token-usage variance

Three factors explain **95% of performance variance** on BrowseComp-style evals; **token usage alone explains 80%** [1]. Tool-call count and model choice are the other two. Practical implication: *effort in (tokens+tool calls) is the dominant predictor of research quality*, so Stoke's effort knob should map directly to a token/tool budget rather than to a vague "depth" enum. Cost envelope: agents use ~4x chat tokens; multi-agent systems ~15x chat tokens.

## 2. OpenAI Deep Research & Google Gemini Deep Research

### OpenAI Deep Research (Feb 2025, o3-deep-research) [7][8]
- Four-phase pipeline: **Triage -> Clarification -> Instruction -> Research**. Modular agents per phase.
- Single-threaded research loop (no lead+subagents parallelism); depth comes from long tool-using chains.
- Grounded on Bing search. Heavy on structured output.

### Gemini Deep Research (Dec 2025, Gemini 3 Pro) [9][10]
- **Planner -> Retriever -> Reader -> Reasoner -> Synthesis**. User can edit the plan before execution starts.
- Stop condition: stopping-score OR 60-minute wall clock.
- Async task manager holding shared state between planner and task workers so partial progress survives errors.

### Lessons for Stoke
- **Expose the plan for review** (Gemini) — Stoke can route the plan through the CTO/Reviewer stance before spawning subagents.
- **Hard stop condition** (Gemini) — Stoke needs a wall clock and a score threshold, not just "done when done".
- **Keep subagents narrow** (OpenAI) — single-role agents (clarify vs. search vs. synthesize) are easier to eval.
- **Parallelism is the win** (Anthropic) — OpenAI's sequential pipeline loses the 90% wall-clock reduction Anthropic gets from parallel subagents.

## 3. Claim verification

### 3.1 State of the art (2026)

Multi-stage pipelines dominate [11][12]:
1. **Claim extraction** — decompose report into atomic claims.
2. **Evidence retrieval** — pull the cited URL/doc.
3. **Passage matching** — find the span in the source that putatively supports the claim.
4. **LLM-as-judge alignment** — "does this passage entail this claim?" yielding {supports, contradicts, unrelated}.
5. **Calibrated judgment** — confidence, typically from probabilistic certainty + reasoning consistency (PCC framework).

CiteAudit [11] is the relevant benchmark for hallucinated citations and uses exactly this decomposition.

### 3.2 Stoke's recommended verification function

```go
// VerifyClaim returns support verdict for a single atomic claim.
func (e *ResearchExecutor) VerifyClaim(ctx, claim Claim) (Verdict, error)

type Verdict struct {
    Label      string   // "supported" | "contradicted" | "unrelated" | "unreachable"
    Confidence float64  // 0..1
    Passage    string   // quoted span from source that drove the verdict
    SourceURL  string
    JudgeModel string
}
```

Implementation:
1. Fetch `claim.SourceURL` via `browser.Client` (shared fetch cache).
2. Chunk the page, embed, top-k retrieve against the claim text (`vecindex`).
3. Pass top-3 passages + claim to Sonnet 4.5 with a deterministic judge prompt. Require JSON `{label, confidence, passage}`.
4. Cross-reference: if `claim.CrossRefs` has >=2 sources, accept if any two independently `supports`.
5. Cache (claim_hash, source_hash) -> Verdict in ledger so T4 repair doesn't re-verify unchanged claims.

## 4. Filesystem layout

```
.stoke/research/<run-id>/
  plan.md                 # Lead's decomposition; also mirrored to ledger as ResearchPlan node
  memory.json             # Lead's persistent scratchpad (survives truncation)
  subagent-<N>/
    objective.md          # Exactly the 4-field prompt from 1.4
    findings.md           # Subagent's writeup (what goes back to Lead by reference)
    sources.jsonl         # {url, fetched_at, excerpt, content_hash} per source
    transcript.jsonl      # Tool-call log (for replay + cost accounting)
  synthesis.md            # Lead's final report with inline [^N] citation markers
  claims.jsonl            # {id, text, source_urls[]} — extracted atomic claims
  verifications.jsonl     # {claim_id, label, confidence, passage, judge_model}
  cost.json               # tokens + USD per stage (lead / each subagent / citation / verify)
  report.json             # final Deliverable envelope
```

This layout is a literal filesystem mirror of Anthropic's artifact pattern (1.5). The `sources.jsonl` is the raw substrate the CitationAgent and the verifier both read.

## 5. Go implementation sketch

```go
type ResearchExecutor struct {
    LeadProvider provider.Provider // Opus 4.7
    SubProvider  provider.Provider // Sonnet 4.5
    JudgeProvider provider.Provider // Sonnet 4.5 or Haiku
    Browser      browser.Client
    SearchAPI    search.Client
    Store        research.Store   // FTS5-backed, per pkg `research/`
    Ledger       ledger.Writer
}

type Effort int
const (
    EffortFact Effort = iota   // 1 subagent,  3-10 calls
    EffortCompare              // 2-4 subagents, 10-15 calls each
    EffortDeep                 // 10+ subagents, 15+ calls each
)

func (e *ResearchExecutor) Execute(ctx context.Context, plan ResearchPlan, effort Effort) (Deliverable, error)

func (e *ResearchExecutor) BuildCriteria(task Task, d Deliverable) []AcceptanceCriterion
```

### 5.1 BuildCriteria output

One `AcceptanceCriterion` per **atomic claim**, grouped under per-section criteria for UI readability:

- **Hard criteria** (must pass; block T8 soft-pass):
  - `report.exists && non-empty`
  - `claims.count >= 1`
  - `every claim has >=1 source_url`
  - `no claim has label=contradicted` (contradiction is never soft-passable)
- **Per-claim criteria** (soft-passable):
  - `claim.<id>.verified` where verified = `label=supported && confidence>=0.7`
- **Coverage criteria** (soft-passable with explicit marking):
  - `verified_fraction >= 0.9` (soft-pass at 0.8 if unverifiable claims are annotated in synthesis.md as `[unverified: reason]`).

Grouping: claims in the same paragraph share a `group_id` so the UI collapses. Verdict rollup is `min(child confidences)`.

## 6. Integration with the descent engine

The descent engine (H-91, `STOKE_DESCENT=1`) has tiers T0..T8. For research tasks:

- **T0-T3 (build/test/lint/vet)**: N/A — research produces markdown, not compiled code. These tiers trivially pass.
- **T4 (repair)**: triggered when one or more claim criteria fail. Repair = re-spawn a *targeted* subagent with objective "re-verify claim <id>: <text>; prior verdict was <label> with <passage>". Budget: 1 subagent, 10-15 tool calls (EffortCompare slice). Write a new `verifications.jsonl` row; never mutate the old one (append-only, matches ledger semantics).
- **T5 (env fix)**: research-specific environment failures — (a) rate-limited search API (429), (b) blocked fetch (403/robots), (c) DNS/network. Env-fix actions: rotate search provider (Brave -> Tavily -> Bing), back off with jitter, switch to archived snapshot (Wayback) for blocked URLs. If all providers down, mark `unreachable` (not `unverified`).
- **T6-T7 (rebuild/retry)**: reuse `fetch cache` keyed by `content_hash`; do not refetch identical URLs within a run.
- **T8 (soft-pass)**: granted when `verified_fraction >= 0.8` AND every unverified claim is explicitly annotated in `synthesis.md` with a visible `[unverified: <reason>]` marker AND no claim is `contradicted`. The soft-pass verdict is written as a ledger `SoftPassGrant` node citing `verifications.jsonl`.

### 6.1 Ledger nodes

New node types to add in `ledger/nodes/`:
- `ResearchPlan` (plan.md content hash, subagent count, effort)
- `SubagentRun` (objective, findings hash, cost, parent plan id)
- `ClaimNode` (text, source_urls, group_id)
- `VerificationNode` (claim_id, verdict, judge_model, passage_hash)

These plug directly into the consensus loop; a Reviewer stance can dissent on a claim verdict and trigger T4 repair.

## 7. Open questions

1. Should the Lead plan be gated through the CTO stance (Gemini's "edit the plan" pattern) before any subagents spawn? Likely yes for `EffortDeep`, no for `EffortFact`.
2. Do we embed the verifier in the CitationAgent pass, or keep them separate? Anthropic separates them (citation = "where is this supported", verify = "is it actually supported"). Recommend: separate, because verification fails more loudly than citation and the ledger wants distinct nodes.
3. Concurrency cap on parallel subagents? Anthropic implies 10+, but this is bounded by search-API quotas in practice. Propose a per-run `max_parallel=5` default with override.

---

## Sources

[1] Anthropic Engineering, "How we built our multi-agent research system", June 2025 — https://www.anthropic.com/engineering/multi-agent-research-system
[2] Simon Willison, "Anthropic: How we built our multi-agent research system", 2025-06-14 — https://simonwillison.net/2025/Jun/14/multi-agent-research-system/
[3] ByteByteGo, "How Anthropic Built a Multi-Agent Research System" — https://blog.bytebytego.com/p/how-anthropic-built-a-multi-agent
[4] ZenML LLMOps Database, "Building a Multi-Agent Research System for Complex Information Tasks" — https://www.zenml.io/llmops-database/building-a-multi-agent-research-system-for-complex-information-tasks
[5] Anthropic, "Introducing Claude Opus 4.5" — https://www.anthropic.com/news/claude-opus-4-5
[6] Vellum, "Claude Opus 4.7 Benchmarks Explained" — https://www.vellum.ai/blog/claude-opus-4-7-benchmarks-explained
[7] OpenAI, "Introducing deep research" — https://openai.com/index/introducing-deep-research/
[8] OpenAI Cookbook, "Deep Research API with the Agents SDK" — https://cookbook.openai.com/examples/deep_research_api/introduction_to_deep_research_api_agents
[9] Google AI for Developers, "Gemini Deep Research Agent" — https://ai.google.dev/gemini-api/docs/deep-research
[10] Google Blog, "Build with Gemini Deep Research" — https://blog.google/technology/developers/deep-research-agent-gemini-api/
[11] CiteAudit benchmark — https://www.aimodels.fyi/papers/arxiv/citeaudit-you-cited-it-but-did-you
[12] ClaimCheck: Real-Time Fact-Checking with Small Language Models — https://arxiv.org/abs/2510.01226
