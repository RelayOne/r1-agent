# STOKE Implementation Notes

Running log of decisions, blockers, and questions for Eric.

---

## 2026-04-07 — v2 Architecture Implementation (Guide v2 "The Real Mission")

### Phase 0: Scaffolding
- `internal/contentid/` — Content-addressed ID generation (SHA256, 16 prefixes)
- `internal/stokerr/` — Structured error types with 10 error codes
- All tests passing

### Phase 1: Substrate Components
- `internal/ledger/` — Append-only content-addressed graph (AddNode, AddEdge, Query, Resolve, Walk, Batch), git-backed filesystem store, SQLite index
- `internal/bus/` — Durable WAL-backed event bus (Publish, Subscribe, RegisterHook, Replay, PublishDelayed), monotonic sequence numbers, causality references
- `internal/ledger/nodes/` — 22 node type structs (decision, task, draft, loop, research, skill, snapshot, escalation, agree/dissent)
- All tests passing

### Phase 2: Supervisor Rules Engine
- `internal/supervisor/core.go` — Deterministic event loop: subscribe → match → evaluate → fire
- `internal/supervisor/rule.go` — Rule interface with Pattern, Evaluate, Action
- 30 rules across 10 categories:
  - Trust (3): completion/fix/problem require second opinion
  - Consensus (5): draft review, dissent address, convergence detection, iteration threshold, partner timeout
  - Snapshot (2): modification/formatter require CTO
  - Cross-team (1): cross-branch modification requires CTO consensus
  - Hierarchy (3): parent agreement, escalation forwarding, user escalation (interactive + full-auto)
  - Drift (3): judge scheduling, intent alignment, budget threshold
  - Research (3): request dispatch, report unblock, timeout
  - Skill (5): extraction trigger, load audit, application review, contradicts outcome, import consensus
  - SDM (5): file collision, dependency crossing, duplicate work, schedule risk, cross-branch drift
- Three manifests: mission (25 rules), branch (20 rules), SDM (5 rules)
- All tests passing across 11 packages

### Phase 3: Consensus Loop + Concern Field
- `internal/ledger/loops/` — Loop state tracker (7 states, lifecycle queries, convergence checking)
- `internal/concern/` — Concern field builder with 10 section types, 9 role templates, XML rendering
- All tests passing

### Phase 4: Harness + Stances
- `internal/harness/` — Stance lifecycle (spawn, pause, resume, terminate, inspect, recover)
- `internal/harness/stances/` — 11 stance templates (PO, Lead Engineer, Lead Designer, VP Eng, CTO, SDM, QA Lead, Dev, Reviewer, Judge, Stakeholder)
- `internal/harness/tools/` — 12 tool types with per-role authorization matrix
- `internal/harness/models/` — Provider interface + mock implementation
- All tests passing

### Phase 5: Snapshot + Wizard
- `internal/snapshot/` — Manifest with file paths + content hashes, Take/Load/Save/InSnapshot/Promote
- `internal/wizard/` — Init flow, config types, presets (minimal/balanced/strict), SetField, YAML roundtrip
- All tests passing

### Phase 6: Skill Manufacturer
- `internal/skillmfr/` — 4 workflows (shipped import, mission extraction, external import, lifecycle management)
- Confidence levels (candidate → tentative → proven), provenance tracking
- All tests passing

### Phase 7: Bench
- `internal/bench/` — Golden mission set, runner, metrics (trust + consensus), comparison, reports
- Sample golden mission: hello-world greenfield
- All tests passing

### Phase 8: End-to-end Validation
- `go vet ./...` — clean across entire codebase
- `go build ./cmd/stoke` — produces binary
- `go test` — 30 v2 packages pass (22 with tests, 8 exercised through parents)
- All v2 packages compile and interoperate correctly

### Phase 9: Integration + Cleanup
- **Removed 13 deprecated packages** (~3,636 LOC dead code):
  compute, lifecycle, managed, permissions, phaserole, prompttpl, ralph,
  ratelimit, sandattr, sandbox, sandguard, team, toolcache
  All had zero external imports — safe removal confirmed.
- **Created `internal/bridge/`** — V1→V2 bridge adapters:
  - `CostBridge`: wraps costtrack.Tracker, emits cost.recorded + cost.budget.alert bus events, writes cost_record ledger nodes
  - `VerifyBridge`: wraps verify.Pipeline, emits verify.started/completed bus events, writes verification ledger nodes
  - `WisdomBridge`: wraps wisdom.Store, emits wisdom.learning.recorded bus events, writes wisdom_learning ledger nodes
  - `AuditBridge`: wraps audit.AuditReport, emits audit.completed bus events, writes audit_report ledger nodes with references edges
  - 13 new bus event types defined (cost, verify, wisdom, workflow, audit, hook, skill, profile)
  - 5 tests covering all bridges
- **Docs cleanup**:
  - Updated CLAUDE.md with v2 package map section
  - Updated README.md with v2 governance architecture section
  - Updated PACKAGE-AUDIT.md to remove deprecated packages
  - Updated ROADMAP.md to reflect v2 current state
  - Removed 4 legacy forge-* docs
- **CI gate**: 121 packages pass `go build`, `go vet`, `go test`
