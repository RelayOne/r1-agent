# CloudSwarm ↔ Stoke Integration Map

**Document Type:** Ground-truth integration contract research
**Generated:** 2026-04-20
**Source:** Direct code analysis of CloudSwarm platform at `/home/eric/repos/CloudSwarm`
**Scope:** Stoke CLI subprocess execution, event handling, HITL, delegation, policy, concurrency

---

## 1. SUBPROCESS EXECUTION

### Command Construction

CloudSwarm constructs and runs the Stoke binary as a subprocess in `platform/temporal/activities/execute_stoke.py:288-319`.

```python
cmd = ["stoke", "run", "--output", "stream-json"]
if repo_url:
    cmd.extend(["--repo", repo_url])
if branch:
    cmd.extend(["--branch", branch])
if config.get("model"):
    cmd.extend(["--model", str(config["model"])])
cmd.append(task_spec)
```

**Contract:**
- Binary: `stoke` (must be on `$PATH`)
- Subcommand: `run` (currently MISSING in Stoke)
- Flags (mandatory): `--output stream-json`
- Flags (conditional): `--repo`, `--branch`, `--model`
- Positional arg: `task_spec`

### Process Lifecycle
- **Stdout:** Piped async iterator, drained line-by-line as NDJSON
- **Stderr:** Captured + logged
- **Exit Code:** Returned to workflow
- **Stdin:** Used only for HITL approval injection (base64-encoded JSON written by supervisor; Stoke reads plain JSON)
- **Environment:** Copy of `os.environ`

### Supervisor Sidecar (SIGSTOP/SIGCONT)

CloudSwarm runs an optional supervisor sidecar at `platform/temporal/stoke_session_supervisor/main.py` listening on Unix socket `/var/run/stoke-supervisor.sock`.

**RPC Protocol:** Newline-delimited JSON over Unix socket.
**Verbs:**
- `pause` → sends `SIGSTOP` to process group
- `resume` → sends `SIGCONT` to process group
- `inject_stdin` → writes base64-decoded data to subprocess stdin
- `start` → spawns a new supervised subprocess
- `read_next_event` → reads next event from stdout queue
- `terminate` → `SIGTERM` then `SIGKILL` if needed

**Response format:** `{"ok": true, ...}` or `{"ok": false, "error": str}`

---

## 2. EVENT SCHEMA

### Database Schema

**Table: `stoke_events`** (`platform/migrations/040_stoke_sessions.sql:33-39`)

```sql
CREATE TABLE stoke_events (
    id           BIGSERIAL PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES stoke_sessions(session_id) ON DELETE CASCADE,
    event_type   TEXT NOT NULL,
    timestamp    TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL DEFAULT '{}'::jsonb
);
```

**Table: `stoke_sessions`**

```sql
CREATE TABLE stoke_sessions (
    session_id        TEXT PRIMARY KEY,
    account_id        UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    repo_url          TEXT NOT NULL,
    branch            TEXT,
    task_spec         TEXT NOT NULL,
    governance_tier   TEXT NOT NULL CHECK (governance_tier IN ('community', 'enterprise')),
    config            JSONB NOT NULL DEFAULT '{}'::jsonb,
    status            TEXT NOT NULL DEFAULT 'queued'
                          CHECK (status IN ('queued','planning','executing','verifying','committing','paused','completed','failed')),
    current_phase     TEXT,
    turns_completed   INT NOT NULL DEFAULT 0,
    turns_remaining   INT,
    runner            TEXT NOT NULL DEFAULT 'unknown'
                          CHECK (runner IN ('stoke_binary','agent_fallback','unknown')),
    workflow_id       TEXT,
    exit_code         INT,
    error             TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ
);
```

### Event Types Parsed

CloudSwarm parses each stdout NDJSON line. It reads `type` OR `event_type` (either works). Unknown types default to `stoke.event`.

**CloudSwarm-emitted (NOT from Stoke):**
- `session.submitted` (routes/stoke_sessions.py:262-267)
- `session.runner_started` (execute_stoke.py:307-312)
- `session.runner_completed` (execute_stoke.py:400-405)
- `session.runner_failed` (execute_stoke.py:453-458)
- `session.fallback_delegated` (execute_stoke.py:612-621)

**HITL-critical (Stoke MUST emit):**
- `hitl_required` (execute_stoke.py:343-387)

**Pass-through (Stoke may emit any type; stored verbatim):**
- Typical: `plan`, `act`, `thought`, `result` (per test fixtures at test_execute_stoke.py:149-154)

### NATS Publication

All events (except system-only) published to: `stoke.agent.{session_id}.events`

**Payload format:**
```json
{ "event_type": "plan", "data": { ... } }
```

---

## 3. HITL FLOW

### Trigger: Stoke Emits `hitl_required`

When Stoke emits a line with `"type": "hitl_required"` or `"event_type": "hitl_required"`, CloudSwarm (`execute_stoke.py:343-387`):

1. Pauses subprocess via supervisor `pause` (SIGSTOP)
2. Creates approval record in `approvals` table
3. Publishes `hitl_required` event to NATS
4. Returns from activity with `state: "hitl_pending"`

### Expected `hitl_required` Event Schema (from Stoke)

**Minimum:** `{ "type": "hitl_required" }`

**With context:**
```json
{
  "type": "hitl_required",
  "reason": "User approval needed for file creation",
  "file": "src/new_feature.py"
}
```

Fields stored in `approvals.context`:
- `source: "stoke"` (set by CloudSwarm)
- `session_id: str` (set by CloudSwarm)
- `reason: str` (from Stoke payload)
- `file: str` (from Stoke payload)
- `subprocess_handle: str` (set by CloudSwarm)

### Approval Decision Consumer

**File:** `platform/apps/api/src/events/stoke_approval_consumer.py:25-107`

- NATS subject: `cs.approval.>`
- Filter: `context.source == "stoke"`
- Event types: `approval.decided` or `approval.expired`
- Signal to workflow: `hitl_response` with `{approval_id, decision, reason}`

### Resume: Injecting Decision into Stdin

**Activity:** `stoke_supervisor_resume` (`execute_stoke.py:532-566`)

**Decision payload written to Stoke's stdin** (plain JSON — the supervisor decodes base64):
```json
{
  "decision": true,
  "reason": "Approved by user",
  "decided_by": "user@example.com"
}\n
```

### HITL Timeout

**Duration:** 15 minutes (`workflows/stoke_agent.py:23`)
**On timeout:** Auto-rejected with reason `"HITL timeout — auto-rejected after 15 minutes"`

---

## 4. POLICY CHECK INTEGRATION

### CloudSwarm Policy Engine
**Activity:** `policy_check` (`platform/temporal/activities/policy_check.py:404-530`)
**Cedar integration:** Real Cedar at `platform/policies/trustplane_policy.py` → `platform/policies/cedar/engine.py`

### Stoke Does NOT Call Policy Check

The Stoke subprocess **does not** invoke CloudSwarm's policy engine. Policies are evaluated by CloudSwarm when **skills** are executed during the agent loop. Stoke is treated as a single opaque activity with no intra-task policy gates.

**Implication for spec-2 (cloudswarm-protocol):** No policy hook is strictly needed for CloudSwarm integration. A policy hook is a STOKE-STANDALONE feature (local YAML policy) that could be added later.

### Trust Levels (for Delegation Context)
```python
_TRUST_LEVEL_ORDER: dict[TrustLevel, int] = {
    TrustLevel.L0: 0, TrustLevel.L1: 1, TrustLevel.L2: 2, TrustLevel.L3: 3, TrustLevel.L4: 4,
}
```

### Budget Checks (Redis counters, V-111)
- Account daily: `budget:account_cents:{account_id}:daily:{YYYY-MM-DD}`
- Agent daily: `budget:agent_cents:{agent_id}:daily:{YYYY-MM-DD}`
- Org daily: `budget:org_cents:{org_id}:daily:{YYYY-MM-DD}` (V-112)
- Integer cents. Stoke not involved.

---

## 5. CONCURRENCY / PLAN LIMITS

### Enforcement Point
API submission: `platform/apps/api/src/routes/stoke_sessions.py:170-207`

### Per-Account Session Limit (Redis Set)
- Key: `stoke:concurrent:{account_id}`
- Structure: Redis set
- TTL per plan: builder=4h, pro=8h
- Enforcement: `scard(key) >= limit` → 429

### Plan Limits (`stoke_plan_limits.py`)
```python
PLAN_LIMITS = {
    "builder": PlanLimits(max_concurrent_sessions=3, max_session_duration_hours=4),
    "pro":     PlanLimits(max_concurrent_sessions=10, max_session_duration_hours=8),
    "none":    PlanLimits(max_concurrent_sessions=0, max_session_duration_hours=0),
}
```

### Slot Release
Activity `stoke_release_concurrency_slot` (`execute_stoke.py:477-508`) called in workflow finally block.

**No Stoke env var:** Limits enforced entirely upstream; Stoke has no knowledge of concurrency limits. A `STOKE_MAX_WORKERS` env var (per work.md) is NOT what CloudSwarm passes today.

---

## 6. TRUST-CLAMPED DELEGATION

### DelegationContext Model (`platform/temporal/models.py:89-110`)

```python
class DelegationContext(BaseModel):
    delegator_agent_id: str
    delegator_trust_level: int
    effective_trust_level: int  # min(delegator, executor) — the actual ceiling
    delegation_depth: int = 0
    max_depth: int = 3  # MAX_DELEGATION_DEPTH from constants.py
    delegation_token: str = ""  # HMAC-signed
    parent_task_id: str = ""
```

### Trust Ceiling Computation (`delegation.py:128-155`)
`effective_trust = min(requester_trust, provider_trust)`

### Delegation Token Activity
`generate_delegation_token` activity signs with HMAC (parent_agent_id, child_agent_id, effective_trust_level). Currently **V-114 DISABLED**: `_resolve_delegation_claims` always returns `(False, agent_trust)` until a signed-token verification mechanism lands.

### Budget Pre-Reservation (V-69)
`reserve_delegation_budget` activity runs BEFORE child executes. `reconcile_delegation_budget` refunds unused post-execution.

### Confused Deputy Protection (V-114) — DISABLED
```python
# policy_check.py:379-402
@staticmethod
def _resolve_delegation_claims(_input, agent_trust) -> tuple[bool, int]:
    # V-114: today there is NO signed-token verification mechanism...
    return False, agent_trust
```

---

## 7. MEMORY INTEGRATION

### Schema (`db/memory.py`, table `episodic_memory`)
```
agent_id: UUID
task_id: UUID (nullable)
content: TEXT
metadata: JSONB
embedding: vector (pgvector, nullable)
created_at: TIMESTAMPTZ
```

### API
- `store(agent_id, task_id, content, metadata, embedding, memory_type)`
- `search_semantic(agent_id, query_embedding, limit, memory_type)` — pgvector cosine
- `get_recent(agent_id, limit, memory_type)`

### Stoke Integration Status: **NOT INTEGRATED**

Memory is used by `RetrieveMemoriesActivity` / `StoreMemoryActivity` called from `AgentLoopWorkflow`, NOT from `StokeAgentWorkflow`. Stoke subprocess has no memory API integration.

**Implication:** Stoke's standalone memory is a separate concern from CloudSwarm memory. No API integration required for CloudSwarm compatibility.

---

## 8. CURRENT STOKE ENTRY POINT — GAP ANALYSIS

### What CloudSwarm Expects

```bash
stoke run --output stream-json [--repo URL] [--branch BRANCH] [--model MODEL] TASK_SPEC
```

Plus:
1. NDJSON on stdout (each line = one JSON event)
2. Each line carries `type` or `event_type`
3. Special handling for `type: "hitl_required"`
4. Exit code returned to workflow

### Stoke Currently HAS
- `stoke build`, `stoke ship`, other commands
- `simple_loop.go` invokes Claude CLI (not Stoke) with `--output-format stream-json`
- No `stoke run` subcommand
- No `--output`/`--output-format` flag for any Stoke command

### Gap Table

| Requirement | Status | Evidence |
|---|---|---|
| Binary at `$PATH` | ✅ Expected | `execute_stoke.py:168` `shutil.which("stoke")` |
| `run` subcommand | ❌ MISSING | not in `cmd/r1/` |
| `--output stream-json` | ❌ MISSING | no flag in main.go |
| `--repo` flag | ❌ MISSING | not implemented |
| `--branch` flag | ❌ MISSING | not implemented |
| `--model` flag | ⚠️ Partial | in build/ship not run |
| NDJSON stdout events | ❌ MISSING | no emitter |
| `hitl_required` event | ❌ MISSING | no HITL checkpoint |
| Stdin decision reader | ❌ MISSING | no stdin handler |
| SIGSTOP-survivable buffering | ⚠️ Unknown | needs analysis |

---

## CRITICAL CONTRADICTIONS vs. work.md

1. **`hitl_required` is the ONLY event CloudSwarm parses specially.** work.md invents an extensive `stoke.descent.tier`, `stoke.descent.classify`, etc. taxonomy. CloudSwarm doesn't require these — it stores everything verbatim under `event_type`. The taxonomy is useful for DASHBOARD purposes but is NOT part of the mandatory contract.
2. **Policy hook is NOT needed for CloudSwarm integration.** work.md proposes `CLOUDSWARM_POLICY_ENDPOINT` gating every Stoke tool call. Reality: Stoke is opaque to CloudSwarm's policy engine; Cedar is skill-level. If wanted, policy integration is a STANDALONE Stoke feature.
3. **`STOKE_MAX_WORKERS` env var is NOT set by CloudSwarm.** Concurrency is enforced upstream at submission time.
4. **Memory is NOT a CloudSwarm integration point.** work.md proposes a Stoke→CloudSwarm memory API call. Reality: memory is handled entirely by CloudSwarm's AgentLoopWorkflow; Stoke doesn't see it.
5. **`stoke run` subcommand is the PRIMARY gap.** work.md focuses on event emission but assumes the command exists. It doesn't.

---

## IMPLEMENTATION CHECKLIST (the real list)

To integrate Stoke with CloudSwarm, Stoke must implement:

- [ ] `stoke run` subcommand in `cmd/r1/`
- [ ] `--output stream-json` flag (NDJSON streaming)
- [ ] `--repo <url>` flag
- [ ] `--branch <name>` flag
- [ ] `--model <name>` flag
- [ ] Event type definitions + central emitter (internal/events/)
- [ ] HITL checkpoint emitting `{"type":"hitl_required","reason":"...","file":"..."}` and reading decision JSON from stdin
- [ ] Stdin decision reader handling `{"decision":bool,"reason":str,"decided_by":str}`
- [ ] Exit code semantics (0 = success, non-zero = failure)
- [ ] Line-buffered stdout (so supervisor pause doesn't lose partial lines)
- [ ] Graceful shutdown: flush event buffer on SIGINT
- [ ] SIGSTOP-survivability: buffered events survive pause (no intermediate stdout loss)

---

## KEY FILE REFERENCES

| Function | File | Line | Purpose |
|---|---|---|---|
| `ExecuteStokeActivity.execute_binary` | `platform/temporal/activities/execute_stoke.py` | 288 | Subprocess driver |
| `_supervisor_rpc` | `platform/temporal/activities/execute_stoke.py` | 64 | SIGSTOP/SIGCONT/stdin control |
| `_append_event` | `platform/temporal/activities/execute_stoke.py` | 572 | Event persist + NATS pub |
| `StokeAgentWorkflow.run` | `platform/temporal/workflows/stoke_agent.py` | 54 | Workflow orchestration + HITL |
| `_wait_for_hitl` | `platform/temporal/workflows/stoke_agent.py` | 266 | 15-min HITL timeout |
| `stoke_approval_consumer` | `platform/apps/api/src/events/stoke_approval_consumer.py` | 25 | Approval decision routing |
| `submit_session` | `platform/apps/api/src/routes/stoke_sessions.py` | 123 | Session submission + concurrency gate |
| `_fetch_budget_context` | `platform/temporal/activities/policy_check.py` | 269 | Live Redis budget |
| `DelegationContext` | `platform/temporal/models.py` | 89 | Trust-clamped delegation |

---

## NATS CHANNELS

| Subject | Direction | Publisher | Subscriber | Payload |
|---|---|---|---|---|
| `stoke.agent.{session_id}.events` | CloudSwarm→API/Frontend | ExecuteStokeActivity | API listeners | `{event_type,data}` |
| `cs.approval.>` | External approval → Stoke workflow | External approval system | stoke_approval_consumer | `{type:"approval.decided"\|"approval.expired",decision,context}` |

---

**Document End**
