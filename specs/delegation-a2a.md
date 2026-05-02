<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-3 (Executor interface), spec-4 optional (for browser in verify) -->
<!-- BUILD_ORDER: 5 -->

# Delegation + A2A — Trust-Clamped Hiring and Marketplace Participation

## Overview
Stoke gains the ability to (a) **hire** other A2A-compatible agents to satisfy subplans it cannot or should not execute itself, and (b) **be hired** via `stoke serve` as an A2A endpoint. Both sides are clamped by a trust ceiling + HMAC-signed delegation token + pre-reserved budget. This spec extends `internal/delegation/` with a real HMAC verifier (CloudSwarm's V-114 is a deliberate no-op choke-point per RT-09 — we ship the real verifier from day one), adds an `internal/a2a/` client, a `DelegateExecutor` implementing the spec-3 `Executor` interface, and a `stoke serve` subcommand that exposes Stoke as a signed A2A card. Payment is layered via the `a2a-x402` extension URI; TrustPlane handles escrow/settle/refund.

## Stack & Versions
- **Go:** 1.24.4+ (required by `a2a-go` v2.2.0)
- **A2A SDK:** `github.com/a2aproject/a2a-go` v2.2.0 (module path `/v2`, protocol v1.0)
- **HMAC:** `crypto/hmac` + `crypto/sha256` (stdlib)
- **Card signing:** `crypto/ed25519` (stdlib)
- **Nonce store:** `go.etcd.io/bbolt` (already in Stoke tree via session SQLStore sibling)
- **JSON-RPC transport:** via `a2a-go/v2/a2asrv` and `a2aclient`
- **Payment extension:** `https://a2a-protocol.org/extensions/x402/v0.1` (RT-03 §8)

## Existing Patterns to Follow
- Delegation manager: `internal/delegation/delegation.go` — wrap `trustplane.Client` + policy bundles
- Scoping: `internal/delegation/scoping.go` — extend `DelegationContext` (DO NOT replace fields)
- Saga: `internal/delegation/saga.go` — three settlement policies already shipped; add `BudgetRefund` to comp-txn list
- TrustPlane client: `internal/trustplane/real.go` — OAuth 2.0 DPoP ready
- Cost tracking: `internal/costtrack/` — atomic CAS counters; budget reserve uses these primitives
- Bus: `internal/bus/bus.go` — emit `cs.delegation.{started,completed,budget_exceeded,failed,budget_refunded}`
- Stream JSON: `internal/streamjson/emitter.go` — mirror delegation events to NDJSON
- Executor interface (from spec-3): `internal/executor/` — `Execute / BuildCriteria / BuildRepairFunc / BuildEnvFixFunc`

## Library Preferences
- A2A: **`a2a-go` only** — do NOT hand-roll JSON-RPC handlers. Use `a2aclient.FetchCard`, `a2aclient.FromCard`, `a2asrv.NewRequestHandler`, `a2asrv.CardHandler`, `a2asrv.JSONRPCHandler`.
- HMAC: stdlib `crypto/hmac`; constant-time compare via `subtle.ConstantTimeCompare`.
- JWT: **avoid**. Wire form is base64url(payload) + "." + base64url(hmac) — JWT compactness w/o JOSE header churn.
- Nonce store: bbolt bucket keyed by `tok_id`, TTL = 2 × token TTL.

## Data Models

### Extended `DelegationContext` (internal/delegation/scoping.go)

```go
// DelegationContext carries the authority state of a delegated session.
// RT-09 additive fields below DO NOT replace existing shape.
type DelegationContext struct {
    // Existing (preserved verbatim):
    DelegatorDID string
    DelegateeDID string
    DelegationID string
    Scopes       []string
    PolicyBundle string

    // --- RT-09 additions (verbatim from RT-CLOUDSWARM-MAP §6) ---
    DelegatorAgentID    string    // canonical agent id (may differ from DID)
    DelegatorTrustLevel int       // 0..4 (L0..L4 coerced via _trust_int equivalent)
    EffectiveTrustLevel int       // min(delegator, executor)
    DelegationDepth     int       // monotone, starts at 0
    MaxDepth            int       // default 3 (matches CloudSwarm MAX_DELEGATION_DEPTH)
    DelegationToken     string    // base64url(payload).base64url(hmac)
    ParentTaskID        string
    BudgetReserved      float64   // USD, atomic pre-reservation
    Nonce               string    // 16B base64url; replay-protection key
    ExpiresAt           time.Time // absolute; token verifier enforces ±30s skew
}
```

Backward compat: zero-value `MaxDepth == 0` is treated as `3` by `VerifyDelegation`. Zero `EffectiveTrustLevel` + zero `DelegatorTrustLevel` on a non-token path means "legacy local delegation" and skips trust-clamp (logged once at warn level).

### `TokenPayload` (internal/delegation/token.go)

```go
type TokenPayload struct {
    Version      int      `json:"v"`           // always 1 in this spec
    TokenID      string   `json:"tok_id"`      // UUIDv7 preferred
    Parent       string   `json:"parent"`      // parent agent id
    Child        string   `json:"child"`       // child agent id
    EffTrust     int      `json:"eff_trust"`   // min(parent, child)
    Actions      []string `json:"actions"`     // optional action allowlist
    ParentTask   string   `json:"parent_task"`
    Depth        int      `json:"depth"`
    MaxDepth     int      `json:"max_depth"`
    BudgetUSD    float64  `json:"budget_usd"`
    Nonce        string   `json:"nonce"`       // 16B base64url
    IssuedAt     int64    `json:"iat"`         // unix seconds
    ExpiresAt    int64    `json:"exp"`
    KeyID        string   `json:"kid"`         // for rotation (primary|previous)
}
```

Wire form: `base64url(JSON(payload)) + "." + base64url(HMAC_SHA256(key, JSON(payload)))`.

### `AgentCard` (consumed from `a2a-go/v2/a2apb`)
Served at `/.well-known/agent-card.json`. See §"A2A card example" for full signed JSON.

## API / CLI Surface

### `stoke serve` — Hireable mode
**File:** `cmd/r1/serve_cmd.go` (new)

```
stoke serve --port 8080 --caps code,research,browser,deploy [--card-key $STOKE_A2A_CARD_KEY]
```

| Flag | Type | Default | Purpose |
|------|------|---------|---------|
| `--port` | int | 8080 | HTTP listen port |
| `--caps` | csv | `code` | Skills advertised on card (code, research, browser, deploy, audit) |
| `--card-key` | path | `$STOKE_A2A_CARD_KEY` | Ed25519 private-key PEM for signing card |
| `--extension` | csv | `x402` | A2A extensions to advertise (x402 required for paid tasks) |
| `--bind` | string | `0.0.0.0` | Listen address |
| `--healthz` | bool | true | Register `/healthz` + `/readyz` |

### `stoke delegate` — dry-run / ad-hoc
**File:** `cmd/r1/delegate_cmd.go` (new)

```
stoke delegate --spec "translate README to ja" --budget 5.00 --trust 3 [--dry-run]
```

Dry-run prints the delegation plan (candidate agent URLs, trust clamp, reserved budget, token payload) without issuing the SendMessage call. Used for AC: `./stoke delegate --spec "translate to ja" --budget 5.00 --dry-run`.

## Business Logic

### Part 1 — Trust-clamp on `NewDelegation`

```
INPUT:  parent *DelegationContext (nil for root), executorTrust int,
        budgetUSD float64, actions []string, ttl time.Duration,
        reserve BudgetReserver, signer Signer
OUTPUT: child *DelegationContext, error
```

1. Compute `parentTrust := 4` (root) OR `parent.EffectiveTrustLevel`.
2. `effective := min(parentTrust, executorTrust)` — SEC-ITEM-2.
3. `depth := 0` (root) OR `parent.DelegationDepth + 1`.
4. If `depth >= MaxDepth (default 3)`: return `ErrDepthExceeded` immediately (no budget reserve, no signer call).
5. `reserved, err := reserve.Reserve(ctx, accountID, delegationID, budgetUSD)` — atomic; zero = `ErrBudgetReservationFailed`.
6. Build `TokenPayload` with clamped trust, depth, reserved budget, fresh 16B nonce, `iat=now`, `exp=now+ttl`.
7. `token, err := signer.Sign(payload)`.
8. Return populated child `DelegationContext`.

### Part 2 — HMAC verify (`VerifyDelegation`)

1. Split token on `.` → payloadB64, macB64.
2. Base64url-decode both; unmarshal payload JSON.
3. Pick key by `payload.KeyID`: try `STOKE_DELEGATION_SECRET` (current), fall back to `STOKE_DELEGATION_SECRET_OLD` (rotation grace).
4. Recompute HMAC-SHA256(key, payloadJSON); `subtle.ConstantTimeCompare` with provided mac.
5. Validate `iat ≤ now+30s` AND `exp ≥ now-30s` (skew window).
6. Check nonce bucket in bbolt: if `tok_id` present → `ErrNonceReplay`. Else write with TTL = `2 × ttl`.
7. Sanity-check: `payload.Depth < payload.MaxDepth`, `payload.EffTrust ≤ min(payload.parent inferred, payload.child inferred)`.
8. Return typed errors for each failure (ErrBadSignature, ErrTokenExpired, ErrNonceReplay, ErrDepthExceeded, ErrTrustClampViolation).

### Part 3 — Budget reservation + reconcile (budget.go)

Mirrors CloudSwarm V-69 atomic Lua pattern using `costtrack/` CAS primitives:

**Reserve(ctx, accountID, delegationID, budgetUSD) (float64, error)**
1. Atomic-load current `accountRemaining` from costtrack.
2. `granted := min(budgetUSD, accountRemaining)`.
3. CAS-subtract `granted` from account balance; retry up to 3 times on conflict.
4. Write reservation row `{delegation_id, account_id, granted, ts, status=pending}` to new `reservations` table (SQLite).
5. Return `granted` (0 = hard-deny; caller MUST abort).

**Reconcile(ctx, delegationID, actualUSD) error** — three branches:
- **Happy path (actual ≤ reserved):** charge `actual`, refund `reserved - actual` back to account, mark reservation `settled`. Emit `cs.delegation.completed`.
- **Over-budget (actual > reserved):** charge `reserved`, leave overrun UNCHARGED (trust-penalty event emitted for provider), mark reservation `over_budget`. Emit `cs.delegation.budget_exceeded`. Trust penalty delta = `(actual-reserved)/reserved` coerced into L0..L4 step.
- **Failure (child errored):** refund `reserved` fully, mark `refunded`. Emit `cs.delegation.failed`.

Expiry sweep: background goroutine scans `reservations WHERE status=pending AND iat+ttl < now` and reconciles with `actual=0` (full refund) + emits `cs.delegation.expired`.

### Part 4 — Saga BudgetRefund compensator

`internal/delegation/saga.go` already ships three settlement policies (rollback-immediately, complete-then-revoke, checkpoint-and-revoke). Add one compensator type:

```go
// BudgetRefund returns a CompensatingTxn that calls budget.Reconcile
// with actual=0 (full refund). Registered via Saga.Register alongside
// other comp txns. Fires on revocation under any of the three policies.
func (m *Manager) BudgetRefund(dctx *DelegationContext) CompensatingTxn {
    return func(ctx context.Context) error {
        if err := m.budget.Reconcile(ctx, dctx.DelegationID, 0.0); err != nil {
            return err
        }
        m.bus.Publish(bus.Event{Kind: "delegation.budget_refunded", Data: map[string]any{
            "delegation_id": dctx.DelegationID,
            "refund_usd":    dctx.BudgetReserved,
        }})
        return nil
    }
}
```

Wired into saga via `Register(unit, anchor, append(comps, mgr.BudgetRefund(dctx)), snapshot)` at delegation dispatch time.

### Part 5 — A2A client (internal/a2a/client.go)

Wraps `a2a-go/v2/a2aclient`:

```go
func FetchCard(ctx, cardURL string) (*a2apb.AgentCard, error)
  // calls a2aclient.FetchCard; verifies Ed25519 signature via card.Signature.
  // cache hit: map[cardURL]cachedCard, TTL = card.cacheControl or 10min.

func NewClient(ctx, card *a2apb.AgentCard, authToken string) (Client, error)
  // calls a2aclient.FromCard; wires OAuth bearer when card advertises oauth2 scheme.

type Client interface {
    SendMessage(ctx, msg *a2apb.Message) (*a2apb.Task, error)
    SendStreamingMessage(ctx, msg *a2apb.Message) (taskID string, events <-chan a2apb.Event, err error)
    GetTask(ctx, taskID string) (*a2apb.Task, error)
    SubscribeToTask(ctx, taskID string) (<-chan a2apb.Event, error)
    CancelTask(ctx, taskID string) error
}
```

**State mapping** (A2A TaskState → internal):
| A2A | Internal |
|-----|----------|
| `SUBMITTED` | `DelegationPending` |
| `WORKING` | `DelegationInFlight` |
| `INPUT_REQUIRED` | `DelegationNeedsInput` (bubbles to operator via HITL from spec-2) |
| `AUTH_REQUIRED` | `DelegationAuthPending` (TrustPlane re-auth) |
| `COMPLETED` | `DelegationSucceeded` → reconcile(actual) |
| `FAILED` | `DelegationFailed` → reconcile(0) |
| `CANCELED` | `DelegationCanceled` → reconcile(0) |
| `REJECTED` | `DelegationRejected` → reconcile(0) + try-different-agent |

### Part 6 — DelegateExecutor (internal/executor/delegate.go)

Implements `Executor` from spec-3.

**Execute(ctx, plan, effort) (DelegationDeliverable, error)**
1. `candidates := trustplane.FindCapable(ctx, plan.RequiredCapabilities, budget=effort.Budget, minTrust=plan.MinTrust)`.
2. If zero candidates: return `ErrNoCapableAgent`.
3. `best := selectBest(candidates, plan)` — uses memory reliability score (spec-7) if available, else random pick.
4. `dctx := delegation.NewDelegation(parent=plan.Parent, executorTrust=best.TrustLevel, budgetUSD=effort.Budget, actions=plan.Actions, ttl=plan.TTL, ...)` — Part 1.
5. `card := a2a.FetchCard(ctx, best.CardURL)`; `cli := a2a.NewClient(ctx, card, tpAuthToken)`.
6. Build `a2apb.Message{Parts: [{Text: plan.Spec}], Metadata: {"stoke.delegationToken": dctx.DelegationToken, "stoke.trustplane.escrowId": escrowID, "budgetUsd": effort.Budget}}`.
7. `taskID, events, err := cli.SendStreamingMessage(ctx, msg)`; on err → `reconcile(0)` + try next candidate (up to 3 agents).
8. Stream loop: collect `TaskArtifactUpdateEvent` artifacts; on terminal `TaskStatusUpdateEvent` return:
   - `COMPLETED` → `DelegationDeliverable{Artifacts, Cost, AgentURL, TaskID}` + `reconcile(actualCost)`.
   - `FAILED/REJECTED/CANCELED` → `reconcile(0)`, return typed error.
9. Save deliverable to session store; emit `cs.delegation.completed` with content-addressed artifact IDs.

**BuildCriteria(task, deliverable) []AcceptanceCriterion**
- AC-delivery-complete: `VerifyFunc` = all declared artifacts present + non-empty.
- AC-delivery-matches-spec: `VerifyFunc` = reviewer LLM (claude-sonnet-4-6) judges artifact vs. `plan.Spec`. Passes when judge returns `match: true`.
- Task-type-specific mechanical checks:
  - **Translation:** `len(output.text) / len(input.text) ∈ [0.7, 1.6]` (language-pair tolerance).
  - **Image:** dimensions match `spec.Width × spec.Height ± 2px`; file is valid PNG/JPEG header.
  - **Audit:** artifact covers all functions declared in `plan.TargetSymbols` (grep each symbol in report).
  - **Research:** each claim has a citation URL that returns 200 (uses browser from spec-4 if present).

**BuildRepairFunc(plan) RepairFunc**
```go
func(ctx, directive) error {
    // Option A: revision request via A2A
    revMsg := &a2apb.Message{
        Parts: []*a2apb.Part{{Text: "REVISE: " + directive}},
        Metadata: map[string]any{"stoke.revisionOf": plan.OriginalTaskID},
    }
    _, err := cli.SendMessage(ctx, revMsg)
    if err != nil || revisionsRemaining == 0 {
        // Option B: dispute + hire different agent
        trustplane.OpenDispute(ctx, dctx.DelegationID, "revision_refused")
        return delegateToNextCandidate(ctx, plan)
    }
    return nil
}
```

**BuildEnvFixFunc() EnvFixFunc** — delegation never self-fixes env; always returns "retry with different agent" signal.

### Part 7 — `stoke serve` wiring

```go
// cmd/r1/serve_cmd.go
cardKeyPEM := os.Getenv("STOKE_A2A_CARD_KEY")
edPriv := ed25519.PrivateKey(parsePEM(cardKeyPEM))
card := buildSignedCard(caps, edPriv, publicURL)  // signs card JSON with edPriv

exec := &stokeServerExecutor{app: a}  // implements a2asrv.AgentExecutor
h := a2asrv.NewRequestHandler(exec)

mux := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", a2asrv.CardHandler(card))
mux.Handle("/.well-known/jwks.json",       jwksHandler(edPriv.Public()))
mux.Handle("/a2a/",          a2asrv.RESTHandler(h))
mux.Handle("/a2a/jsonrpc",   a2asrv.JSONRPCHandler(h))
mux.HandleFunc("/healthz", healthz)
mux.HandleFunc("/readyz",  readyz)
http.ListenAndServe(":"+port, mux)
```

`stokeServerExecutor` translates incoming `SendMessage` into an internal `mission.Runner` invocation (or `internal/executor/code.Execute` when spec-3 is present). Each `TaskArtifactUpdateEvent` streams a diff/log; terminal state set from `verify.Pipeline` outcome (`COMPLETED` on all-pass, `FAILED` on code_bug, `REJECTED` on out-of-scope skill).

### Part 8 — a2a-x402 payment gating

- Advertise `"https://a2a-protocol.org/extensions/x402/v0.1"` in card `extensions[]`.
- On `SendMessage` receipt, server emits a `payment-required` message (A2A `INPUT_REQUIRED` state) containing a TrustPlane quote ID when `extensions` header includes the x402 URI.
- Client returns `payment-submitted` message with `{trustplane.escrowId, amountUsd, quoteId}` in metadata.
- Server verifies escrow via `trustplane.VerifyEscrow(escrowId)`; on success transitions `INPUT_REQUIRED → WORKING`.
- Terminal `COMPLETED` → `trustplane.Release(escrowId, actualUsd)` + emit `payment-completed`.
- Terminal `FAILED/REJECTED` → `trustplane.Refund(escrowId)`.
- **No core-card `pricing` field** (would break signature verification by third-party clients per RT-03 §9). Pricing lives in the x402 extension sub-document advertised via the extension URI.

### Part 9 — Card signing (Ed25519)

- Env var `STOKE_A2A_CARD_KEY` = PEM-encoded Ed25519 private key (32-byte seed or 64-byte key).
- **Separate from `STOKE_DELEGATION_SECRET`** (different threat model: card is signed for public verification, delegation token is symmetric HMAC for in-cluster auth).
- Card signature block: `{"alg": "EdDSA", "kid": "<sha256[:16] of pub key>", "value": "<base64url ed25519 sig over canonical JSON of card sans signature field>"}`.
- Publish JWKS at `/.well-known/jwks.json` so A2A consumers can verify without out-of-band key distribution.
- Key rotation: maintain N-1 + N keys in JWKS; card uses current `kid`; verifiers look up by `kid`.

## A2A Card Example

Full signed card served at `https://stoke.relay.one/.well-known/agent-card.json`:

```json
{
  "id": "stoke.relay.one/agent",
  "displayName": "Stoke",
  "description": "Autonomous Go/TS code agent with verification descent",
  "provider": { "name": "relay", "url": "https://relay.one" },
  "service": {
    "url": "https://stoke.relay.one/a2a",
    "protocol": "JSONRPC",
    "version": "1.0"
  },
  "capabilities": {
    "streaming": true,
    "pushNotifications": true,
    "extendedAgentCard": true
  },
  "skills": [
    { "id": "code", "name": "Code generation + repair", "tags": ["go","typescript","python","full-stack"],
      "inputModes": ["text/plain","application/json"],
      "outputModes": ["text/x-diff","application/json"] },
    { "id": "research", "name": "Indexed web research", "tags": ["search","citation","synthesis"],
      "inputModes": ["text/plain"], "outputModes": ["text/markdown"] },
    { "id": "browser", "name": "Browser-based verification", "tags": ["rod","screenshot","smoketest"],
      "inputModes": ["application/json"], "outputModes": ["image/png","application/json"] },
    { "id": "deploy", "name": "Fly.io / Vercel deploys", "tags": ["deploy","rollback"],
      "inputModes": ["application/json"], "outputModes": ["application/json"] }
  ],
  "extensions": [
    "https://a2a-protocol.org/extensions/x402/v0.1",
    "https://relay.one/trustplane/v1"
  ],
  "securitySchemes": {
    "oauth": { "type": "oauth2", "flows": { "clientCredentials": { "tokenUrl": "https://trustplane.relay.one/token", "scopes": { "tasks:write": "create+run tasks" } } } },
    "mtls":  { "type": "mutualTLS" }
  },
  "security": [ { "oauth": ["tasks:write"] } ],
  "signature": {
    "alg": "EdDSA",
    "kid": "7a3f9b2c1e4d8a6f",
    "value": "<base64url Ed25519 sig of canonical JSON sans this signature field>"
  }
}
```

## a2a-x402 Extension URI
- **URI:** `https://a2a-protocol.org/extensions/x402/v0.1`
- **Advertised in:** `extensions[]` on card.
- **Request header (client):** `A2A-Extensions: https://a2a-protocol.org/extensions/x402/v0.1`
- **Payload schema** (`payment-required` message metadata):
```json
{
  "x402.quoteId":   "tp_q_<uuid>",
  "x402.amountUsd": 2.50,
  "x402.currency":  "USD",
  "x402.expiresAt": "2026-04-20T15:04:05Z",
  "x402.settlementProvider": "trustplane",
  "x402.escrowEndpoint": "https://trustplane.relay.one/escrow"
}
```
- **`payment-submitted` metadata:** `{"x402.escrowId": "tp_e_<uuid>", "x402.quoteId": "...", "x402.signature": "..."}`.

## Budget Reservation Flow (sequence)

1. Executor calls `budget.Reserve(accountID, delegationID, budgetUSD)`.
2. CAS-subtract from `costtrack` atomic counter (3 retries on conflict).
3. Write `reservations` row (status=pending). Emit bus `delegation.budget_reserved`.
4. Signer embeds `reserved` into `TokenPayload.BudgetUSD`; HMAC over whole payload.
5. Child agent (server side) receives token; `VerifyDelegation` asserts `BudgetUSD == plan.amountUsd` (tamper check).
6. Child executes; costtrack accumulates `actualUSD`.
7. On terminal state: `budget.Reconcile(delegationID, actualUSD)` → happy/over/fail branch.
8. Reservation row status updates to settled/over_budget/refunded; refund returns unused funds to account counter via CAS-add.

## Saga Compensator Table

| Delegation outcome | Compensators fired (reverse order) | Settlement policy compatibility | Terminal status |
|---|---|---|---|
| Revoked mid-flight (NATS `trustplane.delegation.revoked`) | User comp txns + `BudgetRefund` | rollback-immediately | WorkUnitRevoked |
| Revoked but running long step | Deferred; comp runs after step | complete-then-revoke | WorkUnitRevoked |
| Revoked with resumable state | `snapshot` first, then `BudgetRefund` | checkpoint-and-revoke | WorkUnitRevoked |
| Child returned FAILED | `BudgetRefund` only (no user comps — no changes made) | any | WorkUnitFailed |
| Over-budget (actual > reserved) | None — `Reconcile` caps at reserved + emits trust penalty | any | WorkUnitCompleted (with penalty) |
| Token expired | `BudgetRefund` swept by reconciler | any | WorkUnitFailed |
| A→B→C→A cycle detected | No comp — rejected at `NewDelegation` before any side effects | any | n/a |

## Security Threat Matrix

| Threat | Mitigation | Enforcer (file:line) | RT-09 ref |
|---|---|---|---|
| **Confused deputy** (child trust=5 launders parent trust=3) | `effective = min(parent, child)`; Cedar `delegation:confused-deputy` re-checks on every tool call | `scoping.go:Authorize` + Cedar bundle | §1.2, §3 |
| **Scope tamper** (child silently raises `budget=$5 → $50`) | HMAC covers ALL payload fields incl. `BudgetUSD`, `EffTrust`, `Actions`, `MaxDepth` | `token.go:VerifyDelegation` | §3 row 1 |
| **Token replay** | 16B nonce cached in bbolt bucket for `2 × ttl`; `exp ≤ 15min`; `iat` skew ±30s | `token.go:VerifyDelegation` step 6 | §3 row 4 |
| **Delegation cycle** (A→B→C→A) | `DelegationDepth ≥ MaxDepth(3)` → `ErrDepthExceeded` at step 0 of `NewDelegation` | `scoping.go:NewDelegation` | §3 row 3 |
| **Scope-creep via LLM input** | `is_delegated` NEVER read from LLM-shaped input; only from verified HMAC payload (CloudSwarm's V-114 insight applied real) | `token.go:Verify` only entry | §1.3 |
| **Parent revoked mid-flight** | NATS `trustplane.delegation.revoked` → `Saga.OnRevocation` → comp txns incl. `BudgetRefund` | `saga.go:OnRevocation` | §3 row 5 |
| **Budget double-spend race** | Atomic CAS on costtrack counter + reservations table UNIQUE(delegation_id) | `budget.go:Reserve` | §1.4 |
| **Kill-switch bypass** | `delegation:global-kill-switch` Cedar rule denies all actions when active; context flag honored in `Authorize` | `scoping.go:Authorize` + Cedar | §3 row 7 |
| **Card tampering** | Card signed with Ed25519; verifier fetches `/.well-known/jwks.json` by `kid` | `a2a/client.go:FetchCard` verify | RT-03 §3 |
| **Key exposure** | HMAC key rotation via `STOKE_DELEGATION_SECRET_OLD` grace window; card-key separate from delegation key | `token.go:keysFromEnv` | RT-09 §1.3 |

## CloudSwarm-Reuse Note

When Stoke runs INSIDE CloudSwarm (detected via `CLOUDSWARM_DELEGATION_ENDPOINT` env var set by CloudSwarm's worker pod), we CAN skip our local signer and call CloudSwarm's Temporal `generate_delegation_token` activity instead — one less key to manage, unified audit. But:

1. CloudSwarm's V-114 choke point means their verifier is currently a no-op; Stoke's local `VerifyDelegation` MUST still run even when CloudSwarm signs.
2. Env-var switch: `STOKE_DELEGATION_SIGNER=cloudswarm|local` (default `local`). When `cloudswarm`, `NewDelegation` posts payload to `$CLOUDSWARM_DELEGATION_ENDPOINT/sign` and receives token back. Verify path unchanged.
3. Key in `cloudswarm` mode: fetched from CloudSwarm's JWKS (`$CLOUDSWARM_DELEGATION_ENDPOINT/jwks`) and cached 10min.
4. Document the discrepancy between CloudSwarm MAX_DELEGATION_DEPTH=3 (Temporal path) and MAX_DEPTH=10 (trustplane_delegation Pydantic/Redis variant): Stoke picks **3** regardless of signer per RT-09 §7.

This gives CloudSwarm-hosted Stoke operators a single-key story while preserving standalone correctness.

## Error Handling

| Failure | Strategy | User sees | Exit code |
|---|---|---|---|
| `ErrDepthExceeded` at NewDelegation | immediate fail; no budget touched | "delegation depth 3 exceeded; refactor to narrower subtask" | 1 |
| `ErrBudgetReservationFailed` | immediate fail; no token issued | "budget reservation failed (account remaining $X < requested $Y)" | 2 |
| `ErrBadSignature` on verify | reject + audit event `delegation.token_tampered` | 403 on A2A call; `FAILED` state | n/a |
| `ErrTokenExpired` | reject with 401 + hint to re-issue | "delegation token expired; reissue via parent" | n/a |
| `ErrNonceReplay` | reject + audit event `delegation.replay_attempted` | 409 on A2A call | n/a |
| A2A SendMessage network error | 3 retries w/ expo backoff; then fail to next candidate | "agent unreachable after 3 attempts, trying next" | n/a |
| No capable agents found | `ErrNoCapableAgent` | "no A2A agents match capabilities=[...] + budget $X + trust≥N" | 1 |
| Card signature invalid | reject card + cache negative result 60s | "card signature invalid for <url>; skipping" | n/a |
| Child FAILED/REJECTED | `Reconcile(0)` + try next candidate (up to 3) | "agent X failed; trying agent Y" | 1 if all fail |
| Over-budget (actual > reserved) | charge up to reserved, emit trust penalty, mark over_budget | "delegation completed over-budget; penalty applied to provider" | 0 (soft) |
| Revocation mid-flight | saga fires BudgetRefund + user comps | "delegation revoked; N units rolled back" | 1 |

## Boundaries — What NOT To Do
- Do NOT replace existing `DelegationContext` fields — extend only (backward compat).
- Do NOT replicate CloudSwarm's V-114 no-op choke-point verifier — ship real HMAC verify from day 1.
- Do NOT add `pricing` field to the core A2A card — breaks third-party signature verification (RT-03 §9). Pricing lives in x402 extension sub-doc.
- Do NOT read `is_delegated` from LLM-shaped input — only from HMAC-verified token.
- Do NOT hand-roll JSON-RPC handlers — use `a2a-go/v2/a2asrv`.
- Do NOT share `STOKE_A2A_CARD_KEY` (Ed25519) with `STOKE_DELEGATION_SECRET` (HMAC). Different threat models.
- Do NOT implement the TrustPlane OAuth 2.0 DPoP flow (exists in `internal/trustplane/dpop/`).
- Do NOT implement the Executor interface here (defined in spec-3); depend on it.
- Do NOT implement browser verification for claim AC (spec-4 provides).
- Do NOT persist the HMAC secret to disk — env-only.
- Do NOT allow `MaxDepth > 3` for task delegation (matches CloudSwarm MAX_DELEGATION_DEPTH + Cedar `delegation:max-depth`).

## Testing

### Part 1 — DelegationContext trust clamp
- [ ] Happy: parent trust=3, executor trust=4 → `EffectiveTrustLevel=3`, depth=1
- [ ] Clamp: parent trust=4, executor trust=2 → `EffectiveTrustLevel=2`
- [ ] Depth limit: parent depth=2, new depth=3 rejected with `ErrDepthExceeded`; budget NOT reserved
- [ ] Root delegation (parent nil): `DelegatorTrustLevel=4`, `DelegationDepth=0`

### Part 2 — HMAC token round-trip
- [ ] `Sign` then `Verify` on same payload → no error, all fields preserved
- [ ] Tampered payload byte → `ErrBadSignature`
- [ ] Tampered signature byte → `ErrBadSignature`
- [ ] `exp` in past → `ErrTokenExpired`
- [ ] `iat` 5min in future → `ErrTokenExpired` (skew exceeded)
- [ ] Key rotation: signed with `OLD`, verified with both keys loaded → pass; only `NEW` loaded → `ErrBadSignature`

### Part 3 — Nonce replay
- [ ] First `Verify` with nonce N → pass; nonce stored
- [ ] Second `Verify` with same nonce → `ErrNonceReplay`
- [ ] After TTL expiry, same nonce → pass again (swept from bbolt)

### Part 4 — Budget reserve + reconcile
- [ ] Reserve $5 from $100 account → `granted=5`, account balance $95
- [ ] Reserve $200 from $100 → `granted=100`, account $0
- [ ] Reserve from $0 → `granted=0`, error
- [ ] Reconcile(actual=$3) on $5 reservation → charge $3, refund $2, account $98
- [ ] Reconcile(actual=$10) on $5 reservation → charge $5, over_budget flag, trust penalty emitted
- [ ] Reconcile(actual=$0) on $5 reservation → refund $5, account $100

### Part 5 — A2A client
- [ ] `FetchCard` with valid signature → returns card + caches
- [ ] `FetchCard` with tampered signature → error, negative cache 60s
- [ ] `SendMessage` returns taskID; `GetTask(taskID)` returns same
- [ ] `SendStreamingMessage` emits SSE events in order: WORKING → artifact → COMPLETED
- [ ] A2A state mapping: REJECTED → `DelegationRejected` + reconcile(0)

### Part 6 — DelegateExecutor
- [ ] `Execute` with no capable agents → `ErrNoCapableAgent`
- [ ] `Execute` happy path: token issued, task streamed, deliverable returned, reconcile charged
- [ ] `Execute` first agent FAILED → automatically tries second
- [ ] `BuildCriteria` for translation: len ratio 0.7-1.6 → pass; 0.3 → fail
- [ ] `BuildRepairFunc`: first call sends REVISE; second call disputes + tries different agent

### Part 7 — `stoke serve`
- [ ] `./stoke serve --help | grep -q 'caps'`
- [ ] Card served at `/.well-known/agent-card.json` with 200
- [ ] Card signature verifies with published JWKS
- [ ] Incoming `SendMessage` routed to mission runner; artifact stream emitted

### Part 8 — x402 extension
- [ ] Server replies `payment-required` on INPUT_REQUIRED when x402 ext present
- [ ] Client supplies escrowId; state transitions to WORKING
- [ ] COMPLETED → `trustplane.Release` called; FAILED → `trustplane.Refund` called
- [ ] `pricing` field absent from core card (signature still verifies)

### Part 9 — Card signing
- [ ] Card signed with Ed25519 seed from `STOKE_A2A_CARD_KEY`
- [ ] `kid` matches sha256[:16] of public key
- [ ] Missing env var → `stoke serve` refuses to start with clear error

## Acceptance Criteria

- WHEN a parent delegation has `EffectiveTrustLevel=3` and an executor advertises trust=4, THE SYSTEM SHALL clamp the child's `EffectiveTrustLevel` to 3 before signing the token.
- WHEN `DelegationDepth >= MaxDepth`, THE SYSTEM SHALL reject `NewDelegation` with `ErrDepthExceeded` BEFORE any budget reservation or signer call.
- WHEN a delegation token is replayed within `2 × ttl`, THE SYSTEM SHALL reject via `ErrNonceReplay` and emit `delegation.replay_attempted` to the bus.
- WHEN an HMAC verifier sees a token signed under the rotated-out secret, THE SYSTEM SHALL accept if `STOKE_DELEGATION_SECRET_OLD` is set AND within grace window; else reject.
- WHEN a hired agent's A2A task terminates in `COMPLETED`, THE SYSTEM SHALL call `budget.Reconcile(delegationID, actualCost)` and emit `cs.delegation.completed`.
- WHEN a hired agent returns `FAILED` or `REJECTED`, THE SYSTEM SHALL refund the full reservation and attempt the next candidate (up to 3 total).
- WHEN a delegation is revoked via `trustplane.delegation.revoked`, THE SYSTEM SHALL fire the `BudgetRefund` compensator alongside any user-registered comp txns.
- WHEN `stoke serve` starts, THE SYSTEM SHALL publish a signed agent card at `/.well-known/agent-card.json` AND a JWKS at `/.well-known/jwks.json`.
- WHEN an incoming A2A message arrives with no x402 escrow receipt AND the card advertises x402, THE SYSTEM SHALL respond with `INPUT_REQUIRED` + `payment-required` metadata.
- WHEN running inside CloudSwarm with `STOKE_DELEGATION_SIGNER=cloudswarm`, THE SYSTEM SHALL delegate token signing to CloudSwarm's `generate_delegation_token` endpoint but run its own local `VerifyDelegation`.

### AC bash commands (executable)

```bash
go test ./internal/delegation/... -run TestTrustClamp
go test ./internal/delegation/... -run TestHMACRoundTrip
go test ./internal/delegation/... -run TestNonceReplay
go test ./internal/delegation/... -run TestDepthLimit
go test ./internal/delegation/... -run TestBudgetReserveReconcile
go test ./internal/delegation/... -run TestSagaBudgetRefund
go test ./internal/a2a/... -run TestFetchCard
go test ./internal/a2a/... -run TestStateMapping
go test ./internal/executor/... -run TestDelegateExecutor
go test ./internal/executor/... -run TestBuildCriteriaTranslation
./stoke serve --help | grep -q 'caps'
./stoke serve --help | grep -q 'port'
./stoke delegate --spec "translate to ja" --budget 5.00 --dry-run
curl -s http://localhost:8080/.well-known/agent-card.json | jq -e '.capabilities.streaming == true'
curl -s http://localhost:8080/.well-known/agent-card.json | jq -e '.extensions | index("https://a2a-protocol.org/extensions/x402/v0.1")'
curl -s http://localhost:8080/.well-known/jwks.json | jq -e '.keys[0].kty == "OKP"'
```

## Implementation Checklist

1. [ ] **Extend `DelegationContext`** in `internal/delegation/scoping.go`: add `DelegatorAgentID`, `DelegatorTrustLevel`, `EffectiveTrustLevel`, `DelegationDepth`, `MaxDepth`, `DelegationToken`, `ParentTaskID`, `BudgetReserved`, `Nonce`, `ExpiresAt` fields verbatim from RT-CLOUDSWARM-MAP §6. Do NOT remove existing fields. Zero-value `MaxDepth=0` treated as 3 for backward compat.

2. [ ] **Create `internal/delegation/token.go`**: `TokenPayload` struct per spec, `Sign(payload, key) (string, error)` using HMAC-SHA256 + base64url-no-padding, `Verify(token, keys map[string][]byte, nonces NonceStore) (TokenPayload, error)` with constant-time compare + ±30s skew window + bbolt nonce check. Keys loaded via `keysFromEnv()` reading `STOKE_DELEGATION_SECRET` (primary) + optional `STOKE_DELEGATION_SECRET_OLD` (rotation grace).

3. [ ] **Create `internal/delegation/nonces.go`**: BBolt-backed `NonceStore` interface with `Put(tokID string, ttl time.Duration) error` + `Has(tokID) bool`. Background goroutine sweeps expired entries hourly. Bucket name `"delegation_nonces"`.

4. [ ] **Create `internal/delegation/budget.go`**: `Reservations` SQLite table (`id, delegation_id UNIQUE, account_id, granted_usd, actual_usd, status, iat, ttl`). `Reserve(ctx, accountID, delegationID, budgetUSD) (float64, error)` uses `costtrack` CAS primitives with 3 retries. `Reconcile(ctx, delegationID, actualUSD) error` with three branches: happy/over/fail. Expiry sweep goroutine emits `cs.delegation.expired`.

5. [ ] **Add `NewDelegation` + `VerifyDelegation` top-level functions** in `internal/delegation/delegation.go`: `NewDelegation(parent *DelegationContext, executorTrust int, budgetUSD float64, actions []string, ttl time.Duration, reserve BudgetReserver, signer Signer) (*DelegationContext, error)` — clamps trust, bumps depth, reserves budget, signs token. `VerifyDelegation(ctx, dctx *DelegationContext, keys map[string][]byte, nonces NonceStore) error` — checks HMAC, depth, nonce, expiry, trust clamp.

6. [ ] **Extend Saga with `BudgetRefund` compensator**: in `internal/delegation/saga.go` (or new `saga_budget.go`), add `Manager.BudgetRefund(dctx *DelegationContext) CompensatingTxn` that calls `budget.Reconcile(id, 0.0)` + emits `delegation.budget_refunded`. At delegation dispatch time, append this comp to `Saga.Register`'s `compensatingTxns` slice.

7. [ ] **Create `internal/a2a/client.go`**: wraps `a2a-go/v2/a2aclient`. Exports `FetchCard(ctx, url) (*a2apb.AgentCard, error)` with Ed25519 signature verification + TTL cache (map + `sync.RWMutex`, default 10min), `NewClient(ctx, card, authToken) (Client, error)`, `Client` interface with `SendMessage`, `SendStreamingMessage`, `GetTask`, `SubscribeToTask`, `CancelTask`. A2A→internal state mapping in `internal/a2a/states.go`.

8. [ ] **Create `internal/executor/delegate.go`**: `DelegateExecutor` struct implements `Executor` from spec-3. `Execute(ctx, plan, effort)` flow per Part 6 (FindCapable → select → NewDelegation → FetchCard → SendStreamingMessage → collect artifacts → reconcile). `BuildCriteria(task, deliverable)` returns delivery-complete + delivery-matches-spec + task-type-specific ACs (translation len ratio, image dims, audit symbol coverage, research citation 200). `BuildRepairFunc(plan)` sends A2A REVISE or disputes + hires different agent. `BuildEnvFixFunc()` retries with different agent.

9. [ ] **Create `cmd/r1/serve_cmd.go`**: `stoke serve --port --caps --card-key --extension --bind --healthz` flags. Loads Ed25519 key from `$STOKE_A2A_CARD_KEY`, builds signed card via `buildSignedCard(caps, edPriv, publicURL)`, registers mux handlers `/.well-known/agent-card.json`, `/.well-known/jwks.json`, `/a2a/`, `/a2a/jsonrpc`, `/healthz`, `/readyz`. Refuses to start with clear error if `STOKE_A2A_CARD_KEY` unset.

10. [ ] **Create `internal/a2a/server.go`**: `stokeServerExecutor` implements `a2asrv.AgentExecutor`. `Execute(ctx, msg)` translates A2A `Message` into `mission.Runner` invocation (or `internal/executor/code.Execute` if spec-3 is merged). Emits `TaskArtifactUpdateEvent` for each diff/log. Terminal state from `verify.Pipeline`: `COMPLETED` on all-pass, `FAILED` on code_bug, `REJECTED` on out-of-scope skill.

11. [ ] **x402 extension handling**: in server, gate `WORKING` transition on `x402.escrowId` metadata presence + `trustplane.VerifyEscrow(escrowId)`. Emit `payment-required` + INPUT_REQUIRED when missing. Tie terminal COMPLETED → `trustplane.Release(escrowId, actualUsd)`; FAILED/REJECTED → `trustplane.Refund(escrowId)`. Advertise extension URI in card.

12. [ ] **Card signing utilities** in `internal/a2a/cardsign.go`: `SignCard(card *a2apb.AgentCard, priv ed25519.PrivateKey) error` populates signature block with `alg: EdDSA`, `kid: sha256[:16](pub)`, `value: base64url(ed25519.Sign(priv, canonicalJSON(card without signature)))`. `VerifyCardSignature(card *a2apb.AgentCard, jwks map[string]ed25519.PublicKey) error` — round-trip of the above. `buildJWKSResponse(pub ed25519.PublicKey) []byte` returns JWKS with OKP key type.

13. [ ] **CloudSwarm signer adapter** in `internal/delegation/signer_cloudswarm.go`: when `STOKE_DELEGATION_SIGNER=cloudswarm`, POST payload to `$CLOUDSWARM_DELEGATION_ENDPOINT/sign`, receive token back. Key cache from `$CLOUDSWARM_DELEGATION_ENDPOINT/jwks` with 10min TTL. Local `VerifyDelegation` still runs regardless. Default `local` signer uses env-var key directly.

14. [ ] **Create `cmd/r1/delegate_cmd.go`**: `stoke delegate --spec --budget --trust --dry-run` prints delegation plan (candidate URLs, clamp values, reserved budget, token payload) without calling SendMessage. Used for smoke tests + operator visibility.

15. [ ] **Bus event emission**: ensure all six lifecycle events (`cs.delegation.started`, `cs.delegation.completed`, `cs.delegation.budget_exceeded`, `cs.delegation.failed`, `cs.delegation.expired`, `delegation.budget_refunded`) publish via `internal/bus`. Mirror to `internal/streamjson` as `_stoke.dev/delegation.*` subtypes per decision C1.

16. [ ] **Tests** in `internal/delegation/token_test.go`, `budget_test.go`, `nonces_test.go`, `internal/a2a/client_test.go`, `internal/executor/delegate_test.go`, `cmd/r1/serve_cmd_test.go`. Each AC listed above must have a matching `go test -run` target. Integration smoke: boot `stoke serve`, curl card, verify signature, send a test message, assert lifecycle events emitted in order.

17. [ ] **Documentation**: update `specs/stoke-api.yaml` with `/delegate` and `/serve` surface. Add README section on `STOKE_DELEGATION_SECRET` rotation. Document env vars: `STOKE_DELEGATION_SECRET`, `STOKE_DELEGATION_SECRET_OLD`, `STOKE_DELEGATION_SIGNER`, `STOKE_A2A_CARD_KEY`, `CLOUDSWARM_DELEGATION_ENDPOINT`.
