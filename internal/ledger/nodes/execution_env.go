package nodes

import (
	"fmt"
	"time"
)

// ExecutionEnvironment records a provisioned execution environment for a mission.
// Captures backend, resource spec, provisioning timestamps, and cost for audit
// and reproducibility. Verification nodes reference this to record what
// environment they ran in.
type ExecutionEnvironment struct {
	Backend   string `json:"backend"`    // inproc, docker, ssh, fly, ember
	BaseImage string `json:"base_image,omitempty"`
	WorkDir   string `json:"work_dir"`

	// Resource spec.
	CPUs     int `json:"cpus,omitempty"`
	MemoryMB int `json:"memory_mb,omitempty"`
	Size     string `json:"size,omitempty"` // fly/ember sizing (e.g. "performance-4x")

	// Services provisioned alongside the main environment.
	Services []string `json:"services,omitempty"` // service names (e.g. "postgres", "redis")

	// Lifecycle timestamps.
	ProvisionedAt time.Time  `json:"provisioned_at"`
	TornDownAt    *time.Time `json:"torn_down_at,omitempty"`

	// Cost tracking (required for paid backends, zero for free).
	CostUSD float64 `json:"cost_usd"`

	// Backend-specific metadata (container ID, machine ID, etc.).
	Meta map[string]string `json:"meta,omitempty"`

	// Mission/task association.
	MissionRef string `json:"mission_ref,omitempty"`
	TaskRef    string `json:"task_ref,omitempty"`

	Version int `json:"schema_version"`
}

var validEnvBackends = map[string]bool{
	"inproc": true,
	"docker": true,
	"ssh":    true,
	"fly":    true,
	"ember":  true,
}

func (e *ExecutionEnvironment) NodeType() string   { return "execution_environment" }
func (e *ExecutionEnvironment) SchemaVersion() int { return e.Version }

func (e *ExecutionEnvironment) Validate() error {
	if e.Backend == "" {
		return fmt.Errorf("execution_environment: backend is required")
	}
	if !validEnvBackends[e.Backend] {
		return fmt.Errorf("execution_environment: invalid backend %q", e.Backend)
	}
	if e.WorkDir == "" {
		return fmt.Errorf("execution_environment: work_dir is required")
	}
	if e.ProvisionedAt.IsZero() {
		return fmt.Errorf("execution_environment: provisioned_at is required")
	}
	return nil
}

func init() {
	Register("execution_environment", func() NodeTyper { return &ExecutionEnvironment{Version: 1} })
}
