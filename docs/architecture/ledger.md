# Ledger: Content-Addressed Reasoning Graph

Package: `internal/ledger/`

## Design

The ledger is an append-only, content-addressed graph. Nodes are immutable
once written; updates create new nodes linked via supersedes edges.

## Node Structure

```go
type Node struct {
    ID            string    // SHA256 content-addressed
    Type          string    // from internal/ledger/nodes/
    SchemaVersion int
    CreatedAt     time.Time
    CreatedBy     string    // emitter ID
    MissionID     string
    Content       json.RawMessage
}
```

## Node Types (`internal/ledger/nodes/`)

13+ registered node types:

| Type | Purpose |
|------|---------|
| `task` | DAG nodes with granularity, state, acceptance criteria |
| `draft` | Candidate artifacts (PRD, SOW, PR, refactor, fix, verdict) |
| `decision_internal` | Structured internal decisions |
| `decision_repo` | Repository-affecting decisions |
| `loop` | Consensus loop tracking |
| `escalation` | Escalated issues |
| `research_request` | Research task requests |
| `research_report` | Research findings |
| `skill` | Extracted reusable patterns |
| `snapshot_annotation` | System state snapshots |
| `execution_environment` | Environment specifications |
| `supervisor.checkpoint` | Supervisor state records |

## Edge Types

```go
const (
    EdgeSupersedes  // newer version of a node
    EdgeDependsOn   // prerequisite relationship
    EdgeContradicts // conflicting information
    EdgeExtends     // adds to without replacing
    EdgeReferences  // non-structural reference
    EdgeResolves    // resolves an escalation
    EdgeDistills    // summarizes/compresses
)
```

Edge creation enforces a directionality and node-type compatibility matrix.

## Storage

- **Filesystem**: Each node stored as a JSON file at `{rootDir}/{id}.json`
- **SQLite index**: Queryable index with WAL mode for concurrent reads

## Query API

```go
ledger.Get(id)                    // retrieve by content-addressed ID
ledger.Query(QueryFilter{...})    // search by type, mission, creator, time range
ledger.Resolve(id)                // follow supersedes chain to current version
ledger.Walk(id, direction, types) // graph traversal
ledger.Batch([]BatchOp)           // atomic multi-operation writes
ledger.Verify(ctx)                // integrity check
ledger.RebuildIndex()             // rebuild SQLite from filesystem
```

## Connections

- **Bus**: Emits `ledger.node.added` and `ledger.edge.added` events
- **Supervisor**: Queries during rule evaluation, writes checkpoints
- **Bridge**: Persists cost, audit, and wisdom records from v1
- **Failure**: Traces decision history for recovery analysis
