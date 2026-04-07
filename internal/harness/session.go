package harness

import "time"

// StanceSession is the internal mutable state of an active stance.
type StanceSession struct {
	ID              string
	Role            string
	Status          StanceStatus
	Model           string
	SystemPrompt    string
	ConcernField    string // rendered concern field text
	AuthorizedTools []string
	SpawnRequest    SpawnRequest
	TokensUsed      int64
	CostUSD         float64
	CreatedAt       time.Time
	PauseReason     string
	AdditionalCtx   string
}

// StanceState is the public read-only snapshot returned by InspectStance.
type StanceState struct {
	StanceHandle
	Model       string
	TokensUsed  int64
	CostUSD     float64
	CreatedAt   time.Time
	PauseReason string
}
