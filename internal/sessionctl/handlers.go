package sessionctl

import (
	"bytes"
	"encoding/json"
	"time"
)

// Deps is the set of dependencies handlers need. Any nil optional dependency
// produces a graceful "unavailable" error at call time instead of panicking,
// so callers can wire up handlers before every upstream subsystem exists.
type Deps struct {
	SessionID string
	Router    *ApprovalRouter
	Signaler  Signaler
	PGID      int // process group ID to signal on pause/resume

	// Task injection callback -- scheduler may or may not exist yet.
	// When nil, Inject handler returns OK=false, error:"inject unavailable".
	InjectTask func(text string, priority int) (taskID string, err error)

	// Cost cap update -- optional.
	BudgetAdd func(deltaUSD float64, dryRun bool) (prev, next float64, err error)

	// Status snapshot -- optional. When nil returns a minimal default.
	Status func() StatusSnapshot

	// Event publish -- pass-through for now; integrate with bus/eventlog
	// once spec-3 lands. Returns an eventID string for audit.
	Emit func(kind string, payload any) (eventID string)

	// Takeover manages the single-slot takeover state machine. Optional:
	// when nil, the takeover_{request,release} handlers return
	// "takeover unavailable" so non-POSIX builds and early wiring paths
	// degrade gracefully.
	Takeover *TakeoverManager
}

// StatusSnapshot is the shape returned by the `status` verb.
type StatusSnapshot struct {
	State     string  `json:"state"` // "idle" | "executing" | "waiting" | "paused" | "done" | "crashed"
	Mode      string  `json:"mode"`  // "chat" | "ship" | "run"
	PlanID    string  `json:"plan_id,omitempty"`
	Task      *Task   `json:"task,omitempty"`
	CostUSD   float64 `json:"cost_usd"`
	BudgetUSD float64 `json:"budget_usd"`
	Paused    bool    `json:"paused"`
}

// Task is a minimal task descriptor embedded in StatusSnapshot.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Phase string `json:"phase"`
}

// DefaultHandlers constructs the 9-verb handler map used by the sessionctl
// server. Takeover verbs return "takeover unavailable" when deps.Takeover
// is nil, matching the nil-Router / nil-Signaler pattern used elsewhere.
func DefaultHandlers(deps Deps) map[string]Handler {
	return map[string]Handler{
		VerbStatus:          statusHandler(deps),
		VerbApprove:         approveHandler(deps),
		VerbOverride:        overrideHandler(deps),
		VerbBudgetAdd:       budgetAddHandler(deps),
		VerbPause:           pauseHandler(deps),
		VerbResume:          resumeHandler(deps),
		VerbInject:          injectHandler(deps),
		VerbTakeoverRequest: takeoverRequestHandler(deps),
		VerbTakeoverRelease: takeoverReleaseHandler(deps),
	}
}

// decodeStrict decodes raw JSON into dst with DisallowUnknownFields. A
// nil/empty payload is allowed (dst is left as its zero value) so verbs
// with `{}` payloads don't require clients to send an explicit empty object.
func decodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	// Treat the literal `null` as empty too.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// marshal is a tiny helper that swallows marshal errors (return nil) because
// everything we marshal here is built from known concrete types.
func marshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// emit is a convenience wrapper so handlers don't have to nil-check Emit.
func emit(deps Deps, kind string, payload any) string {
	if deps.Emit == nil {
		return ""
	}
	return deps.Emit(kind, payload)
}

// ---- status ----------------------------------------------------------------

func statusHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var empty struct{}
		if err := decodeStrict(req.Payload, &empty); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		var snap StatusSnapshot
		if deps.Status != nil {
			snap = deps.Status()
		} else {
			snap = StatusSnapshot{State: "idle", Mode: "unknown"}
		}
		return marshal(snap), "", ""
	}
}

// ---- approve ---------------------------------------------------------------

type approvePayload struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
}

func approveHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var p approvePayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if deps.Router == nil {
			return nil, "router unavailable", ""
		}
		askID := p.ApprovalID
		if askID == "" {
			askID = deps.Router.OldestOpen()
		}
		if askID == "" {
			return nil, "no pending approvals", ""
		}
		err := deps.Router.Resolve(askID, Decision{
			AskID:     askID,
			Choice:    p.Decision,
			Reason:    p.Reason,
			Actor:     "cli:socket",
			Timestamp: time.Now().UTC(),
		})
		if err != nil {
			return nil, err.Error(), ""
		}
		evtID := emit(deps, "operator.approve", map[string]any{
			"session_id": deps.SessionID,
			"ask_id":     askID,
			"decision":   p.Decision,
			"reason":     p.Reason,
		})
		return marshal(map[string]string{"matched_ask_id": askID}), "", evtID
	}
}

// ---- override --------------------------------------------------------------

type overridePayload struct {
	ACID   string `json:"ac_id"`
	Reason string `json:"reason"`
}

func overrideHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var p overridePayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if p.ACID == "" {
			return nil, "ac_id required", ""
		}
		if p.Reason == "" {
			return nil, "reason required", ""
		}
		// Spec-1 owns the AC state machine; until it lands this handler is
		// audit-only: it records the operator's override decision on the
		// event log so nothing is lost.
		evtID := emit(deps, "operator.override", map[string]any{
			"session_id": deps.SessionID,
			"ac_id":      p.ACID,
			"reason":     p.Reason,
		})
		return marshal(map[string]string{"ac_id": p.ACID}), "", evtID
	}
}

// ---- budget_add ------------------------------------------------------------

type budgetAddPayload struct {
	DeltaUSD float64 `json:"delta_usd"`
	DryRun   bool    `json:"dry_run"`
}

func budgetAddHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var p budgetAddPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if deps.BudgetAdd == nil {
			return nil, "budget tracking unavailable", ""
		}
		prev, next, err := deps.BudgetAdd(p.DeltaUSD, p.DryRun)
		if err != nil {
			return nil, err.Error(), ""
		}
		data := marshal(map[string]float64{
			"prev_budget": prev,
			"new_budget":  next,
		})
		if p.DryRun {
			return data, "", ""
		}
		evtID := emit(deps, "operator.budget_change", map[string]any{
			"session_id": deps.SessionID,
			"delta":      p.DeltaUSD,
			"prev":       prev,
			"next":       next,
			"dry_run":    p.DryRun,
		})
		return data, "", evtID
	}
}

// ---- pause -----------------------------------------------------------------

func pauseHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var empty struct{}
		if err := decodeStrict(req.Payload, &empty); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if deps.Signaler == nil || deps.PGID == 0 {
			return nil, "signaler unavailable", ""
		}
		if err := deps.Signaler.Pause(deps.PGID); err != nil {
			return nil, err.Error(), ""
		}
		now := time.Now().UTC()
		evtID := emit(deps, "operator.pause", map[string]any{
			"session_id": deps.SessionID,
			"paused_at":  now,
		})
		return marshal(map[string]any{"paused_at": now}), "", evtID
	}
}

// ---- resume ----------------------------------------------------------------

func resumeHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var empty struct{}
		if err := decodeStrict(req.Payload, &empty); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if deps.Signaler == nil || deps.PGID == 0 {
			return nil, "signaler unavailable", ""
		}
		if err := deps.Signaler.Resume(deps.PGID); err != nil {
			return nil, err.Error(), ""
		}
		now := time.Now().UTC()
		evtID := emit(deps, "operator.resume", map[string]any{
			"session_id": deps.SessionID,
			"resumed_at": now,
		})
		return marshal(map[string]any{"resumed_at": now}), "", evtID
	}
}

// ---- inject ----------------------------------------------------------------

type injectPayload struct {
	Text     string `json:"text"`
	Priority int    `json:"priority"`
}

func injectHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		var p injectPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if p.Text == "" {
			return nil, "text required", ""
		}
		if deps.InjectTask == nil {
			return nil, "inject unavailable", ""
		}
		taskID, err := deps.InjectTask(p.Text, p.Priority)
		if err != nil {
			return nil, err.Error(), ""
		}
		evtID := emit(deps, "operator.inject", map[string]any{
			"session_id": deps.SessionID,
			"task_id":    taskID,
			"text":       p.Text,
			"priority":   p.Priority,
		})
		return marshal(map[string]string{"task_id": taskID}), "", evtID
	}
}

// ---- takeover (CDC-10) -----------------------------------------------------

type takeoverRequestPayload struct {
	Reason       string `json:"reason"`
	MaxDurationS int    `json:"max_duration_s"`
}

type takeoverReleasePayload struct {
	TakeoverID string `json:"takeover_id"`
	Reason     string `json:"reason"`
}

// takeoverRequestHandler pauses the agent PGID and allocates a takeover slot
// via deps.Takeover. The manager emits operator.takeover_start itself, so
// this handler returns an empty eventID (the router marks OK=true regardless).
func takeoverRequestHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		if deps.Takeover == nil {
			return nil, "takeover unavailable", ""
		}
		var p takeoverRequestPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		maxDur := time.Duration(p.MaxDurationS) * time.Second
		if maxDur == 0 {
			maxDur = 10 * time.Minute
		}
		id, pty, err := deps.Takeover.Request(p.Reason, maxDur)
		if err != nil {
			return nil, err.Error(), ""
		}
		return marshal(map[string]string{"takeover_id": id, "pty_path": pty}), "", ""
	}
}

// takeoverReleaseHandler resumes the agent, computes the diff summary and
// has the manager emit operator.takeover_end. Default reason is "user" when
// the payload omits one.
func takeoverReleaseHandler(deps Deps) Handler {
	return func(req Request) (json.RawMessage, string, string) {
		if deps.Takeover == nil {
			return nil, "takeover unavailable", ""
		}
		var p takeoverReleasePayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, "payload: " + err.Error(), ""
		}
		if p.TakeoverID == "" {
			return nil, "takeover_id required", ""
		}
		reason := p.Reason
		if reason == "" {
			reason = "user"
		}
		diff, err := deps.Takeover.Release(p.TakeoverID, reason)
		if err != nil {
			return nil, err.Error(), ""
		}
		return marshal(map[string]string{"diff_summary": diff}), "", ""
	}
}
