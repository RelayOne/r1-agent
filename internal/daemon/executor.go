package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExecutionResult is what an Executor returns after running a Task.
//
// ActualBytes is the real touched-byte count, used for the
// estimate-vs-actual delta check on the queue. MissionID is an opaque
// reference (matches mission.Mission.ID when the executor wraps the R1
// mission runner). ProofsPath is the on-disk location of the PROOFS.md
// the executor wrote.
type ExecutionResult struct {
	ActualBytes int64
	MissionID   string
	ProofsPath  string
	Err         error
}

// Executor runs a single Task. It is responsible for:
//   - producing real work (calling the R1 mission runner / claude / codex)
//   - writing PROOFS.md to disk with file:line citations
//   - returning the actual touched-byte count for the delta check
//
// Implementations MUST be safe to call from multiple goroutines.
type Executor interface {
	Type() string
	Capabilities() []string
	Execute(ctx context.Context, t *Task) ExecutionResult
}

type ToolCheckRequest struct {
	ToolName string
	ToolArgs json.RawMessage
	RepoRoot string
	TaskID   string
}

type ToolCheckResult struct {
	Verdict string
	Reason  string
}

type ToolChecker interface {
	CheckTool(ctx context.Context, req ToolCheckRequest) (ToolCheckResult, error)
}

type GuardedExecutor struct {
	Base    Executor
	Checker ToolChecker
}

func (g GuardedExecutor) Type() string {
	if g.Base == nil {
		return "guarded"
	}
	return g.Base.Type()
}

func (g GuardedExecutor) Capabilities() []string {
	if g.Base == nil {
		return nil
	}
	return g.Base.Capabilities()
}

func (g GuardedExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	if g.Base == nil {
		return ExecutionResult{Err: errors.New("guarded executor: base executor is nil")}
	}
	if g.Checker != nil {
		req, ok := taskToolCheckRequest(t)
		if ok {
			res, err := g.Checker.CheckTool(ctx, req)
			if err != nil {
				return ExecutionResult{Err: fmt.Errorf("user rules check failed: %w", err)}
			}
			if strings.EqualFold(strings.TrimSpace(res.Verdict), "BLOCK") {
				return ExecutionResult{Err: fmt.Errorf("user rule blocked tool %q: %s", req.ToolName, strings.TrimSpace(res.Reason))}
			}
		}
	}
	return g.Base.Execute(ctx, t)
}

// NormalizeRunner canonicalizes task runner names into the daemon's routing vocabulary.
func NormalizeRunner(runner string) string {
	runner = strings.ToLower(strings.TrimSpace(runner))
	switch runner {
	case "", "hybrid":
		return "hybrid"
	case "claude":
		return "claude-code"
	case "native", "shell":
		return "bash"
	default:
		return runner
	}
}

// SupportsRunner reports whether exec can satisfy the requested runner.
func SupportsRunner(exec Executor, runner string) bool {
	if exec == nil {
		return false
	}
	normalized := NormalizeRunner(runner)
	if normalized == "hybrid" {
		return true
	}
	for _, capability := range exec.Capabilities() {
		capability = NormalizeRunner(capability)
		if capability == "*" || capability == normalized {
			return true
		}
	}
	return false
}

func taskToolCheckRequest(t *Task) (ToolCheckRequest, bool) {
	if t == nil || t.Meta == nil {
		return ToolCheckRequest{}, false
	}
	toolName := strings.TrimSpace(t.Meta["tool_name"])
	if toolName == "" {
		toolName = strings.TrimSpace(t.Meta["proposed_tool_name"])
	}
	if toolName == "" {
		return ToolCheckRequest{}, false
	}
	rawArgs := strings.TrimSpace(t.Meta["tool_args"])
	if rawArgs == "" {
		rawArgs = strings.TrimSpace(t.Meta["proposed_tool_args"])
	}
	if rawArgs == "" {
		rawArgs = "{}"
	}
	return ToolCheckRequest{
		ToolName: toolName,
		ToolArgs: json.RawMessage(rawArgs),
		RepoRoot: strings.TrimSpace(t.Repo),
		TaskID:   t.ID,
	}, true
}

// ProofRecord is one entry in proofs.json: a single claim and its evidence.
type ProofRecord struct {
	Claim         string `json:"claim"`
	EvidenceType  string `json:"evidence_type"`  // file_line | commit | pr | gh_url | curl_probe | cloud_run_rev
	EvidenceValue string `json:"evidence_value"` // e.g. "apps/portal/src/lib/foo.ts:42"
	Source        string `json:"source"`         // worker / model / human
}

// WriteProofs writes proofs.json and a human-readable PROOFS.md to outDir.
// outDir is created if it does not exist. Returns the absolute path of
// PROOFS.md (suitable for Task.ProofsPath).
func WriteProofs(outDir, taskID string, records []ProofRecord) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir proof dir: %w", err)
	}
	jsonPath := filepath.Join(outDir, "proofs.json")
	mdPath := filepath.Join(outDir, "PROOFS.md")

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal proofs: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write proofs.json: %w", err)
	}

	md := fmt.Sprintf("# Proofs for task %s\n\nGenerated: %s\n\nTotal claims: %d\n\n",
		taskID, time.Now().UTC().Format(time.RFC3339), len(records))
	for i, r := range records {
		md += fmt.Sprintf("## Claim %d\n\n%s\n\n- Evidence type: `%s`\n- Evidence value: `%s`\n- Source: `%s`\n\n",
			i+1, r.Claim, r.EvidenceType, r.EvidenceValue, r.Source)
	}
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		return "", fmt.Errorf("write PROOFS.md: %w", err)
	}
	return mdPath, nil
}

// NoopExecutor is the default executor used when no real one is wired.
// It records a single "executor-not-configured" proof and returns 0 bytes.
// Useful for tests and for daemon-startup smoke runs before the mission
// runner is connected.
type NoopExecutor struct {
	OutBase string // base dir for proof output; tasks land under <OutBase>/<task-id>/
}

func (n NoopExecutor) Type() string { return "noop" }

func (n NoopExecutor) Capabilities() []string { return []string{"*"} }

func (n NoopExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	out := filepath.Join(n.OutBase, t.ID)
	path, err := WriteProofs(out, t.ID, []ProofRecord{{
		Claim:         "noop executor — no real work performed",
		EvidenceType:  "internal",
		EvidenceValue: "internal/daemon/executor.go:NoopExecutor",
		Source:        "daemon.NoopExecutor",
	}})
	if err != nil {
		return ExecutionResult{Err: err}
	}
	return ExecutionResult{
		ActualBytes: int64(len(t.Prompt)), // pretend we touched as much as the prompt
		MissionID:   "noop-" + t.ID,
		ProofsPath:  path,
	}
}
