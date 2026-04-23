<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 (event emission), spec-3 (tool-call hook point) -->
<!-- BUILD_ORDER: 10 -->

# Policy Engine — Implementation Spec

## Overview

Stoke needs a local authorization gate so enterprise operators can govern tool use (bash, file IO, MCP calls) without relying on CloudSwarm. Per RT-CLOUDSWARM-MAP §4, CloudSwarm's Cedar engine is skill-level and does NOT gate Stoke's tool calls; Stoke is an opaque subprocess to CloudSwarm. This spec is therefore STANDALONE and deferred from spec-2 per decision D-9. It ships `internal/policy/` with three backends (cedar-agent HTTP client, local YAML engine, null permit-all) selected by env vars, plus a hook in the native tool path that denies actions before the engine dispatches them. Fail-closed on transport failure. No CloudSwarm-side work; no MCP client coupling (spec-8); no delegation token verification (spec-5). spec-2 documented `CLOUDSWARM_POLICY_ENDPOINT` as a no-op env-var; this spec wires it to a live backend.

## Stack & Versions

- Go 1.22+
- stdlib `net/http`, `encoding/json`, `regexp`, `context`
- `gopkg.in/yaml.v3` (already in go.mod for config parsing)
- Existing `internal/streamjson/emitter.go` for `policy.check` / `policy.denied` events
- Existing `internal/bus/bus.go` for internal publish
- No new third-party deps (cedar-go NOT adopted — HTTP sidecar only, per RT-02 §2 alternatives-rejected)

## Existing Patterns to Follow

- Package layout: mirror `internal/trustplane/` (client.go + factory.go + real.go + tests)
- Factory from env: mirror `internal/trustplane/factory.go::NewFromEnv`
- Event emission: reuse `streamjson.Emitter.EmitSystem` with `subtype: "policy.check"` and `_stoke.dev/policy.*` extension keys
- Hook point: `cmd/stoke/sow_native.go` native tool dispatch (spec-3 tool-call hook; wrap ToolUse block before `NativeRunner.Run` invokes the handler)
- Config surface: env-var first; no YAML config section (policy file path is itself the config)

## Library Preferences

- HTTP: `net/http` with 2s default `Client.Timeout`
- YAML parsing: `gopkg.in/yaml.v3` (same as `internal/config/`)
- Regex: stdlib `regexp` (RE2); compile-on-load, cache compiled patterns on the rule struct
- JSON: stdlib `encoding/json`

## Data Models

### `policy.Decision`

```go
type Decision int
const (
    DecisionDeny  Decision = 0 // zero value = safe default (fail-closed)
    DecisionAllow Decision = 1
)
```

### `policy.Request`

| Field | Type | Purpose | Example |
|-------|------|---------|---------|
| `Principal` | `string` | Cedar-style entity UID | `Stoke::"cli"` or `Agent::"session-42"` |
| `Action` | `string` | Tool name prefixed | `bash.run`, `file.write`, `mcp.call.github.search` |
| `Resource` | `string` | What is acted on | `File::"/etc/passwd"` or `Cmd::"rm -rf /"` |
| `Context` | `map[string]any` | Freeform attrs | `{"command":"ls","trust_level":3,"budget_remaining_usd":4.12}` |

### `policy.Result`

| Field | Type | Purpose |
|-------|------|---------|
| `Decision` | `Decision` | Allow or Deny |
| `Reasons` | `[]string` | Matched rule IDs (cedar-agent `diagnostics.reason`) |
| `Errors` | `[]string` | Eval errors (cedar-agent `diagnostics.errors`); non-empty on Allow is suspicious |

Per RT-02 §5: returning a struct (not `(Decision, string, error)`) maps cleanly to cedar-agent's `diagnostics` shape and supports audit ledger `PolicyDecisionNode`.

### `policy.Client` interface

```go
type Client interface {
    Check(ctx context.Context, req Request) (Result, error)
}
```

## API Endpoints (cedar-agent HTTP contract)

### POST `/v1/is_authorized`

**Auth:** Bearer token from `CLOUDSWARM_POLICY_TOKEN` env (if set; otherwise none).

**Request body** (PARC JSON, verbatim from RT-02 §3):

```json
{
  "principal": "User::\"alice\"",
  "action":    "Action::\"bash.exec\"",
  "resource":  "Tool::\"bash\"",
  "context": {
    "command": "rm -rf /tmp/x",
    "trust_level": 3,
    "budget_remaining_usd": 4.12,
    "worktree": "wt-42"
  },
  "entities": [
    {
      "uid": { "type": "User", "id": "alice" },
      "attrs": { "role": "dev" },
      "parents": [ { "type": "Group", "id": "devs" } ]
    }
  ]
}
```

Stoke v1 sends `entities: []` (empty array) — entity hierarchy is reserved for future work.

**Success (200)** — verbatim from RT-02 §3:

```json
{
  "decision": "Allow",
  "diagnostics": {
    "reason": ["policy0", "policy7"],
    "errors": []
  }
}
```

`decision` is `Allow` or `Deny` (deny-by-default, deny-overrides). `reason` lists satisfied policy IDs; `errors` non-empty on Allow → treat as suspicious (log warning, still honor decision).

**Errors:**

| Status | When | Stoke Behavior |
|--------|------|----------------|
| 4xx | Malformed request | Return `err`; caller gets `DecisionDeny` + reason `"policy-engine request malformed"` |
| 5xx | Engine failure | Return `err` (no retry); caller gets `DecisionDeny` + reason `"policy-engine unavailable"` |
| transport | Network / timeout | Return `err` (no retry); caller gets `DecisionDeny` + reason `"policy-engine unavailable"` |

## YAML Rule Grammar

Schema for `STOKE_POLICY_FILE`:

```
document   := "rules:" "\n" rule-list
rule-list  := ( "  - " rule )+
rule       := "effect:" effect-value "\n"
              [ "id:" ident "\n" ]
              [ "actions:" "[" action-list "]" "\n" ]
              [ "principal_trust:" trust-expr "\n" ]
              [ "when:" "\n" pred-list ]
effect-value := "permit" | "forbid"
action-list  := quoted-string ( "," quoted-string )*
trust-expr   := ( ">=" | "<=" | ">" | "<" | "=" ) digit+
pred-list    := ( "      - " pred-key ":" pred-expr "\n" )+
pred-key     := "command" | "path" | "url" | "tool_name" | "resource"
              | "budget_remaining_usd" | "trust_level" | "phase" | <any Context key>
pred-expr    := "matches " regex
              | "startswith " value
              | "equals " value
              | "in [" value-list "]"
              | ( ">=" | "<=" | ">" | "<" ) number
```

**Example (from scope brief):**

```yaml
rules:
  - id: allow-safe-bash
    effect: permit
    actions: ["bash.run"]
    principal_trust: ">=3"
    when:
      - command: "matches ^(go|npm|pnpm|ls|cat|grep|find) "
  - id: deny-etc-write
    effect: forbid
    actions: ["file.write"]
    when:
      - path: "matches ^/etc/"
  - id: allow-workspace-write
    effect: permit
    actions: ["file.write"]
    when:
      - path: "startswith /workspace/"
```

**Evaluation order:**

1. Rules applied top-to-bottom.
2. First `forbid` whose actions + when all match → `DecisionDeny` with `Reasons=[rule.id]`. Stop.
3. If any `permit` whose actions + when all match → `DecisionAllow` with `Reasons=[rule.id]`. Stop.
4. No match → `DecisionDeny` with `Reasons=["default-deny"]` (fail-closed).

**Action match:** exact string equality against `req.Action`. Wildcards NOT supported in v1.

**Principal-trust match:** compare `principal_trust` expr against `req.Context["trust_level"]` (int). Missing trust → treated as 0.

**When-predicate match:** ALL predicates in the `when` list must be true (AND semantics). Each predicate looks up its key in `req.Context` (with special handling for `command`→`req.Context["command"]`, `path`→`req.Context["path"]`, etc.). Missing key → predicate false.

## Predicate List (all supported operators)

| Operator | Types | Semantics | Example |
|----------|-------|-----------|---------|
| `matches <regex>` | string | RE2 partial match via `regexp.MatchString` | `command: "matches ^go "` |
| `startswith <prefix>` | string | `strings.HasPrefix` | `path: "startswith /workspace/"` |
| `equals <value>` | string/num/bool | `==` after type-coerce to context value | `phase: "equals execute"` |
| `in [<list>]` | string | membership in string list | `tool_name: "in [\"bash\", \"file.read\"]"` |
| `>=` | number | `float64 >=` | `budget_remaining_usd: ">= 0.25"` |
| `<=` | number | `float64 <=` | `trust_level: "<= 5"` |
| `>` | number | `float64 >` | `turns_completed: "> 0"` |
| `<` | number | `float64 <` | `turns_remaining: "< 100"` |

Unsupported (per RT-02 §4 limitations): `&&`/`||`/`!` composition within one predicate, entity-group `in` membership, typed records/sets, policy templates.

## Business Logic

### Decision Flow (prose sequence)

1. Stoke native tool path about to invoke `bash.run` / `file.write` / `mcp.call.*`.
2. Build `policy.Request` from tool args: Principal=`Stoke::"<session_id>"`, Action=`<tool_name>`, Resource=`<primary-arg-path-or-cmd>`, Context=`{command, path, trust_level, budget_remaining_usd, phase, worktree}`.
3. Call `client.Check(ctx, req)` (ctx has 2s default deadline for HTTP backend).
4. On `err != nil`: emit `policy.check` event with `decision=deny, error=<msg>`, return `Result{DecisionDeny, Reasons:["policy-engine unavailable"], Errors:[err.Error()]}` — fail-closed.
5. On `Result.Decision == DecisionAllow`: emit `policy.check` event, proceed with tool invocation.
6. On `Result.Decision == DecisionDeny`: emit `policy.denied` event with `reasons`, fail the tool call with `fmt.Errorf("policy denied: %s", strings.Join(reasons, ","))`. Do NOT retry.
7. Tool-call error propagates up to the agentloop turn; the LLM sees a tool_result with the denial reason and may adapt.

### Factory Selection (`NewFromEnv`)

Priority order (first match wins):

1. `CLOUDSWARM_POLICY_ENDPOINT` set → cedar-agent HTTP client (`NewHTTPClient(endpoint, token)`).
2. Else `STOKE_POLICY_FILE` set → YAML engine (`NewYAMLClient(path)`); file must exist and parse, else startup error.
3. Else → `NullClient{}` (always returns `DecisionAllow` with `Reasons=["null-client: no policy configured"]`).

Startup log (stderr, one line): `policy: backend=<http|yaml|null> source=<endpoint|path|"standalone-dev">`.

### Metrics

Every `Check` call emits one `policy.check` event:

```
type=system subtype=policy.check
_stoke.dev/policy.decision = "allow" | "deny"
_stoke.dev/policy.latency_ms = <int>
_stoke.dev/policy.reasons_count = <int>
_stoke.dev/policy.backend = "http" | "yaml" | "null"
```

On deny, additionally emit `policy.denied` with `reasons` array for the audit trail.

## Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| HTTP transport error / timeout | Fail-closed; 0 retries | Tool call fails: `policy denied: policy-engine unavailable` |
| HTTP 5xx | Fail-closed; 0 retries | Tool call fails: `policy denied: policy-engine unavailable` |
| HTTP 4xx | Fail-closed; log body | Tool call fails: `policy denied: policy-engine request malformed` |
| YAML file missing at startup | Hard startup error (exit 2) | `policy: failed to load /path/to/policy.yaml: no such file` |
| YAML parse error | Hard startup error (exit 2) | `policy: parse error in rules[2].when[0]: unknown operator "contains"` |
| Regex compile error at load | Hard startup error (exit 2) | `policy: rule "allow-safe-bash": invalid regex: <err>` |
| Context key missing for predicate | Predicate evaluates false (safe) | Rule does not match |
| NullClient | Always allow | No user-visible error; startup log notes `backend=null` |

## Fail-Closed Rationale

Policy engines are security controls. A transport error could mask:

1. A deliberately down-ed cedar-agent (attacker blocking gate).
2. A misconfigured endpoint (accidental disable).
3. A network partition during credential rotation.

Default-permit on error would silently revert to pre-policy behavior, defeating the control. RT-02 §5 explicitly calls out `err != nil must never be treated as allow at call sites.` The fail-closed cost is low — Cedar eval is 4-11us (RT-02 §6), HTTP RTT dominates, and a local YAML fallback is always available via `STOKE_POLICY_FILE` for operators who want offline continuity.

## Performance Note

Per RT-02 §6: Cedar native evaluation is 4-11us median, sub-millisecond p99. HTTP round-trip (~100-500us loopback) dominates. **No local cache in v1.** Add only if profiling shows `policy.Check` in the top N% of tool-call overhead. Key would be `sha256(principal|action|resource|stableContextHash)` with 1-5s TTL, invalidated on any policy update event. Do NOT cache across trust_level / budget_remaining changes — those are request-scoped.

## Boundaries — What NOT To Do

- Do NOT embed cedar-go directly (RT-02 §2 alternative rejected: requires Cedar DSL authoring + entity hydration, too much surface).
- Do NOT implement FFI to Rust cedar-policy (breaks pure-Go build).
- Do NOT build an OPA/Rego engine (duplicates `scan/` + `hooks/` concepts).
- Do NOT modify CloudSwarm-side Cedar engine (lives in `platform/policies/cedar/engine.py`; out of scope per RT-CLOUDSWARM-MAP §4).
- Do NOT couple to MCP client (spec-8) — policy hook only sees the action string `mcp.call.<server>.<tool>`.
- Do NOT couple to delegation tokens (spec-5) — `trust_level` comes from `req.Context`, set by the caller.
- Do NOT retry on transport error — masks network issues.
- Do NOT default-permit on startup if policy file missing when `STOKE_POLICY_FILE` is set — that is a hard error.
- Do NOT add a local cache in v1.

## Testing

### `internal/policy/cedar_agent_test.go`

- [ ] Happy: httptest server returns `{decision:"Allow",diagnostics:{reason:["p0"],errors:[]}}` → Result{Allow, Reasons:["p0"]}
- [ ] Deny: server returns `{decision:"Deny",...}` → Result{Deny, Reasons:[...]}
- [ ] Transport: server closed mid-request → err != nil AND callers fall through to DecisionDeny
- [ ] Timeout: server sleeps 3s, 2s client timeout → err != nil, fail-closed
- [ ] Malformed JSON: server returns `{not-json` → err != nil, fail-closed
- [ ] Bearer token: when `CLOUDSWARM_POLICY_TOKEN` set, header `Authorization: Bearer <tok>` present
- [ ] Empty token: no Authorization header sent
- [ ] PARC shape: request body contains `principal`, `action`, `resource`, `context`, `entities` keys (entities=[] v1)

### `internal/policy/yaml_engine_test.go`

- [ ] Parse: minimal 1-rule YAML loads without error
- [ ] Permit match: `allow-safe-bash` + action=bash.run + command=ls → Allow
- [ ] Forbid overrides: deny-etc-write before allow-all → Deny for /etc/passwd
- [ ] Default deny: no rules match → Deny with Reasons=["default-deny"]
- [ ] Regex compile error at load → hard error
- [ ] Predicate: `matches` applied to `command`
- [ ] Predicate: `startswith` applied to `path`
- [ ] Predicate: `equals` applied to `phase`
- [ ] Predicate: `in [...]` applied to `tool_name`
- [ ] Predicate: `>=` applied to `trust_level`
- [ ] Missing context key → predicate false → rule does not match
- [ ] Principal-trust expr `>=3` vs trust_level=2 → rule not applied

### `internal/policy/factory_test.go`

- [ ] `CLOUDSWARM_POLICY_ENDPOINT` set → HTTP client returned
- [ ] `STOKE_POLICY_FILE` set, endpoint unset → YAML client returned
- [ ] Both unset → NullClient returned; always Allow
- [ ] YAML file missing → startup error
- [ ] Startup log emitted on stderr with backend name

### `internal/policy/failclosed_test.go`

- [ ] TestFailClosedOnTransport: httptest server closed → DecisionDeny, Reasons contains "policy-engine unavailable"
- [ ] TestFailClosedOnTimeout: 3s sleep + 50ms timeout → DecisionDeny
- [ ] TestFailClosedOn5xx: server returns 500 → DecisionDeny, no retry attempted

### Integration (`internal/policy/integration_test.go`)

- [ ] Tier 1 (all-permit YAML): every tool call passes through with Allow event emitted
- [ ] Tier 2 (HTTP round-trip): in-memory cedar-agent emulator with configurable decisions; Allow/Deny both round-trip correctly
- [ ] Denied tool call surfaces reason in agentloop tool_result
- [ ] `policy.check` event emitted on every call with latency_ms populated
- [ ] `policy.denied` event emitted on every deny with reasons array

## Acceptance Criteria

- WHEN `STOKE_POLICY_FILE` points at an all-deny YAML THE SYSTEM SHALL fail every tool call with `policy denied: default-deny` in the error
- WHEN `CLOUDSWARM_POLICY_ENDPOINT` is unreachable THE SYSTEM SHALL emit `DecisionDeny` with reason `"policy-engine unavailable"` and not retry
- WHEN no env vars are set THE SYSTEM SHALL select NullClient and log `backend=null` to stderr at startup
- WHEN a tool call is allowed THE SYSTEM SHALL emit one `policy.check` event with `decision=allow` and `latency_ms` populated
- WHEN a tool call is denied THE SYSTEM SHALL emit one `policy.check` event AND one `policy.denied` event with reasons array
- WHEN the YAML policy file has a regex compile error THE SYSTEM SHALL fail startup with exit code 2

### Bash acceptance commands

```bash
go test ./internal/policy/... -run TestCedarAgentHTTP
go test ./internal/policy/... -run TestYAMLEngine
go test ./internal/policy/... -run TestFailClosedOnTransport
go test ./internal/policy/... -run TestFailClosedOnTimeout
go test ./internal/policy/... -run TestFactoryFromEnv
go test ./internal/policy/... -run TestIntegrationTier1
go test ./internal/policy/... -run TestIntegrationTier2
./stoke policy validate /tmp/test.yaml
./stoke policy test /tmp/test.yaml 'principal=user action=bash.run resource="ls"'
./stoke policy trace --last-N 100
STOKE_POLICY_FILE=/tmp/deny.yaml ./stoke run "rm -rf /" 2>&1 | grep -q 'policy denied'
CLOUDSWARM_POLICY_ENDPOINT=http://127.0.0.1:1 ./stoke run "ls" 2>&1 | grep -q 'policy-engine unavailable'
go vet ./internal/policy/...
go build ./cmd/stoke
```

## Implementation Checklist

1. [ ] Create `internal/policy/types.go` — define `Decision` (int with iota, DecisionDeny=0, DecisionAllow=1), `Request{Principal,Action,Resource string; Context map[string]any}`, `Result{Decision; Reasons,Errors []string}`, `Client interface { Check(ctx,req) (Result,error) }`. Godoc every exported symbol. Zero-value Decision is Deny for safety. No deps beyond stdlib.

2. [ ] Create `internal/policy/null_client.go` — `NullClient struct{}` implementing `Client`. `Check` always returns `Result{DecisionAllow, Reasons:["null-client: no policy configured"], Errors:nil}, nil`. Used in standalone dev mode.

3. [ ] Create `internal/policy/cedar_agent.go` — `HTTPClient struct { endpoint, token string; hc *http.Client }`. `NewHTTPClient(endpoint, token string) (*HTTPClient, error)` returns client with 2s default `Client.Timeout`. `Check` builds PARC JSON body (principal/action/resource/context/entities=[]), POSTs to `<endpoint>/v1/is_authorized`, sets `Content-Type: application/json` + `Authorization: Bearer <token>` if token non-empty. Parses `{decision,diagnostics:{reason,errors}}`. Maps `"Allow"`->DecisionAllow, `"Deny"`->DecisionDeny. Returns `err != nil` on transport / 5xx / malformed body; callers fail-closed. 0 retries. Copy PARC example from RT-02 §3 verbatim into godoc.

4. [ ] Create `internal/policy/yaml_engine.go` — `YAMLClient struct { rules []compiledRule; path string }`. `NewYAMLClient(path string) (*YAMLClient, error)` parses YAML, compiles regex on each `matches` predicate (fail hard on compile error), pre-parses `principal_trust` op+operand, pre-parses numeric predicates. `compiledRule` has `Effect (permit|forbid)`, `ID`, `Actions []string`, `TrustOp`, `TrustVal`, `Predicates []compiledPred`. `Check` iterates top-to-bottom: for each rule (a) action match exact, (b) principal_trust compare against Context["trust_level"] (missing→0), (c) all predicates true. First forbid-match→Deny; any permit-match→Allow; otherwise→Deny with Reasons=["default-deny"].

5. [ ] Create `internal/policy/yaml_predicates.go` — implement 8 predicate evaluators: `matches` (RE2 partial via `regexp.MatchString`), `startswith` (`strings.HasPrefix`), `equals` (==), `in` (membership in string slice parsed from `[a, b, c]`), `>=`/`<=`/`>`/`<` (float64 compare after JSON number coerce). Missing Context key → false. Type mismatch → false. All predicates in a `when` block AND-composed.

6. [ ] Create `internal/policy/factory.go` — `NewFromEnv(ctx context.Context) (Client, error)`. Order: `CLOUDSWARM_POLICY_ENDPOINT` (with optional `CLOUDSWARM_POLICY_TOKEN`) → HTTPClient; else `STOKE_POLICY_FILE` → YAMLClient; else NullClient. Log to stderr: `policy: backend=<http|yaml|null> source=<value>`. YAML file missing or parse error is a hard error.

7. [ ] Wire policy hook in `cmd/stoke/sow_native.go` native tool dispatch — before each tool invocation (bash, file_write, file_read, MCP call), build `policy.Request{Principal:"Stoke::"+sessionID, Action:<toolName>, Resource:<primaryArg>, Context:{command,path,trust_level,budget_remaining_usd,phase,worktree}}`. trust_level defaults to 3 when delegation ctx is absent (spec-5 will replace this default with real trust derivation). Call `client.Check(ctx, req)` with 2s deadline. On err or Deny: emit `policy.denied` event, fail the tool call with `fmt.Errorf("policy denied: %s", strings.Join(reasons, ","))`, DO NOT retry. On Allow: emit `policy.check` event, proceed.

8. [ ] Add policy events to `internal/streamjson/emitter.go` — new helpers `EmitPolicyCheck(decision, latencyMs int, reasonsCount int, backend string)` and `EmitPolicyDenied(reasons []string, principal, action, resource string)`. Both emit type=system with subtype=`policy.check` / `policy.denied` and `_stoke.dev/policy.*` keys per existing pattern.

9. [ ] Add CLI subcommand `stoke policy validate <file.yaml>` in `cmd/stoke/main.go` — calls `NewYAMLClient(path)`; on success prints `OK: <N> rules loaded` and exits 0; on failure prints error and exits 2.

10. [ ] Add CLI subcommand `stoke policy test <file.yaml> "principal=X action=Y resource=Z [k=v ...]"` in `cmd/stoke/main.go` — parses k=v pairs, builds `policy.Request`, calls `YAMLClient.Check`, prints `decision=<allow|deny> reasons=[...] errors=[...]`, exits 0 on Allow / 1 on Deny.

11. [ ] Add CLI subcommand `stoke policy trace --last-N <int>` in `cmd/stoke/main.go` — reads last N `policy.check` / `policy.denied` events from the session event log (same source streamjson writes to), renders tabular: `ts | decision | action | resource | reasons`. Exits 0.

12. [ ] Create in-memory cedar-agent emulator `internal/policy/testing/emulator.go` — httptest.Server with configurable `DecisionFn func(req Request) Result`. Export `NewEmulator(DecisionFn) *Emulator` with `.URL() string` for test wiring. Used by integration tests.

13. [ ] Write `internal/policy/cedar_agent_test.go` — 8 cases per Testing section above. Use `httptest.NewServer` + emulator. Cover happy/deny/transport/timeout/malformed/token/empty-token/PARC-shape.

14. [ ] Write `internal/policy/yaml_engine_test.go` — 12 cases per Testing section. Cover parse, permit-match, forbid-override, default-deny, regex compile error, each predicate type, missing context key, principal-trust expr.

15. [ ] Write `internal/policy/factory_test.go` — 5 cases: endpoint-wins, file-wins, both-unset-null, file-missing-error, startup-log-written. Use `t.Setenv` for env manipulation.

16. [ ] Write `internal/policy/failclosed_test.go` — 3 cases: transport-closed, timeout, 5xx. All verify `Result.Decision == DecisionDeny` and `Reasons` contains "policy-engine unavailable".

17. [ ] Write `internal/policy/integration_test.go` — 2 tiers: Tier-1 all-permit YAML with real tool dispatch sees Allow events; Tier-2 emulator round-trip with Allow/Deny cases; verify events emitted to streamjson.

18. [ ] Update `specs/cloudswarm-protocol.md` cross-reference — add note in D-9 section pointing at this spec file; mark `CLOUDSWARM_POLICY_ENDPOINT` env-var as "implemented in spec-10 (policy-engine.md)" instead of no-op. (Doc-only edit; no code change outside this spec's files.)

19. [ ] Add package godoc in `internal/policy/doc.go` — explain three backends, fail-closed invariant, PARC JSON shape with verbatim RT-02 example, YAML grammar summary, performance note (4-11us Cedar eval, no cache v1).

20. [ ] Verify build + tests: `go build ./cmd/stoke && go test ./internal/policy/... && go vet ./internal/policy/...` all pass. Run acceptance bash commands from section above; confirm each exit/grep-filter matches spec.
