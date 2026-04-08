# V2 Governance Overview

Stoke v2 adds a multi-role consensus layer on top of the v1 execution engine.

## Substrate Layer

### Ledger (`internal/ledger/`)

Append-only content-addressed graph. Nodes are immutable; updates create new
nodes linked via `EdgeSupersedes` edges.

- 13+ node types: task, draft, decision, loop, escalation, research, skill, snapshot, environment, checkpoint
- 7 edge types: supersedes, depends_on, contradicts, extends, references, resolves, distills
- Storage: filesystem (nodes as JSON files) + SQLite index for queries
- Content-addressed IDs: SHA256 of node content

### Bus (`internal/bus/`)

Durable WAL-backed event bus with three participant types:

- **Publishers**: Any component emits events
- **Hooks**: Privileged handlers (authority="supervisor"), fire synchronously before subscribers
- **Subscribers**: Passive observers with buffered async delivery

30+ event types across worker, ledger, supervisor, skill, mission, and bus namespaces.
Prefix indexing for O(1) lookup by event type.

## Supervisor (`internal/supervisor/`)

Deterministic rules engine subscribing to bus events. Three supervisor types:
mission (full lifecycle), branch (single branch), SDM (stance-detection).

### Rule Execution Flow

1. Pattern match (cheap, no I/O)
2. Evaluate condition (may query ledger)
3. Execute action (publish events on bus)
4. Record fire stats + rationale

### 30 Rules Across 10 Categories

| Category | Count | Examples |
|----------|-------|---------|
| Trust | 3 | Completion/fix/problem requires second opinion |
| Consensus | 5 | Draft requires review, dissent requires address, convergence detection |
| Snapshot | 2 | Modification requires CTO, formatter requires consent |
| Cross-team | 1 | Modification requires CTO approval |
| Hierarchy | 2 | Completion requires parent agreement, user escalation |
| Drift | 3 | Judge scheduled, intent alignment, budget threshold |
| Research | 3 | Request dispatches researchers, report unblocks, timeout |
| Skill | 5 | Extraction trigger, load audit, application review |
| SDM | 5 | Collision, dependency, drift, duplicate, schedule risk |

Rules are loaded from manifests (`internal/supervisor/manifests/`) with
wizard-configurable overrides per rule.

## Consensus Loops (`internal/ledger/loops/`)

7-state machine for structured agreement:

```
Proposed → Reviewing → Revised → Approved → Committed
                    ↓
                  Rejected → Escalated
```

Each loop tracks participants, votes, and convergence criteria.

## Harness & Stances (`internal/harness/`)

Stance lifecycle management with 11 roles:

| Role | Posture | Purpose |
|------|---------|---------|
| po | absolute_completion_and_quality | Product ownership |
| cto | absolute_completion_and_quality | Technical direction |
| lead_engineer | balanced | Engineering leadership |
| dev | pragmatic | Implementation |
| reviewer | absolute_completion_and_quality | Code review |
| qa_lead | absolute_completion_and_quality | Quality assurance |
| judge | absolute_completion_and_quality | Verdict rendering |
| sdm | balanced | Stance detection management |
| vp_eng | balanced | Engineering strategy |
| lead_designer | balanced | Design leadership |
| stakeholder | balanced | External stakeholder |

Each stance has:
- Role-specific system prompt
- Per-role tool authorization
- Concern field context (10 sections, 9 templates)
- Model selection (override -> role -> default)
- Cooperative pause/resume via signal channels
