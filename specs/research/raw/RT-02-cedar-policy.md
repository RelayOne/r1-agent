# RT-02: Cedar Policy Integration for Stoke

Date: 2026-04-20
Scope: How Stoke (Go CLI) should integrate with a Cedar policy engine hosted by
CloudSwarm, and what standalone-mode policy model should ship when no engine is
reachable.

## 1. Cedar ecosystem state (April 2026)

Cedar is an open-source authorization language originally published by AWS
(Amazon Verified Permissions uses it internally) and now governed by the
`cedar-policy` GitHub org. PARC model: Principal, Action, Resource, Context.

Implementations relevant to Stoke:

- **cedar-policy (Rust)** -- canonical engine, `cedar-policy` crate on crates.io.
  Exposes authorizer, schema validator, formatter, analyzer. Also has a CLI.
- **cedar-go** -- official Go port at `github.com/cedar-policy/cedar-go`,
  v1.6.0 released 2026-03-20. Community-maintained under the cedar-policy org.
  Includes core authorizer, JSON marshal/unmarshal, core/extended types
  (datetime, duration), schema parsing. **Missing:** schema validator,
  formatter, partial evaluation, policy templates, CLI.
- **Python bindings** -- `cedarpy` wraps the Rust core via PyO3.
- **cedar-agent (Rust HTTP server)** -- `github.com/permitio/cedar-agent`,
  maintained by Permit.io. Default port 8180. REST endpoints for policy/entity
  CRUD plus `/v1/is_authorized`. Swagger at `/swagger-ui`, RapiDoc at `/rapidoc`.
- **cedar-agent-go-sdk** -- `github.com/kevinmichaelchen/cedar-agent-go-sdk`,
  community client for the Permit.io server. Not AWS/cedar-policy official.
- **OPAL** -- sibling project (also Permit.io) that distributes and syncs
  policies to cedar-agent/OPA fleets.

There is no AWS-signed Go SDK for Cedar; cedar-go under the org is the closest.
There is no official cedar-agent Go client.

## 2. Integration pattern (Stoke inside CloudSwarm)

The industry pattern is exactly what's described: run an HTTP sidecar (Cedar
engine + policy store) and have clients POST authorization queries. The
canonical reference implementation is **permit.io/cedar-agent**; Permit's own
TinyTodo example ships a Rust server + Python client against it. AWS Verified
Permissions is the hosted SaaS variant of the same pattern
(`IsAuthorized`/`IsAuthorizedWithToken` API).

Recommended shape for CloudSwarm:

- CloudSwarm embeds cedar-agent (or its own Cedar-wrapping service) on a UNIX
  socket or loopback HTTP endpoint per-sandbox.
- Stoke receives `CLOUDSWARM_POLICY_ENDPOINT` (e.g. `http://127.0.0.1:8180`)
  plus `CLOUDSWARM_POLICY_TOKEN` for auth.
- Every tool call wraps `PolicyClient.Check(...)` before dispatching to the
  engine/agentloop tool handler.

Alternatives considered and rejected:

- **Embed cedar-go directly in Stoke standalone.** Doable, but the language
  requires authoring Cedar policies (`.cedar` DSL), a schema, and entity
  hydration. Too much surface for a "minimal standalone" story.
- **FFI to Rust cedar-policy.** Needs cgo + shipped `.so`; breaks the
  pure-Go build invariant.
- **OPA/Rego.** Stoke already has internal gates (`scan/`, `hooks/`); adding
  Rego duplicates concepts without Cedar's analyzability benefits.

## 3. Canonical request / response schemas

**Cedar authorization request (PARC)** -- principal/action/resource are entity
UIDs of the form `Type::"id"`, context is a record:

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

**cedar-agent `/v1/is_authorized` response:**

```json
{
  "decision": "Allow",
  "diagnostics": {
    "reason": ["policy0", "policy7"],
    "errors": []
  }
}
```

`decision` is `Allow` or `Deny` (deny-by-default, deny-overrides). `reason`
lists the policy IDs that determined the outcome (the satisfied `permit`
policies when Allow; satisfied `forbid` policies when Deny). `errors` lists
policy IDs whose evaluation failed (entity miss, type error) -- a non-empty
`errors` on an Allow should be treated as suspicious.

cedar-agent also exposes `/v1/policies`, `/v1/data` for CRUD; no built-in
batch authorize endpoint (you loop client-side). Streaming is not part of
Cedar -- policies are synchronous point evaluations.

## 4. Standalone YAML policy model

Goal: 80% expressivity for tool-gating without Cedar's entity graph. Policies
are ordered; first match wins; default deny. Conditions are Go template-style
expressions evaluated against the request context.

```yaml
# .stoke/policy.yaml
version: 1
default: deny

principals:
  dev:     { trust_level: 3 }
  reviewer:{ trust_level: 4 }
  cto:     { trust_level: 5 }

rules:
  - id: allow-safe-reads
    effect: allow
    actions: [file.read, repo.grep, repo.glob]
    when: "trust_level >= 1"

  - id: allow-bash-safe
    effect: allow
    actions: [bash.exec]
    when: |
      trust_level >= 2 &&
      command matches "^(ls|cat|git status|git diff|go (build|test|vet)) " &&
      budget_remaining_usd > 0.25

  - id: deny-bash-destructive
    effect: deny            # forbid overrides any allow below
    actions: [bash.exec]
    when: 'command matches "rm -rf|:(){ :|:& };:|curl .*\\| ?sh"'

  - id: allow-file-write-scoped
    effect: allow
    actions: [file.write, file.edit]
    when: |
      trust_level >= 3 &&
      path startswith worktree &&
      !(path matches "\\.env$|credentials|_key\\.pem$")

  - id: allow-mcp-readonly
    effect: allow
    actions: [mcp.invoke]
    when: "tool_name matches '^(search|read|list)_' && trust_level >= 2"
```

Expression primitives: `&&`, `||`, `!`, comparisons, `matches` (regex),
`startswith`, `contains`. Supported context keys are the ones Stoke populates
(command, path, tool_name, trust_level, budget_remaining_usd, worktree,
role, mission_id).

**Known limitations vs real Cedar:**

- No entity hierarchy / `in` group membership. Flat attribute lookup only.
- No policy templates, no schema validation, no analyzer (`cedar analyze`
  can prove policy equivalence; our YAML can't).
- No typed records/sets; everything is string/number/bool at top level.
- No `when`/`unless` clause composition beyond a single expression.
- First-match-wins instead of Cedar's lattice (permit-overrides + forbid-wins).
  We approximate forbid-wins by convention: put deny rules above allow rules.

Document these so users who outgrow it know to graduate to CloudSwarm/Cedar.

## 5. Go client factoring

Proposed interface (matches the question's sketch, small tweaks):

```go
package policy

type Decision int
const (
    DecisionDeny  Decision = 0 // zero value = safe default
    DecisionAllow Decision = 1
)

type Request struct {
    Principal string         // "User::alice"
    Action    string         // "bash.exec"
    Resource  string         // "Tool::bash"
    Context   map[string]any
}

type Result struct {
    Decision    Decision
    Reasons     []string // matched policy IDs
    Errors      []string // eval errors (non-empty => fail closed)
}

type Client interface {
    Check(ctx context.Context, req Request) (Result, error)
}
```

Factoring is right. Selection lives in one constructor:

```go
func NewFromEnv() (Client, error) {
    if ep := os.Getenv("CLOUDSWARM_POLICY_ENDPOINT"); ep != "" {
        return NewHTTPClient(ep, os.Getenv("CLOUDSWARM_POLICY_TOKEN"))
    }
    path := os.Getenv("STOKE_POLICY_FILE")
    if path == "" { path = ".stoke/policy.yaml" }
    return NewYAMLClient(path) // returns a deny-all stub if file missing and STOKE_POLICY_STRICT=1
}
```

Notes:

- Return a `Result` struct rather than `(Decision, string, error)` -- the
  `Reasons`/`Errors` split maps cleanly to cedar-agent's `diagnostics` and is
  needed for audit logging (ledger `PolicyDecisionNode`).
- Fail-closed on transport errors: `err != nil` must never be treated as
  allow at call sites. Provide a `MustAllow(ctx, req)` helper that panics-
  on-err for callers that want the invariant enforced.
- Keep the HTTP client small: one POST to `/v1/is_authorized`, 1s default
  timeout, retry-once on 5xx only.

## 6. Performance & caching

Cedar native evaluation is extremely fast. Published benchmarks:

- Google-Drive-shaped workload: **4-5 us median**, <10 us p99, across
  5-50 entities.
- GitHub-shaped workload: **~11 us median**, <20 us p99.
- 28-35x faster than OpenFGA, 42-80x faster than OPA/Rego on large policy
  sets. Sub-millisecond for all realistic policy counts.

The Cedar paper (Cutler et al., arXiv:2403.04651) reports the same order of
magnitude.

Implication for Stoke:

- **Engine evaluation is free.** Any observable latency is HTTP round-trip
  (~100-500 us over localhost).
- **Caching:** not needed for correctness. Only worth it if you're doing
  hundreds of checks per second (e.g. a bulk file-scan path). If added, key
  on `sha256(principal|action|resource|stableContextHash)` with a very short
  TTL (1-5 s) and invalidate on any policy/entity update event. Do not cache
  across trust_level / budget_remaining changes -- those are request-scoped.
- **CloudSwarm-side:** assume cedar-agent in-process or as sidecar. Permit.io
  deploys it with OPAL for policy sync; no external cache layer needed.

Recommendation: ship without a cache. Revisit only if profiling shows
`policy.Check` in the top N% of tool-call overhead.

## Sources

- [cedar-policy GitHub org](https://github.com/cedar-policy)
- [cedar-go (official Go port)](https://github.com/cedar-policy/cedar-go)
- [cedar-go pkg.go.dev](https://pkg.go.dev/github.com/cedar-policy/cedar-go)
- [permitio/cedar-agent](https://github.com/permitio/cedar-agent)
- [kevinmichaelchen/cedar-agent-go-sdk](https://github.com/kevinmichaelchen/cedar-agent-go-sdk)
- [permitio/tinytodo reference](https://github.com/permitio/tinytodo)
- [Cedar authorization docs](https://docs.cedarpolicy.com/auth/authorization.html)
- [Cedar schema JSON format](https://docs.cedarpolicy.com/schema/json-schema.html)
- [Cedar Request (Rust)](https://docs.rs/cedar-policy/latest/cedar_policy/struct.Request.html)
- [Policy engine benchmarks (Permit.io)](https://www.permit.io/blog/policy-engine-showdown-opa-vs-openfga-vs-cedar)
- [Policy language benchmarks (Teleport)](https://goteleport.com/blog/benchmarking-policy-languages/)
- [Cedar paper arXiv:2403.04651](https://arxiv.org/pdf/2403.04651)
- [StrongDM Cedar 2026 guide](https://www.strongdm.com/cedar-policy-language)
