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
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// StokeServer is an MCP tool server that exposes Stoke build operations to
// Claude Code or any other MCP-aware client.
type StokeServer struct {
	mu       sync.Mutex
	missions map[string]*missionRecord
	// stokeBin is the path to the stoke binary used to spawn build subprocesses.
	// If empty, the server's own argv[0] is used.
	stokeBin string
}

// missionRecord tracks an in-progress or completed Stoke build.
type missionRecord struct {
	ID         string
	RepoRoot   string
	SOWPath    string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string // running | success | failed
	ExitCode   int
	StdoutPath string
	StderrPath string
	cmd        *exec.Cmd
}

// NewStokeServer creates a new Stoke MCP server.
func NewStokeServer(stokeBin string) *StokeServer {
	return &StokeServer{
		missions: make(map[string]*missionRecord),
		stokeBin: stokeBin,
	}
}

// ToolDefinitions returns the MCP tool definitions for Stoke build operations.
func (s *StokeServer) ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name: "stoke_build_from_sow",
			Description: "Kick off a Stoke build of a project from a Statement of Work (SOW). " +
				"The SOW describes sessions, tasks, acceptance criteria, infrastructure requirements, " +
				"and stack details. Stoke will build the project session-by-session, gating each session " +
				"on its acceptance criteria, and supervising agents for liveness instead of timing out. " +
				"Returns a mission_id for status polling. The build runs in the background — use " +
				"stoke_get_mission_status to check progress.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"repo_root": {
						"type": "string",
						"description": "Absolute path to the project directory where Stoke will build (will be created if it doesn't exist)"
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
					}
				},
				"required": ["repo_root", "sow"]
			}`),
		},
		{
			Name: "stoke_get_mission_status",
			Description: "Check the status of a Stoke build started with stoke_build_from_sow. " +
				"Returns: status (running|success|failed), exit_code, started_at, finished_at, " +
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
				"Stoke build. Useful for showing the user what Stoke is doing without waiting " +
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
			Name: "stoke_list_missions",
			Description: "List all Stoke builds started in this server session, with their current status.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}
}

// HandleToolCall dispatches an MCP tool invocation to the appropriate handler.
func (s *StokeServer) HandleToolCall(toolName string, args map[string]interface{}) (string, error) {
	switch toolName {
	case "stoke_build_from_sow":
		return s.handleBuildFromSOW(args)
	case "stoke_get_mission_status":
		return s.handleGetStatus(args)
	case "stoke_get_mission_logs":
		return s.handleGetLogs(args)
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
	sowText, _ := args["sow"].(string)
	if sowText == "" {
		return "", fmt.Errorf("sow is required")
	}

	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		return "", fmt.Errorf("create repo_root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".stoke"), 0700); err != nil {
		return "", fmt.Errorf("create .stoke: %w", err)
	}

	// Persist SOW
	sowPath := filepath.Join(repoRoot, ".stoke", "mcp-sow.json")
	if err := os.WriteFile(sowPath, []byte(sowText), 0600); err != nil {
		return "", fmt.Errorf("write sow: %w", err)
	}

	// Allocate mission ID
	missionID := fmt.Sprintf("m-%d", time.Now().UnixNano())
	stdoutPath := filepath.Join(repoRoot, ".stoke", missionID+".stdout.log")
	stderrPath := filepath.Join(repoRoot, ".stoke", missionID+".stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		return "", fmt.Errorf("create stdout: %w", err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		stdout.Close()
		return "", fmt.Errorf("create stderr: %w", err)
	}

	// Build the stoke command line
	bin := s.stokeBin
	if bin == "" {
		bin = "stoke"
	}
	cmdArgs := []string{
		"sow",
		"--repo", repoRoot,
		"--file", sowPath,
	}
	if runner, _ := args["runner"].(string); runner != "" {
		cmdArgs = append(cmdArgs, "--runner", runner)
	} else {
		cmdArgs = append(cmdArgs, "--runner", "native")
	}
	if base, _ := args["native_base_url"].(string); base != "" {
		cmdArgs = append(cmdArgs, "--native-base-url", base)
	}
	if model, _ := args["native_model"].(string); model != "" {
		cmdArgs = append(cmdArgs, "--native-model", model)
	}

	cmd := exec.Command(bin, cmdArgs...)
	cmd.Dir = repoRoot
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		return "", fmt.Errorf("start stoke: %w", err)
	}

	rec := &missionRecord{
		ID:         missionID,
		RepoRoot:   repoRoot,
		SOWPath:    sowPath,
		StartedAt:  time.Now(),
		Status:     "running",
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
		cmd:        cmd,
	}

	s.mu.Lock()
	s.missions[missionID] = rec
	s.mu.Unlock()

	// Background waiter: updates status when the build finishes.
	go func() {
		err := cmd.Wait()
		stdout.Close()
		stderr.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		rec.FinishedAt = time.Now()
		if err != nil {
			rec.Status = "failed"
			if exitErr, ok := err.(*exec.ExitError); ok {
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
		"command":     append([]string{bin}, cmdArgs...),
		"description": "Stoke build started in background. Poll stoke_get_mission_status with this mission_id.",
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleGetStatus(args map[string]interface{}) (string, error) {
	id, _ := args["mission_id"].(string)
	if id == "" {
		return "", fmt.Errorf("mission_id is required")
	}
	s.mu.Lock()
	rec, ok := s.missions[id]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("mission %q not found", id)
	}

	resp := map[string]interface{}{
		"mission_id":  rec.ID,
		"status":      rec.Status,
		"exit_code":   rec.ExitCode,
		"repo_root":   rec.RepoRoot,
		"started_at":  rec.StartedAt.Format(time.RFC3339),
		"stdout_log":  rec.StdoutPath,
		"stderr_log":  rec.StderrPath,
	}
	if !rec.FinishedAt.IsZero() {
		resp["finished_at"] = rec.FinishedAt.Format(time.RFC3339)
		resp["duration_sec"] = rec.FinishedAt.Sub(rec.StartedAt).Seconds()
	} else {
		resp["elapsed_sec"] = time.Since(rec.StartedAt).Seconds()
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
	s.mu.Lock()
	rec, ok := s.missions[id]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("mission %q not found", id)
	}

	stdout := tailFile(rec.StdoutPath, tail)
	stderr := tailFile(rec.StderrPath, tail)
	resp := map[string]interface{}{
		"mission_id": rec.ID,
		"status":     rec.Status,
		"stdout":     stdout,
		"stderr":     stderr,
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return string(out), nil
}

func (s *StokeServer) handleListMissions() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []map[string]interface{}
	for _, rec := range s.missions {
		list = append(list, map[string]interface{}{
			"mission_id": rec.ID,
			"status":     rec.Status,
			"repo_root":  rec.RepoRoot,
			"started_at": rec.StartedAt.Format(time.RFC3339),
		})
	}
	out, _ := json.MarshalIndent(map[string]interface{}{"missions": list}, "", "  ")
	return string(out), nil
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
				toolList = append(toolList, map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": json.RawMessage(t.InputSchema),
				})
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
