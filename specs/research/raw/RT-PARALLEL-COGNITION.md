# RT-PARALLEL-COGNITION.md — Parallel Thoughts with Shared Context

**Scope:** Prior art survey for r1's "parallel concerns over shared workspace" feature. Maps cognitive-science models and software frameworks to concrete Go primitives and r1 package names.

**Frame:** r1 already has process-isolated subagent fan-out (`specexec/`, `scheduler/`, `harness/stances/`). The new feature is *not* that — it is **multiple specialist threads sharing a single working memory**, each posting findings to a workspace that a main "actor" thread reads. Society-of-Mind, not contractor-pool.

**Method:** 10 web searches across 8 topics, sources cited inline.

---

## 1. Society of Mind (Marvin Minsky)

**Core idea.** The mind is a society of many simple **agents** organized into **agencies** (small functional groups). No central executive — intelligence is an emergent property of agents triggering, suppressing, and collaborating with one another via *connection lines*. **K-lines** are memory traces that re-activate a previously useful coalition of agents when a similar situation recurs. The **B-brain** watches the **A-brain** for errors and intervenes — a meta-cognitive observer over the object-level reasoner ([Wikipedia: Society of Mind](https://en.wikipedia.org/wiki/Society_of_Mind), [Singh, *Examining the Society of Mind*](http://jfsowa.com/ikl/Singh03.htm)).

**Software primitive.** Pub/sub on a shared bus where many small handlers can subscribe to the same channel; `K-lines` map to **named coalitions** (preset subscriber sets) re-bound on a trigger. The B-brain is a **supervisor goroutine** observing the message stream of the A-brain and emitting corrective events.

**Naming for r1.**
- `mind/agents` (specialist threads — *not* "agents" since that collides with r1's existing stance/agent terminology — prefer **`concern`** which already exists)
- `mind/agency` for grouped concerns
- `kline` package for "remembered coalitions" (preset groups of concerns to wake on a pattern)
- `metabrain` or `bbrain` — already foreshadowed by existing `supervisor/` (rules engine fits the B-brain role exactly)

**Gotchas.** Minsky's model is intentionally *vague about control flow* — there is no scheduler. Pure SoM implementations devolve into thrash. You need an arbiter (this is exactly why GWT was invented as the next layer).

---

## 2. Global Workspace Theory (Bernard Baars)

**Core idea.** Consciousness is a *theater stage*: many unconscious specialists compete for a **spotlight of attention**; the winning content is **broadcast globally** so every other specialist can see it. Information enters the global workspace only after winning a competition; once in, it is broadcast to all subscribers; broadcast is the unit of "consciousness." Baars explicitly designed this as the *cognitive analog of the AI blackboard system* ([Wikipedia: Global workspace theory](https://en.wikipedia.org/wiki/Global_workspace_theory), [Baars: 50 Years of GWT](https://bernardbaars.com/publications/fifty-years-of-consciousness-science-varieties-of-global-workspace-theory-gw-citations/)).

**Recent LLM mappings.** "Theater of Mind for LLMs" (arXiv 2604.08206) replaces passive shared memory with an **active, event-driven broadcasting hub** where heterogeneous LLM agents (divergent generation, logical critique, executive arbitration) compete for stage time ([arxiv.org/html/2604.08206](https://arxiv.org/html/2604.08206)). The "Unified Mind Model" (arXiv 2503.03459) and CogniPair (arXiv 2506.03543) both build GWT-on-LLM stacks ([arxiv.org/html/2503.03459v1](https://arxiv.org/html/2503.03459v1)).

**Software primitive.**
- **One writable channel + N subscriber channels** (broadcast fan-out).
- **Selection-broadcast cycle**: a coalition layer scores incoming messages → the winner is appended to a single-source-of-truth log → the log is published to all subscribers.
- Implementation in Go: `bus/` (already exists) is *exactly* the GWT broadcast hub. What's missing is the *competition/selection* phase before broadcast.

**Naming for r1.**
- **`workspace`** package — the shared mutable view all concerns read from. (Better than "Blackboard" because it matches the GWT term and avoids overloading r1's `bus/`.)
- `workspace.Spotlight` — the currently broadcast item.
- `workspace.Subscribe(role)` — concern-typed subscriptions.
- The selection phase: `workspace.Arbiter` or reuse `supervisor/` rules.

**Gotchas.**
- Single-writer bottleneck: the broadcast hub is intentionally serial. Don't try to parallelize the spotlight itself; parallelize the competitors *before* it and the consumers *after* it.
- "Ignition" — content must reach a competitive threshold before broadcast. Without this, every concern's output dilutes the spotlight and the actor thread drowns.

---

## 3. CoALA — Cognitive Architectures for Language Agents (Sumers, Yao, Narasimhan, Griffiths, 2023/2024)

**Core idea.** A formal framework that splits an LLM agent's memory into four tiers and an explicit decision cycle ([arXiv 2309.02427](https://arxiv.org/abs/2309.02427), [Cognee blog explainer](https://www.cognee.ai/blog/fundamentals/cognitive-architectures-for-language-agents-explained)):

| Memory     | Lifetime           | What it holds                                  |
|------------|--------------------|------------------------------------------------|
| Working    | Single decision    | Active perception, retrieval results, scratchpad |
| Episodic   | Long-term          | "Last time I tried X, Y happened"              |
| Semantic   | Long-term          | Facts about the world                          |
| Procedural | Long-term / weights| How to do things (tool wrappers, code, prompts)|

The decision cycle is propose → evaluate → select → execute, with reasoning and retrieval as inner actions on memory.

**Concurrent processes.** CoALA itself doesn't mandate parallelism, but the four-memory split is *what enables* concurrent specialists to share context safely: working memory is the contended resource (mutex / channel), the three long-term tiers are mostly read-mostly stores (RWMutex / append-only logs).

**Software primitive.** `RWMutex`-protected stores per tier; working memory as a versioned snapshot passed to each concern; episodic memory as an append-only log indexed for retrieval.

**Naming for r1.**
- r1 already has `memory/` (long-term), `wisdom/` (episodic-style learnings), `research/` (FTS5-indexed semantic store), `skill/` (procedural). **Adopt the CoALA tier names as a vocabulary**, mapping:
  - working → `workspace` (new)
  - episodic → existing `wisdom`
  - semantic → existing `research` + `repomap`
  - procedural → existing `skill` + `prompts`

**Gotchas.** The seductive trap is to put everything in working memory. CoALA's discipline is that working memory is *small, ephemeral, and the only place writes contend*. Long-term tiers are append-mostly.

---

## 4. Blackboard Architecture (Hearsay-II lineage)

**Core idea.** A shared data structure (the **blackboard**) holds the evolving solution. Multiple **knowledge sources** (KSs) — independent specialists — watch the blackboard; when their preconditions match, they fire and post hypotheses. A **control component / scheduler** decides which KS runs next when several are eligible ([Wikipedia: Blackboard system](https://en.wikipedia.org/wiki/Blackboard_system), [GeeksforGeeks: Blackboard Architecture](https://www.geeksforgeeks.org/system-design/blackboard-architecture/), [Schepis: Democratic Multi-Agent AI Part 1](https://medium.com/@edoardo.schepis/patterns-for-democratic-multi-agent-ai-blackboard-architecture-part-1-69fed2b958b4)).

**Conflict resolution.** Three classic strategies: (1) priority/agenda-based scheduling so only one KS fires at a time; (2) allow conflicting hypotheses to coexist with confidence scores until evidence accumulates; (3) explicit voting/consensus by a meta-KS ([Curate Partners](https://curatepartners.com/tech-skills-tools-platforms/understanding-the-blackboard-architectural-pattern-collaborative-problem-solving-for-complex-systems-curate-partners/)).

**Software primitive.**
- Blackboard = a structured, versioned, mutex-protected store (Go: `sync.RWMutex` over a typed map keyed by hypothesis level).
- KS = a goroutine that selects on `blackboardChanged` events and writes new hypotheses.
- Scheduler = a single goroutine that drains a "ready KS" priority queue.

**Naming for r1.** `blackboard` is the historically correct name but **GWT's `workspace` reads cleaner in 2026** and avoids the "blackboard" term sounding dated. The Hearsay-II *control structure* is what r1 needs to lift directly: an **agenda** of KS activations + a meta-controller. Suggested r1 packages: `workspace/` (data) + `agenda/` (scheduling) + reuse `supervisor/` (meta-controller / B-brain).

**Gotchas.**
- The single shared structure is the synchronization bottleneck. Mitigation: partition by "level" (Hearsay had phonetic / lexical / syntactic levels — r1's analogs would be evidence / hypothesis / decision).
- Conflict resolution is the hard part. Don't allow KSes to *delete* each other's posts; only append + supersede. This matches r1's existing `ledger/` append-only model.

---

## 5. LangGraph parallel branches / fan-out

**Core idea.** LangGraph borrows Pregel's **superstep** model: nodes run in synchronized rounds; nodes inside a superstep run concurrently; state updates are reduced at the superstep boundary via channel reducers. Fan-out is "add multiple edges from one node"; **`Send`** lets you create branches dynamically at runtime (map-reduce). A **checkpointer** persists state after every superstep so a thread can resume ([LangChain Graph API docs](https://docs.langchain.com/oss/python/langgraph/graph-api), [Murro: Parallel Nodes in LangGraph](https://medium.com/@gmurro/parallel-nodes-in-langgraph-managing-concurrent-branches-with-the-deferred-execution-d7e94d03ef78), [LangGraph TS Persistence](https://langgraphjs.guide/persistence/)).

**Send pattern.**
```python
def route_to_researchers(state):
    return [Send("researcher", {"topic": t}) for t in state["topics"]]
# all Sends executed concurrently within one superstep
```

**Synchronization.** Each state field has a *reducer* (`operator.add`, custom merge fn). Conflicting writes within a superstep are deterministically combined; reads inside a superstep see the *previous* checkpoint. **Deferred nodes** wait until all peers in the superstep finish before reading their merged outputs ([LangGraph defer issue #6320](https://github.com/langchain-ai/langgraph/issues/6320)).

**Software primitive (Go).**
- "Superstep" = a `WaitGroup` round.
- `Send` = a typed message dropped onto a fan-out channel.
- "Reducer" = a merge function called under a state mutex at round end.
- "Checkpointer" = serialize state to `session/SQLStore` after each round (r1 already has this).

**Naming for r1.** Don't import "LangGraph" terminology wholesale — but the **superstep barrier** is the design pattern r1 needs for "all concerns post their findings, then the actor reads the merged view." Name it `mind.Tick` or `workspace.Round`. The reducer functions belong in `workspace/reducers/`.

**Gotchas.** [LangGraph issue #6320](https://github.com/langchain-ai/langgraph/issues/6320) — a slow sibling in a superstep blocks unrelated downstream nodes because of the synchronous barrier. Lesson: don't pin *every* concern to the same tick rate. r1 should support **streaming concerns** (post incrementally) plus **gated concerns** (only readable at tick boundaries).

---

## 6. AutoGen GroupChat / GroupChatManager

**Core idea.** The `GroupChatManager` is a meta-agent that selects "who speaks next" via one of: round-robin, manual, LLM-decided ("auto"), or custom function. **It is fundamentally turn-based.** One agent speaks, the manager broadcasts the message to all others, the manager picks the next speaker ([AutoGen Group Chat docs](https://microsoft.github.io/autogen/stable//user-guide/core-user-guide/design-patterns/group-chat.html), [AutoGen 0.2 patterns](https://microsoft.github.io/autogen/0.2/docs/tutorial/conversation-patterns/)). Concurrent execution is supported only via the lower-level Core API's pub/sub subscriptions ([AutoGen Concurrent Agents](https://microsoft.github.io/autogen/stable//user-guide/core-user-guide/design-patterns/concurrent-agents.html), [GitHub issue #210](https://github.com/microsoft/autogen/issues/210)).

**Software primitive.** A single shared message log + a "next speaker" selector. Conceptually a state machine, not a parallel system.

**Naming for r1.** Don't model after this. r1 wants *concurrent* concerns, not turn-based. But the **selector function** abstraction is worth borrowing — `workspace.Speaker` could be a pluggable function for "what gets the spotlight."

**Gotchas.** Turn-based means total throughput = 1 / latency-of-slowest-turn. Bad fit for "many specialists watching simultaneously." Useful only as a *fallback* mode when concerns serialize (e.g., during a structured review).

---

## 7. OpenAI Swarm / Anthropic Claude Agent SDK / Managed Agents

**OpenAI Swarm / Agents SDK.** Two primitives: `Agent` (instructions + tools) and `handoff` (a tool call that returns another agent). Control transfers entirely; **no shared mutable context across agents**. Strictly sequential. Swarm has been superseded by the Agents SDK ([github.com/openai/swarm](https://github.com/openai/swarm), [OpenAI cookbook: Orchestrating Agents](https://developers.openai.com/cookbook/examples/orchestrating_agents), [OpenAI Agents SDK docs](https://openai.github.io/openai-agents-python/)).

**Anthropic Claude Agent SDK / Managed Agents.** Tool-use chains with subagents; Managed Agents adds Anthropic-hosted orchestration ([Composio framework comparison](https://composio.dev/content/claude-agents-sdk-vs-openai-agents-sdk-vs-google-adk), [QubitTool 2026 showdown](https://qubittool.com/blog/ai-agent-framework-comparison-2026)).

**Anthropic's research multi-agent system** is the most relevant prior art Anthropic has published. An **orchestrator** Claude Opus spins up **3-5 subagents** (Sonnet) in parallel, each with its **own context window**, each running tools in parallel within itself. Outperforms single-agent Opus by 90.2% on research evals ([Anthropic: How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system), [ZenML LLMOps DB](https://www.zenml.io/llmops-database/building-a-multi-agent-research-system-for-complex-information-tasks)).

**Software primitive.** Sequential handoff (Swarm) = a function-call return chain. Anthropic's parallel subagents = isolated context windows + result aggregation back to orchestrator. **Crucially: Anthropic's subagents do NOT share context** — each gets a clean window and returns a summary.

**Naming for r1.** r1's `harness/stances/` already does the Anthropic-style orchestrator/subagent thing. The new feature is *different* — it's the **opposite trade-off**: shared context for tight coupling, isolated agents for loose coupling. Call out the distinction explicitly: keep `harness/` for isolated subagents; introduce `mind/` (or `workspace/` + `concern/`) for shared-context concerns.

**Gotchas.** Anthropic's writeup explicitly notes multi-agent systems "use about 15× more tokens than single-agent chats" ([Anthropic engineering post](https://www.anthropic.com/engineering/multi-agent-research-system)). Shared-context parallel concerns will be even more expensive — every concern reads the full workspace. Mitigation: aggressive prompt caching (already in r1's `promptcache/`) and tier the workspace so concerns only read the slice they need.

---

## 8. Generative Agents (Park et al., 2023)

**Core idea.** Agent architecture with three modules: **memory stream** (append-only log of natural-language observations), **reflection** (periodic synthesis of recent stream entries into higher-level inferences), and **planning** (translate reflections + current state into hierarchical plans). Memory retrieval scores entries by *recency × importance × relevance* ([arXiv 2304.03442](https://arxiv.org/abs/2304.03442), [Park 2023 PDF](https://3dvar.com/Park2023Generative.pdf)).

**Parallelism.** The original paper runs 25 agents in parallel in a sandbox, but each agent is *internally sequential*. The interesting parallelism is *across* agents (each is an independent process); within-agent it is observe → retrieve → reflect → plan, serially.

**Software primitive.** Append-only log + scored retrieval (BM25 + embedding + decay). Reflection is a *cron-like* scheduled task that batches recent log entries and writes synthesized higher-level entries back into the same log.

**Naming for r1.** r1's `wisdom/` and `memory/` packages already cover the memory-stream lane. The contribution from Park is the **reflection cadence pattern** — periodic synthesis is a clean fit for the B-brain / supervisor role over the workspace. Recommend a `reflect` subpackage (or absorb into `supervisor/rules/`) that periodically reads the workspace and posts higher-level summaries.

**Gotchas.** Reflection thresholds matter: too-frequent reflection thrashes context; too-sparse reflection means the actor thread drowns in raw observations. Park used a sum-of-importance threshold to trigger reflection — port this idea, not a fixed cadence.

---

## Synthesis Table

| Concept           | Primitive (Go)                          | r1 package name (recommended)        | Closest existing r1 |
|-------------------|-----------------------------------------|--------------------------------------|---------------------|
| Specialist        | goroutine + role prompt                 | `concern` (existing)                 | `concern/`          |
| Coalition / KS    | named subscriber set                    | `kline` (new) or reuse `concern/templates` | `concern/templates/` |
| Shared workspace  | RWMutex over typed store + broadcast bus| `workspace` (new)                    | `bus/` + `ledger/`  |
| Spotlight / arbiter| single goroutine + score-and-publish   | `workspace.Spotlight`                | `supervisor/`       |
| Working memory    | versioned snapshot / scratchpad         | `workspace.Scratch`                  | `context/`          |
| Episodic memory   | append-only log + retrieval             | `wisdom` (existing)                  | `wisdom/`           |
| Semantic memory   | indexed corpus                          | `research` + `repomap` (existing)    | `research/`         |
| Procedural memory | tool defs + skills                      | `skill` + `prompts` (existing)       | `skill/`            |
| Superstep / tick  | WaitGroup round + reducer               | `workspace.Round` or `mind.Tick`     | `agentloop/`        |
| Send / fan-out    | typed chan of work items                | `workspace.Send`                     | `scheduler/`        |
| Reflection        | scheduled synthesis goroutine           | `reflect` or `supervisor/rules/reflect` | `supervisor/`    |
| B-brain           | observer over A-brain stream            | `metabrain` or reuse `supervisor/`   | `supervisor/`       |
| Checkpointer      | serialize state per round               | reuse `session/SQLStore`             | `session/`          |

---

## Sources

- [Wikipedia: Society of Mind](https://en.wikipedia.org/wiki/Society_of_Mind)
- [Singh: Examining the Society of Mind](http://jfsowa.com/ikl/Singh03.htm)
- [Wikipedia: Global workspace theory](https://en.wikipedia.org/wiki/Global_workspace_theory)
- [Baars: Fifty Years of GWT](https://bernardbaars.com/publications/fifty-years-of-consciousness-science-varieties-of-global-workspace-theory-gw-citations/)
- ["Theater of Mind" for LLMs (arXiv 2604.08206)](https://arxiv.org/html/2604.08206)
- [Unified Mind Model (arXiv 2503.03459)](https://arxiv.org/html/2503.03459v1)
- [CogniPair GNWT Multi-Agent (arXiv 2506.03543)](https://arxiv.org/abs/2506.03543)
- [Sumers et al.: Cognitive Architectures for Language Agents (arXiv 2309.02427)](https://arxiv.org/abs/2309.02427)
- [Cognee: CoALA Explained](https://www.cognee.ai/blog/fundamentals/cognitive-architectures-for-language-agents-explained)
- [Wikipedia: Blackboard system](https://en.wikipedia.org/wiki/Blackboard_system)
- [GeeksforGeeks: Blackboard Architecture](https://www.geeksforgeeks.org/system-design/blackboard-architecture/)
- [Schepis: Democratic Multi-Agent AI — Blackboard Pt 1](https://medium.com/@edoardo.schepis/patterns-for-democratic-multi-agent-ai-blackboard-architecture-part-1-69fed2b958b4)
- [Curate Partners: Blackboard Pattern](https://curatepartners.com/tech-skills-tools-platforms/understanding-the-blackboard-architectural-pattern-collaborative-problem-solving-for-complex-systems-curate-partners/)
- [LangChain Graph API docs](https://docs.langchain.com/oss/python/langgraph/graph-api)
- [Murro: Parallel Nodes in LangGraph](https://medium.com/@gmurro/parallel-nodes-in-langgraph-managing-concurrent-branches-with-the-deferred-execution-d7e94d03ef78)
- [LangGraph TS Persistence Guide](https://langgraphjs.guide/persistence/)
- [LangGraph issue #6320: superstep blocking](https://github.com/langchain-ai/langgraph/issues/6320)
- [AutoGen Group Chat docs](https://microsoft.github.io/autogen/stable//user-guide/core-user-guide/design-patterns/group-chat.html)
- [AutoGen Concurrent Agents](https://microsoft.github.io/autogen/stable//user-guide/core-user-guide/design-patterns/concurrent-agents.html)
- [AutoGen issue #210: parallel responses in GroupChat](https://github.com/microsoft/autogen/issues/210)
- [OpenAI Swarm GitHub](https://github.com/openai/swarm)
- [OpenAI Cookbook: Orchestrating Agents](https://developers.openai.com/cookbook/examples/orchestrating_agents)
- [OpenAI Agents SDK docs](https://openai.github.io/openai-agents-python/)
- [Anthropic: How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)
- [Composio: Claude Agents SDK vs OpenAI Agents SDK vs Google ADK](https://composio.dev/content/claude-agents-sdk-vs-openai-agents-sdk-vs-google-adk)
- [Park et al.: Generative Agents (arXiv 2304.03442)](https://arxiv.org/abs/2304.03442)
- [Park et al.: Generative Agents PDF](https://3dvar.com/Park2023Generative.pdf)

---

## Recommendation Summary

**Central design pattern: Global Workspace Theory broadcast on top of a CoALA memory tier split, scheduled via LangGraph-style supersteps, governed by a Society-of-Mind B-brain (= r1's existing `supervisor/`).**

- Adopt **`workspace`** (not "blackboard") as the package name for the shared mutable view. Reason: GWT vocabulary is current, blackboard sounds 1980s, and r1 already has `bus/` and `ledger/` so a third generic-data-store name needs to be distinctive.
- Reuse **`concern`** for specialists (already exists in r1 — perfect fit for "agents/knowledge sources").
- Add **`workspace.Round` / `workspace.Send` / `workspace.Spotlight`** to formalize the superstep + fan-out + selection cycle.
- Map memory tiers to existing packages with CoALA names as docstring vocabulary: working→`workspace`, episodic→`wisdom`, semantic→`research`+`repomap`, procedural→`skill`.
- The B-brain / arbiter / control component is **already `supervisor/`** — extend it with reflection rules (Park-style) and ignition/spotlight rules (GWT).
- Closest prior art: a hybrid of **Hearsay-II blackboard control structure + GWT broadcast/ignition + LangGraph Send/superstep**. The "Theater of Mind for LLMs" paper (arXiv 2604.08206) is the single most directly applicable reference.
- Avoid AutoGen GroupChat (turn-based) and OpenAI Swarm (sequential handoff) as templates — both contradict the shared-context-parallel goal. Anthropic's multi-agent research system is parallel but *isolates* context per subagent — the opposite of what's wanted here. Cite it as the *contrast* case.
