# 02 — The Ledger

The ledger is Stoke's internal git for reasoning. Same fundamental data model as git — append-only history, immutable past, content-addressed entries, branching and merging, an audit trail you can walk — applied to decisions, tasks, skills, and snapshot annotations rather than to source code. The ledger is a first-class software component of Stoke. It ships in the box. It has its own package, its own API, its own tests, its own validation gate, and its own git hook for storage-layer enforcement.

Every other component of Stoke is built on top of the ledger. The team operates *through* the ledger. The consensus loop's terminal state is a ledger commit. The concern field is a query against the ledger. The CTO's snapshot defense is a query against the ledger. The skill library is a slice of the ledger. The Judge's "are we stuck" check is a query against the ledger. There is no part of Stoke's persistent reasoning that lives outside the ledger.

This file specifies what the ledger is and what its API contracts are. It does not specify implementation details that may change as the codebase matures — the contracts are what other components depend on, and the contracts are what get locked in here.

---

## What the ledger holds

The ledger is a directed graph. Nodes are entries; edges are typed relationships between entries. Both nodes and edges are immutable once committed. Both can be added; neither can be modified or deleted.

There are several **node types**, each with its own schema, but all sharing the same substrate:

- **Decision nodes (internal)** — the team's record of how it reached agreement during a task. Task-scoped. Anti-loop context.
- **Decision nodes (repo)** — the codebase's record of why it looks the way it does. Repo-scoped. Next-developer context.
- **Task nodes** — nodes in the task DAG. Tickets, features, milestones, branches. The decomposition of work the Lead Engineer produces from the SOW.
- **Skill nodes** — patterns the convergence loop has proven through prior consensus. Read by stances during their work. Manufactured from completed tasks.
- **Snapshot annotation nodes** — the CTO's notes on the protected baseline. What's intentional in the existing codebase, what's accidental, what conventions are coherent, what areas are known to be load-bearing.

Each node type has its own required fields, validation rules, and lifecycle states. Each node type lives in its own directory under `.stoke/ledger/`. The node-type-specific schemas are spelled out in component 5 (Node Types). This file specifies only the substrate they all share.

There are several **edge types**, each with defined semantics, all of which can connect any pair of nodes regardless of node type:

- **Supersedes** — this node replaces a prior node. The prior remains in the graph (append-only) but is marked as no longer the active record on its topic.
- **Depends on** — this node's validity rests on a prior node remaining valid. If the prior is superseded, this node may need to be re-evaluated.
- **Contradicts** — this node was committed knowing it conflicts with a prior node, and the conflict was resolved through consensus in favor of the new node. The prior is not superseded — it remains valid in its original scope. The edge documents the known tension.
- **Extends** — this node refines or builds on a prior node without replacing it. Both remain active.
- **References** — this node was informed by a prior node but doesn't formally depend on it. The prior was considered as relevant context but not load-bearing.
- **Resolves** — this node resolves an open question or escalation that a prior node raised but didn't answer.
- **Distills** — used when a repo decision node is distilled from a slice of internal decision nodes. The repo node carries distills edges to the internal nodes that fed it. This is the audit trail from the public record back to the working notes.

New edge types can be added as the system matures, with each new type carrying defined semantics. Adding an edge type is a substrate change that requires updating the validation layer; it is not something stances do at runtime. Existing edge types cannot be redefined.

---

## The append-only enforcement

The ledger is append-only at multiple layers, with the lower layers acting as defense in depth against bugs in the higher layers.

**Layer 1: API enforcement.** The ledger API has no `Update`, `Delete`, or `Modify` operations. Callers can `AddNode`, `AddEdge`, `Query`, and `Resolve`, and that's the entire mutating surface. There is no programmatic way to express "change this node's content" or "remove this edge." A caller that wants to revise a prior decision must add a new node with a `supersedes` edge to the prior; the API does not offer any other path.

**Layer 2: Storage enforcement via git hook.** The canonical store is in the repo as git-tracked files under `.stoke/ledger/`. A pre-commit hook installed by Stoke at initialization rejects any commit that:

1. Modifies the content of an existing file under `.stoke/ledger/`
2. Deletes an existing file under `.stoke/ledger/`
3. Renames an existing file under `.stoke/ledger/`
4. Creates a new file under `.stoke/ledger/` whose schema doesn't validate against the node-type schema for its directory
5. Creates a new file whose declared edges point to target node IDs that don't exist in the ledger as of the parent commit

The hook is the failsafe. If the API has a bug that lets a caller construct an invalid mutation, the hook catches it before it reaches the store. If a stance bypasses the API entirely and writes to the files directly, the hook catches it. If a malicious or compromised process tries to scrub history, the hook catches it. The hook is enforced unless the user explicitly removes it (which the CLI surfaces as a warning, and which weakens the CTO's defense materially).

**Layer 3: Verifiability.** Because the canonical store is git, any consumer can verify the append-only property without trusting Stoke. `git log .stoke/ledger/` shows every entry that was ever written, in order. `git diff` between any two ledger commits shows what was added; the diff for legitimate operations only ever shows additions. A user auditing Stoke's history can confirm with standard git tooling that no historical entry was modified, regardless of what Stoke claims.

**Compaction does not modify history.** As long-running tasks accumulate many internal decision nodes, the ledger supports *summary nodes* that aggregate older nodes into a more compact representation. Summary nodes are themselves new nodes — they get their own IDs, their own commit history, and they carry edges to the nodes they summarize. The summarized nodes remain in place. A future query that asks "what are all the decisions in this scope" returns both the summary and the underlying nodes; the caller can choose whether to read the summary or walk the originals. Compaction is additive, never destructive.

---

## The API surface

The ledger exposes a small Go package at `internal/ledger`. The public surface is intentionally narrow.

```go
package ledger

// AddNode creates a new node in the ledger. Returns the assigned NodeID.
// Validates the node's schema against its declared NodeType. Validates that
// any edges declared on the node point to existing target nodes. Returns
// an error if validation fails.
//
// AddNode is the only way to introduce new content to the ledger. There is
// no Update, no Modify, no Delete.
AddNode(ctx context.Context, node Node) (NodeID, error)

// AddEdge attaches a new edge between two existing nodes. The edge carries
// its own metadata (author stance, reasoning, context). Both source and
// target node IDs must exist in the ledger.
//
// Edges, like nodes, are immutable once added. There is no RemoveEdge.
AddEdge(ctx context.Context, edge Edge) error

// Query runs a graph query against the ledger and returns the matching
// nodes (with their edges, optionally followed to a configurable depth).
//
// Queries are read-only and have no side effects on the ledger. They
// hit the index for performance but always reflect the canonical state
// of the git-tracked store.
Query(ctx context.Context, q Query) (QueryResult, error)

// Resolve takes a NodeID and returns the current effective state of that
// node — following supersedes edges to find the latest non-superseded node
// on the same topic, if any. Used by callers that want "the current
// decision on X" rather than "the historical decision on X."
Resolve(ctx context.Context, id NodeID) (Node, error)

// Walk traverses the graph starting from a NodeID, following edges of the
// specified types, returning all reachable nodes. Used for impact analysis
// ("what depends on this?") and for the concern field's projection queries.
Walk(ctx context.Context, start NodeID, edgeTypes []EdgeType, maxDepth int) ([]Node, error)
```

The Node and Edge types are structured records with required fields. Their schemas are defined per node type in component 5; this file specifies only that they exist and that they are validated at AddNode/AddEdge time.

Crucially, the API has **no operation that deletes, modifies, or rewrites** anything. The mutating surface is `AddNode` and `AddEdge`, and that's all. A caller wanting to "fix a mistake" in a prior node must use AddNode with a supersedes edge — there is no other path. The API forces append-only at compile time, not at runtime, by simply not exposing the operations that would violate it.

---

## IDs

Every node has a stable, immutable, content-addressed ID assigned at AddNode time. The ID is derived from a hash of the node's content plus its commit timestamp plus a small randomness component (to disambiguate identical nodes added in the same instant). Once assigned, the ID is permanent and travels with the node forever.

IDs are short, typed by node type, and human-readable in their prefix. Examples:

- `dec-r-3f8a2c` — repo decision node
- `dec-i-9b1d44` — internal decision node
- `task-feat-12-7e5a` — task DAG node, feature scope
- `task-tic-47-1c8b` — task DAG node, ticket scope
- `skill-2a3f` — skill node
- `snap-anno-f841` — snapshot annotation node

The prefix tells a reader what kind of node they're looking at without having to load it. The hash suffix guarantees uniqueness. The node type prefix and the hash are both stable and content-addressed; an ID can be cited in any context (a decision entry, a code comment, a chat message) and resolved later by anyone with access to the ledger.

IDs are the handles that decision log entries, task DAG nodes, skills, and snapshot annotations use to refer to each other. Because IDs are immutable, every reference is permanent. Because they're content-addressed, every reference is verifiable — a node whose content has somehow been corrupted will have a hash mismatch detectable by recomputing the ID from the content.

---

## Indexing

Querying the canonical git-backed store directly would be slow for non-trivial queries. The ledger maintains a SQLite index at `.stoke/ledger/.index.db` (gitignored, regenerated from the canonical store on demand) that mirrors the graph structure for query performance.

The index is **acceleration, not truth.** The git-backed store is the source of truth. The index is rebuilt from the git history if it's missing, corrupted, or out of date. The rebuild is deterministic — running it twice on the same git history produces the same index — so there is no scenario where the index "drifts" and produces a result the canonical store wouldn't.

Two consequences:

**The index can be deleted at any time.** Stoke will rebuild it on the next ledger operation. The user can remove `.stoke/ledger/.index.db` if they suspect corruption, and Stoke continues working with a brief delay for the rebuild.

**The canonical store can be inspected without the index.** A user reading the ledger with `git log`, `cat`, `jq`, or any other tool gets the same data Stoke gets. The audit trail is not locked inside the ledger component. The ledger component is the *primary* consumer of the canonical store, but it's not the only possible consumer.

The index is updated incrementally on each `AddNode` and `AddEdge` call, with the update happening after the git commit succeeds. If the git commit succeeds but the index update fails, the next operation triggers a partial rebuild from the most recent successful index commit forward. Index update failures are recoverable; canonical store failures are not, which is why the canonical store is git and the index is the disposable layer.

---

## Inheritance

The ledger is **always present** as context for any consensus loop or any stance operation. There is no "no ledger" state. The ledger may be empty, may contain only inherited entries from a prior Stoke run, may contain inherited entries from human-authored decision records (ADRs, design docs, architecture markdowns) that the wizard imported at initialization, or may be a mature ledger from many prior tasks. The substrate is the same in all cases.

**Inheritance from a prior Stoke run** is the simplest case: Stoke is invoked on a repo where `.stoke/ledger/` already exists from a previous run. The ledger loads its contents directly. Every prior node and edge is available immediately. The new task starts with the full graph as context.

**Inheritance from human-authored decision records** is the wizard's job at initialization. The wizard asks the user "does this repo already have a decision log we should respect?" and offers to import ADRs, design docs, or architecture markdowns into the ledger as initial repo decision nodes. The imported entries carry a `provenance: inherited-human` field on the node, and they have a special status:

- They are **read-only**, like all ledger entries — Stoke cannot edit them.
- They can be **superseded** by new Stoke-authored entries through the normal consensus loop, with a supersedes edge documenting the new decision and citing the inherited one.
- The CTO defends them with the same posture as snapshot code: a stance proposing a change that contradicts an inherited entry has to make the case, the same way a stance proposing a change to snapshot code has to.
- They may have **partial schemas** — a human-authored ADR doesn't have all the fields a Stoke-authored entry would have. The validation layer is tolerant of missing fields on inherited entries; the strict schema applies only to Stoke-authored new entries.

**Initialization with no inherited content** produces an empty ledger. The first AddNode call creates the first node. The substrate, the API, the hook, and the index are all in place from the moment of initialization — only the content is empty. Stoke can run on a brand-new project from commit zero with an empty ledger and accumulate content as it works.

The directionality rule between internal and repo decision nodes:

- **Internal nodes can cite repo nodes.** Internal entries carry edges to repo entries when the team's working notes refer to repo-level decisions.
- **Repo nodes cannot cite internal nodes.** Repo entries are the public record and have to be self-contained for next-developer context. They cite other repo nodes, snapshot annotations, and (rarely) skill nodes — they do not cite internal working notes that future readers won't have access to.

The validation layer enforces this directionality on AddEdge: an attempt to add an edge from a repo decision node to an internal decision node is rejected.

---

## Concurrency

Multiple stances can read the ledger simultaneously without coordination — reads are non-blocking and always reflect the most recent committed state.

Multiple stances can write to the ledger simultaneously, but writes are serialized through the underlying git operations. The ledger API wraps writes in a per-process lock; cross-process writes (multiple Stoke processes operating on the same repo) are serialized by git itself, with the second writer's commit being rejected if it conflicts with the first writer's. The rejected writer retries with a refreshed view of the ledger.

The append-only nature of the substrate means **conflicts are rare**. Two writers can never modify the same content because nothing is ever modified. The only conflicts are when two writers try to add edges to the same target node, or try to add a node whose ID hash collides with another node added in the same instant — both of which the retry mechanism handles trivially.

There is no need for transactions across multiple AddNode/AddEdge calls in normal operation — each call is its own atomic git commit. For consensus loops that need to commit a decision node and several edges atomically (e.g., a new decision that supersedes three prior decisions and depends on two others), the API provides a `Batch` operation that groups multiple AddNode and AddEdge calls into a single git commit. Batches are still append-only; they just commit their additions together.

---

## Schema evolution

When the ledger's schema needs to evolve (a new node type is added, a new required field on an existing node type, a new edge type), the substrate handles it through versioned schemas, similar to how databases handle migrations.

Each node carries a `schema_version` field indicating the schema it was written under. The ledger's read path knows how to read every historical schema version and project it into the current API surface. Old nodes written under earlier schemas are not migrated in place (which would violate append-only) — they remain in their original form and are read with their original schema, with default values supplied for fields that are required in the current schema but didn't exist when the node was written.

New writes always use the current schema version. A node can never be "downgraded" to an older schema. The ledger's version is tracked in `.stoke/ledger/.schema_version` (which is git-tracked but is one of the few files Stoke is allowed to modify, because it's a pointer to the current schema, not a historical record).

This means a Stoke version upgrade does not require a migration step on the ledger — old entries remain valid forever in their original form, and new entries follow the current rules. The cost is that the read path has to know about every historical schema version, which adds code complexity over time. The benefit is that the audit trail is never disturbed by schema changes, which is the property the substrate exists to guarantee.

---

## What the ledger does not do

A few things the ledger explicitly does not handle, with brief notes on where they live instead:

- **Source code.** Source code lives in the repo's normal git tracking, not in the ledger. The ledger holds *reasoning about* source code (decisions, snapshot annotations, skills), not source code itself. The boundary between "source code" and "reasoning about source code" is the boundary between the repo's normal files and the `.stoke/ledger/` directory.

- **Runtime events.** Hub events from the runtime are not ledger nodes. The hub has its own audit log (Phase 3 in the existing guide; will be revisited as a runtime component in the new guide). Some hub events may *result in* ledger entries (e.g., a CTO consultation hub event produces a decision node in the ledger), but the events themselves are runtime artifacts, not persistent reasoning.

- **Active stance state.** The current state of an active stance — its system prompt, its loaded context, its in-flight thinking — is runtime state held by the stance itself. It is not in the ledger. Only the *output* of stance work (decisions, plans, code, edges) is committed to the ledger.

- **Wizard configuration.** The wizard's output configures Stoke, but the configuration is a separate file (`.stoke/config.yaml` or similar), not a ledger entry. Configuration changes are normal git-tracked file edits with normal commit history; they're not append-only.

- **The skill library's index of "currently active skills."** The skill nodes are in the ledger, but the question "which skills should be loaded into a particular stance's prompt right now" is answered by a query at stance construction time, not by a stored "active skills list." The active set is dynamic, scoped by the task and the stance role, and computed fresh for each stance.

The ledger holds the *reasoning, the structure, and the history*. Everything else — code, runtime state, configuration, transient indexes — lives in its appropriate layer.

---

## Validation gate

Before any other component depends on the ledger, the ledger has to pass its own validation gate. The gate is:

1. ✅ `go vet ./...` clean, `go test ./internal/ledger/...` passes with >70% coverage
2. ✅ `go build ./cmd/stoke` succeeds
3. ✅ AddNode rejects nodes with missing required fields
4. ✅ AddNode rejects nodes whose declared edges target nonexistent nodes
5. ✅ AddEdge rejects edges between nodes when the directionality rule is violated (e.g., repo→internal)
6. ✅ The ledger has no `Update`, `Modify`, or `Delete` operations on its public API surface (verified by grep on the package)
7. ✅ The git hook installs successfully and rejects a test commit that modifies an existing entry file
8. ✅ The git hook rejects a test commit that creates an entry file with a missing required field
9. ✅ The git hook rejects a test commit that creates an entry file with an edge to a nonexistent target
10. ✅ The SQLite index can be deleted and rebuilt deterministically from the git history
11. ✅ Two AddNode calls with content that hash-collides produce two different IDs (the disambiguation component works)
12. ✅ A Batch commit containing one node and three edges produces exactly one git commit
13. ✅ Concurrent AddNode calls from separate processes serialize correctly through git's normal locking
14. ✅ Inherited human-authored entries with partial schemas can be queried, walked, and resolved without errors
15. ✅ Inherited entries cannot be modified or deleted by AddNode (because there is no Modify or Delete in the API)
16. ✅ The schema_version field is present on every node and the read path handles at least two different versions
17. ✅ The validation gate file is committed to `STOKE-IMPL-NOTES.md`

The ledger gate is more elaborate than most component gates because the ledger is more load-bearing than most components. Other components depend on the ledger's contracts being correct; if the ledger's contracts are wrong, every component built on top of it inherits the bug.

---

## Forward references

This file is component 2 of the new guide. It refers to several things specified in later components:

- **Node-type schemas** are specified in component 5. This file specifies that schemas exist and that the substrate validates them, but not what fields each node type requires. The node-type file will fill that in.
- **Query templates** for the concern field are specified in component 4. This file specifies the `Query` API but not the specific query patterns the concern field uses.
- **The consensus loop's terminal state** as a ledger commit is specified in component 3. This file specifies that AddNode + Batch are the operations the loop uses, but not the loop's state machine.
- **The wizard's import flow** for inherited human-authored decision records is specified in the wizard component. This file specifies that inherited entries exist and how they're flagged, but not how they get into the ledger in the first place.
- **The CTO's snapshot defense** uses `Walk` and `Query` against snapshot annotation nodes. This file specifies the API but not the CTO's specific access patterns.

The next file to write is `03-the-consensus-loop.md`.
