// localhandler.go — production Handler for the read-only desktop verbs.
//
// Until audit A011 the only Handler in the repo was NotImplemented{}
// (desktopapi.go), so every JSON-RPC verb the Tauri host round-tripped
// through `r1 desktop-rpc` returned -32010 not_implemented — the ledger
// viewer, memory inspector, and cost panel all errored on every call.
//
// LocalHandler lands the FIRST production implementation. It backs the
// six read-only data verbs with the real in-repo subsystems:
//
//   - ledger.get_node / ledger.list_events → internal/ledger (Store)
//   - memory.list_scopes / memory.query    → internal/memory/membus (Bus)
//   - cost.get_current / cost.get_history  → internal/costtrack (Tracker)
//
// The remaining verbs stay honestly unimplemented: session.start/pause/
// resume, descent.current_tier/tier_history, lane control, workdir
// binding, and daemon control each return a *stokerr.Error tagged with
// the "not_implemented" code and a message naming the missing
// dependency — NOT the blanket NotImplemented sentinel. This preserves
// audit A029's per-verb not_implemented contract (the dispatcher maps
// the code to -32010, and errors.Is(err, ErrNotImplemented) still holds
// because *stokerr.Error compares by Code) while telling operators
// exactly what is missing for each verb.
//
// A nil backend field degrades that verb-group to not_implemented, so a
// host binary can wire only the subsystems it actually has open.
package desktopapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/stokerr"
)

// ---------------------------------------------------------------------
// Backend read surfaces
//
// Narrow interfaces so LocalHandler is decoupled from the concrete
// subsystems and unit-testable with real (temp-dir / temp-db) backends.
// The concrete *ledger.Store, *membus.Bus, and *costtrack.Tracker each
// satisfy the matching interface directly — no adapters required.
// ---------------------------------------------------------------------

// LedgerReader is the read surface LocalHandler needs from internal/ledger.
// *ledger.Store satisfies it.
type LedgerReader interface {
	ReadNode(id string) (ledger.Node, error)
	ListNodes() ([]ledger.Node, error)
	ListNodesForSession(sessionID string) ([]ledger.Node, error)
	ListEdges() ([]ledger.Edge, error)
}

// MemoryReader is the read surface LocalHandler needs from the memory
// bus. *membus.Bus satisfies it.
type MemoryReader interface {
	Recall(ctx context.Context, req membus.RecallRequest) ([]membus.Memory, error)
}

// CostReader is the read surface LocalHandler needs from the cost
// tracker. *costtrack.Tracker satisfies it.
type CostReader interface {
	Total() float64
	TokenTotals() (input, output, cacheRead, cacheWrite int)
	Records() []costtrack.Usage
}

// ---------------------------------------------------------------------
// LocalHandler
// ---------------------------------------------------------------------

// LocalHandler is the production Handler for the read-only desktop
// verbs. Any nil backend causes its verbs to return not_implemented.
//
// The zero value is usable (every verb returns not_implemented); wire
// the fields to activate the corresponding panels.
type LocalHandler struct {
	// Ledger backs ledger.get_node / ledger.list_events. nil => those
	// verbs return not_implemented.
	Ledger LedgerReader

	// Memory backs memory.query. nil => memory.query returns
	// not_implemented. (memory.list_scopes returns the canonical scope
	// enumeration regardless — it is a fixed contract list.)
	Memory MemoryReader

	// Cost backs cost.get_current / cost.get_history. nil => those verbs
	// return not_implemented.
	Cost CostReader

	// Now is injectable for deterministic tests. nil => time.Now.
	Now func() time.Time
}

// compile-time assertion that LocalHandler satisfies Handler.
var _ Handler = (*LocalHandler)(nil)

func (h *LocalHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// notImplementedf builds a *stokerr.Error carrying the "not_implemented"
// code so the JSON-RPC dispatch emits -32010 and IsNotImplemented /
// errors.Is(err, ErrNotImplemented) still match (stokerr compares by
// Code). Unlike ErrNotImplemented, the message names the specific verb
// and the missing dependency so operators know exactly what is absent.
func notImplementedf(format string, args ...any) *stokerr.Error {
	return stokerr.New(errNotImplementedCode, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------
// Ledger query (§2.2) — real, backed by internal/ledger
// ---------------------------------------------------------------------

// LedgerGetNode returns a single ledger node (header + decoded content
// payload + direct outbound edges) by content hash.
func (h *LocalHandler) LedgerGetNode(_ context.Context, req LedgerGetNodeRequest) (LedgerNode, error) {
	if h.Ledger == nil {
		return LedgerNode{}, notImplementedf("ledger.get_node: no ledger backend wired")
	}
	if strings.TrimSpace(req.Hash) == "" {
		return LedgerNode{}, stokerr.Validationf("ledger.get_node: hash is required")
	}
	node, err := h.Ledger.ReadNode(req.Hash)
	if err != nil {
		return LedgerNode{}, stokerr.NotFoundf("ledger.get_node: node %q not found", req.Hash)
	}

	payload := map[string]any{}
	if len(node.Content) > 0 {
		if err := json.Unmarshal(node.Content, &payload); err != nil {
			// Content isn't a JSON object (scalar/array) — surface it raw
			// rather than dropping it, so the viewer shows something real.
			payload = map[string]any{"_raw": string(node.Content)}
		}
	}

	allEdges, err := h.Ledger.ListEdges()
	if err != nil {
		return LedgerNode{}, stokerr.Internalf("ledger.get_node: list edges: %v", err)
	}
	edges := make([]LedgerEdge, 0)
	for _, e := range allEdges {
		if e.From == node.ID {
			edges = append(edges, LedgerEdge{To: e.To, Kind: string(e.Type)})
		}
	}

	return LedgerNode{
		Hash:     node.ID,
		NodeType: node.Type,
		Payload:  payload,
		Edges:    edges,
	}, nil
}

// LedgerListEvents lists ledger nodes newest-first, optionally scoped to
// a session (matched against the node MissionID) and bounded by a `since`
// upper-bound cursor and a limit. NextCursor pages backwards (older).
func (h *LocalHandler) LedgerListEvents(_ context.Context, req LedgerListEventsRequest) (LedgerListEventsResponse, error) {
	if h.Ledger == nil {
		return LedgerListEventsResponse{}, notImplementedf("ledger.list_events: no ledger backend wired")
	}

	var (
		nodes []ledger.Node
		err   error
	)
	if req.SessionID != "" {
		nodes, err = h.Ledger.ListNodesForSession(req.SessionID)
	} else {
		nodes, err = h.Ledger.ListNodes()
	}
	if err != nil {
		return LedgerListEventsResponse{}, stokerr.Internalf("ledger.list_events: %v", err)
	}

	// Newest-first: the desktop feed reads top-down from most recent.
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].CreatedAt.After(nodes[j].CreatedAt)
	})

	// `since` is an exclusive upper bound (RFC3339). Unparseable values
	// are ignored (best-effort viewer) rather than erroring the panel.
	if req.Since != "" {
		if upper, perr := time.Parse(time.RFC3339, req.Since); perr == nil {
			filtered := nodes[:0]
			for _, n := range nodes {
				if n.CreatedAt.Before(upper) {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	events := make([]LedgerEventSummary, 0, len(nodes))
	for _, n := range nodes {
		events = append(events, LedgerEventSummary{
			Hash:     n.ID,
			NodeType: n.Type,
			At:       n.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	nextCursor := ""
	if len(events) > limit {
		events = events[:limit]
		// The oldest event in this page is the cursor the client passes
		// back as `since` to fetch the next (older) page.
		nextCursor = events[len(events)-1].At
	}

	return LedgerListEventsResponse{Events: events, NextCursor: nextCursor}, nil
}

// ---------------------------------------------------------------------
// Memory inspection (§2.3) — real, backed by internal/memory/membus
// ---------------------------------------------------------------------

// desktopToMembusScope maps the five canonical desktop scopes to their
// membus wire values. Keep in sync with AllMemoryScopes().
var desktopToMembusScope = map[MemoryScope]membus.Scope{
	MemoryScopeSession:     membus.ScopeSession,
	MemoryScopeWorker:      membus.ScopeWorker,
	MemoryScopeAllSessions: membus.ScopeAllSessions,
	MemoryScopeGlobal:      membus.ScopeGlobal,
	MemoryScopeAlways:      membus.ScopeAlways,
}

// MemoryListScopes returns the canonical five-scope enumeration. This is
// a fixed contract list (desktop/IPC-CONTRACT.md §2.3), so it is always
// answerable — the WebView needs it to populate the scope selector even
// before any memory has been written.
func (h *LocalHandler) MemoryListScopes(_ context.Context) (MemoryListScopesResponse, error) {
	return MemoryListScopesResponse{Scopes: AllMemoryScopes()}, nil
}

// MemoryQuery returns memory-bus rows for a scope, optionally filtered by
// a key prefix. Rows are ordered oldest-first (membus Recall order).
func (h *LocalHandler) MemoryQuery(ctx context.Context, req MemoryQueryRequest) (MemoryQueryResponse, error) {
	if h.Memory == nil {
		return MemoryQueryResponse{}, notImplementedf("memory.query: no memory backend wired")
	}
	sc, ok := desktopToMembusScope[req.Scope]
	if !ok {
		return MemoryQueryResponse{}, stokerr.Validationf("memory.query: unknown scope %q", req.Scope)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 256
	}

	// Fetch limit+1 so we can distinguish "exactly limit" from "more
	// exist" for the Truncated flag. membus Recall filters by exact key
	// only, so the key-prefix filter is applied here.
	raw, err := h.Memory.Recall(ctx, membus.RecallRequest{Scope: sc, Limit: limit + 1})
	if err != nil {
		return MemoryQueryResponse{}, stokerr.Internalf("memory.query: recall: %v", err)
	}

	entries := make([]MemoryEntry, 0, len(raw))
	for _, m := range raw {
		if req.KeyPrefix != "" && !strings.HasPrefix(m.Key, req.KeyPrefix) {
			continue
		}
		entries = append(entries, MemoryEntry{
			Key:       m.Key,
			Value:     m.Content,
			UpdatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	truncated := false
	if len(entries) > limit {
		entries = entries[:limit]
		truncated = true
	} else if len(raw) > limit {
		// More scope rows exist beyond the fetch window; further
		// prefix-matching rows may lie past it.
		truncated = true
	}

	return MemoryQueryResponse{Entries: entries, Truncated: truncated}, nil
}

// ---------------------------------------------------------------------
// Cost (§2.4) — real, backed by internal/costtrack
// ---------------------------------------------------------------------

// CostGetCurrent returns the live in-process cost snapshot. The tracker
// is the single-session process accumulator; SessionID is accepted for
// contract compatibility but not used to sub-filter (the desktop
// subprocess owns exactly one session's tracker).
func (h *LocalHandler) CostGetCurrent(_ context.Context, _ CostGetCurrentRequest) (CostSnapshot, error) {
	if h.Cost == nil {
		return CostSnapshot{}, notImplementedf("cost.get_current: no cost backend wired")
	}
	in, out, _, _ := h.Cost.TokenTotals()
	return CostSnapshot{
		USD:       h.Cost.Total(),
		TokensIn:  int64(in),
		TokensOut: int64(out),
		AsOf:      h.now().UTC().Format(time.RFC3339),
	}, nil
}

// CostGetHistory buckets the tracker's per-request usage records into
// minute / hour / day time buckets (default hour), oldest-first.
func (h *LocalHandler) CostGetHistory(_ context.Context, req CostGetHistoryRequest) (CostHistoryResponse, error) {
	if h.Cost == nil {
		return CostHistoryResponse{}, notImplementedf("cost.get_history: no cost backend wired")
	}

	bucket := strings.ToLower(strings.TrimSpace(req.Bucket))
	if bucket == "" {
		bucket = "hour"
	}
	switch bucket {
	case "minute", "hour", "day":
	default:
		return CostHistoryResponse{}, stokerr.Validationf(
			"cost.get_history: bucket must be one of minute|hour|day, got %q", req.Bucket)
	}

	var since time.Time
	if req.Since != "" {
		if t, perr := time.Parse(time.RFC3339, req.Since); perr == nil {
			since = t.UTC()
		}
	}

	type agg struct {
		usd    float64
		tokens int64
	}
	buckets := map[time.Time]*agg{}
	for _, u := range h.Cost.Records() {
		ts := u.Timestamp.UTC()
		if !since.IsZero() && ts.Before(since) {
			continue
		}
		key := bucketStart(ts, bucket)
		a := buckets[key]
		if a == nil {
			a = &agg{}
			buckets[key] = a
		}
		a.usd += u.Cost
		a.tokens += int64(u.InputTokens + u.OutputTokens)
	}

	keys := make([]time.Time, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	out := make([]CostBucket, 0, len(keys))
	for _, k := range keys {
		a := buckets[k]
		out = append(out, CostBucket{
			At:     k.Format(time.RFC3339),
			USD:    a.usd,
			Tokens: a.tokens,
		})
	}
	return CostHistoryResponse{Buckets: out}, nil
}

// bucketStart truncates ts to the start of its minute/hour/day bucket in
// UTC. Day uses calendar-midnight (not a 24h Truncate, which anchors to
// the Unix epoch and drifts off midnight).
func bucketStart(ts time.Time, bucket string) time.Time {
	ts = ts.UTC()
	switch bucket {
	case "minute":
		return ts.Truncate(time.Minute)
	case "day":
		return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
	default: // hour
		return ts.Truncate(time.Hour)
	}
}

// ---------------------------------------------------------------------
// Session control (§2.1) — honestly not_implemented
//
// Starting / pausing / resuming a real session requires integrating the
// session runner (SessionHub) into this subprocess; that wiring is a
// separate product surface. Until it lands, each verb names the missing
// dependency per audit A029 (rather than the blanket NotImplemented).
// ---------------------------------------------------------------------

func (h *LocalHandler) SessionStart(_ context.Context, _ SessionStartRequest) (SessionStartResponse, error) {
	return SessionStartResponse{}, notImplementedf(
		"session.start: requires the session runner (SessionHub) to be integrated into r1 desktop-rpc")
}

func (h *LocalHandler) SessionPause(_ context.Context, _ SessionIDRequest) (SessionPauseResponse, error) {
	return SessionPauseResponse{}, notImplementedf(
		"session.pause: requires the session runner (SessionHub) to be integrated into r1 desktop-rpc")
}

func (h *LocalHandler) SessionResume(_ context.Context, _ SessionIDRequest) (SessionResumeResponse, error) {
	return SessionResumeResponse{}, notImplementedf(
		"session.resume: requires the session runner (SessionHub) to be integrated into r1 desktop-rpc")
}

// ---------------------------------------------------------------------
// Descent state (§2.5) — honestly not_implemented
//
// There is no persistent per-(session, ac) descent state store in the
// repo. Tier progression is computed transiently by
// plan.VerificationDescent's OnTierEvent callbacks during a run and is
// never persisted for later query. Backing these verbs requires that
// store to be built and instrumented first.
// ---------------------------------------------------------------------

func (h *LocalHandler) DescentCurrentTier(_ context.Context, _ DescentCurrentTierRequest) ([]DescentTierRow, error) {
	return nil, notImplementedf(
		"descent.current_tier: no persistent per-(session,ac) descent state store exists; " +
			"tier progression is currently ephemeral via plan.VerificationDescent OnTierEvent callbacks")
}

func (h *LocalHandler) DescentTierHistory(_ context.Context, _ DescentTierHistoryRequest) (DescentTierHistoryResponse, error) {
	return DescentTierHistoryResponse{}, notImplementedf(
		"descent.tier_history: no persistent per-(session,ac) descent state store exists; " +
			"tier progression is currently ephemeral via plan.VerificationDescent OnTierEvent callbacks")
}

// ---------------------------------------------------------------------
// Lane control (§2.7) — host-side (audit A052); not_implemented here
//
// Lane dispatch lives in the Rust host since audit A052; the Go
// subprocess intentionally reports not_implemented so the host falls
// back to its own lane machinery instead of a hard -32601.
// ---------------------------------------------------------------------

func (h *LocalHandler) SessionLanesList(_ context.Context, _ SessionLanesListRequest) (SessionLanesListResponse, error) {
	return SessionLanesListResponse{}, notImplementedf("session.lanes.list: lane dispatch is host-side (audit A052)")
}

func (h *LocalHandler) SessionLanesSubscribe(_ context.Context, _ SessionLanesSubscribeRequest) (SessionLanesSubscribeResponse, error) {
	return SessionLanesSubscribeResponse{}, notImplementedf("session.lanes.subscribe: lane dispatch is host-side (audit A052)")
}

func (h *LocalHandler) SessionLanesUnsubscribe(_ context.Context, _ SessionLanesUnsubscribeRequest) (SessionLanesUnsubscribeResponse, error) {
	return SessionLanesUnsubscribeResponse{}, notImplementedf("session.lanes.unsubscribe: lane dispatch is host-side (audit A052)")
}

func (h *LocalHandler) SessionLanesKill(_ context.Context, _ SessionLanesKillRequest) (SessionLanesKillResponse, error) {
	return SessionLanesKillResponse{}, notImplementedf("session.lanes.kill: lane dispatch is host-side (audit A052)")
}

// ---------------------------------------------------------------------
// Workdir binding (spec desktop-cortex-augmentation §7) — needs the
// session runner; not_implemented until that lands.
// ---------------------------------------------------------------------

func (h *LocalHandler) SessionSetWorkdir(_ context.Context, _ SessionSetWorkdirRequest) (SessionSetWorkdirResponse, error) {
	return SessionSetWorkdirResponse{}, notImplementedf(
		"session.set_workdir: requires the session runner to be integrated into r1 desktop-rpc")
}

// ---------------------------------------------------------------------
// Daemon control (§2.8) — a `r1 serve` daemon concern, not the desktop
// subprocess; not_implemented here.
// ---------------------------------------------------------------------

func (h *LocalHandler) DaemonStatus(_ context.Context) (DaemonStatusResponse, error) {
	return DaemonStatusResponse{}, notImplementedf("daemon.status: not served by the r1 desktop-rpc subprocess")
}

func (h *LocalHandler) DaemonShutdown(_ context.Context, _ DaemonShutdownRequest) (DaemonShutdownResponse, error) {
	return DaemonShutdownResponse{}, notImplementedf("daemon.shutdown: not served by the r1 desktop-rpc subprocess")
}
