// Package hooks provides the anti-deception enforcement layer for Stoke.
// It installs pre/post tool-use hooks into Claude Code worktrees that guard
// protected files, block git mutations, and detect type bypasses and secret leaks.
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookCommand represents a single hook command entry in Claude Code settings.
type HookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// HookSet contains the pre and post tool-use hook commands.
type HookSet struct {
	PreToolUse  []HookCommand `json:"PreToolUse,omitempty"`
	PostToolUse []HookCommand `json:"PostToolUse,omitempty"`
}

// HooksSettingsOverlay is the typed structure merged into Claude Code settings.json.
// It contains only the "hooks" key that gets merged with the base settings.
type HooksSettingsOverlay struct {
	Hooks HookSet `json:"hooks"`
}

// PermissionsConfig represents the permissions block in Claude Code settings.
type PermissionsConfig struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny,omitempty"`
}

// InteractiveSettings is the full typed settings for interactive mode (yolo/scope).
type InteractiveSettings struct {
	Hooks        HookSet            `json:"hooks"`
	Permissions  PermissionsConfig  `json:"permissions"`
	APIKeyHelper *string            `json:"apiKeyHelper"` // JSON null via nil pointer
}

// safeWrite writes data to a file, rejecting symlinks at any path component.
// Prevents symlink redirection attacks where .stoke or CLAUDE.md is a symlink
// pointing outside the repo.
func safeWrite(target string, data []byte, perm os.FileMode) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	// Walk every component checking for symlinks
	parts := strings.Split(abs, string(filepath.Separator))
	check := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		check = filepath.Join(check, part)
		if info, lErr := os.Lstat(check); lErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink rejected at %q (potential repo escape)", check)
			}
		}
	}
	// Use CreateTemp (O_CREATE|O_EXCL) in the target directory to avoid
	// temp-file symlink attacks. target+".tmp" as a symlink would bypass
	// the check above and redirect writes outside the repo.
	dir := filepath.Dir(abs)
	f, err := os.CreateTemp(dir, ".stoke-safe-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	f.Close()
	os.Chmod(tmpPath, perm)
	if err := os.Rename(tmpPath, abs); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// safeMkdirAll creates directories, rejecting any existing symlinks in the path.
func safeMkdirAll(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	parts := strings.Split(abs, string(filepath.Separator))
	check := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		check = filepath.Join(check, part)
		if info, lErr := os.Lstat(check); lErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink rejected at %q", check)
			}
		}
	}
	return os.MkdirAll(dir, 0755)
}

// Install writes enforcer hook files into a worktree's .claude/settings.json hooks section.
// These fire during claude -p execution as fast-filter guards.
func Install(runtimeDir string) error {
	hookDir := filepath.Join(runtimeDir, "hooks")
	if err := safeMkdirAll(hookDir); err != nil {
		return fmt.Errorf("create hook dir: %w", err)
	}

	// PreToolUse hook: blocks dangerous patterns before execution
	// Full guard suite from the enforcer: git stash, mass revert, nested claude,
	// protected files, destructive commands, remote code execution.
	//
	// Uses proper JSON parsing via Python (available in all CI environments)
	// instead of brittle grep/regex. Falls back to grep if Python unavailable.
	// Inspired by claw-code-parity's structured permission enforcement.
	preToolUse := `#!/bin/bash
# Stoke enforcer hook: PreToolUse guard
# Blocks dangerous tool invocations before they execute.
# Uses structured JSON parsing for reliability.

set -euo pipefail

INPUT=$(cat)

BLOCK() {
    printf '{"decision":"block","reason":"%s"}\n' "$1"
    exit 0
}

ALLOW() {
    echo '{"decision":"allow"}'
    exit 0
}

# Parse JSON input using Python (more reliable than grep for nested JSON)
# Falls back to grep-based extraction if Python is unavailable
if command -v python3 &>/dev/null; then
    TOOL_NAME=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('tool_name',''))" 2>/dev/null || echo "")
    TOOL_INPUT_JSON=$(echo "$INPUT" | python3 -c "
import sys,json
d=json.load(sys.stdin)
ti=d.get('tool_input',{})
if isinstance(ti,str): print(ti)
else: print(json.dumps(ti))
" 2>/dev/null || echo "")
    FILE_PATH=$(echo "$INPUT" | python3 -c "
import sys,json
d=json.load(sys.stdin)
ti=d.get('tool_input',{})
if isinstance(ti,dict): print(ti.get('file_path',ti.get('path','')))
else: print('')
" 2>/dev/null || echo "")
    COMMAND=$(echo "$INPUT" | python3 -c "
import sys,json
d=json.load(sys.stdin)
ti=d.get('tool_input',{})
if isinstance(ti,dict): print(ti.get('command',''))
else: print(str(ti))
" 2>/dev/null || echo "")
else
    # Fallback: grep-based extraction
    TOOL_NAME=$(echo "$INPUT" | grep -o '"tool_name":"[^"]*"' | head -1 | cut -d'"' -f4 || echo "")
    TOOL_INPUT_JSON=$(echo "$INPUT" | grep -o '"tool_input":{[^}]*}' | head -1 || echo "")
    FILE_PATH=""
    COMMAND="$TOOL_INPUT_JSON"
fi

if [ -z "$TOOL_NAME" ]; then
    TOOL_NAME="${1:-}"
fi

# Block file writes to protected paths (structured path check)
case "$TOOL_NAME" in
    Write|Edit|MultiEdit)
        if [ -n "$FILE_PATH" ]; then
            case "$FILE_PATH" in
                */.claude/*|*/.stoke/*|*/CLAUDE.md|*/.env|*/.env.*|*/stoke.policy.yaml|*/settings.json)
                    BLOCK "Protected file: cannot modify .claude/, .stoke/, CLAUDE.md, .env*, or stoke.policy.yaml"
                    ;;
                .claude/*|.stoke/*|CLAUDE.md|.env|.env.*|stoke.policy.yaml|settings.json)
                    BLOCK "Protected file: cannot modify .claude/, .stoke/, CLAUDE.md, .env*, or stoke.policy.yaml"
                    ;;
            esac
        fi
        ;;
esac

# Block dangerous bash commands (using extracted command string)
if [ "$TOOL_NAME" = "Bash" ] && [ -n "$COMMAND" ]; then
    CMD="$COMMAND"

    # Git mutations that hide work from verification
    echo "$CMD" | grep -qE '\bgit\s+stash\b' && BLOCK "git stash hides work from verification. Commit or revert."
    echo "$CMD" | grep -q 'git checkout -- \.' && BLOCK "Mass revert. Use specific file paths."
    echo "$CMD" | grep -qE 'git\s+reset\s+--hard' && BLOCK "git reset --hard destroys evidence."
    echo "$CMD" | grep -qE 'git\s+push' && BLOCK "git push blocked. Stoke controls when to push."
    echo "$CMD" | grep -qE 'git\s+rebase' && BLOCK "git rebase blocked in Stoke-managed worktrees."
    echo "$CMD" | grep -qE 'git\s+commit.*--no-verify' && BLOCK "git commit --no-verify blocked. Hooks must run."
    echo "$CMD" | grep -qE 'git\s+force' && BLOCK "Force operations blocked."

    # Nested Claude/Codex sessions
    echo "$CMD" | grep -qE '\bclaude\b.*--dangerously-skip-permissions' && BLOCK "Cannot spawn nested Claude sessions."
    echo "$CMD" | grep -qE '\bclaude\b.*-p\b' && BLOCK "Cannot spawn nested Claude headless sessions."
    echo "$CMD" | grep -qE '\bcodex\b.*exec\b' && BLOCK "Cannot spawn nested Codex sessions."

    # Destructive commands
    echo "$CMD" | grep -qE 'rm\s+-rf\s+/' && BLOCK "Destructive: rm -rf / blocked."
    echo "$CMD" | grep -qE 'rm\s+-rf\s+~' && BLOCK "Destructive: rm -rf ~ blocked."
    echo "$CMD" | grep -qE '\bsudo\s+rm\b' && BLOCK "Destructive: sudo rm blocked."
    echo "$CMD" | grep -qE 'chmod\s+-R\s+777' && BLOCK "Destructive: chmod -R 777 blocked."

    # Remote code execution
    echo "$CMD" | grep -qE 'curl.*\|\s*bash' && BLOCK "Remote code execution: curl | bash blocked."
    echo "$CMD" | grep -qE 'wget.*-O\s*-.*\|\s*sh' && BLOCK "Remote code execution: wget | sh blocked."

    # Hook/settings tampering
    echo "$CMD" | grep -qE 'rm.*\.stoke/hooks' && BLOCK "Cannot remove Stoke hooks."
    echo "$CMD" | grep -qE 'chmod.*\.stoke/hooks' && BLOCK "Cannot modify Stoke hook permissions."

    # Process manipulation (prevent killing Stoke or its subprocesses)
    echo "$CMD" | grep -qE 'kill\s+-9\s+1\b' && BLOCK "Cannot kill PID 1."
    echo "$CMD" | grep -qE 'pkill.*stoke' && BLOCK "Cannot kill Stoke processes."
fi

ALLOW
`

	// PostToolUse hook: detects policy violations after execution
	postToolUse := `#!/bin/bash
# Stoke enforcer hook: PostToolUse monitor
# Detects policy violations after tool execution.

TOOL_NAME="$1"
TOOL_OUTPUT="$2"

# Check for type bypass patterns in write output
if [ "$TOOL_NAME" = "Write" ] || [ "$TOOL_NAME" = "Edit" ]; then
    if echo "$TOOL_OUTPUT" | grep -qE '@ts-ignore|as any|eslint-disable|# type: ignore|# noqa|\.only\(' 2>/dev/null; then
        echo '{"warning":"Policy violation detected: type/lint bypass in written code"}'
    fi
fi

# Check for secret leaks in bash output
if [ "$TOOL_NAME" = "Bash" ]; then
    if echo "$TOOL_OUTPUT" | grep -qiE 'sk-[a-zA-Z0-9]{20,}|AKIA[A-Z0-9]{16}|ghp_[a-zA-Z0-9]{36}' 2>/dev/null; then
        echo '{"warning":"Possible secret leak detected in command output"}'
    fi
fi
`

	if err := safeWrite(filepath.Join(hookDir, "pre-tool-use.sh"), []byte(preToolUse), 0755); err != nil {
		return err
	}
	if err := safeWrite(filepath.Join(hookDir, "post-tool-use.sh"), []byte(postToolUse), 0755); err != nil {
		return err
	}

	return nil
}

// HooksConfig returns the Claude Code hooks configuration for settings.json.
// This tells Claude Code to call our hook scripts on tool use events.
func HooksConfig(runtimeDir string) map[string]interface{} {
	hookDir := filepath.Join(runtimeDir, "hooks")
	overlay := HooksSettingsOverlay{
		Hooks: HookSet{
			PreToolUse:  []HookCommand{{Type: "command", Command: filepath.Join(hookDir, "pre-tool-use.sh")}},
			PostToolUse: []HookCommand{{Type: "command", Command: filepath.Join(hookDir, "post-tool-use.sh")}},
		},
	}
	// Marshal to map for merge compatibility with existing settings.json merge path
	data, err := json.Marshal(overlay)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}

// Cleanup removes hook files from a runtime directory.
func Cleanup(runtimeDir string) {
	os.RemoveAll(filepath.Join(runtimeDir, "hooks"))
}

// InstallInRepo installs hooks directly in a repo (for yolo/scope interactive modes).
// Unlike Install() which writes to RuntimeDir for headless mode, this writes to
// repoRoot/.stoke/hooks/ so that GenerateSettings() can reference them.
func InstallInRepo(repoRoot string) error {
	stokeDir := filepath.Join(repoRoot, ".stoke")
	return Install(stokeDir)
}

// GenerateSettings writes a claude-settings.json for interactive mode.
// mode: "yolo" (full tools) or "scope" (read-only + plan output)
// outputFile: for scope mode, the one file Write is allowed to (e.g. "stoke-plan.json")
func GenerateSettings(repoRoot, mode, outputFile string) (string, error) {
	settingsDir := filepath.Join(repoRoot, ".stoke", "generated")
	if err := safeMkdirAll(settingsDir); err != nil {
		return "", err
	}

	hookDir := filepath.Join(repoRoot, ".stoke", "hooks")
	settings := InteractiveSettings{
		Hooks: HookSet{
			PreToolUse:  []HookCommand{{Type: "command", Command: filepath.Join(hookDir, "pre-tool-use.sh")}},
			PostToolUse: []HookCommand{{Type: "command", Command: filepath.Join(hookDir, "post-tool-use.sh")}},
		},
		Permissions:  PermissionsConfig{Allow: []string{}},
		APIKeyHelper: nil, // JSON null suppresses repo helpers (Mode 1)
	}

	if mode == "yolo" {
		settings.Permissions = PermissionsConfig{
			Allow: []string{"Read", "Write", "Edit", "MultiEdit", "Bash", "Glob", "Grep"},
		}
	} else {
		// scope: read + write only to the plan output file
		// Write is allowed so Claude can save the plan artifact.
		// The PreToolUse hook blocks protected files.
		// Bash is denied to keep the session non-destructive.
		settings.Permissions = PermissionsConfig{
			Allow: []string{"Read", "Write", "Glob", "Grep", "WebSearch", "WebFetch"},
			Deny:  []string{"Edit", "MultiEdit", "Bash"},
		}
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}

	path := filepath.Join(settingsDir, fmt.Sprintf("claude-%s-settings.json", mode))
	if err := safeWrite(path, data, 0644); err != nil {
		return "", err
	}

	// For scope: install a scope-specific write guard that only allows the plan output
	if mode == "scope" && outputFile != "" {
		installScopeWriteGuard(repoRoot, outputFile)
	}

	return path, nil
}

// installScopeWriteGuard overwrites the PreToolUse hook to only allow writes to the plan file.
func installScopeWriteGuard(repoRoot, allowedFile string) {
	hookDir := filepath.Join(repoRoot, ".stoke", "hooks")
	absAllowed := filepath.Join(repoRoot, allowedFile)

	// Escape single quotes for safe bash embedding (replace ' with '\'' )
	escAbs := strings.ReplaceAll(absAllowed, "'", "'\\''")
	escFile := strings.ReplaceAll(allowedFile, "'", "'\\''")

	guard := `#!/bin/bash
# Stoke scope guard: only allows writes to the plan output file.
INPUT=$(cat)
TOOL_NAME=$(echo "$INPUT" | grep -o '"tool_name":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -z "$TOOL_NAME" ]; then TOOL_NAME="$1"; fi

if [ "$TOOL_NAME" = "Write" ]; then
    FILE_PATH=$(echo "$INPUT" | grep -o '"file_path":"[^"]*"' | head -1 | cut -d'"' -f4)
    REAL_PATH=$(realpath "$FILE_PATH" 2>/dev/null || echo "$FILE_PATH")
    ALLOWED='` + escAbs + `'
    ALLOWED_REL='` + escFile + `'
    if [ "$REAL_PATH" = "$ALLOWED" ] || [ "$FILE_PATH" = "$ALLOWED_REL" ]; then
        echo '{"decision":"allow"}'
        exit 0
    fi
    echo '{"decision":"block","reason":"Scope mode: writes only allowed to '"$ALLOWED_REL"'"}'
    exit 0
fi

echo '{"decision":"allow"}'
`

	safeWrite(filepath.Join(hookDir, "pre-tool-use.sh"), []byte(guard), 0755)
}

// GenerateCLAUDEmd writes a CLAUDE.md file with Stoke context for interactive sessions.
// outputFile: for scope mode, the allowed plan output path.
func GenerateCLAUDEmd(repoRoot, mode, outputFile string) error {
	content := `# Stoke-Managed Session

This repo is under Stoke orchestration. Rules:

## Non-negotiable
- Do NOT modify .stoke/, .claude/, CLAUDE.md, or .env files
- Do NOT use git push, git reset --hard, git rebase, or git stash
- Do NOT spawn nested claude or codex sessions
- Do NOT use --no-verify on git commits
- Do NOT classify failures as "pre-existing" or "out of scope"
- Do NOT mark tasks as FIXED without a real commit hash

## Quality
- No @ts-ignore, as any, eslint-disable, # noqa, or // nolint
- No empty catch blocks
- No placeholder code (TODO, FIXME, NotImplementedError)
- No tautological tests (expect(true).toBe(true))
- No test.todo or .skip()

## Workflow
- One task at a time
- Commit after each completed task: git add -A && git commit -m "feat(TASK-ID): description"
- Run build/test/lint before claiming done
- If blocked, say BLOCKED with reason -- do not fake progress
`

	if mode == "scope" {
		content += fmt.Sprintf(`
## Scope Mode
- You are in SCOPE mode. Read the codebase, discuss with the user, plan what to build.
- You CAN read any file (Read, Glob, Grep).
- You CAN write ONLY to: %s
- You CANNOT edit existing files, run bash commands, or modify anything else.
- When the plan is ready, save it as valid JSON to %s using the Write tool.
- The JSON should follow this format:
  {"id": "plan-YYYYMMDD", "description": "...", "tasks": [{"id": "TASK-1", "description": "...", "files": [...], "dependencies": [], "type": "refactor"}]}
`, outputFile, outputFile)
	}

	return safeWrite(filepath.Join(repoRoot, "CLAUDE.md"), []byte(content), 0644)
}
