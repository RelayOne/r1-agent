package hub

import "time"

// EventType identifies a specific hook point in the pipeline.
type EventType string

// --- Session & Mission Lifecycle (16 events) ---
const (
	EventSessionInit         EventType = "session.init"
	EventSessionConfigured   EventType = "session.configured"
	EventSessionDispose      EventType = "session.dispose"

	EventMissionCreated      EventType = "mission.created"
	EventMissionResearchStart EventType = "mission.research.start"
	EventMissionResearchDone EventType = "mission.research.done"
	EventMissionPlanStart    EventType = "mission.plan.start"
	EventMissionPlanDone     EventType = "mission.plan.done"
	EventMissionExecuteStart  EventType = "mission.execute.start"
	EventMissionValidateStart EventType = "mission.validate.start"
	EventMissionConsensusStart EventType = "mission.consensus.start"
	EventMissionConverged    EventType = "mission.converged"
	EventMissionFailed       EventType = "mission.failed"
	EventMissionCancelled    EventType = "mission.cancelled"

	EventTaskDispatched      EventType = "task.dispatched"
	EventTaskStarted         EventType = "task.started"
	EventTaskCompleted       EventType = "task.completed"
	EventTaskFailed          EventType = "task.failed"
	EventTaskRetrying        EventType = "task.retrying"
	EventTaskBlocked         EventType = "task.blocked"
	EventTaskSkipped         EventType = "task.skipped"
)

// --- Tool Use (8 events) ---
const (
	EventToolPreUse    EventType = "tool.pre_use"
	EventToolPostUse   EventType = "tool.post_use"
	EventToolBlocked   EventType = "tool.blocked"
	EventToolError     EventType = "tool.error"
	EventToolFileRead  EventType = "tool.file_read"
	EventToolFileWrite EventType = "tool.file_write"
	EventToolBashExec  EventType = "tool.bash_exec"
	EventToolSearch    EventType = "tool.search"
)

// --- Model API (8 events) ---
const (
	EventModelPreCall       EventType = "model.pre_call"
	EventModelPostCall      EventType = "model.post_call"
	EventModelError         EventType = "model.error"
	EventModelRateLimited   EventType = "model.rate_limited"
	EventModelFallback      EventType = "model.fallback"
	EventModelCostThreshold EventType = "model.cost_threshold"
	EventModelTokenThreshold EventType = "model.token_threshold" // #nosec G101 -- event type name, not a credential.
	EventModelCacheHit      EventType = "model.cache_hit"
)

// --- Prompt Construction (6 events) ---
const (
	EventPromptBuilding      EventType = "prompt.building"
	EventPromptSkillsMatched EventType = "prompt.skills_matched"
	EventPromptSkillsInjected EventType = "prompt.skills_injected"
	EventPromptContextPacked EventType = "prompt.context_packed"
	EventPromptWisdomInjected EventType = "prompt.wisdom_injected"
	EventPromptFinalized     EventType = "prompt.finalized"
)

// --- Git Operations (8 events) ---
const (
	EventGitPreCommit       EventType = "git.pre_commit"
	EventGitPostCommit      EventType = "git.post_commit"
	EventGitPreMerge        EventType = "git.pre_merge"
	EventGitPostMerge       EventType = "git.post_merge"
	EventGitMergeConflict   EventType = "git.merge_conflict"
	EventGitWorktreeCreated EventType = "git.worktree_created"
	EventGitWorktreeCleaned EventType = "git.worktree_cleaned"
	EventGitBranchCreated   EventType = "git.branch_created"
)

// --- Verification & Quality (10 events) ---
const (
	EventVerifyBuildStart       EventType = "verify.build_start"
	EventVerifyBuildResult      EventType = "verify.build_result"
	EventVerifyTestStart        EventType = "verify.test_start"
	EventVerifyTestResult       EventType = "verify.test_result"
	EventVerifyLintResult       EventType = "verify.lint_result"
	EventVerifyScopeCheck       EventType = "verify.scope_check"
	EventVerifyConvergenceStart EventType = "verify.convergence_start"
	EventVerifyConvergenceResult EventType = "verify.convergence_result"
	EventVerifyCriticReview     EventType = "verify.critic_review"
	EventVerifyCrossModelReview EventType = "verify.cross_model_review"
)

// --- Security (6 events) ---
const (
	EventSecurityScanResult      EventType = "security.scan_result"
	EventSecuritySecretDetected  EventType = "security.secret_detected"
	EventSecurityDependencyVuln  EventType = "security.dependency_vuln"
	EventSecurityInjectionRisk   EventType = "security.injection_risk"
	EventSecurityBypassAttempt   EventType = "security.bypass_attempt"
	EventSecurityPolicyViolation EventType = "security.policy_violation"
)

// --- Skill & Knowledge (6 events) ---
const (
	EventSkillMatched          EventType = "skill.matched"
	EventSkillInjected         EventType = "skill.injected"
	EventSkillUpdateSuggested  EventType = "skill.update_suggested"
	EventSkillEffectiveness    EventType = "skill.effectiveness"
	EventResearchStored        EventType = "research.stored"
	EventWisdomRecorded        EventType = "wisdom.recorded"
)

// --- Cost & Resource (4 events) ---
const (
	EventCostBudget50       EventType = "cost.budget_50"
	EventCostBudget80       EventType = "cost.budget_80"
	EventCostBudget90       EventType = "cost.budget_90"
	EventCostBudgetExceeded EventType = "cost.budget_exceeded"
)

// --- Custom (1 event) ---
const (
	EventCustom EventType = "custom.event"
)

// --- Lanes (6 events) ---
//
// Lanes are the per-surface-visible thread of activity inside a single r1d
// session: the main agent thread, a cortex Lobe, an in-flight tool call, or
// a mission-task. Cortex-core (specs/cortex-core.md) owns the lifecycle;
// this taxonomy freezes the wire-level event family.
//
// See specs/lanes-protocol.md §4 for the full event-type catalog. Adding a
// seventh event type is a wire-version bump per spec §5.6.
const (
	EventLaneCreated EventType = "lane.created"
	EventLaneStatus  EventType = "lane.status"
	EventLaneDelta   EventType = "lane.delta"
	EventLaneCost    EventType = "lane.cost"
	EventLaneNote    EventType = "lane.note"
	EventLaneKilled  EventType = "lane.killed"
)

// --- Legacy compat-window event (1 event) ---
//
// EventSessionDelta is the pre-lanes assistant-text delta event. Per
// specs/lanes-protocol.md §"Out of scope" item 1 and §10.5, the main lane
// continues to emit session.delta in parallel with lane.delta for one
// minor release so existing desktop clients keep working without code
// changes. Removal is a follow-up minor release; do NOT add new emitters
// of this event — the only producer is the dual-emit bridge in
// internal/server/lanes_compat.go.
const (
	EventSessionDelta EventType = "session.delta"
)

// --- Cortex (9 events) ---
const (
	EventCortexNotePublished           EventType = "cortex.note.published"
	EventCortexPreWarmFired            EventType = "cortex.prewarm.fired"
	EventCortexPreWarmFailed           EventType = "cortex.prewarm.failed"
	EventCortexRouterDecided           EventType = "cortex.router.decided"
	EventCortexLobeStarted             EventType = "cortex.lobe.started"
	EventCortexLobePanic               EventType = "cortex.lobe.panic"
	EventCortexSpotlightChanged        EventType = "cortex.spotlight.changed"
	EventCortexWorkspaceMemoryAdded    EventType = "cortex.workspace.memory_added"

	// EventCortexUserConfirmedPlanChange is emitted when the user
	// confirms a queued PlanUpdateLobe proposal. Custom["queue_id"]
	// carries the queue identifier the Lobe stamped onto the
	// user-confirm Note's Meta. Spec: specs/cortex-concerns.md item 20.
	EventCortexUserConfirmedPlanChange EventType = "cortex.user.confirmed_plan_change"

	// EventCortexUserMessage is emitted by the orchestrator after each
	// user turn lands. ClarifyingQLobe subscribes to this to drive the
	// once-per-user-turn Haiku call that drafts clarifying questions.
	// Custom["text"] carries the raw user message text; Custom["history"]
	// carries the recent provider.ChatMessage tail when available.
	// Spec: specs/cortex-concerns.md item 24.
	EventCortexUserMessage EventType = "cortex.user.message"

	// EventCortexUserAnsweredQuestion is emitted when the user answers
	// a queued clarifying question. Custom["question_id"] carries the
	// identifier the Lobe stamped on the original question Note's Meta.
	// ClarifyingQLobe resolves the matching outstanding Note when this
	// event fires. Spec: specs/cortex-concerns.md item 25.
	EventCortexUserAnsweredQuestion EventType = "cortex.user.answered_question"
)

// Mode determines how the hook participates.
type Mode string

const (
	ModeGateStrict Mode = "gate_strict" // sync, fail-closed: error/timeout/panic -> DENY
	ModeGate       Mode = "gate"        // sync, fail-open: error/timeout/panic -> ALLOW (advisory)
	ModeTransform  Mode = "transform"   // sync, can modify payload
	ModeObserve    Mode = "observe"     // async, fire-and-forget
)

// Decision is what a gate hook returns.
type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	Abstain Decision = "abstain"
)

// Event is the universal payload carried through the hook pipeline.
type Event struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`

	// Context
	MissionID  string `json:"mission_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	WorktreeID string `json:"worktree_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	Phase      string `json:"phase,omitempty"`

	// Payload (varies by event type)
	Tool      *ToolEvent      `json:"tool,omitempty"`
	File      *FileEvent      `json:"file,omitempty"`
	Model     *ModelEvent     `json:"model,omitempty"`
	Git       *GitEvent       `json:"git,omitempty"`
	Cost      *CostEvent      `json:"cost,omitempty"`
	Prompt    *PromptEvent    `json:"prompt,omitempty"`
	Skill     *SkillEvent     `json:"skill,omitempty"`
	Lifecycle *LifecycleEvent `json:"lifecycle,omitempty"`
	Test      *TestEvent      `json:"test,omitempty"`
	Security  *SecurityEvent  `json:"security,omitempty"`
	Lane      *LaneEvent      `json:"lane,omitempty"`
	Custom    map[string]any  `json:"custom,omitempty"`
}

// ToolEvent carries data about tool invocations.
type ToolEvent struct {
	Name     string         `json:"name"`
	Input    map[string]any `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	FilePath string         `json:"file_path,omitempty"`
	Command  string         `json:"command,omitempty"`
	Duration time.Duration  `json:"duration,omitempty"`
	ExitCode int            `json:"exit_code,omitempty"`
}

// FileEvent carries data about file operations.
type FileEvent struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	OldPath   string `json:"old_path,omitempty"`
	DiffSize  int    `json:"diff_size,omitempty"`
	Language  string `json:"language,omitempty"`
}

// ModelEvent carries data about model API interactions.
type ModelEvent struct {
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Role         string        `json:"role,omitempty"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	CachedTokens int           `json:"cached_tokens,omitempty"`
	CostUSD      float64       `json:"cost_usd"`
	Duration     time.Duration `json:"duration"`
	StopReason   string        `json:"stop_reason,omitempty"`
	ToolCalls    int           `json:"tool_calls,omitempty"`
}

// GitEvent carries data about git operations.
type GitEvent struct {
	Operation    string   `json:"operation"`
	Branch       string   `json:"branch,omitempty"`
	CommitHash   string   `json:"commit_hash,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
	Conflicts    []string `json:"conflicts,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// CostEvent carries budget and cost threshold data.
type CostEvent struct {
	TotalSpent    float64 `json:"total_spent"`
	BudgetLimit   float64 `json:"budget_limit"`
	PercentUsed   float64 `json:"percent_used"`
	ProjectedCost float64 `json:"projected_cost"`
	Threshold     string  `json:"threshold"`
}

// PromptEvent carries data about prompt construction.
type PromptEvent struct {
	Phase         string   `json:"phase"`
	TotalTokens   int      `json:"total_tokens"`
	SkillTokens   int      `json:"skill_tokens,omitempty"`
	ContextTokens int      `json:"context_tokens,omitempty"`
	TaskTokens    int      `json:"task_tokens,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	WindowUsage   float64  `json:"window_usage,omitempty"`
}

// SkillEvent carries data about skill lifecycle.
type SkillEvent struct {
	Name       string  `json:"name"`
	Action     string  `json:"action"`
	Source     string  `json:"source,omitempty"`
	MatchScore float64 `json:"match_score,omitempty"`
}

// LifecycleEvent carries data about session/mission/task lifecycle.
type LifecycleEvent struct {
	Entity   string        `json:"entity"`
	State    string        `json:"state"`
	Duration time.Duration `json:"duration,omitempty"`
	Attempt  int           `json:"attempt,omitempty"`
}

// TestEvent carries data about test execution.
type TestEvent struct {
	Phase        string        `json:"phase"`
	Passed       int           `json:"passed"`
	Failed       int           `json:"failed"`
	Skipped      int           `json:"skipped"`
	Coverage     float64       `json:"coverage,omitempty"`
	Duration     time.Duration `json:"duration"`
	FailedTests  []string      `json:"failed_tests,omitempty"`
	NewTests     []string      `json:"new_tests,omitempty"`
	RemovedTests []string      `json:"removed_tests,omitempty"`
}

// SecurityEvent carries data about security-relevant events.
type SecurityEvent struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Details  string `json:"details"`
	FilePath string `json:"file_path,omitempty"`
	Rule     string `json:"rule,omitempty"`
}

// LaneContentBlock carries one streamed content block for a lane.delta
// event. It mirrors the shape of Anthropic's content blocks (see
// agentloop.ContentBlock) so renderers can pass through, but is defined
// inside hub to avoid an import cycle (agentloop imports hub for event
// emission). Conversions are defined in cortex's lane.go.
//
// See specs/lanes-protocol.md §4.3 for the content_block.type enum
// (text_delta, thinking_delta, tool_use_start, tool_use_input_delta,
// tool_use_end, tool_result, note_ref).
type LaneContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     []byte `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	NoteID    string `json:"note_id,omitempty"` // for note_ref
}

// LaneEvent is the unified payload for the six EventLane* event types
// declared above. Fields are populated based on the event Type; consumers
// should branch on Event.Type and read only the relevant fields.
//
// See specs/lanes-protocol.md §4 for per-type field requirements. The
// JSON encoding follows §4 verbatim (omitempty on every optional field).
type LaneEvent struct {
	// Common fields, present on every lane event.

	// LaneID identifies the lane. ULID, monotonic-by-time per session.
	LaneID string `json:"lane_id"`

	// SessionID names the session this lane belongs to. The value is
	// duplicated at the envelope level by the wire formatter; this field
	// is used by hub-only consumers that don't see the wire envelope.
	SessionID string `json:"session_id,omitempty"`

	// Seq is the per-session monotonic sequence number assigned by the
	// single-writer goroutine in the cortex Workspace. seq=0 is reserved
	// for the synthetic session.bound event per spec §5.5.
	Seq uint64 `json:"seq,omitempty"`

	// --- lane.created fields (spec §4.1) ---

	// Kind is the lane category. Required on lane.created.
	Kind LaneKind `json:"kind,omitempty"`

	// ParentID is the parent lane's ID. Empty for the main lane and for
	// top-level mission tasks; required on every other lane.
	ParentID string `json:"parent_id,omitempty"`

	// Label is the human-readable label shown in surfaces.
	Label string `json:"label,omitempty"`

	// LobeName names the cortex Lobe when Kind == "lobe".
	LobeName string `json:"lobe_name,omitempty"`

	// Labels is the structured label map (per spec §4.1 example: model,
	// deterministic). Free-form k/v.
	Labels map[string]string `json:"labels,omitempty"`

	// StartedAt is the lane creation timestamp. Set on lane.created.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// EndedAt is the lane termination timestamp. Set on terminal
	// lane.status events.
	EndedAt *time.Time `json:"ended_at,omitempty"`

	// --- lane.status fields (spec §4.2) ---

	// Status is the new lifecycle state. Required on lane.status.
	Status LaneStatus `json:"status,omitempty"`

	// PrevStatus is the previous lifecycle state. Set on lane.status to
	// help surfaces detect transitions without keeping local state.
	PrevStatus LaneStatus `json:"prev_status,omitempty"`

	// Reason is the human-readable explanation for the transition.
	Reason string `json:"reason,omitempty"`

	// ReasonCode is the stable enum tag (started, tool_dispatch,
	// awaiting_user, awaiting_review, awaiting_dependency, unblocked,
	// ok, cancelled_by_operator, cancelled_by_parent,
	// cancelled_by_budget, errored, plus stokerr codes). See spec §4.2.
	ReasonCode string `json:"reason_code,omitempty"`

	// --- orthogonal flag (spec §3.2) ---

	// Pinned reflects the orthogonal pinned flag. Mirrored on
	// lane.created snapshots; mutations flow through the r1.lanes.pin
	// MCP tool, not via lane events.
	Pinned bool `json:"pinned,omitempty"`

	// --- lane.delta fields (spec §4.3) ---

	// DeltaSeq is the per-lane monotonic sequence for content streams.
	// Distinct from the session-wide Seq; lets the surface detect
	// intra-lane gaps.
	DeltaSeq uint64 `json:"delta_seq,omitempty"`

	// Block carries the streamed content block on lane.delta.
	Block *LaneContentBlock `json:"content_block,omitempty"`

	// --- lane.cost fields (spec §4.4) ---

	// TokensIn is the input tokens consumed in this tick.
	TokensIn int `json:"tokens_in,omitempty"`

	// TokensOut is the output tokens produced in this tick.
	TokensOut int `json:"tokens_out,omitempty"`

	// CachedTokens is the prompt-cache hit count for this tick.
	CachedTokens int `json:"cached_tokens,omitempty"`

	// USD is the dollar cost of this tick.
	USD float64 `json:"usd,omitempty"`

	// CumulativeUSD is the running total dollar cost for the lane.
	CumulativeUSD float64 `json:"cumulative_usd,omitempty"`

	// --- lane.note fields (spec §4.5) ---

	// NoteID points at the cortex Note. The full Note is fetched via the
	// r1.cortex.notes MCP tool; this event is a lightweight pointer.
	NoteID string `json:"note_id,omitempty"`

	// NoteSeverity is the cortex Note severity (info|warn|critical).
	// "critical" gates end_turn per cortex D-C4.
	NoteSeverity string `json:"note_severity,omitempty"`

	// NoteKind is the cortex Note kind (e.g. memory_recall).
	NoteKind string `json:"note_kind,omitempty"`

	// NoteSummary is the short human-readable summary surfaced to the
	// renderer. The full body is in the cortex Note.
	NoteSummary string `json:"note_summary,omitempty"`

	// --- lane.killed fields (spec §4.6) ---

	// Actor names who killed the lane (operator, parent, budget).
	Actor string `json:"actor,omitempty"`

	// ActorID is the principal id (user ULID, parent lane id, etc).
	ActorID string `json:"actor_id,omitempty"`
}

// HookResponse is the structured response from any hook.
type HookResponse struct {
	Decision   Decision       `json:"decision"`
	Reason     string         `json:"reason,omitempty"`
	Mutations  map[string]any `json:"mutations,omitempty"`
	Injections []Injection    `json:"injections,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Suppress   bool           `json:"suppress,omitempty"`
}

// Injection is content that a transform hook wants injected.
type Injection struct {
	Position string `json:"position"`
	Content  string `json:"content"`
	Label    string `json:"label"`
	Priority int    `json:"priority"`
	Budget   int    `json:"budget,omitempty"`
}
