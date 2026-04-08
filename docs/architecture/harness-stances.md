# Harness & Stances

Package: `internal/harness/` and `internal/harness/stances/`

## Stance Lifecycle

```
SpawnStance() → Running → PauseStance() → Paused → ResumeStance() → Running
                       → TerminateStance() → Terminated
```

Pause/resume uses cooperative signaling via Go channels. Stances call
`CheckpointCheck()` at safe points; the harness uses this to coordinate
pause/resume without data corruption.

## 11 Stance Templates

| Role | Display Name | Posture | Default Model |
|------|-------------|---------|---------------|
| po | Product Owner | absolute | claude-opus-4-6 |
| cto | CTO | absolute | claude-opus-4-6 |
| lead_engineer | Lead Engineer | balanced | claude-opus-4-6 |
| dev | Developer | pragmatic | claude-sonnet-4-6 |
| reviewer | Reviewer | absolute | claude-opus-4-6 |
| qa_lead | QA Lead | absolute | claude-opus-4-6 |
| judge | Judge | absolute | claude-opus-4-6 |
| sdm | SDM | balanced | claude-sonnet-4-6 |
| vp_eng | VP Engineering | balanced | claude-opus-4-6 |
| lead_designer | Lead Designer | balanced | claude-sonnet-4-6 |
| stakeholder | Stakeholder | balanced | claude-sonnet-4-6 |

### Consensus Postures

- **absolute_completion_and_quality**: Will not approve until all criteria met
- **balanced**: Weighs completeness against pragmatic constraints
- **pragmatic**: Favors shipping over perfection

## Tool Authorization (`internal/harness/tools/`)

12 tool categories authorized per role:

| Tool | po | cto | dev | reviewer | qa_lead |
|------|----|----|-----|----------|---------|
| Read | Y | Y | Y | Y | Y |
| Write | N | N | Y | N | N |
| Edit | N | N | Y | N | N |
| Bash | N | Y | Y | N | Y |
| Glob/Grep | Y | Y | Y | Y | Y |
| EnvExec | N | Y | Y | N | Y |
| LedgerQuery | Y | Y | Y | Y | Y |
| BusPublish | N | Y | N | N | N |

## Spawn Flow

1. Validate role against registered templates
2. Build concern field context (10 sections, 9 role templates)
3. Construct system prompt with authorized tools + concern field
4. Select model: override -> role default -> harness default
5. Create StanceSession with unique ID (`stance-{role}-{seq}`)
6. Publish `worker.spawned` event on bus
7. Return StanceHandle

## Concern Field Integration

Each stance receives a context-projected view of the mission state via
`internal/concern/`. The concern builder queries the ledger and constructs
role-specific sections:

- Mission context
- Task DAG state
- Recent decisions
- Open escalations
- Research status
- Skill inventory
- Snapshot state
- Budget/cost status
- Team composition
- Relevant history

## Recovery

`Recover()` rebuilds harness state from bus event replay, restoring all
active stances and their current status without restarting the mission.
