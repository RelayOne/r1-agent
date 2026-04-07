// Package tools implements the tool execution layer for Stoke's native
// agent loop. Each tool follows the Anthropic Messages API tool_use pattern,
// executing operations and returning string results.
//
// Architecture decisions from P62 research:
//   - str_replace (Edit) is the default editing strategy, not whole-file rewrite
//   - Read must be called before Edit (prevents blind edits)
//   - Uniqueness enforcement on old_string match
//   - File content in cat -n format with line numbers
//   - Bash output truncated at 30KB with middle truncation
//   - Error results are structured and actionable
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/provider"
)

const (
	// MaxBashOutput is the max output length from bash commands (30KB).
	MaxBashOutput = 30 * 1024
	// DefaultBashTimeout is the default bash command timeout.
	DefaultBashTimeout = 2 * time.Minute
	// MaxBashTimeout is the maximum allowed bash timeout.
	MaxBashTimeout = 10 * time.Minute
	// MaxReadLines is the default max lines to read.
	MaxReadLines = 2000
	// MaxLineLength is the max chars per line before truncation.
	MaxLineLength = 2000
)

// Registry manages available tools and tracks state (e.g., which files have been read).
type Registry struct {
	workDir  string
	readFiles sync.Map // tracks files that have been read (for Edit guard)
}

// NewRegistry creates a tool registry rooted at the given working directory.
func NewRegistry(workDir string) *Registry {
	return &Registry{workDir: workDir}
}

// Definitions returns the tool definitions for the Anthropic API.
func (r *Registry) Definitions() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "read_file",
			Description: "Read contents of a file. Returns content in cat -n format with line numbers.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]string{"type": "string", "description": "File path (relative to working directory)"},
					"offset": map[string]interface{}{"type": "integer", "description": "Starting line number (1-indexed, default 1)"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to read (default 2000)"},
				},
				"required": []string{"path"},
			}),
		},
		{
			Name:        "edit_file",
			Description: "Replace an exact string match in a file. File must have been read first. old_string must be unique in the file.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":        map[string]string{"type": "string", "description": "File path"},
					"old_string":  map[string]string{"type": "string", "description": "Exact text to find and replace"},
					"new_string":  map[string]string{"type": "string", "description": "Replacement text"},
					"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default false)"},
				},
				"required": []string{"path", "old_string", "new_string"},
			}),
		},
		{
			Name:        "write_file",
			Description: "Create or overwrite a file with the given content.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]string{"type": "string", "description": "File path"},
					"content": map[string]string{"type": "string", "description": "File content to write"},
				},
				"required": []string{"path", "content"},
			}),
		},
		{
			Name:        "bash",
			Description: "Execute a bash command. Output truncated at 30KB.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]string{"type": "string", "description": "The bash command to execute"},
					"timeout": map[string]interface{}{"type": "integer", "description": "Timeout in milliseconds (default 120000, max 600000)"},
				},
				"required": []string{"command"},
			}),
		},
		{
			Name:        "grep",
			Description: "Search file contents using ripgrep regex. Returns matching file paths by default.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]string{"type": "string", "description": "Regex pattern to search for"},
					"path":    map[string]string{"type": "string", "description": "Directory or file to search (default: working directory)"},
					"glob":    map[string]string{"type": "string", "description": "File glob filter (e.g. '*.go')"},
					"context": map[string]interface{}{"type": "integer", "description": "Context lines around matches"},
				},
				"required": []string{"pattern"},
			}),
		},
		{
			Name:        "glob",
			Description: "Find files matching a glob pattern. Returns paths sorted by modification time.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]string{"type": "string", "description": "Glob pattern (e.g. '**/*.go', 'src/**/*.ts')"},
					"path":    map[string]string{"type": "string", "description": "Base directory (default: working directory)"},
				},
				"required": []string{"pattern"},
			}),
		},
	}
}

// Handle dispatches a tool call to the appropriate handler.
func (r *Registry) Handle(ctx context.Context, name string, input json.RawMessage) (string, error) {
	switch name {
	case "read_file":
		return r.handleRead(input)
	case "edit_file":
		return r.handleEdit(input)
	case "write_file":
		return r.handleWrite(input)
	case "bash":
		return r.handleBash(ctx, input)
	case "grep":
		return r.handleGrep(ctx, input)
	case "glob":
		return r.handleGlob(input)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// --- Tool handlers ---

func (r *Registry) handleRead(input json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path, pathErr := r.resolvePath(args.Path)
	if pathErr != nil {
		return "", pathErr
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", args.Path, err)
	}

	// Track that this file has been read (for Edit guard)
	r.readFiles.Store(path, true)

	lines := strings.Split(string(content), "\n")
	offset := args.Offset
	if offset < 1 {
		offset = 1
	}
	limit := args.Limit
	if limit < 1 {
		limit = MaxReadLines
	}

	// Convert to 0-indexed
	start := offset - 1
	if start >= len(lines) {
		return "(empty — offset beyond end of file)", nil
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}

	// Format as cat -n
	var sb strings.Builder
	for i := start; i < end; i++ {
		line := lines[i]
		if len(line) > MaxLineLength {
			line = line[:MaxLineLength] + "... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("%6d\t%s\n", i+1, line))
	}
	return sb.String(), nil
}

func (r *Registry) handleEdit(input json.RawMessage) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path, pathErr := r.resolvePath(args.Path)
	if pathErr != nil {
		return "", pathErr
	}

	// Guard: must have read file first
	if _, ok := r.readFiles.Load(path); !ok {
		return "", fmt.Errorf("must read %s before editing — call read_file first", args.Path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", args.Path, err)
	}

	s := string(content)

	// Use cascading str_replace: exact → whitespace → ellipsis → fuzzy
	result, err := StrReplace(s, args.OldString, args.NewString, args.ReplaceAll)
	if err != nil {
		return "", fmt.Errorf("%s: %w", args.Path, err)
	}

	if err := os.WriteFile(path, []byte(result.NewContent), 0644); err != nil {
		return "", err
	}

	if args.ReplaceAll {
		return fmt.Sprintf("Replaced %d occurrences in %s (method: %s)", result.Replacements, args.Path, result.Method), nil
	}
	msg := fmt.Sprintf("Edited %s successfully", args.Path)
	if result.Method != "exact" {
		msg += fmt.Sprintf(" (matched via %s, confidence: %.0f%%)", result.Method, result.Confidence*100)
	}
	return msg, nil
}

func (r *Registry) handleWrite(input json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path, pathErr := r.resolvePath(args.Path)
	if pathErr != nil {
		return "", pathErr
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil {
		return "", err
	}

	// Track as read since we know the content
	r.readFiles.Store(path, true)
	return fmt.Sprintf("Wrote %s (%d bytes)", args.Path, len(args.Content)), nil
}

func (r *Registry) handleBash(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"` // milliseconds
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	timeout := DefaultBashTimeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Millisecond
		if timeout > MaxBashTimeout {
			timeout = MaxBashTimeout
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
	cmd.Dir = r.workDir

	output, err := cmd.CombinedOutput()
	result := string(output)

	// Truncate if too long (preserve first and last portions)
	if len(result) > MaxBashOutput {
		half := MaxBashOutput / 2
		result = result[:half] +
			fmt.Sprintf("\n\n[output truncated: %d chars, showing first %d and last %d]\n\n", len(result), half, half) +
			result[len(result)-half:]
	}

	if err != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("command timed out after %v", timeout)
		}
		// Include exit code in result, don't return error (let model see output)
		return fmt.Sprintf("%s\n(exit code: %s)", result, err.Error()), nil
	}
	return result, nil
}

func (r *Registry) handleGrep(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
		Context int    `json:"context"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	rgArgs := []string{"--no-heading", "--line-number"}
	if args.Context > 0 {
		rgArgs = append(rgArgs, fmt.Sprintf("-C%d", args.Context))
	}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	rgArgs = append(rgArgs, args.Pattern)

	searchPath := r.workDir
	if args.Path != "" {
		var pathErr error
		searchPath, pathErr = r.resolvePath(args.Path)
		if pathErr != nil {
			return "", pathErr
		}
	}
	rgArgs = append(rgArgs, searchPath)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	output, err := cmd.CombinedOutput()
	result := string(output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "(no matches found)", nil
		}
		// rg might not be installed, fall back to grep
		if strings.Contains(err.Error(), "not found") {
			return "", fmt.Errorf("ripgrep (rg) not found — install it or use bash with grep")
		}
		return result, nil
	}

	if len(result) > MaxBashOutput {
		result = result[:MaxBashOutput] + "\n(results truncated)"
	}
	return result, nil
}

func (r *Registry) handleGlob(input json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	base := r.workDir
	if args.Path != "" {
		var pathErr error
		base, pathErr = r.resolvePath(args.Path)
		if pathErr != nil {
			return "", pathErr
		}
	}

	pattern := filepath.Join(base, args.Pattern)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid glob pattern: %w", err)
	}

	if len(matches) == 0 {
		return "(no files found)", nil
	}

	// Make paths relative
	var results []string
	for _, m := range matches {
		rel, err := filepath.Rel(r.workDir, m)
		if err != nil {
			rel = m
		}
		results = append(results, rel)
	}
	return strings.Join(results, "\n"), nil
}

// --- Helpers ---

// resolvePath resolves a path relative to workDir and ensures it stays within
// the workDir boundary. Absolute paths are rejected unless they are under workDir.
func (r *Registry) resolvePath(path string) (string, error) {
	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Join(r.workDir, path)
	}
	resolved = filepath.Clean(resolved)

	// Ensure the resolved path is within workDir (path confinement).
	workDirClean := filepath.Clean(r.workDir)
	if !strings.HasPrefix(resolved, workDirClean+string(filepath.Separator)) && resolved != workDirClean {
		return "", fmt.Errorf("path %q escapes working directory %q", path, workDirClean)
	}
	return resolved, nil
}

func mustJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
