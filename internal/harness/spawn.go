package harness

// SpawnRequest describes what stance to create.
type SpawnRequest struct {
	Role             string   // concern field role name (e.g. "dev", "reviewer")
	Face             string   // "proposing" or "reviewing"
	TaskDAGScope     string   // task node ID
	LoopRef          string   // loop ID, may be empty
	SupervisorID     string   // ID of the supervisor that requested the spawn
	CausalityRef     string   // bus event ID that caused the spawn
	ModelOverride    string   // optional model override
	ToolAuthOverride []string // optional tool authorization override
	AdditionalCtx    string   // optional additional context injected into prompt
	BudgetUSD        float64  // 0 = inherit mission budget
}

// StanceHandle is a lightweight reference to a running stance.
type StanceHandle struct {
	ID    string
	Role  string
	State StanceStatus
}

// StanceStatus represents the lifecycle state of a stance.
type StanceStatus string

const (
	StatusRunning          StanceStatus = "running"
	StatusPaused           StanceStatus = "paused"
	StatusWaitingResearch  StanceStatus = "waiting_on_research"
	StatusWaitingConsensus StanceStatus = "waiting_on_consensus"
	StatusTerminated       StanceStatus = "terminated"
)
