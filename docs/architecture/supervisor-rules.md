# Supervisor Rules

Package: `internal/supervisor/` and `internal/supervisor/rules/`

## Rule Interface

```go
type Rule interface {
    Name() string
    Pattern() Pattern           // cheap match against event type/scope
    Priority() int              // lower = higher priority
    Evaluate(ctx, event, ledger) bool  // condition check (may query ledger)
    Action(ctx, event, bus) error      // side effect (publish events)
    Rationale() string          // human-readable explanation
}
```

## Execution Flow

1. Event arrives on bus
2. Pattern match (no I/O)
3. Evaluate condition (may query ledger for context)
4. If true: execute action (publish events, modify state)
5. Record fire stats and publish `supervisor.rule.fired`

## Rules by Category

### Trust (3 rules) — `internal/supervisor/rules/trust/`

| Rule | Trigger | Action |
|------|---------|--------|
| CompletionRequiresSecondOpinion | Task marked complete | Request review from different stance |
| FixRequiresSecondOpinion | Fix applied | Request verification from reviewer |
| ProblemRequiresSecondOpinion | Problem identified | Request confirmation from second analyst |

### Consensus (5 rules) — `internal/supervisor/rules/consensus/`

| Rule | Trigger | Action |
|------|---------|--------|
| DraftRequiresReview | Draft node created | Route to reviewer stance |
| DissentRequiresAddress | Dissent recorded | Block progress until addressed |
| ConvergenceDetected | All participants agree | Advance loop to approved |
| IterationThreshold | Loop iterations exceed limit | Escalate to higher authority |
| PartnerTimeout | Partner unresponsive | Escalate or reassign |

### Snapshot (2 rules) — `internal/supervisor/rules/snapshot/`

| Rule | Trigger | Action |
|------|---------|--------|
| ModificationRequiresCTO | Snapshot change proposed | Require CTO approval |
| FormatterRequiresConsent | Formatter changes detected | Require explicit consent |

### Hierarchy (2 rules) — `internal/supervisor/rules/hierarchy/`

| Rule | Trigger | Action |
|------|---------|--------|
| CompletionRequiresParentAgreement | Child task complete | Verify parent agrees |
| UserEscalation | Unresolvable conflict | Escalate to human |

### Drift (3 rules) — `internal/supervisor/rules/drift/`

| Rule | Trigger | Action |
|------|---------|--------|
| JudgeScheduled | Periodic check | Schedule drift judge evaluation |
| IntentAlignmentCheck | Significant progress | Verify work aligns with intent |
| BudgetThreshold | Cost milestone | Alert on budget consumption |

### Research (3 rules) — `internal/supervisor/rules/research/`

| Rule | Trigger | Action |
|------|---------|--------|
| RequestDispatchesResearchers | Research request | Spawn researcher stances |
| ReportUnblocksRequester | Research complete | Notify blocked stance |
| Timeout | Research overdue | Escalate or cancel |

### Skill (5 rules) — `internal/supervisor/rules/skill/`

| Rule | Trigger | Action |
|------|---------|--------|
| ExtractionTrigger | Pattern detected | Begin skill extraction |
| LoadAudit | Skill loaded | Verify skill applicability |
| ApplicationRequiresReview | Skill applied | Review application correctness |
| ContradictsOutcome | Skill contradicts result | Flag for investigation |
| ImportConsensus | Skill imported | Require consensus on adoption |

### SDM (5 rules) — `internal/supervisor/rules/sdm/`

| Rule | Trigger | Action |
|------|---------|--------|
| CollisionFileModification | Concurrent file edits | Block conflicting stance |
| DependencyCrossed | Cross-dependency detected | Coordinate ordering |
| DriftCrossBranch | Branch divergence | Alert on drift |
| DuplicateWorkDetected | Redundant effort | Consolidate or cancel |
| ScheduleRiskCriticalPath | Critical path delay | Reprioritize |

### Cross-Team (1 rule) — `internal/supervisor/rules/cross_team/`

| Rule | Trigger | Action |
|------|---------|--------|
| ModificationRequiresCTO | Cross-team change | Require CTO sign-off |

## Manifests

Rules are grouped into manifests loaded per supervisor type:

- **MissionRules()**: 17 rules for full mission lifecycle
- **BranchRules()**: Subset for single-branch supervision
- **SDMRules()**: SDM-specific detection rules

## Configuration

Rules can be enabled/disabled and parameterized via the wizard:

```go
type RuleConfig struct {
    Enabled    bool
    Parameters map[string]interface{}
}
```

Each rule declares its configurable fields via `ConfigField` with type, default, and description.
