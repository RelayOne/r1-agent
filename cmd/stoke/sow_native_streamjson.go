// cmd/stoke/sow_native_streamjson.go — spec-2 cloudswarm-protocol item 10
//
// Per-session streamjson lifecycle helpers. When a sowNativeConfig
// carries a StreamJSON emitter, runSessionNative and its helpers use
// these methods to announce session + task + AC boundaries. Every
// method is a no-op when cfg.StreamJSON is nil (legacy invocations
// via stoke sow / stoke ship stay quiet).
//
// Events emitted:
//
//   session.start     — runSessionNative entry, per-session ID
//   session.complete  — runSessionNative exit, per-session ID
//   task.start        — before a task dispatches to the native runner
//   task.complete     — after a task terminates (success or failure)
//   ac.result         — one line per acceptance-criterion evaluation
//
// Field shape mirrors spec-2 §Data Models table — session_id carries
// the TwoLane's UUID, _stoke.dev/session carries the plan.Session.ID.
package main

import (
	"github.com/ericmacdougall/stoke/internal/plan"
)

// emitSessionStart announces a session boot on the streamjson wire.
// No-op when the emitter is absent or disabled.
func (cfg sowNativeConfig) emitSessionStart(session plan.Session) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	cfg.StreamJSON.EmitSystem("stoke.session.start", map[string]any{
		"_stoke.dev/session":   session.ID,
		"_stoke.dev/title":     session.Title,
		"_stoke.dev/task_count": len(session.Tasks),
		"_stoke.dev/ac_count":  len(session.AcceptanceCriteria),
	})
}

// emitSessionEnd closes a session span with the terminal pass/fail
// status. Observability lane — drop-oldest safe.
//
// Also emits the CS-5 canonical session-end snapshot fields
// (work-stoke-alignment): session_id + ledger_digest + memory_delta_ref
// + cost_total + plan_summary. Fields that can't be computed without a
// larger refactor are emitted as zero/empty to preserve the SHAPE;
// CloudSwarm's session hand-off parser keys off field PRESENCE.
//
// CS-4: when cfg.Bus is non-nil, also emits a stoke.session.memory_delta
// event carrying every memory row written during this session (bus
// rows whose created_at > cfg.SessionStartedAt). CloudSwarm's supervisor
// persists these rows back to its per-task memory store between
// sessions. Emitted BEFORE the session.end event so downstream
// consumers can correlate the delta with the terminal snapshot on
// the same stream.
func (cfg sowNativeConfig) emitSessionEnd(session plan.Session, passed bool, reason string) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	// CS-4: memory delta emit. Nil-safe on Bus == nil; empty-safe
	// when the session wrote zero rows (we still emit the event so
	// CloudSwarm sees the canonical shape on every session boundary
	// rather than having to branch on presence vs absence).
	cfg.emitMemoryDelta(session)

	// Compute plan_summary from the session's task list + pass flag.
	tasksCompleted, tasksFailed := summarizeSessionTasks(session, passed)

	cfg.StreamJSON.EmitSystem("stoke.session.end", map[string]any{
		"_stoke.dev/session": session.ID,
		"_stoke.dev/passed":  passed,
		"_stoke.dev/reason":  reason,
		// CS-5 canonical snapshot fields.
		"session_id":       session.ID,
		"ledger_digest":    "", // populated once ledger exposes HeadDigest(); see CS-5 follow-up
		"memory_delta_ref": "", // populated once CS-4 memory-delta path threads into this emit
		"cost_total":       0.0, // populated once costtrack snapshot threads into cfg
		"plan_summary": map[string]any{
			"tasks_completed": tasksCompleted,
			"tasks_failed":    tasksFailed,
		},
	})
}

// emitMemoryDelta emits stoke.session.memory_delta carrying every
// memory-bus row written during the session. CS-4 of
// work-stoke-alignment. When cfg.Bus is nil the event is skipped
// entirely — there is no meaningful delta to report without a bus
// handle. When the bus is present but empty (no rows written during
// the session) the event fires with count=0 and rows=[] so the
// CloudSwarm supervisor always sees one delta event per session.
func (cfg sowNativeConfig) emitMemoryDelta(session plan.Session) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	if cfg.Bus == nil {
		return
	}
	rows := cfg.Bus.ExportDeltaSince(cfg.SessionStartedAt)
	cfg.StreamJSON.EmitSystem("stoke.session.memory_delta", map[string]any{
		"session_id": session.ID,
		"count":      len(rows),
		"rows":       rows,
	})
}

// summarizeSessionTasks counts completed vs failed tasks for the
// CS-5 snapshot. When the session passed, every task is considered
// completed; when it failed, we assume one task failed and the rest
// completed (best-effort — the plan.Session struct does not carry
// per-task terminal state in the current API, so a precise count
// would require either a structural extension or a separate pass
// over the task-state tracker).
func summarizeSessionTasks(session plan.Session, passed bool) (completed, failed int) {
	total := len(session.Tasks)
	if passed {
		return total, 0
	}
	if total == 0 {
		return 0, 0
	}
	return total - 1, 1
}

// emitTaskStart announces one task's dispatch. Includes the task ID,
// descriptor, and declared file list so downstream observers can
// correlate with ledger nodes and tool-call logs.
func (cfg sowNativeConfig) emitTaskStart(sessionID string, task plan.Task) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	cfg.StreamJSON.EmitSystem("stoke.task.start", map[string]any{
		"_stoke.dev/session":     sessionID,
		"_stoke.dev/task_id":     task.ID,
		"_stoke.dev/description": task.Description,
		"_stoke.dev/files":       task.Files,
	})
}

// emitTaskEnd closes a task span. Critical lane (task.complete is in
// the isCriticalType allowlist) so CloudSwarm never loses terminal
// task verdicts under back-pressure.
func (cfg sowNativeConfig) emitTaskEnd(sessionID string, task plan.Task, success bool, reason string) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	cfg.StreamJSON.EmitSystem("task.complete", map[string]any{
		"_stoke.dev/session":     sessionID,
		"_stoke.dev/task_id":     task.ID,
		"_stoke.dev/description": task.Description,
		"_stoke.dev/success":     success,
		"_stoke.dev/reason":      reason,
	})
}

// emitACResult announces one acceptance-criterion evaluation. Used
// inside the session repair / descent loop; distinct from
// stoke.ac.result per spec-2 §Data Models.
func (cfg sowNativeConfig) emitACResult(sessionID string, result plan.AcceptanceResult) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() {
		return
	}
	cfg.StreamJSON.EmitSystem("stoke.ac.result", map[string]any{
		"_stoke.dev/session":       sessionID,
		"_stoke.dev/ac_id":         result.CriterionID,
		"_stoke.dev/description":   result.Description,
		"_stoke.dev/passed":        result.Passed,
		"_stoke.dev/judge_ruled":   result.JudgeRuled,
		"_stoke.dev/judge_reason":  result.JudgeReasoning,
	})
}

// emitPlanReady is the sow-mode counterpart to session.start. Emitted
// after SOW parse + normalization so observers can snapshot the task
// order before the first task dispatches.
func (cfg sowNativeConfig) emitPlanReady(sowDoc *plan.SOW) {
	if cfg.StreamJSON == nil || !cfg.StreamJSON.Enabled() || sowDoc == nil {
		return
	}
	sessionIDs := make([]string, 0, len(sowDoc.Sessions))
	for _, s := range sowDoc.Sessions {
		sessionIDs = append(sessionIDs, s.ID)
	}
	cfg.StreamJSON.EmitSystem("plan.ready", map[string]any{
		"_stoke.dev/session_count": len(sowDoc.Sessions),
		"_stoke.dev/session_ids":   sessionIDs,
	})
}
