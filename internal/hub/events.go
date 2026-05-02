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

// --- Cortex (4 events) ---
const (
	EventCortexNotePublished EventType = "cortex.note.published"
	EventCortexPreWarmFired  EventType = "cortex.prewarm.fired"
	EventCortexPreWarmFailed EventType = "cortex.prewarm.failed"
	EventCortexRouterDecided EventType = "cortex.router.decided"
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
