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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ericmacdougall/stoke/internal/env"
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
	workDir   string
	readFiles sync.Map // tracks files that have been read (for Edit guard)
	environ   env.Environment // optional execution environment (nil = no env tools)
	envHandle *env.Handle     // optional environment handle
}

// SetEnvironment configures the registry with an execution environment,
// enabling the env_exec, env_copy_in, and env_copy_out tools.
func (r *Registry) SetEnvironment(e env.Environment, h *env.Handle) {
	r.environ = e
	r.envHandle = h
}

// NewRegistry creates a tool registry rooted at the given working directory.
func NewRegistry(workDir string) *Registry {
	return &Registry{workDir: workDir}
}

// WorkDir returns the resolved absolute path the registry is rooted at.
// Tools interpret all relative paths as being relative to this directory.
func (r *Registry) WorkDir() string { return r.workDir }

// Definitions returns the tool definitions for the Anthropic API.
//
// All path parameters interpret relative paths as being relative to the
// registry's working directory (see WorkDir()). The bash tool also runs
// its commands with that directory as cwd. Tools that modify files echo
// back the resolved absolute path in their response so the model can
// verify writes without guessing where they landed.
func (r *Registry) Definitions() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "read_file",
			Description: "Read contents of a file. Path may be relative to the project working directory (preferred) or absolute (must still be under the working directory). Returns content prefixed with the resolved absolute path plus cat -n formatted lines with 1-indexed line numbers. Call this before edit_file.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]string{"type": "string", "description": "File path, relative to the working directory OR absolute (must resolve under the working directory)"},
					"offset": map[string]interface{}{"type": "integer", "description": "Starting line number (1-indexed, default 1)"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to read (default 2000)"},
				},
				"required": []string{"path"},
			}),
		},
		{
			Name:        "edit_file",
			Description: "Replace an exact string match in a file. You MUST read_file first on this path — the edit will reject any path that hasn't been read. old_string must be unique in the file unless replace_all is true. Response includes the resolved absolute path on success.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":        map[string]string{"type": "string", "description": "File path, relative to the working directory OR absolute"},
					"old_string":  map[string]string{"type": "string", "description": "Exact text to find and replace"},
					"new_string":  map[string]string{"type": "string", "description": "Replacement text"},
					"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default false)"},
				},
				"required": []string{"path", "old_string", "new_string"},
			}),
		},
		{
			Name:        "write_file",
			Description: "Create or overwrite a file with the given content. Parent directories are created automatically. Response includes the resolved absolute path AND working_dir so you can verify where the file landed — if you then run `pwd && ls -la` via bash, the cwd will match working_dir and you'll see the file.",
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
		{
			Name:        "env_exec",
			Description: "Execute a command in the provisioned execution environment. Returns stdout, stderr, and exit code.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]string{"type": "string", "description": "Command to execute (passed to bash -lc)"},
					"dir":     map[string]string{"type": "string", "description": "Working directory override (default: environment work dir)"},
					"timeout": map[string]interface{}{"type": "integer", "description": "Timeout in milliseconds (default 120000)"},
				},
				"required": []string{"command"},
			}),
		},
		{
			Name:        "env_copy_in",
			Description: "Copy a local file or directory into the execution environment.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"src": map[string]string{"type": "string", "description": "Local source path"},
					"dst": map[string]string{"type": "string", "description": "Remote destination path in the environment"},
				},
				"required": []string{"src", "dst"},
			}),
		},
		{
			Name:        "env_copy_out",
			Description: "Copy a file or directory from the execution environment to the local filesystem.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"src": map[string]string{"type": "string", "description": "Remote source path in the environment"},
					"dst": map[string]string{"type": "string", "description": "Local destination path"},
				},
				"required": []string{"src", "dst"},
			}),
		},
	}
}

// Handle dispatches a tool call to the appropriate handler.
// Accepts both snake_case (read_file) and PascalCase (Read) tool names
// since different models/providers may use either convention.
func (r *Registry) Handle(ctx context.Context, name string, input json.RawMessage) (string, error) {
	fmt.Fprintf(os.Stderr, "[tools] %s in workDir=%s input=%s\n", name, r.workDir, string(input)[:min(len(input), 200)])
	switch name {
	case "read_file", "Read", "read":
		return r.handleRead(input)
	case "edit_file", "Edit", "edit", "str_replace_editor":
		return r.handleEdit(input)
	case "write_file", "Write", "write", "create_file":
		return r.handleWrite(input)
	case "bash", "Bash", "execute_bash", "run_command":
		return r.handleBash(ctx, input)
	case "grep", "Grep", "search":
		return r.handleGrep(ctx, input)
	case "glob", "Glob", "find_files":
		return r.handleGlob(input)
	case "env_exec":
		return r.handleEnvExec(ctx, input)
	case "env_copy_in":
		return r.handleEnvCopyIn(ctx, input)
	case "env_copy_out":
		return r.handleEnvCopyOut(ctx, input)
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

	// Format as cat -n, prefixed with the resolved absolute path so
	// the model has an unambiguous anchor to reason about.
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (read %d of %d lines)\n", path, end-start, len(lines))
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

	if err := os.WriteFile(path, []byte(result.NewContent), 0644); err != nil { // #nosec G306 -- str_replace tool target (existing source file); 0644 preserves source perms.
		return "", err
	}

	rel, _ := filepath.Rel(r.workDir, path)
	if rel == "" {
		rel = args.Path
	}
	if args.ReplaceAll {
		return fmt.Sprintf("Replaced %d occurrences in %s (method: %s)\n  absolute: %s", result.Replacements, rel, result.Method, path), nil
	}
	msg := fmt.Sprintf("Edited %s successfully", rel)
	if result.Method != "exact" {
		msg += fmt.Sprintf(" (matched via %s, confidence: %.0f%%)", result.Method, result.Confidence*100)
	}
	msg += fmt.Sprintf("\n  absolute: %s", path)
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

	// Some models double-escape newlines in tool-call content —
	// emit `\\n` where they should emit `\n`. JSON unmarshal then
	// produces literal "\n" (2 chars: backslash + n) instead of a
	// real newline, and the written file is one huge line with
	// visible escape sequences. Detect + fix: if content has many
	// literal `\n` pairs and few real newlines, rewrite with real
	// escapes. (Run 15 bug: sonnet-4-6 emitted entire API-client
	// source with \\n and \\t double-escaped.)
	args.Content = maybeUnescapeContent(args.Content)

	// Pre-flight syntax check on code/data files: reject writes whose
	// brackets, braces, parens, or string literals are unbalanced. The
	// model gets a structured error and can retry with corrected
	// content rather than the broken file landing on disk and tripping
	// downstream tsc/eslint/build checks. Conservative — only checks
	// obvious top-level imbalance for languages where {/}/[/]/(/) and
	// "/' are the universal balancing tokens. Skips markdown, plain
	// text, and unknown extensions.
	if synErr := validateContentSyntax(args.Path, args.Content); synErr != nil {
		return "", synErr
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

	if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil { // #nosec G306 -- str_replace tool target (existing source file); 0644 preserves source perms.
		return "", err
	}

	// Track as read since we know the content
	r.readFiles.Store(path, true)

	// Report the resolved absolute path AND the working directory so
	// the model has an unambiguous anchor. Without this, a relative
	// write like "Cargo.toml" echoes back as "Wrote Cargo.toml" — no
	// confirmation of WHERE the file actually went — and the next
	// `pwd && ls -la` can look like a failure to the model even though
	// the write succeeded. (This was the "3 consecutive tool errors"
	// root cause.)
	rel, _ := filepath.Rel(r.workDir, path)
	if rel == "" {
		rel = args.Path
	}
	return fmt.Sprintf("Wrote %s (%d bytes)\n  absolute: %s\n  working_dir: %s", rel, len(args.Content), path, r.workDir), nil
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

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.Dir = r.workDir
	// Process-group isolation: pnpm (and other build tools) spawn
	// hundreds of child processes. exec.CommandContext only kills
	// the immediate child on timeout; grandchildren inherit the
	// stdout pipe and keep it open, blocking CombinedOutput()
	// forever even after the parent is killed. Put the command in
	// its own process group so Cancel can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID = kill the process group.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	}
	// WaitDelay bounds how long cmd.Wait will hang after Cancel
	// fires reading leftover pipe output. Without this, a
	// group-killed pnpm with hundreds of zombie grandchildren can
	// still hold the pipe open for a long time. 5s is enough to
	// drain any in-flight buffer + bail.
	cmd.WaitDelay = 5 * time.Second

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
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
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
	results := make([]string, 0, len(matches))
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

// --- Environment tool handlers ---

func (r *Registry) handleEnvExec(ctx context.Context, input json.RawMessage) (string, error) {
	if r.environ == nil || r.envHandle == nil {
		return "", fmt.Errorf("no execution environment configured")
	}
	var args struct {
		Command string `json:"command"`
		Dir     string `json:"dir"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	opts := env.ExecOpts{Dir: args.Dir}
	if args.Timeout > 0 {
		opts.Timeout = time.Duration(args.Timeout) * time.Millisecond
	}

	result, err := r.environ.Exec(ctx, r.envHandle, []string{"bash", "-lc", args.Command}, opts)
	if err != nil {
		return "", fmt.Errorf("env exec: %w", err)
	}

	output := result.CombinedOutput()
	if len(output) > MaxBashOutput {
		half := MaxBashOutput / 2
		output = output[:half] + "\n... [truncated] ...\n" + output[len(output)-half:]
	}

	return fmt.Sprintf("exit_code: %d\nduration: %s\n\n%s", result.ExitCode, result.Duration, output), nil
}

func (r *Registry) handleEnvCopyIn(ctx context.Context, input json.RawMessage) (string, error) {
	if r.environ == nil || r.envHandle == nil {
		return "", fmt.Errorf("no execution environment configured")
	}
	var args struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// Resolve local path with confinement.
	srcPath, err := r.resolvePath(args.Src)
	if err != nil {
		return "", err
	}

	if err := r.environ.CopyIn(ctx, r.envHandle, srcPath, args.Dst); err != nil {
		return "", fmt.Errorf("env copy-in: %w", err)
	}
	return fmt.Sprintf("copied %s → env:%s", args.Src, args.Dst), nil
}

func (r *Registry) handleEnvCopyOut(ctx context.Context, input json.RawMessage) (string, error) {
	if r.environ == nil || r.envHandle == nil {
		return "", fmt.Errorf("no execution environment configured")
	}
	var args struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// Resolve local destination path with confinement.
	dstPath, err := r.resolvePath(args.Dst)
	if err != nil {
		return "", err
	}

	if err := r.environ.CopyOut(ctx, r.envHandle, args.Src, dstPath); err != nil {
		return "", fmt.Errorf("env copy-out: %w", err)
	}
	return fmt.Sprintf("copied env:%s → %s", args.Src, args.Dst), nil
}

func mustJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// maybeUnescapeContent detects + fixes the double-escape pattern
// that some models (observed: sonnet-4-6 through LiteLLM) emit in
// tool-call content. The model produces `\\n` in the JSON string
// where it should produce `\n`, causing json.Unmarshal to leave the
// literal backslash+n in the output string and the written file to
// have visible escape sequences instead of newlines.
//
// Heuristic: if the string has ≥5 literal `\n` (two bytes:
// backslash + 'n') and fewer real newlines than literal escapes,
// the file is double-escaped. Re-apply strconv.Unquote as a pass
// through standard JSON/Go escape rules; if that fails, fall back
// to targeted per-sequence replacement for the common escapes.
//
// Returns the content unchanged when the heuristic doesn't trip so
// files that genuinely contain the literal `\n` string (e.g. shell
// scripts that echo `\n` as text, test fixtures) are preserved.
func maybeUnescapeContent(content string) string {
	literalEscapes := strings.Count(content, `\n`)
	realNewlines := strings.Count(content, "\n")
	if literalEscapes < 5 {
		return content
	}
	if realNewlines > literalEscapes/2 {
		// File has plenty of real newlines relative to literal
		// escapes — probably a legitimate fixture containing the
		// string. Don't touch.
		return content
	}
	if unq, err := strconv.Unquote(`"` + content + `"`); err == nil {
		return unq
	}
	// Fallback for content that won't round-trip through Unquote
	// (e.g. literal quotes inside). Replace the common escape
	// sequences directly.
	out := content
	out = strings.ReplaceAll(out, `\n`, "\n")
	out = strings.ReplaceAll(out, `\t`, "\t")
	out = strings.ReplaceAll(out, `\r`, "\r")
	out = strings.ReplaceAll(out, `\\`, `\`)
	return out
}

// validateContentSyntax performs a fast brace/bracket/paren/string
// balance check on file content before it lands on disk. Returns a
// non-nil error when the content is clearly broken (unclosed string,
// negative depth, or non-zero depth at EOF) so the model retries
// rather than committing a syntactically broken file. Returns nil
// for file types where balance-checking is unsafe (markdown,
// templated languages, plain text) or the content is well-formed.
//
// Only activates for: .go .ts .tsx .js .jsx .mjs .cjs .json .jsonc
// .css .scss .less. Skips everything else conservatively.
func validateContentSyntax(path, content string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".json", ".jsonc", ".css", ".scss", ".less":
	default:
		return nil
	}
	// JSON files use a strict parse instead of balance counting —
	// trailing commas, unquoted keys, etc. all matter for downstream
	// tools. If content is empty, allow it.
	if (ext == ".json" || ext == ".jsonc") && strings.TrimSpace(content) != "" {
		var v interface{}
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return fmt.Errorf("write_file rejected: %s contains invalid JSON: %w — fix the content and retry", path, err)
		}
		return nil
	}
	// For Go/JS/TS/CSS: walk the content tracking bracket depth and
	// string state. Skip line and block comments so commented-out
	// braces don't trip the counter. Skip backtick template literals
	// in JS/TS (they may legitimately contain unbalanced `{` inside
	// `${...}` interpolation but those balance internally, so we
	// just track the literal as a string-like region).
	var (
		depthCurly, depthSquare, depthParen int
		inLine, inBlock                     bool
		inStr                               byte // 0, '"', '\'', '`'
		escape                              bool
		isJSLike                            = ext != ".go"
	)
	n := len(content)
	for i := 0; i < n; i++ {
		c := content[i]
		if inLine {
			if c == '\n' {
				inLine = false
			}
			continue
		}
		if inBlock {
			if c == '*' && i+1 < n && content[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if inStr != 0 {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == inStr {
				inStr = 0
			}
			// Don't count braces inside strings; template literals
			// with `${...}` would need recursive tracking but for a
			// pre-flight check we accept the false-negative.
			continue
		}
		// Outside strings/comments.
		if c == '/' && i+1 < n {
			if content[i+1] == '/' {
				inLine = true
				i++
				continue
			}
			if content[i+1] == '*' {
				inBlock = true
				i++
				continue
			}
		}
		switch c {
		case '"', '\'':
			inStr = c
		case '`':
			if isJSLike {
				inStr = c
			}
		case '{':
			depthCurly++
		case '}':
			depthCurly--
			if depthCurly < 0 {
				return fmt.Errorf("write_file rejected: %s has unbalanced '}' (extra closer at byte %d) — fix and retry", path, i)
			}
		case '[':
			depthSquare++
		case ']':
			depthSquare--
			if depthSquare < 0 {
				return fmt.Errorf("write_file rejected: %s has unbalanced ']' (extra closer at byte %d) — fix and retry", path, i)
			}
		case '(':
			depthParen++
		case ')':
			depthParen--
			if depthParen < 0 {
				return fmt.Errorf("write_file rejected: %s has unbalanced ')' (extra closer at byte %d) — fix and retry", path, i)
			}
		}
	}
	if inStr != 0 {
		return fmt.Errorf("write_file rejected: %s ends inside an unclosed %c string literal — fix and retry", path, inStr)
	}
	if inBlock {
		return fmt.Errorf("write_file rejected: %s ends inside an unclosed /* ... */ block comment — fix and retry", path)
	}
	if depthCurly != 0 {
		return fmt.Errorf("write_file rejected: %s has %d unclosed '{' (final depth %d) — fix and retry", path, depthCurly, depthCurly)
	}
	if depthSquare != 0 {
		return fmt.Errorf("write_file rejected: %s has %d unclosed '[' (final depth %d) — fix and retry", path, depthSquare, depthSquare)
	}
	if depthParen != 0 {
		return fmt.Errorf("write_file rejected: %s has %d unclosed '(' (final depth %d) — fix and retry", path, depthParen, depthParen)
	}
	return nil
}
