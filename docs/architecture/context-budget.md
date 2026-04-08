# Context Budget: L0-L3 Framing

Package: `internal/context/`

## Four Context Layers

Following the MemPalace articulation, Stoke's context budget is organized into
four logical layers mapped to three Go tiers:

| Layer | Name | Tokens | Loading | Go Tier |
|-------|------|--------|---------|---------|
| L0 | Identity | ~50 | Always loaded | TierActive |
| L1 | Critical Facts | ~120 | Always loaded | TierActive |
| L2 | Topical Recall | Variable | On-demand | TierSession |
| L3 | Deep Semantic | Variable | On-demand | TierProject |

### L0 — Identity (~50 tokens)

System identity, role constraints, and operating mode. Always present in every
API call regardless of compaction level.

Examples: "You are a Stoke developer stance", "Do not modify protected files",
"Follow Go best practices".

### L1 — Critical Facts (~120 tokens)

Active task description, key blockers, retry context. Always present.

Examples: "Current task: TASK-3 Add JWT auth", "Previous attempt failed:
missing import", "Budget remaining: $0.35".

### L2 — Topical Recall (on-demand)

Relevant file content, recent tool outputs, plan state. Promoted from disk
when the task context requires it. Subject to gentle compaction (truncation)
under pressure.

Examples: Recent file reads, test output, lint findings.

### L3 — Deep Semantic (on-demand)

Full semantic search results, historical context, CLAUDE.md, project map,
learned patterns from the wisdom store. Loaded via semantic search when the
agent needs historical context. Subject to aggressive compaction (dropping)
under pressure.

Examples: Wisdom store learnings, research findings, repo map.

## Compaction Strategy

Progressive compaction as utilization rises:

| Threshold | Level | Action |
|-----------|-------|--------|
| < 50% | None | All context preserved |
| 50-65% | Gentle | Truncate L2 tool outputs to summaries |
| 65-80% | Moderate | Compress L2 file reads to "read X, found Y" |
| > 80% | Aggressive | Drop L3 blocks entirely, keep only L0+L1 |

## Event-Driven Reminders

The context manager fires reminders based on state:

| Trigger | Condition | Reminder |
|---------|-----------|---------|
| FileWriteToTest | Agent writing test file | "Run tests after writing" |
| ContextAbove60Pct | Utilization > 60% | "Focus on the task at hand" |
| ErrorRepeated3x | Same error 3 times | "Consider a different approach" |
| TaskRunning20Min | Task > 20 min wall time | "Wrap up or request extension" |
| PolicyViolationSeen | Policy violation detected | "Review policy constraints" |
| ScopeViolationSeen | Scope violation detected | "Only modify declared files" |

## Integration

- **MCP Memory Surface**: `stoke_wisdom_as_of` queries L3-level historical context
- **Wisdom Store**: Temporal validity ensures only currently-valid learnings are loaded
- **Research Store**: FTS5 + semantic search provides L3 deep recall
- **Concern Field**: Role-specific L1 context built from ledger + bus state
