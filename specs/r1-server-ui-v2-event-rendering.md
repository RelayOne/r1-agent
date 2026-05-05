<!-- STATUS: done -->
<!-- CREATED: 2026-05-05 -->
<!-- BUILD_STARTED: 2026-05-05 -->
<!-- BUILD_COMPLETED: 2026-05-05 -->
<!-- DEPENDS_ON: r1-server-ui-v2-foundation -->
<!-- BUILD_ORDER: 30 -->
<!-- BLOCKED_PARTIAL: T4 (compactor_evicted caller wiring) + T5 (scope_exit caller wiring). EmitSkillUnloaded helper shipped; integration deferred. See feat(TASK-4,TASK-5) commit. -->

# r1-server UI v2 — Event Rendering (skill load/unload + redaction)

## 1. Overview

Two cross-cutting render concerns share the same waterfall + 3D pipeline:

1. **Skill load/unload visualisation.** When a skill is injected into a stance's concern field (`skill_loaded` ledger node) or evicted by the compactor (`skill_unloaded`), the waterfall row gets a 🧬 icon, the 3D graph desaturates the skill node, and the side panel surfaces tokens injected + reason for unload. Spec r1-server-ui-v2 §7.
2. **Redacted-node rendering.** When a `redaction` ledger node references another node, the waterfall row shows a 🔒 lock, the side panel shows `[content redacted]` plus a list of signed redaction events, and the 3D graph desaturates + adds a lock sprite. Spec r1-server-ui-v2 §9.

These are grouped in one spec because they share:
- The same Go-side helper signature: `func IsRedacted(node) bool` mirrors `func IsSkillLoaded(node) bool`.
- The same waterfall template branch logic — both add slot icons + state badges per row.
- The same 3D-graph `setColorAt` + sprite-attach pattern — both render visibility transitions through the time scrubber from Spec 2.

The skill_unloaded ledger node was added in PR #150. The Skill type emission paths (skill_loaded by `internal/hub/builtin/skill_injector.go`, skill_unloaded by the compactor) are NOT yet wired — this spec wires them.

## 2. Stack & Versions

- Go (chi-style mux + html/template) for the helpers + template branches
- three.js InstancedMesh from Spec 2 for 3D rendering
- ledger-redaction spec already defines `Redaction` ledger node type + `ledger.Store.RedactionsFor(nodeID) []Redaction`
- ledger node types `SkillLoaded` (already shipped), `SkillUnloaded` (PR #150)
- internal/hub/builtin/skill_injector.go (existing) + microcompact (existing) for the emission hook points

## 3. Architecture

```
                                ┌─────────────────────┐
                                │  ledger.Store       │
  internal/hub/builtin/         │  + .Redactions(...) │
  skill_injector.go             │  + Skill events     │
  ├─emits skill_loaded ─────────┤                     │
  └─emits stoke.skill.loaded    └──────────┬──────────┘
                                            │
  internal/microcompact/                    ▼
  compact.go                       cmd/r1-server/redaction.go
  ├─emits skill_unloaded ────────► ├─ IsRedacted(node) bool
  └─emits stoke.skill.unloaded     └─ RedactionsFor(node)
                                          + skills.go
                                          ├─ IsSkillLoaded(node)
                                          ├─ IsSkillUnloaded(node)
                                          └─ SkillEventsFor(skillID)
                                                  │
                                                  ▼
                                          web/partials/
                                          ├─ waterfall-row.html  (slot branches)
                                          ├─ node-side-panel.html (placeholders)
                                          ├─ redaction-events.html
                                          └─ skill-events.html
                                                  │
                                                  ▼
                                          web/js/graph.js
                                          ├─ setColorAt for desat
                                          ├─ sprite-attach for lock
                                          └─ opacity for skill state
```

## 4. Helpers (Go)

### 4.1 `IsRedacted` + `RedactionMap`

Per RT-REDACTION-UI-PATTERNS recommendation: a per-node check + a batch-loaded map keyed by nodeID, populated once per request to avoid N+1 ledger queries.

```go
// cmd/r1-server/redaction.go

// IsRedacted returns true when at least one signed Redaction node
// references the target node. The check is O(1) against the
// RedactionMap loaded once per request.
type RedactionMap map[NodeID][]Redaction

func LoadRedactionMap(store ledger.Store, sessionID string) (RedactionMap, error)
func (m RedactionMap) IsRedacted(id NodeID) bool { return len(m[id]) > 0 }
func (m RedactionMap) Events(id NodeID) []Redaction { return m[id] }
```

### 4.2 `IsSkillLoaded` / `IsSkillUnloaded` / `SkillEventsFor`

```go
// cmd/r1-server/skills.go

// SkillEventMap groups skill_loaded + skill_unloaded events by
// (sessionID, skillID). The handler uses it to compute the active-
// state predicate at any cursor time during rendering.
type SkillEventMap map[string][]SkillEvent

type SkillEvent struct {
    Type   string    // "skill_loaded" | "skill_unloaded"
    SkillID string
    LoadID string   // SkillUnloaded.LoadRef
    Reason string   // SkillUnloaded.Reason
    At     time.Time
    Tokens int      // SkillLoaded.PromptTokensInjected
}

func LoadSkillEventMap(store ledger.Store, sessionID string) (SkillEventMap, error)
func (m SkillEventMap) IsActiveAt(skillID string, t time.Time) bool
```

## 5. Emission paths

### 5.1 `skill_loaded` emission

Hook in `internal/hub/builtin/skill_injector.go` `handle()` — at the end, after skills are selected and prompt is mutated:

```go
for _, skill := range selectedSkills {
    n := &nodes.SkillLoaded{
        SkillRef:              skill.ID,
        LoadingStanceID:       ev.StanceID,
        LoadingStanceRole:     ev.StanceRole,
        ConcernFieldTemplate:  ev.ConcernFieldTemplate,
        MatchingApplicability: skill.Applicability[0], // or full join
        TaskDAGScope:          ev.TaskID,
        LoopRef:               ev.LoopID,
        CreatedAt:             time.Now(),
        Version:               1,
    }
    if err := bus.PublishLedgerNode(ctx, n); err != nil { ... }
    bus.Emit(ctx, hub.EventStokeSkillLoaded, hub.Event{...})
}
```

### 5.2 `skill_unloaded` emission

Hook the compactor: search `internal/microcompact/compact.go` + `internal/context/compact*.go` for the eviction path. Right before evicting a skill from the active set:

```go
n := &nodes.SkillUnloaded{
    SkillRef:          skillID,
    LoadRef:           originatingLoadID,
    StanceID:          stanceID,
    StanceRole:        stanceRole,
    Reason:            "compactor_evicted",
    BudgetTokensFreed: tokensFreed,
    CreatedAt:         time.Now(),
    Version:           1,
}
bus.PublishLedgerNode(ctx, n)
bus.Emit(ctx, hub.EventStokeSkillUnloaded, hub.Event{...})
```

The scope-exit path (skill goes out of TaskDAGScope) emits with `Reason="scope_exit"`. The explicit-unload path (operator UI; future) emits with `Reason="explicit_unload"`.

## 6. Waterfall rendering

<!-- RESOLVED: content-visibility: auto + htmx server-paged chunks (hx-trigger="revealed") + server-side aggregation. Aggregate adjacent rows when same-type AND same-parent AND gap<50ms AND run-length>=3 AND no errors. Soft-collapsible: bus.event/tool.partial/stream.chunk/log.line/cache.*/prompt.token/model.heartbeat. Hard-protected (always shown): task.*/mission.*/consensus.*/error.*/verify.*/merge.*/snapshot.*. Typically reduces 5k rows to 2-3k visible. Fallback: Clusterize.js if FPS<50. See specs/research/raw/RT-WATERFALL-DENSITY.md. -->

Per RT-REDACTION-UI-PATTERNS §"Waterfall row":

```html
<!-- web/partials/waterfall-row.html -->
{{ define "waterfall-row" }}
<tr class="row {{ if .IsRedacted }}row--redacted{{ end }}"
    data-node-id="{{ .NodeID }}"
    data-node-type="{{ .NodeType }}"
    data-cursor="{{ .CreatedAtUnix }}"
    style="content-visibility: auto;">
  <td class="indent" style="--depth: {{ .Depth }};">{{ .Chevron }}</td>
  <td class="icon">
    {{- if .IsRedacted -}}
      <svg class="icon-lock" aria-hidden="true">{{ template "icon-lock" }}</svg>
    {{- else if eq .NodeType "skill_loaded" -}}
      🧬
    {{- else if eq .NodeType "skill_unloaded" -}}
      🧬❌
    {{- else -}}
      {{ .TypeIcon }}
    {{- end -}}
  </td>
  <td class="title">{{ .Title }}</td>
  <td class="duration">{{ .DurationStr }}</td>
  <td class="cost">{{ .CostStr }}</td>
  <td class="badge">{{ template "verify-badge" . }}</td>
  <td class="redacted-slot">
    {{- if .IsRedacted -}}
      <span aria-label="redacted" data-redacted="true">🔒</span>
    {{- end -}}
  </td>
</tr>
{{ end }}
```

Key: rows for redacted nodes are **not hidden or collapsed** — only the content slot shows the lock. Per RT findings: transparency about absence is the goal, not concealment.

## 7. Side panel

```html
<!-- web/partials/node-side-panel.html -->
{{ define "node-side-panel" }}
<aside id="side-panel" data-node-id="{{ .NodeID }}">
  <header>
    <h2>{{ .NodeType }} — {{ .ShortHash }}</h2>
    <dl>
      <dt>id</dt><dd>{{ .NodeID }}</dd>
      <dt>created_at</dt><dd>{{ .CreatedAt.UTC.Format "2006-01-02 15:04:05" }} UTC</dd>
      <dt>created_by</dt><dd>{{ .CreatedBy }}</dd>
      {{ if .ParentHash }}<dt>parent</dt><dd><code>{{ .ParentHash | trunc 12 }}</code></dd>{{ end }}
    </dl>
  </header>

  {{ if .IsRedacted }}
    <section class="redacted-placeholder" aria-label="content redacted">
      <div class="redacted-block">[content redacted]</div>
      {{ template "redaction-events" .Redactions }}
    </section>
  {{ else }}
    <section class="content">
      {{ if eq .NodeType "agent_io" }}
        {{ template "agent-io-bubbles" . }}
      {{ else if eq .NodeType "skill_loaded" }}
        {{ template "skill-loaded-detail" . }}
      {{ else if eq .NodeType "skill_unloaded" }}
        {{ template "skill-unloaded-detail" . }}
      {{ else }}
        <pre><code>{{ .RawJSON | mustPretty }}</code></pre>
      {{ end }}
    </section>
  {{ end }}

  <footer>
    <a href="/api/session/{{ .SessionID }}/node/{{ .NodeID }}/raw"
       hx-get="..."
       hx-target="#raw-modal">View raw node JSON →</a>
  </footer>
</aside>
{{ end }}
```

```html
<!-- web/partials/redaction-events.html -->
{{ define "redaction-events" }}
<details class="redaction-events" open>
  <summary>{{ len . }} redaction event{{ if ne (len .) 1 }}s{{ end }} for this node</summary>
  <ul>
    {{ range . }}
    <li>
      <time datetime="{{ .RedactedAt.Format "2006-01-02T15:04:05Z" }}">{{ .RedactedAt.Format "2006-01-02 15:04:05" }} UTC</time>
      <span class="reason">{{ .ReasonHumanReadable }}</span>
      <span class="signer">by {{ .Signer }}</span>
      <code class="sig">{{ .SignatureHash | trunc 8 }}</code>
    </li>
    {{ end }}
  </ul>
</details>
{{ end }}
```

Per RT recommendation: reason wording is the *specific* cause (`"redacted by retention policy (90d)"`), never the bare word "redacted".

```html
<!-- web/partials/skill-loaded-detail.html -->
{{ define "skill-loaded-detail" }}
<dl class="skill-detail">
  <dt>skill</dt><dd>{{ .SkillName }} ({{ .SkillRef | trunc 12 }})</dd>
  <dt>loaded into</dt><dd>{{ .LoadingStanceRole }} <code>{{ .LoadingStanceID }}</code></dd>
  <dt>concern field</dt><dd>{{ .ConcernFieldTemplate }}</dd>
  <dt>task scope</dt><dd>{{ .TaskDAGScope }}</dd>
  <dt>tokens injected</dt><dd>{{ .PromptTokensInjected }}</dd>
  {{ if .UnloadedAt }}<dt>unloaded</dt><dd>{{ .UnloadedAt }} ({{ .UnloadReason }})</dd>{{ end }}
</dl>
{{ end }}
```

## 8. 3D rendering

Per RT-REDACTION-UI-PATTERNS §"3D graph":

- **Redacted nodes:** desaturate to ~15% saturation × 0.7 lightness. Attach a flat-SVG lock sprite at `(0, +0.6 × radius, 0)`, billboarded toward the camera. Edges remain at full opacity (topology is non-sensitive). Hover label: `Node {id8} — redacted`.
- **Skill nodes:** between `skill_loaded.created_at` and the matching `skill_unloaded.created_at`, opacity 1.0. After unload, opacity 0.3. The time scrubber from Spec 2 toggles this.
- **Skill events themselves** (the `skill_loaded` / `skill_unloaded` ledger nodes) render as small 🧬 marker glyphs on the originating stance's lane in the 3D timeline view.

Implementation lives in Spec 2's `web/js/graph.js`. This spec adds a hook: `renderRedactionLayer(redactionMap)` and `renderSkillLayer(skillEventMap)` are called once per state-change rather than per frame; they iterate the InstancedMesh once and call `setColorAt` / `setMatrixAt` for the affected indices.

## 9. Boundaries

- This spec does NOT touch the 3D worker (Spec 2). It only writes to the existing InstancedMesh + sprite layer.
- This spec does NOT add new ledger node types — `Redaction` is from ledger-redaction, `SkillLoaded` is shipped, `SkillUnloaded` shipped via PR #150.
- This spec does NOT change the `microcompact` algorithm — only adds the emission hook.
- This spec does NOT cover the operator-driven explicit-unload UI ("explicit_unload" reason). That's a future spec.
- Accessibility: the lock SVG is `aria-hidden="true"`; meaning conveyed via adjacent `[content redacted]` text. The skill 🧬 emoji is fine for SR users (announces as "dna").

## 10. Implementation checklist (12 items — self-contained)

### Helpers

- [ ] Write `cmd/r1-server/redaction.go` with `RedactionMap`, `LoadRedactionMap(store, sessionID)`, `IsRedacted(id)`, `Events(id)` per §4.1. The loader walks the chain-tier ledger once + queries `ledger.Store.RedactionsFor(nodeID)` for each candidate redaction marker; cache-keyed by sessionID with a 60s TTL via `internal/util/cache.TTLCache`. Add 8+ unit tests in `redaction_test.go` covering: empty session, single redaction, multiple redactions per node, anomaly (`isRedacted=true` AND `len(events)==0`).
- [ ] Write `cmd/r1-server/skills.go` with `SkillEventMap`, `LoadSkillEventMap(store, sessionID)`, `IsActiveAt(skillID, t)`. The map indexes both `skill_loaded` and `skill_unloaded` ledger nodes by `(stanceID, skillRef)` so `IsActiveAt(t)` runs in O(log N) via a sorted-events binary search. Tests in `skills_test.go` covering: never-loaded, loaded-and-unloaded, double-load, scope-exit reason, compactor-evicted reason.

### Emission

- [ ] Wire `skill_loaded` emission in `internal/hub/builtin/skill_injector.go`: after skills are selected and the prompt is mutated, for each selected skill emit a `nodes.SkillLoaded` ledger node + a `hub.EventStokeSkillLoaded` bus event per §5.1. Add `skill_injector_test.go` fixture: inject 3 skills, assert 3 ledger nodes + 3 bus events, assert dedup-key `(skill_id, created_at)` distinguishes them.
- [ ] Wire `skill_unloaded` emission in `internal/microcompact/compact.go`: locate the eviction loop (grep for the existing `"evicting skill"` log line); right before mutation, emit a `nodes.SkillUnloaded` with `Reason="compactor_evicted"`, `BudgetTokensFreed=<n>`, `LoadRef=<originating skill_loaded node id>`. Add `compact_test.go` test: simulate budget overflow → 2 skills evicted → assert 2 SkillUnloaded events + correct LoadRef linkage.
- [ ] Wire scope-exit emission in `internal/agentloop/scope.go` (or wherever task-DAG scope is closed): when a stance leaves a task scope, emit `SkillUnloaded{Reason:"scope_exit"}` for each skill loaded into that scope. Test: simulate task completion → loaded skills get scope_exit emission.

### Waterfall + side panel templates

- [ ] Write `cmd/r1-server/ui/web/partials/waterfall-row.html` per §6: row with redacted slot + skill icon branch + content-visibility CSS. Include the inline lock SVG via `{{ template "icon-lock" }}`. Mount under `/session/{id}/waterfall` (existing `serveTraceWaterfall` handler — extend to load `RedactionMap` + `SkillEventMap` once per request, pass them in the template context).
- [ ] Write `cmd/r1-server/ui/web/partials/node-side-panel.html` + `redaction-events.html` per §7. Plus `skill-loaded-detail.html` + `skill-unloaded-detail.html` for the `eq .NodeType "skill_*"` branches. Mount under `GET /api/session/{id}/node/{node_id}` — the existing handler returns JSON; add an HTMX-aware branch (`r.Header.Get("HX-Request") == "true"`) that returns the rendered partial instead.
- [ ] Add CSS for redacted styling in `web/css/base.css`: `.row--redacted .title { opacity: 0.7 }`, `.redacted-block { background: var(--color-redacted-overlay); padding: 1em; font-style: italic; color: var(--color-muted); }`, `.icon-lock { width: 12px; height: 12px; vertical-align: -2px; }`. Test contrast against light + dark + hc themes; redacted text MUST stay ≥ 4.5:1 contrast (WCAG AA).

### 3D rendering hooks

- [ ] In `web/js/graph.js` (from Spec 2), add `applyRedactionLayer(redactionMap)`: iterates the `instances[]` side-table, for each redacted nodeId looks up the InstancedMesh + index, calls `setColorAt(i, desaturatedColor)`, attaches a recycled lock sprite from a pool, billboards toward camera. Cost: O(redacted count), called once per state change.
- [ ] In `web/js/graph.js`, add `applySkillLayer(skillEventMap, cursorTime)`: for each skill node, look up its loaded/unloaded events; if cursor between load and unload (or no unload yet), opacity 1.0 via `setColorAt` lerp; otherwise opacity 0.3. Called by the scrubber on cursor change.

### Tests

- [ ] Add `cmd/r1-server/integration_test.go` test `TestEventRendering_RedactedNode_RendersLockInWaterfall`: build a fixture session with 5 events, redact event #3, render `/session/{id}/waterfall`, assert the rendered HTML has `data-redacted="true"` on row #3 and `[content redacted]` in the side panel response.
- [ ] Add `cmd/r1-server/integration_test.go` test `TestEventRendering_SkillLoadUnloadCycle`: fixture with 1 skill load + 1 skill unload; render `/session/{id}/node/{skill-id}`, assert side panel shows both timestamps + reason; render the waterfall, assert both rows have 🧬 icons.

## 11. Acceptance

- `go test ./internal/hub/builtin/... ./internal/microcompact/... ./cmd/r1-server/...` clean.
- Manual: `R1_SERVER_UI_V2=1` + a fixture session with 1 redacted node + 1 skill load/unload pair → waterfall shows lock + 🧬 icons, side panel renders the placeholder + events list, 3D graph desaturates the redacted node + dims the unloaded skill.
- WCAG AA contrast verified for `[content redacted]` text under light, dark, and high-contrast themes.
- N+1 query check: a 1k-event session with 50 redactions loads `/waterfall` in < 200 ms (no per-row ledger query).
