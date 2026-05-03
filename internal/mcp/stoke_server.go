// Stoke MCP server: exposes Stoke itself as a tool that Claude Code (or any
// MCP-aware client) can drive.
//
// This is the inverse of codebase_server.go. Where codebase_server gives
// Claude Code read access to a project's symbols/dependencies/content,
// stoke_server gives Claude Code the ability to ASK Stoke to build a project
// from a Statement of Work and report progress.
//
// The user's flow:
//   1. User opens Claude Code in any directory.
//   2. User pastes a SOW and says "use stoke to build this".
//   3. Claude Code calls the stoke_build_from_sow tool with the SOW JSON.
//   4. The tool kicks off a `stoke sow` build in the background and returns
//      a mission ID.
//   5. Claude Code polls stoke_get_mission_status until the build completes.
//   6. Claude Code reports the result back to the user.
//
// Connect via Claude Code's --mcp-config:
//   { "mcpServers": { "stoke": { "command": "stoke", "args": ["mcp-serve-stoke"] } } }
//
// SECURITY (outbound sanitization policy):
// The tool responses emitted by this server (mission IDs, build logs, SOW
// echoes, status payloads) are NOT pre-sanitized for prompt-injection. This
// is intentional:
//   1. Non-LLM clients (CI runners, dashboards, scripts) consume these
//      responses programmatically and would be broken by payload mutation.
//   2. Different LLM clients apply different sanitization conventions; the
//      MCP server cannot know which strategy its consumer wants.
// Downstream MCP consumers that feed these payloads into an LLM prompt MUST
// apply their own prompt-injection defenses. See docs/mcp-security.md for
// the full responsibility boundary.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/logging"
	"github.com/RelayOne/r1/internal/procutil"
	"github.com/RelayOne/r1/internal/r1dir"
)

// maxLogBytes caps the size of any single mission stdout/stderr log. When the
// writer exceeds this, the file is truncated to the last maxLogBytes/2 bytes
// so long-running builds can't exhaust disk.
const maxLogBytes = 16 * 1024 * 1024 // 16 MiB per stream

// missionGCAge is the age at which completed missions are pruned from the
// in-memory map. Logs on disk are kept until explicit cleanup.
const missionGCAge = 24 * time.Hour

// StokeServer is an MCP tool server that exposes Stoke build operations to
// Claude Code or any other MCP-aware client.
type StokeServer struct {
	mu       sync.Mutex
	missions map[string]*missionRecord
	// stokeBin is the path to the stoke binary used to spawn build subprocesses.
	// If empty, the server's own argv[0] is used (resolved at Spawn time).
	stokeBin string
	// spawner is the process spawner. Tests can override this to avoid
	// actually exec'ing a subprocess. Defaults to realSpawn.
	spawner spawnFunc
	// now is the clock source. Tests can override.
	now func() time.Time

	// lanes optionally aggregates the lanes-protocol MCP server's
	// five tools (specs/lanes-protocol.md §7) into this StokeServer's
	// tools/list and tools/call handlers. Wired via WithLanesServer
	// per TASK-24; nil disables lane-tool exposure (the StokeServer
	// behaves exactly as before).
	lanes *LanesServer
}

// WithLanesServer attaches a *LanesServer so this StokeServer's
// MCP tools/list and tools/call dispatch include the five
// r1.lanes.* tools alongside the stoke_*/r1_* tools. Spec
// TASK-24 calls for "one-line addition to the MCP registry init";
// callers achieve that by chaining: NewStokeServer(bin).WithLanesServer(ls).
//
// Passing nil clears any previously-attached lanes server.
func (s *StokeServer) WithLanesServer(ls *LanesServer) *StokeServer {
	s.mu.Lock()
	s.lanes = ls
	s.mu.Unlock()
	return s
}

// spawnFunc starts a subprocess and returns a handle. The handle's Wait()
// will be called in a background goroutine.
type spawnFunc func(bin string, args []string, workdir string, env []string, stdout, stderr io.Writer) (processHandle, error)

// processHandle abstracts what the server needs from an *exec.Cmd so tests
// can supply their own implementation.
type processHandle interface {
	Wait() error
	Kill() error
	Pid() int
}

// missionRecord tracks an in-progress or completed Stoke build.
type missionRecord struct {
	ID         string
	RepoRoot   string
	SOWPath    string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string // running | success | failed | cancelled
	ExitCode   int
	StdoutPath string
	StderrPath string
	Command    []string
	Cancel     chan struct{} // closed when cancellation is requested
	proc       processHandle
}

// NewStokeServer creates a new Stoke MCP server.
func NewStokeServer(stokeBin string) *StokeServer {
	return &StokeServer{
		missions: make(map[string]*missionRecord),
		stokeBin: stokeBin,
		spawner:  realSpawn,
		now:      time.Now,
	}
}

// ToolDefinitions returns the MCP tool definitions for Stoke build
// operations. S1-4 of work-r1-rename.md mandates that every legacy
// stoke_* tool is also published under the canonical r1_* name until
// v2.0.0; both names dispatch to the same handler via HandleToolCall,
// which normalizes the prefix before switching. The canonical r1_*
// entry is emitted first in each pair so clients that pick the first
// match prefer it.
//
// When a LanesServer is attached via WithLanesServer (TASK-24), the
// five r1.lanes.* tools are appended after the stoke/r1 build tools
// so a single MCP endpoint exposes both surfaces.
func (s *StokeServer) ToolDefinitions() []ToolDefinition {
	base := s.baseToolDefinitions()
	out := make([]ToolDefinition, 0, len(base)*2)
	for _, t := range base {
		if r1 := canonicalStokeServerToolName(t.Name); r1 != t.Name {
			alias := t
			alias.Name = r1
			out = append(out, alias)
		}
		out = append(out, t)
	}
	s.mu.Lock()
	lanes := s.lanes
	s.mu.Unlock()
	if lanes != nil {
		out = append(out, lanes.ToolDefinitions()...)
	}
	return out
}

// canonicalStokeServerToolName returns the r1_* canonical alias for a
// legacy stoke_* tool name. Names without the stoke_ prefix pass through
// unchanged.
func canonicalStokeServerToolName(legacy string) string {
	const legacyPrefix = "stoke_"
	if strings.HasPrefix(legacy, legacyPrefix) {
		return "r1_" + strings.TrimPrefix(legacy, legacyPrefix)
	}
	return legacy
}

// legacyStokeServerToolName returns the legacy stoke_* form for a
// canonical r1_* tool name. Names without the r1_ prefix pass through
// unchanged. This lets HandleToolCall dispatch either prefix via a
// single switch.
func legacyStokeServerToolName(canonical string) string {
	const canonicalPrefix = "r1_"
	if strings.HasPrefix(canonical, canonicalPrefix) {
		return "stoke_" + strings.TrimPrefix(canonical, canonicalPrefix)
	}
	return canonical
}

// baseToolDefinitions is the canonical source of tool shapes under the
// legacy stoke_* naming. ToolDefinitions wraps this and emits each entry
// under both stoke_* (legacy) and r1_* (canonical) names.
func (s *StokeServer) baseToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name: "stoke_build_from_sow",
			Description: "Kick off an R1 build of a project from a Statement of Work (SOW). " +
				"The SOW describes sessions, tasks, acceptance criteria, infrastructure requirements, " +
				"and stack details. R1 will build the project session-by-session, gating each session " +
				"on its acceptance criteria, and supervising agents for liveness instead of timing out. " +
				"Returns a mission_id for status polling. The build runs in the background — use " +
				"stoke_get_mission_status to check progress.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"repo_root": {
						"type": "string",
						"description": "Absolute path to the project directory where R1 will build (will be created if it doesn't exist)"
					},
					"sow": {
						"type": "string",
						"description": "The full Statement of Work content as a JSON or YAML string"
					},
					"runner": {
						"type": "string",
						"description": "Runner backend: native (default, uses local LiteLLM/Anthropic), claude (uses Claude Code CLI), codex (uses Codex CLI)",
						"enum": ["native", "claude", "codex", ""],
						"default": "native"
					},
					"native_base_url": {
						"type": "string",
						"description": "Base URL for native runner (e.g. http://localhost:8000 for local LiteLLM). Defaults to LITELLM_BASE_URL env var."
					},
					"native_model": {
						"type": "string",
						"description": "Model name for native runner (default: claude-sonnet-4-6)",
						"default": "claude-sonnet-4-6"
					},
					"native_api_key": {
						"type": "string",
						"description": "API key for native runner (LiteLLM master key or Anthropic key). Defaults to the server's LITELLM_API_KEY / ANTHROPIC_API_KEY env vars."
					},
					"env": {
						"type": "object",
						"description": "Extra environment variables to pass into the stoke subprocess (merged over the server's environ). Useful for passing LITELLM_BASE_URL, database credentials, or project-specific config.",
						"additionalProperties": {"type": "string"}
					},
					"resume": {
						"type": "boolean",
						"description": "If true, resume a prior incomplete SOW build for this repo_root instead of starting from scratch.",
						"default": false
					},
					"continue_on_failure": {
						"type": "boolean",
						"description": "If true, keep running subsequent sessions when a session's acceptance criteria fail. Defaults to false (halt on first failure).",
						"default": false
					}
				},
				"required": ["repo_root", "sow"]
			}`),
		},
		{
			Name: "stoke_get_mission_status",
			Description: "Check the status of an R1 build started with stoke_build_from_sow. " +
				"Returns: status (running|success|failed|cancelled), exit_code, started_at, finished_at, " +
				"and the path to stdout/stderr log files for inspection.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mission_id": {
						"type": "string",
						"description": "The mission_id returned by stoke_build_from_sow"
					}
				},
				"required": ["mission_id"]
			}`),
		},
		{
			Name: "stoke_get_mission_logs",
			Description: "Fetch the most recent stdout/stderr lines from a running or completed " +
				"R1 build. Useful for showing the user what R1 is doing without waiting " +
				"for the build to finish.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mission_id": {
						"type": "string",
						"description": "The mission_id returned by stoke_build_from_sow"
					},
					"tail_lines": {
						"type": "integer",
						"description": "Number of trailing lines to return (default 200)",
						"default": 200
					}
				},
				"required": ["mission_id"]
			}`),
		},
		{
			Name: "stoke_cancel_mission",
			Description: "Cancel a running R1 build. Sends SIGTERM to the entire process group " +
				"and waits briefly for a clean exit before escalating to SIGKILL. No-op if the " +
				"mission is already finished.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mission_id": {
						"type": "string",
						"description": "The mission_id returned by stoke_build_from_sow"
					}
				},
				"required": ["mission_id"]
			}`),
		},
		{
			Name: "stoke_list_missions",
			Description: "List all R1 builds started in this server session, with their current status.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}
}

// HandleToolCall dispatches an MCP tool invocation to the appropriate
// handler. S1-4 dual-accept: canonical r1_* and legacy stoke_* names
// both resolve here; legacyStokeServerToolName normalizes the prefix
// so each case arm handles the pair.
//
// When a LanesServer is attached via WithLanesServer (TASK-24), tool
// names with the r1.lanes.* prefix are routed to the lanes server so
// a single StokeServer endpoint serves both surfaces.
func (s *StokeServer) HandleToolCall(toolName string, args map[string]interface{}) (string, error) {
	// Lane-tool prefix routing. Checked before the stoke switch so a
	// future legacy stoke tool that happens to share a name with a
	// lane tool cannot accidentally shadow it.
	if strings.HasPrefix(toolName, "r1.lanes.") {
		s.mu.Lock()
		lanes := s.lanes
		s.mu.Unlock()
		if lanes != nil {
			return lanes.HandleToolCall(context.Background(), toolName, args)
		}
		return "", fmt.Errorf("unknown tool: %s (lanes server not wired)", toolName)
	}
	switch legacyStokeServerToolName(toolName) {
	case "stoke_build_from_sow":
		return s.handleBuildFromSOW(args)
	case "stoke_get_mission_status":
		return s.handleGetStatus(args)
	case "stoke_get_mission_logs":
		return s.handleGetLogs(args)
	case "stoke_cancel_mission":
		return s.handleCancelMission(args)
	case "stoke_list_missions":
		return s.handleListMissions()
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

func (s *StokeServer) handleBuildFromSOW(args map[string]interface{}) (string, error) {
	repoRoot, _ := args["repo_root"].(string)
	if repoRoot == "" {
		return "", fmt.Errorf("repo_root is required")
	}
	if !filepath.IsAbs(repoRoot) {
		abs, err := filepath.Abs(repoRoot)
		if err != nil {
			return "", fmt.Errorf("resolve repo_root: %w", err)
		}
		repoRoot = abs
	}
	sowText, _ := args["sow"].(string)
	if sowText == "" {
		return "", fmt.Errorf("sow is required")
	}
	// Reject blank/pathological SOW content early so the subprocess doesn't
	// fail with a cryptic parse error 30s later.
	if trimmed := strings.TrimSpace(sowText); len(trimmed) < 8 {
		return "", fmt.Errorf("sow is too short to be valid (got %d bytes)", len(trimmed))
	}

	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		return "", fmt.Errorf("create repo_root: %w", err)
	}
	stokeDir := r1dir.JoinFor(repoRoot)
	if err := os.MkdirAll(stokeDir, 0700); err != nil {
		return "", fmt.Errorf("create r1 data dir: %w", err)
	}

	// Allocate mission ID first so the SOW filename is unique per mission.
	// Using a unique path prevents concurrent builds from stomping on each
	// other's SOW files and simplifies debugging.
	missionID := fmt.Sprintf("m-%d", s.now().UnixNano())

	// Detect YAML vs JSON based on the first non-whitespace char. If the
	// content starts with '{' assume JSON; otherwise write as .yaml so the
	// sow loader picks the right parser.
	sowExt := "json"
	if !strings.HasPrefix(strings.TrimLeftFunc(sowText, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '\r'
	}), "{") {
		sowExt = "yaml"
	}
	sowPath := filepath.Join(stokeDir, missionID+"-sow."+sowExt)
	if err := os.WriteFile(sowPath, []byte(sowText), 0600); err != nil {
		return "", fmt.Errorf("write sow: %w", err)
	}

	stdoutPath := filepath.Join(stokeDir, missionID+".stdout.log")
	stderrPath := filepath.Join(stokeDir, missionID+".stderr.log")
	stdoutW, err := newCappedWriter(stdoutPath, maxLogBytes)
	if err != nil {
		return "", fmt.Errorf("create stdout: %w", err)
	}
	stderrW, err := newCappedWriter(stderrPath, maxLogBytes)
	if err != nil {
		stdoutW.Close()
		return "", fmt.Errorf("create stderr: %w", err)
	}

	// Build the stoke command line. Prefer our own Executable so a
	// sibling binary layout (tests / dev shells) works; fall back to
	// the PATH "stoke" lookup when os.Executable is unavailable.
	bin := s.stokeBin
	if bin == "" {
		if exe, exeErr := os.Executable(); exeErr == nil {
			bin = exe
		} else {
			bin = "stoke"
		}
	}
	cmdArgs := []string{
		"sow",
		"--repo", repoRoot,
		"--file", sowPath,
	}
	runner, _ := args["runner"].(string)
	if runner == "" {
		runner = "native"
	}
	cmdArgs = append(cmdArgs, "--runner", runner)
	if base, _ := args["native_base_url"].(string); base != "" {
		cmdArgs = append(cmdArgs, "--native-base-url", base)
	}
	if model, _ := args["native_model"].(string); model != "" {
		cmdArgs = append(cmdArgs, "--native-model", model)
	}
	if key, _ := args["native_api_key"].(string); key != "" {
		cmdArgs = append(cmdArgs, "--native-api-key", key)
	}
	if resume, _ := args["resume"].(bool); resume {
		cmdArgs = append(cmdArgs, "--resume")
	}
	if cont, _ := args["continue_on_failure"].(bool); cont {
		cmdArgs = append(cmdArgs, "--continue-on-failure")
	}

	// Merge caller-provided env vars over the server environ so MCP callers
	// can pass LITELLM_BASE_URL, database creds, etc. without the user
	// having to preconfigure the Claude Code launcher env.
	env := os.Environ()
	if envMap, ok := args["env"].(map[string]interface{}); ok {
		for k, v := range envMap {
			if sv, ok := v.(string); ok && k != "" {
				env = append(env, k+"="+sv)
			}
		}
	}

	proc, err := s.spawner(bin, cmdArgs, repoRoot, env, stdoutW, stderrW)
	if err != nil {
		stdoutW.Close()
		stderrW.Close()
		return "", fmt.Errorf("start stoke: %w", err)
	}

	rec := &missionRecord{
		ID:         missionID,
		RepoRoot:   repoRoot,
		SOWPath:    sowPath,
		StartedAt:  s.now(),
		Status:     "running",
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		Command:    append([]string{bin}, cmdArgs...),
		Cancel:     make(chan struct{}),
		proc:       proc,
	}

	s.mu.Lock()
	s.gcLocked()
	s.missions[missionID] = rec
	s.mu.Unlock()

	// Background waiter: updates status when the build finishes.
	go func() {
		waitErr := proc.Wait()
		stdoutW.Close()
		stderrW.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		rec.FinishedAt = s.now()
		// If we requested cancellation, mark cancelled regardless of exit error
		select {
		case <-rec.Cancel:
			rec.Status = "cancelled"
			rec.ExitCode = -1
			return
		default:
		}
		if waitErr != nil {
			rec.Status = "failed"
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				rec.ExitCode = exitErr.ExitCode()
			} else {
				rec.ExitCode = -1
			}
		} else {
			rec.Status = "success"
			rec.ExitCode = 0
		}
	}()

	resp := map[string]interface{}{
		"mission_id":  missionID,
		"status":      "running",
		"repo_root":   repoRoot,
		"sow_path":    sowPath,
		"stdout_log":  stdoutPath,
		"stderr_log":  stderrPath,
		"started_at":  rec.StartedAt.Format(time.RFC3339),
		"pid":         proc.Pid(),
		"command":     rec.Command,
		"description": "R1 build started in background. Poll stoke_get_mission_status with this mission_id.",
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleGetStatus(args map[string]interface{}) (string, error) {
	id, _ := args["mission_id"].(string)
	if id == "" {
		return "", fmt.Errorf("mission_id is required")
	}
	// Hold the lock for the whole snapshot. The background waiter
	// goroutine writes Status / FinishedAt / ExitCode under the same
	// lock when the subprocess exits, so reading without the lock
	// races. Snapshot every field we need into locals while holding
	// the lock and assemble the response after release.
	s.mu.Lock()
	rec, ok := s.missions[id]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("mission %q not found", id)
	}
	snap := struct {
		ID, Status, RepoRoot, StdoutPath, StderrPath string
		ExitCode                                     int
		StartedAt, FinishedAt                        time.Time
		Command                                      []string
	}{
		ID:         rec.ID,
		Status:     rec.Status,
		RepoRoot:   rec.RepoRoot,
		StdoutPath: rec.StdoutPath,
		StderrPath: rec.StderrPath,
		ExitCode:   rec.ExitCode,
		StartedAt:  rec.StartedAt,
		FinishedAt: rec.FinishedAt,
		Command:    rec.Command,
	}
	s.mu.Unlock()

	resp := map[string]interface{}{
		"mission_id": snap.ID,
		"status":     snap.Status,
		"exit_code":  snap.ExitCode,
		"repo_root":  snap.RepoRoot,
		"started_at": snap.StartedAt.Format(time.RFC3339),
		"stdout_log": snap.StdoutPath,
		"stderr_log": snap.StderrPath,
		"command":    snap.Command,
	}
	if !snap.FinishedAt.IsZero() {
		resp["finished_at"] = snap.FinishedAt.Format(time.RFC3339)
		resp["duration_sec"] = snap.FinishedAt.Sub(snap.StartedAt).Seconds()
	} else {
		resp["elapsed_sec"] = s.now().Sub(snap.StartedAt).Seconds()
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleGetLogs(args map[string]interface{}) (string, error) {
	id, _ := args["mission_id"].(string)
	if id == "" {
		return "", fmt.Errorf("mission_id is required")
	}
	tail := 200
	if v, ok := args["tail_lines"].(float64); ok {
		tail = int(v)
	}
	if tail < 1 {
		tail = 1
	}
	if tail > 10000 {
		tail = 10000
	}
	s.mu.Lock()
	rec, ok := s.missions[id]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("mission %q not found", id)
	}
	// Snapshot under the lock — Status is mutated by the waiter goroutine.
	missionID := rec.ID
	status := rec.Status
	stdoutPath := rec.StdoutPath
	stderrPath := rec.StderrPath
	s.mu.Unlock()

	stdout := tailFile(stdoutPath, tail)
	stderr := tailFile(stderrPath, tail)
	resp := map[string]interface{}{
		"mission_id": missionID,
		"status":     status,
		"stdout":     stdout,
		"stderr":     stderr,
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleCancelMission(args map[string]interface{}) (string, error) {
	id, _ := args["mission_id"].(string)
	if id == "" {
		return "", fmt.Errorf("mission_id is required")
	}
	s.mu.Lock()
	rec, ok := s.missions[id]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("mission %q not found", id)
	}
	missionID := rec.ID
	currentStatus := rec.Status
	cancelCh := rec.Cancel
	proc := rec.proc
	s.mu.Unlock()

	if currentStatus != "running" {
		resp := map[string]interface{}{
			"mission_id": missionID,
			"status":     currentStatus,
			"message":    "mission already finished",
		}
		out, _ := json.MarshalIndent(resp, "", "  ")
		return string(out), nil
	}
	// Signal cancellation first so the waiter marks the status correctly
	// even if Kill() races the natural exit.
	select {
	case <-cancelCh:
		// Already cancelled
	default:
		close(cancelCh)
	}
	if err := proc.Kill(); err != nil {
		return "", fmt.Errorf("kill mission: %w", err)
	}
	resp := map[string]interface{}{
		"mission_id": missionID,
		"status":     "cancelling",
		"message":    "SIGTERM/SIGKILL sent; status will flip to cancelled when process exits",
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleListMissions() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]map[string]interface{}, 0, len(s.missions))
	// Stable ordering: newest first
	ids := make([]string, 0, len(s.missions))
	for id := range s.missions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.missions[ids[i]].StartedAt.After(s.missions[ids[j]].StartedAt)
	})
	for _, id := range ids {
		rec := s.missions[id]
		entry := map[string]interface{}{
			"mission_id": rec.ID,
			"status":     rec.Status,
			"repo_root":  rec.RepoRoot,
			"started_at": rec.StartedAt.Format(time.RFC3339),
		}
		if !rec.FinishedAt.IsZero() {
			entry["finished_at"] = rec.FinishedAt.Format(time.RFC3339)
			entry["exit_code"] = rec.ExitCode
		}
		list = append(list, entry)
	}
	out, _ := json.MarshalIndent(map[string]interface{}{"missions": list}, "", "  ")
	return string(out), nil
}

// gcLocked removes completed missions older than missionGCAge from the
// in-memory map. Caller must hold s.mu. Logs on disk are left alone so a
// later invocation can still inspect them by path.
func (s *StokeServer) gcLocked() {
	cutoff := s.now().Add(-missionGCAge)
	for id, rec := range s.missions {
		if rec.Status == "running" {
			continue
		}
		if !rec.FinishedAt.IsZero() && rec.FinishedAt.Before(cutoff) {
			delete(s.missions, id)
		}
	}
}

// tailFile returns the last n lines of a file. Best-effort: missing files
// or read errors return an empty string rather than failing the tool call.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n*2 {
			lines = lines[len(lines)-n:]
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// cappedWriter wraps a file and truncates to the tail half when the file
// exceeds maxBytes. This prevents a runaway build from filling disk with
// its own log output.
type cappedWriter struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	written  int64
	maxBytes int64
}

func newCappedWriter(path string, maxBytes int64) (*cappedWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &cappedWriter{path: path, f: f, maxBytes: maxBytes}, nil
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, fmt.Errorf("cappedWriter closed")
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	if w.written > w.maxBytes {
		// Truncate to tail half. This is a last-resort safety net — a sane
		// build should never hit it. We keep the file open to not break the
		// subprocess's stdout handle.
		w.truncateLocked()
	}
	return n, err
}

func (w *cappedWriter) truncateLocked() {
	// Read the tail half
	half := w.maxBytes / 2
	if _, err := w.f.Seek(-half, io.SeekEnd); err != nil {
		return
	}
	buf := make([]byte, half)
	n, _ := w.f.Read(buf)
	// Reopen truncated
	w.f.Close()
	f, err := os.Create(w.path)
	if err != nil {
		w.f = nil
		return
	}
	notice := fmt.Sprintf("[stoke-mcp] log truncated to tail %d bytes\n", half)
	f.Write([]byte(notice))
	f.Write(buf[:n])
	w.f = f
	w.written = int64(len(notice)) + int64(n)
}

func (w *cappedWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// realSpawn is the default spawnFunc: launches bin with its own process
// group so cancellation can target the whole tree.
func realSpawn(bin string, args []string, workdir string, env []string, stdout, stderr io.Writer) (processHandle, error) {
	cmd := exec.Command(bin, args...) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.Dir = workdir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = env
	procutil.ConfigureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &realHandle{cmd: cmd}, nil
}

type realHandle struct {
	cmd *exec.Cmd
}

func (h *realHandle) Wait() error { return h.cmd.Wait() }

func (h *realHandle) Pid() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

// Kill sends SIGTERM to the process group, waits briefly for a clean exit,
// then escalates to SIGKILL if still alive. Matches engine's process-group
// isolation pattern.
func (h *realHandle) Kill() error {
	if h.cmd.Process == nil {
		return fmt.Errorf("no process")
	}
	// Getpgid returning an error is expected when the process has
	// already exited (ESRCH). In that case there is nothing left to
	// kill and Kill() has nothing to do, so we skip the signal path
	// entirely and return the default (nil) error. We log the
	// lookup error so an unexpected failure (not just ESRCH) is
	// still visible in operator logs.
	if err := procutil.Terminate(h.cmd); err == nil {
		// Give the subprocess a moment to flush and exit cleanly.
		done := make(chan struct{})
		go func() {
			_, _ = h.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = procutil.Kill(h.cmd)
		}
	} else {
		logging.Global().Info("mcp.stoke_server: Kill() terminate failed; treating as process-already-gone",
			"pid", h.cmd.Process.Pid, "err", err)
	}
	return nil
}

// ServeStdio runs the MCP server over stdin/stdout (the MCP stdio transport).
// Speaks JSON-RPC 2.0; same protocol as CodebaseServer.ServeStdio.
func (s *StokeServer) ServeStdio() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32700, Message: "Parse error"})
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]bool{"listChanged": false},
				},
				"serverInfo": map[string]string{
					"name":    "stoke",
					"version": "1.0.0",
				},
			}, nil)
		case "notifications/initialized":
			// No response
		case "tools/list":
			tools := s.ToolDefinitions()
			var toolList []map[string]interface{}
			for _, t := range tools {
				entry := map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": t.InputSchema,
				}
				// Lane tools (TASK-24) advertise an output_schema per
				// spec §7. Other stoke/r1 tools omit it, so the shape
				// stays additive: clients that don't understand
				// outputSchema simply ignore the extra key.
				if len(t.OutputSchema) > 0 {
					entry["outputSchema"] = t.OutputSchema
				}
				toolList = append(toolList, entry)
			}
			writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{"tools": toolList}, nil)
		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			paramsBytes, _ := json.Marshal(req.Params)
			if err := json.Unmarshal(paramsBytes, &params); err != nil {
				writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32602, Message: "Invalid params"})
				continue
			}
			result, err := s.HandleToolCall(params.Name, params.Arguments)
			if err != nil {
				writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("Error: %v", err)}},
					"isError": true,
				}, nil)
			} else {
				writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": result}},
				}, nil)
			}
		default:
			writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32601, Message: "Method not found"})
		}
	}
	return scanner.Err()
}
