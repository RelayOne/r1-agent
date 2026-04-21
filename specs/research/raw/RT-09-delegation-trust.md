# RT-09 — Trust-Clamped Agent Delegation (Stoke)

**Status:** research-raw · **Date:** 2026-04-20 · **Owner:** STK-delegation
**Related:** CloudSwarm V-69 (budget reservation), SEC-ITEM-2 (trust ceiling),
SEC-ITEM-6 (depth limit), V-114 (signed-token choke point), STOKE-014/015/016.

## 1. CloudSwarm delegation workflow (source of truth)

File: `platform/temporal/workflows/delegation.py` (Temporal `DelegationWorkflow`).
Models: `platform/temporal/models.py`. Cedar rules:
`platform/policies/cedar/delegation.cedar`. Constant `MAX_DELEGATION_DEPTH = 3`
in `platform/temporal/constants.py`. A Pydantic/Redis record variant
(`DelegationRecord`, `MAX_DEPTH=10`) lives in
`platform/policies/trustplane_delegation.py` for capability-bundle delegations;
Stoke should mirror the Temporal variant for task delegation.

### 1.1 `DelegationContext` struct (models.py:89)

```python
class DelegationContext(BaseModel):
    delegator_agent_id: str
    delegator_trust_level: int
    effective_trust_level: int      # min(delegator, executor)
    delegation_depth: int = 0
    max_depth: int = 3              # == MAX_DELEGATION_DEPTH
    delegation_token: str = ""      # HMAC-signed token binding this delegation
    parent_task_id: str = ""
```

The context rides on every `TaskInput` so the Cedar evaluator can enforce the
ceiling on every tool call, not just on the initial delegate.

### 1.2 Trust-level assignment and propagation

Trust is an enum `L0..L4` coerced to `int[0,4]` via `_trust_int()` at
`delegation.py:53`. On each delegation:

1. Load `requester_config` and `provider_config` (`load_agent_config` activity).
2. `requester_trust = _trust_int(requester.trust_level)`;
   `provider_trust = _trust_int(provider.trust_level)`.
3. `effective_trust = min(requester_trust, provider_trust)` — SEC-ITEM-2.
4. Cedar policy `delegation:confused-deputy` (cedar rule ids in §3) re-checks
   that `principal.trust_level ≤ context.delegator_trust_level` at every action
   so a high-trust executor cannot launder its own privileges.

The depth field is monotone: `new_depth = input.delegation_depth + 1`, and the
workflow short-circuits with `"Delegation depth limit exceeded"` before loading
any config when `delegation_depth >= MAX_DELEGATION_DEPTH` (delegation.py:97).

### 1.3 HMAC token format (`DelegationTokenRequest`, models.py:113)

```python
class DelegationTokenRequest(BaseModel):
    parent_agent_id: str
    child_agent_id: str
    effective_trust_level: int
    allowed_actions: list[str] = []
    ttl_minutes: int = 15
```

The `generate_delegation_token` activity is declared but, per V-114
(`policy_check.py:379-402`), verification is intentionally a **no-op choke
point**: `_resolve_delegation_claims` returns `(False, agent_trust)` until a
real HMAC verifier ships. This is a deliberate fail-closed design — reading
`is_delegated` from LLM-shaped input is a trust-escalation primitive, so the
Cedar delegation branch is unreachable until the verifier exists. Stoke must
not replicate the no-op; we need a real HMAC from day one.

Recommended HMAC payload (JSON, base64url-signed with a rotating 32-byte secret
from `CS_DELEGATION_HMAC_KEY` / `STOKE_DELEGATION_HMAC_KEY`):

```json
{ "v":1, "tok_id":"<uuid>", "parent":"<did>", "child":"<did>",
  "eff_trust":3, "actions":["email_send","calendar_read"],
  "parent_task":"<uuid>", "depth":1, "max_depth":3,
  "nonce":"<16B b64url>", "iat":1745174400, "exp":1745175300 }
```

Token wire form: `b64url(payload) + "." + b64url(hmac_sha256(key, payload))`.
Verify order: decode → HMAC-compare (`subtle.ConstantTimeCompare`) → `exp`
window (±30s skew) → `nonce` cache miss (24h Redis/BBolt set) → structural
checks. Rotate by accepting `kid` header and holding N-1 + N keys.

### 1.4 Budget pre-reservation (V-69)

Before the child `AgentLoopWorkflow` launches (`delegation.py:175`):

```
reserved = reserve_delegation_budget(account_id, delegation_id, cap_usd)
if reserved <= 0: return error "Budget reservation failed"
```

The activity wraps `BudgetManager.check_and_consume` (atomic Redis Lua) so
concurrent delegations cannot both see the full daily budget and double-spend.
Returns `min(requested_cap, account_remaining)`. Zero = hard-deny.

### 1.5 Reconciliation paths (three branches)

| Outcome | Call |
|---|---|
| Happy path | `reconcile(account, id, reserved, actual)` — refund `reserved - actual` |
| Over-budget (cost > cap) | `reconcile(..., reserved, min(actual, reserved))` + trust penalty event |
| Child failure (`ChildWorkflowError`) | `reconcile(..., reserved, 0.0)` — full refund |
| Expiry (not explicit) | scheduled reconciler picks up stale reservations via TTL |

All three emit NATS events: `cs.delegation.{account}.{id}.{started|completed|budget_exceeded|failed}`.
Trust is updated for **both** parties: provider gets `success`/`failure`,
requester gets `delegation_success`/`delegation_failure`.

## 2. Industry patterns

### 2.1 OAuth 2.0 Token Exchange (RFC 8693)
Downscoping is the governing rule: the issued token MUST NOT have broader
privileges than the subject token. The `act` claim records "A acting for B"
(delegation); `may_act` advertises who is allowed. Mirrors our
effective-trust = min(delegator, executor). RFC 8693 is the closest
off-the-shelf analog and its token shape (JWT with `act`/`may_act`) is worth
mimicking for HTTP-facing agents.

### 2.2 UCAN (User-Controlled Authorization Network)
**Attenuation-only** invariant: each link in the delegation chain has
capabilities ⊆ parent's, and time bounds ⊆ parent's. JWT-shaped with Ed25519
signatures; v1.0 uses DAG-CBOR. Chain verification walks witnesses proving
provenance. Directly analogous to our depth-limited chain; the attenuation
check is strictly stronger than our `min(trust)` clamp and should inform our
Cedar rules.

### 2.3 Macaroons (Google, Stanford)
HMAC-chained cookies with append-only **caveats**. A holder can restrict (never
expand) a macaroon by appending a caveat + re-HMACing. Third-party caveats
require a discharge macaroon from an external service — directly useful for
TrustPlane as the issuer of "trust ≥ X" discharges. Fly.io's blog documents
production lessons. Closest match for our HMAC-signed, parameter-bound token.

### 2.4 SPIFFE / SPIRE
Workload identity (X.509 SVID or JWT-SVID) per `spiffe://trust-domain/path`.
The **Delegated Identity API** lets an attested workload obtain SVIDs on behalf
of un-attestable workloads, subject to an allowlist. Useful for bootstrap
identity but doesn't carry scope or budget — complementary, not a replacement.

### 2.5 AIP (Agent Identity Protocol, draft-prakash-aip-00, Mar 2026)
Emerging IETF draft defining **Invocation-Bound Capability Tokens (IBCTs)**.
Two modes: JWT/Ed25519 single-hop, and Biscuit tokens with append-only blocks
+ Datalog policy for multi-hop chains. Bindings for MCP, A2A, HTTP. Most
relevant published work on trust-clamped multi-agent systems; watch for
stabilization. Biscuits are essentially "macaroons + Datalog" and are worth
short-listing over raw macaroons.

## 3. Threats and mitigations

| Threat | Mitigation | Enforcer |
|---|---|---|
| Scope tamper (B silently raises `budget=$5 → $50`) | HMAC covers **all** params including `budget_cap_usd`, `effective_trust_level`, `allowed_actions` | `VerifyDelegation` before dispatch |
| Trust laundering (B trust=5, A trust=3) | `effective = min(A,B)`; Cedar `delegation:confused-deputy` and `delegation:trust-ceiling` | Cedar `delegation.cedar` |
| Delegation cycle A→B→C→A | `delegation_depth ≥ MAX_DEPTH=3` → reject | `DelegationWorkflow` step 0 + Cedar `delegation:max-depth` |
| Token replay | `nonce` (16B) cached for `2 × ttl` window; `exp` ≤ 15min; `iat` skew ±30s | verifier-side nonce store |
| Parent revoked mid-flight | NATS `trustplane.delegation.revoked` → `Saga.OnRevocation` → compensating txns | `internal/delegation/saga.go` (exists) |
| Budget over-spend races | Atomic Lua `check_and_consume` pre-reservation | `reserve_delegation_budget` activity |
| Kill-switch bypass | `delegation:global-kill-switch` denies all actions when active | Cedar rule + context flag |

## 4. Go implementation sketch for Stoke

Extend existing `internal/delegation/scoping.go#DelegationContext` (currently
DID+scopes only — §5) with trust+depth+budget fields, then add a new
`internal/delegation/token.go` for HMAC sign/verify.

```go
// scoping.go — extended DelegationContext
type DelegationContext struct {
    DelegatorDID        string
    DelegateeDID        string
    DelegationID        string
    Scopes              []string
    PolicyBundle        string

    // --- RT-09 additions ---
    DelegatorTrustLevel int       // 0..4
    EffectiveTrustLevel int       // min(delegator, executor)
    Depth               int       // monotone ≥ 0
    MaxDepth            int       // default 3
    ParentTaskID        string
    BudgetReservedUSD   float64
    Token               string    // HMAC-signed, see token.go
    Nonce               string    // 16B b64url
    IssuedAt            time.Time
    ExpiresAt           time.Time
}

// token.go
type TokenPayload struct {
    Version     int      `json:"v"`
    TokenID     string   `json:"tok_id"`
    Parent      string   `json:"parent"`
    Child       string   `json:"child"`
    EffTrust    int      `json:"eff_trust"`
    Actions     []string `json:"actions"`
    ParentTask  string   `json:"parent_task"`
    Depth       int      `json:"depth"`
    MaxDepth    int      `json:"max_depth"`
    BudgetUSD   float64  `json:"budget_usd"`
    Nonce       string   `json:"nonce"`
    IssuedAt    int64    `json:"iat"`
    ExpiresAt   int64    `json:"exp"`
    KeyID       string   `json:"kid"`
}

func SignToken(key []byte, p TokenPayload) (string, error)             // b64(payload).b64(hmac)
func VerifyToken(keys map[string][]byte, tok string) (TokenPayload, error)
// NewDelegation clamps trust, bumps depth, reserves budget, signs token.
func NewDelegation(parent *DelegationContext, executorTrust int,
    budgetUSD float64, actions []string, ttl time.Duration,
    reserve BudgetReserver, signer Signer) (*DelegationContext, error)
// VerifyDelegation checks HMAC, depth, nonce-replay, expiry, and clamp.
func VerifyDelegation(ctx *DelegationContext, keys map[string][]byte,
    nonces NonceStore) error
```

Wire into `Manager.Authorize` (scoping.go:91) so every tool call reloads the
context from the token rather than trusting caller-supplied fields. Nonce store
= BBolt bucket keyed by `tok_id`, TTL = `2 × token_ttl`.

## 5. Integration with existing Stoke code

| File | Current shape | RT-09 change |
|---|---|---|
| `internal/delegation/delegation.go` | `Manager` wraps `trustplane.Client` with policy bundles | Add `Delegate()` wrapper that calls `NewDelegation` → `tp.CreateDelegation` |
| `internal/delegation/scoping.go` | `DelegationContext{DIDs, Scopes, PolicyBundle}` | **Extend** — don't replace — add trust/depth/budget/token fields |
| `internal/delegation/saga.go` | Settlement on revocation (3 policies, comp txns, checkpoint) | Already fits; add `BudgetRefund` to `CompensatingTxn` list on revocation |
| New: `internal/delegation/token.go` | — | HMAC sign/verify per §4 |
| New: `internal/delegation/budget.go` | — | `BudgetReserver`/`Reconciler` port of CloudSwarm V-69 (atomic CAS backed by `costtrack`) |
| `configs/policies/cedar/delegation.cedar` | — | Port CloudSwarm's 6 rules verbatim |

Write new code rather than retrofitting: STOKE-014/015/016 already draw a clean
boundary (delegation + scoping + saga) so RT-09 is **additive**. The saga
settlement (§3 comp txns) already exists and only needs a budget-refund
compensator.

## 6. Payment / ledger tie-in

TrustPlane owns settle/dispute; Stoke writes double-entry ledger nodes via
`bridge/` so the v2 content-addressed graph has an audit trail. Use the
existing `bridge.CostNode` / `bridge.AuditNode` shapes.

| Lifecycle step | Ledger entry | Bus event |
|---|---|---|
| `Delegate()` begins | `budget_reserved{delegation_id, amount, parent_task}` (debit pending) | `cs.delegation.started` |
| Child work in flight | no ledger change; `costtrack` accumulates actual | tool-use events |
| Success, `actual ≤ reserved` | `settle{actual}` + `refund{reserved-actual}` (release pending, debit actual) | `cs.delegation.completed` |
| Over-budget | `settle{min(actual, reserved)}` + `dispute{actual-reserved, reason:"budget_exceeded"}` + `trust_penalty{provider, -Δ}` | `cs.delegation.budget_exceeded` |
| Child failed | `reversal{reserved, reason:"child_failure"}` + `trust_penalty{provider, -Δ}` + `trust_penalty{requester, -δ}` | `cs.delegation.failed` |
| Token expired | `reversal{reserved, reason:"expired"}` (swept by reconciler on TTL) | `cs.delegation.expired` |
| Cascade revocation | per WorkUnit: `reversal{reserved}` + `comp_txn_log{entries}` | `trustplane.delegation.revoked` → saga |

Every ledger entry is content-addressed via `contentid/` and linked to the
parent `DelegationNode` through `ledger/edges`, giving operators a replayable
audit trail per `delegation_id`.

## 7. Open items (decide at spec time)

1. **Signer transport.** CloudSwarm wants HMAC (symmetric, one cluster).
   TrustPlane already does Ed25519 for cross-tenant. RT-09 should pick HMAC
   in-cluster + Ed25519 for cross-tenant — or just use TrustPlane's Ed25519
   everywhere and skip the HMAC path.
2. **Biscuit vs JWT.** AIP favors Biscuits for multi-hop. If we expect depth>1
   frequently, Biscuits (append-only blocks, Datalog caveats) are strictly
   better than re-issuing JWTs at each hop. Start with JWT+HMAC, migrate if
   depth>1 traffic shows up.
3. **Nonce store scaling.** BBolt is fine for single-node. Multi-node Stoke
   clusters need Redis SET-NX + EX.
4. **`MAX_DEPTH` value.** CloudSwarm's Temporal path = 3, trustplane_delegation
   path = 10. Stoke should pick **3** for task delegation (matches Cedar rule
   `delegation:max-depth`) and document the discrepancy.

## Sources

- [UCAN Specification](https://ucan.xyz/specification/)
- [ucan-wg/spec](https://github.com/ucan-wg/spec)
- [ucan-wg/delegation](https://github.com/ucan-wg/delegation)
- [Macaroons: Cookies with Contextual Caveats (Stanford/Google)](https://theory.stanford.edu/~ataly/Papers/macaroons.pdf)
- [libmacaroons (rescrv)](https://github.com/rescrv/libmacaroons)
- [Macaroons Escalated Quickly — Fly.io](https://fly.io/blog/macaroons-escalated-quickly/)
- [macaroon-bakery Go package](https://pkg.go.dev/gopkg.in/macaroon-bakery.v2/bakery)
- [RFC 8693 — OAuth 2.0 Token Exchange](https://datatracker.ietf.org/doc/html/rfc8693)
- [ZITADEL — Token Exchange guide](https://zitadel.com/docs/guides/integrate/token-exchange)
- [SPIFFE / SPIRE Concepts](https://spiffe.io/docs/latest/spire-about/spire-concepts/)
- [SPIFFE — Registering workloads](https://spiffe.io/docs/latest/deploying/registering/)
- [AIP: Agent Identity Protocol (arXiv 2603.24775)](https://arxiv.org/abs/2603.24775)
- [draft-prakash-aip-00 — IETF](https://datatracker.ietf.org/doc/draft-prakash-aip/)
- [Top AI Agent Protocols in 2026 — getstream.io](https://getstream.io/blog/ai-agent-protocols/)
