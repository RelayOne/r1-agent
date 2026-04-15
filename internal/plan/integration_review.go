// Package plan — integration_review.go
//
// Integration reviewer: an LLM agent with real tool authority
// (read_file, grep, glob, bash) that runs AFTER a session's
// parallel workers have all completed and BEFORE the foundation
// sanity + acceptance criteria gates fire. It exists to close a
// specific structural gap in stoke's existing per-task reviewer
// (see ReviewTaskWork in task_judge.go): that reviewer receives
// pre-selected file excerpts scoped to ONE task and makes a
// single-shot judgement. It cannot see cross-file consistency
// bugs that only emerge when multiple tasks' outputs are
// composed — e.g. package A imports Foo from B but B never
// exports it, tsconfig.json include paths that resolve to zero
// files, package.json main/exports/types pointing at missing
// files, turbo.json pipeline deps naming tasks another package
// doesn't define, or interface drift between packages whose
// authors each updated their own half.
//
// The integration reviewer explores the repo for those bugs
// itself. It runs as a multi-turn tool-use loop against the
// provider.Provider Chat API until it emits a JSON verdict
// matching IntegrationReport. Each gap it returns gets routed
// back to the caller, which spawns a focused repair dispatch so
// the cross-file contract is fixed before foundation sanity
// runs.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// IntegrationGap is one cross-file consistency bug the integration
// reviewer identified in the codebase after a session's tasks
// completed.
type IntegrationGap struct {
	// Location is a "file:line" or "package:symbol" string that
	// pinpoints the issue. Always populated. Used by the caller
	// when routing a follow-up to the right owning task.
	Location string `json:"location"`

	// Kind classifies the bug. One of: "missing-export",
	// "missing-file", "config-mismatch", "interface-drift",
	// "empty-include", "dangling-reference", "other".
	Kind string `json:"kind"`

	// Detail is the reviewer's prose explanation — one paragraph
	// citing the specific symbols / files / lines involved.
	Detail string `json:"detail"`

	// SuggestedFollowup is a concrete, actionable directive a
	// repair worker can execute: "Open packages/X/src/index.ts
	// and export Foo which is imported by apps/Y/app/page.tsx."
	// Never vague ("improve the types").
	SuggestedFollowup string `json:"suggested_followup"`

	// OwningTask is the task ID whose scope best matches this
	// gap's fix location, when the reviewer can infer it from
	// the session's task list. Empty when unclear — caller
	// dispatches to a generic repair worker in that case.
	OwningTask string `json:"owning_task,omitempty"`
}

// IntegrationReport is the full output of one integration review.
type IntegrationReport struct {
	// Gaps is the cross-file consistency bugs the reviewer found.
	// Empty list means the reviewer scanned the repo and declared
	// the integration surface clean.
	Gaps []IntegrationGap `json:"gaps"`

	// Summary is the reviewer's one-paragraph overview of what
	// it checked. Always populated, even when Gaps is empty.
	Summary string `json:"summary"`
}

// IntegrationReviewInput is what the caller supplies to start a
// review run.
type IntegrationReviewInput struct {
	// RepoRoot is the absolute path to the repository. All tool
	// calls are rooted here with cwd-escape protection.
	RepoRoot string

	// Session is the session whose tasks just completed. Used
	// for task list + acceptance criteria context so the
	// reviewer knows what the workers were aiming at.
	Session Session

	// SOWSpec is the relevant session spec excerpt — the prose
	// the reviewer can cross-reference against the integration
	// surface it finds on disk.
	SOWSpec string

	// PriorGaps is a list of gaps fixed in earlier attempts;
	// the reviewer should verify they stayed fixed and avoid
	// re-flagging them. Optional.
	PriorGaps []string

	// ScopeHint narrows the reviewer's focus to a specific sub-path
	// of the repo. Empty = full repo scope. Set by chunked-mode
	// recursion to limit per-bucket review to one package at a time.
	ScopeHint string

	// UniversalPromptBlock carries the universal coding-standards +
	// known-gotchas block. When non-empty it is appended to the
	// integration-reviewer system prompt so the reviewer judges
	// integrations against the same baseline rules every agent sees.
	UniversalPromptBlock string
}

// integrationBashDeny is the deny list for the bash tool exposed
// to the integration reviewer. Every substring here is refused
// pre-exec and the refusal is reported back to the model so it
// can choose a different command.
// integrationBashEscapeDeny holds regex patterns that detect
// attempts to escape the repo working directory. Anchored to
// catch the common forms (cd absolute, cd .., pushd, leading
// abspath, output redirects writing to /etc, /var, $HOME, ~).
//
// We refuse rather than silently sandbox because (a) bash itself
// can be sneaky about path resolution and (b) the integration
// reviewer is supposed to read from the repo only — a refusal
// gives the model a clear signal to use relative paths instead.
var integrationBashEscapeDeny = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bcd\s+/`),                                     // cd /abs
	regexp.MustCompile(`(?i)\bcd\s+\.\.`),                                   // cd ..
	regexp.MustCompile(`(?i)\bpushd\b`),                                     // pushd anything
	regexp.MustCompile(`(?i)\bcd\s+~`),                                      // cd ~
	regexp.MustCompile(`(?i)\bcd\s+\$\{?HOME`),                              // cd $HOME
	regexp.MustCompile(`(?i)>\s*/(?:etc|var|usr|opt|root|home|tmp|dev|proc|sys)\b`), // redirect to host paths
	regexp.MustCompile(`(?i)<\s*/(?:etc|root|home)\b`),                      // read from host paths
	regexp.MustCompile(`\$\(\s*pwd\s*-P\s*\).*\.\.`),                        // sneaky pwd-then-traverse
}

var integrationBashDeny = []string{
	"rm -rf /",
	"rm -rf /*",
	"rm -rf ~",
	"rm -rf $HOME",
	"sudo ",
	"curl |",
	"curl | sh",
	"curl | bash",
	"wget |",
	"wget | sh",
	"wget | bash",
	"mv /",
	":(){:|:&};:",
	"mkfs",
	"dd if=",
	"> /dev/sda",
}

// RunIntegrationReview dispatches an LLM agent with real tool
// authority (read_file, grep, glob, bash) to explore the repo
// for cross-file consistency bugs after a session's parallel
// tasks complete. The agent runs as a multi-turn tool-use loop
// until it emits a final JSON verdict matching IntegrationReport.
//
// Turn cap: 40. Per-command timeout: 90s. Total deadline: 10min.
// Bash is repo-rooted with the same cwd-escape defenses as
// workspace_hygiene_agent.go's execBashTool. Bash deny list
// forbids destructive commands (rm -rf /, sudo, curl|sh, mv /).
//
// Returns nil report + nil error when prov is nil (noop path).
// Returns non-nil report with empty Gaps when the reviewer ran
// and declared the integration surface clean. Only returns
// non-nil error on transport/protocol failures.
func RunIntegrationReview(ctx context.Context, prov provider.Provider, model string, in IntegrationReviewInput) (*IntegrationReport, error) {
	if prov == nil {
		return nil, nil
	}
	if strings.TrimSpace(in.RepoRoot) == "" {
		return nil, fmt.Errorf("integration review: empty repo root")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Budget: honor the caller's ctx deadline if it's already tighter
	// than 10 minutes (chunked-mode recursion sets per-bucket budgets
	// via ctx). Otherwise cap at 10 minutes as the historical default.
	budget := 10 * time.Minute
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < budget {
			budget = remaining
		}
	}
	sessionCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	userText := buildIntegrationUserPrompt(in)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})
	messages := []provider.ChatMessage{{Role: "user", Content: userContent}}

	tools := integrationTools()

	const maxTurns = 40
	for turn := 0; turn < maxTurns; turn++ {
		if sessionCtx.Err() != nil {
			return nil, fmt.Errorf("integration review timed out: %w", sessionCtx.Err())
		}
		sysPrompt := buildIntegrationSystemPrompt(in.ScopeHint)
		if strings.TrimSpace(in.UniversalPromptBlock) != "" {
			sysPrompt += "\n\n" + in.UniversalPromptBlock
		}
		resp, err := prov.Chat(provider.ChatRequest{
			Model:     model,
			System:    sysPrompt,
			Messages:  messages,
			MaxTokens: 8000,
			Tools:     tools,
		})
		if err != nil {
			return nil, fmt.Errorf("integration review chat: %w", err)
		}
		if resp == nil {
			return nil, fmt.Errorf("integration review: nil response")
		}

		// Append the assistant turn verbatim so tool_use IDs line
		// up on the next user message.
		assistantBlocks := marshalAssistantBlocks(resp.Content)
		messages = append(messages, provider.ChatMessage{Role: "assistant", Content: assistantBlocks})

		toolUses := extractToolUses(resp.Content)
		if len(toolUses) == 0 {
			raw, _ := collectModelText(resp)
			var report IntegrationReport
			if _, err := jsonutil.ExtractJSONInto(raw, &report); err != nil {
				// No JSON verdict — the reviewer broke its contract.
				// Previously this was silently treated as "0 gaps,
				// surface clean", which actively hid findings: a
				// reviewer that produced prose findings then failed
				// the JSON wrapper would be parsed as a green light
				// and the run would advance past unresolved gaps.
				//
				// Now we surface a synthetic gap so downstream code
				// dispatches a follow-up (or at minimum the operator
				// sees an unconverged signal in the logs).
				fmt.Printf("  🔗 integration-review: ⚠ NON-COMPLIANT VERDICT (no parseable JSON) — flagging as unknown\n")
				fmt.Printf("    raw response: %s\n", firstLine(raw))
				return &IntegrationReport{
					Summary: "reviewer returned prose instead of JSON; verdict unknown — see raw output",
					Gaps: []IntegrationGap{
						{
							Kind:              "reviewer-noncompliant",
							Location:          "(meta)",
							Detail:            "integration reviewer returned non-JSON output: " + firstLine(raw),
							SuggestedFollowup: "re-run integration review or manually inspect cross-file consistency",
						},
					},
				}, nil
			}
			return &report, nil
		}

		toolResults := make([]map[string]interface{}, 0, len(toolUses))
		for _, tu := range toolUses {
			var result string
			switch tu.Name {
			case "read_file":
				result = integrationReadFile(tu.Input, in.RepoRoot)
			case "grep":
				result = integrationGrep(sessionCtx, tu.Input, in.RepoRoot)
			case "glob":
				result = integrationGlob(tu.Input, in.RepoRoot)
			case "bash":
				result = integrationBash(sessionCtx, tu.Input, in.RepoRoot)
			default:
				result = fmt.Sprintf("--- error ---\nunknown tool: %s", tu.Name)
			}
			toolResults = append(toolResults, map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": tu.ID,
				"content":     result,
			})
		}
		resultJSON, _ := json.Marshal(toolResults)
		messages = append(messages, provider.ChatMessage{Role: "user", Content: resultJSON})
	}

	fmt.Printf("  🔗 integration-review: turn cap reached (%d) — emitting empty verdict\n", maxTurns)
	return &IntegrationReport{Summary: fmt.Sprintf("turn cap %d reached without verdict", maxTurns)}, nil
}

// integrationTools returns the tool set exposed to the integration
// reviewer. Ordered: read_file, grep, glob, bash.
func integrationTools() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file from the repository with line numbers. Input: path (relative or absolute under repo root), optional max_bytes (default 8192, max 32768).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":      {"type": "string",  "description": "file path under the repo root"},
    "max_bytes": {"type": "integer", "description": "max bytes to return, default 8192, max 32768"}
  },
  "required": ["path"]
}`),
		},
		{
			Name:        "grep",
			Description: "Search files for a pattern using ripgrep (or grep) scoped to the repo root. Input: pattern (regex), optional path (subdirectory), optional include (glob filter like '*.ts').",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "regex pattern to search for"},
    "path":    {"type": "string", "description": "optional subdirectory to scope the search"},
    "include": {"type": "string", "description": "optional file glob filter like '*.ts' or '*.json'"}
  },
  "required": ["pattern"]
}`),
		},
		{
			Name:        "glob",
			Description: "List files matching a glob pattern (supports ** for recursive). Input: pattern relative to repo root. Returns up to 200 paths.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "glob pattern, supports ** recursion"}
  },
  "required": ["pattern"]
}`),
		},
		{
			Name:        "bash",
			Description: "Run a short shell command at the repo root. Destructive commands are denied. Input: command, optional timeout_seconds (default 30, max 90).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command":         {"type": "string",  "description": "the shell command to run via bash -lc"},
    "timeout_seconds": {"type": "integer", "description": "per-command timeout, default 30, max 90"}
  },
  "required": ["command"]
}`),
		},
	}
}

// integrationSanitizePath takes a requested path from the model
// and returns the absolute, repo-rooted version — rejecting
// absolute paths that escape and any `..` traversal that lands
// outside repoRoot. Returns an empty string and an error message
// when the path is rejected.
func integrationSanitizePath(requested, repoRoot string) (string, string) {
	if strings.TrimSpace(requested) == "" {
		return "", "empty path"
	}
	rootAbs, _ := filepath.Abs(repoRoot)
	var candAbs string
	// Absolute path: honor as-is IF it's already under repoRoot.
	// Strip-leading-slash-then-join was wrong: it silently rewrote
	// "/absolute/path/that/happens/to/be/under/repo" to
	// "<repo>/absolute/path/...", which breaks multi-step flows
	// where a previous tool returned an abs path the model echoes
	// back into read_file.
	if filepath.IsAbs(requested) {
		cleaned := filepath.Clean(requested)
		abs, _ := filepath.Abs(cleaned)
		candAbs = abs
	} else {
		candAbs, _ = filepath.Abs(filepath.Clean(filepath.Join(repoRoot, requested)))
	}
	if candAbs == rootAbs || strings.HasPrefix(candAbs, rootAbs+string(filepath.Separator)) {
		return candAbs, ""
	}
	return "", fmt.Sprintf("path %q escapes repo root", requested)
}

// integrationReadFile handles the read_file tool call.
func integrationReadFile(input map[string]interface{}, repoRoot string) string {
	path, _ := input["path"].(string)
	abs, reason := integrationSanitizePath(path, repoRoot)
	if abs == "" {
		return fmt.Sprintf("--- read_file ---\nrefused: %s", reason)
	}
	maxBytes := 8192
	switch v := input["max_bytes"].(type) {
	case float64:
		maxBytes = int(v)
	case int:
		maxBytes = v
	}
	if maxBytes <= 0 {
		maxBytes = 8192
	}
	if maxBytes > 32768 {
		maxBytes = 32768
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("--- read_file ---\nerror: %v", err)
	}
	truncated := false
	if len(data) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}

	// Prefix each line with its line number for easy citation.
	var b strings.Builder
	fmt.Fprintf(&b, "--- read_file ---\n%s (%d bytes shown)\n", path, len(data))
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		fmt.Fprintf(&b, "%5d: %s\n", i+1, line)
	}
	if truncated {
		b.WriteString("...[truncated]\n")
	}
	return b.String()
}

// integrationGrep handles the grep tool call. Uses ripgrep when
// available, falling back to plain grep -rn.
func integrationGrep(ctx context.Context, input map[string]interface{}, repoRoot string) string {
	pattern, _ := input["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return "--- grep ---\nerror: empty pattern"
	}
	scope := repoRoot
	if sub, ok := input["path"].(string); ok && strings.TrimSpace(sub) != "" {
		abs, reason := integrationSanitizePath(sub, repoRoot)
		if abs == "" {
			return fmt.Sprintf("--- grep ---\nrefused: %s", reason)
		}
		scope = abs
	}
	include, _ := input["include"].(string)

	rgCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var c *exec.Cmd
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"-n", "--no-heading", "--color=never"}
		if strings.TrimSpace(include) != "" {
			args = append(args, "-g", include)
		}
		args = append(args, "--", pattern, scope)
		c = exec.CommandContext(rgCtx, "rg", args...)
	} else {
		args := []string{"-rn"}
		if strings.TrimSpace(include) != "" {
			args = append(args, "--include="+include)
		}
		args = append(args, "--", pattern, scope)
		c = exec.CommandContext(rgCtx, "grep", args...)
	}
	c.Dir = repoRoot
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	_ = c.Run()

	out := buf.String()
	// Truncate to 100 lines.
	lines := strings.Split(out, "\n")
	if len(lines) > 100 {
		lines = append(lines[:100], "...[truncated, >100 matches]")
	}
	return "--- grep ---\n" + strings.Join(lines, "\n")
}

// integrationGlob handles the glob tool call. Supports ** via
// filepath.WalkDir + component-wise matching.
func integrationGlob(input map[string]interface{}, repoRoot string) string {
	pattern, _ := input["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return "--- glob ---\nerror: empty pattern"
	}
	rootAbs, _ := filepath.Abs(repoRoot)

	matches := []string{}
	err := filepath.WalkDir(rootAbs, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			// Skip common noise.
			if base == ".git" || base == "node_modules" || base == "target" || base == "dist" || base == ".next" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(rootAbs, p)
		if rerr != nil {
			return nil
		}
		if integrationGlobMatch(pattern, rel) {
			matches = append(matches, rel)
			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Sprintf("--- glob ---\nerror: %v", err)
	}
	truncNote := ""
	if len(matches) >= 200 {
		truncNote = "\n...[truncated at 200 paths]"
	}
	return fmt.Sprintf("--- glob ---\n%s%s", strings.Join(matches, "\n"), truncNote)
}

// integrationGlobMatch implements a simple glob matcher with **
// support (matches any number of path components, including
// zero). All other pattern characters fall through to
// filepath.Match semantics on a per-component basis.
func integrationGlobMatch(pattern, name string) bool {
	patParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	return integrationGlobMatchParts(patParts, nameParts)
}

func integrationGlobMatchParts(pat, name []string) bool {
	for len(pat) > 0 && len(name) > 0 {
		if pat[0] == "**" {
			// ** matches zero-or-more components; try every tail.
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if integrationGlobMatchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		ok, _ := filepath.Match(pat[0], name[0])
		if !ok {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	// Consume trailing **'s in pattern that match empty tail.
	for len(pat) > 0 && pat[0] == "**" {
		pat = pat[1:]
	}
	return len(pat) == 0 && len(name) == 0
}

// integrationBash handles the bash tool call. Shares the cwd-
// escape defenses and deny-list shape used by the hygiene agent
// (workspace_hygiene_agent.go's execBashTool).
func integrationBash(ctx context.Context, input map[string]interface{}, repoRoot string) string {
	cmdStr, _ := input["command"].(string)
	if strings.TrimSpace(cmdStr) == "" {
		return "--- bash ---\nerror: empty command"
	}
	lower := strings.ToLower(cmdStr)
	for _, bad := range integrationBashDeny {
		if strings.Contains(lower, strings.ToLower(bad)) {
			return fmt.Sprintf("--- bash ---\nrefused: command matches deny pattern %q", bad)
		}
	}
	// Repo-escape guard: refuse commands that try to step outside
	// the repo via `cd ..`, absolute paths, or common escape verbs.
	// The integration reviewer is supposed to be inspection-only on
	// the current checkout; bash that mutates the parent process's
	// working directory or reads from arbitrary host paths defeats
	// that boundary. Each refused pattern is reported back so the
	// model can choose a different command.
	for _, escape := range integrationBashEscapeDeny {
		if escape.MatchString(cmdStr) {
			return fmt.Sprintf("--- bash ---\nrefused: command appears to escape the repo (matched %q). Use only relative paths under the repo root.", escape.String())
		}
	}

	timeoutSec := 30
	switch v := input["timeout_seconds"].(type) {
	case float64:
		timeoutSec = int(v)
	case int:
		timeoutSec = v
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if timeoutSec > 90 {
		timeoutSec = 90
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	c := exec.CommandContext(cctx, "bash", "-lc", cmdStr)
	c.Dir = repoRoot
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	out := buf.String()
	if len(out) > 6000 {
		out = out[:6000] + "\n...[truncated]"
	}
	if err != nil {
		return fmt.Sprintf("--- bash ---\nexit error: %v\n%s", err, out)
	}
	return fmt.Sprintf("--- bash ---\nexit 0\n%s", out)
}

// buildIntegrationUserPrompt renders the opening user message
// with session context (task list, ACs, SOW excerpt, prior gaps).
func buildIntegrationUserPrompt(in IntegrationReviewInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository root: %s\n\n", in.RepoRoot)
	fmt.Fprintf(&b, "SESSION %s — %s\n", in.Session.ID, in.Session.Title)
	if strings.TrimSpace(in.Session.Description) != "" {
		fmt.Fprintf(&b, "  description: %s\n", in.Session.Description)
	}
	b.WriteString("\n")

	if len(in.Session.Tasks) > 0 {
		b.WriteString("TASKS THAT JUST COMPLETED:\n")
		for _, t := range in.Session.Tasks {
			fmt.Fprintf(&b, "  - %s: %s\n", t.ID, t.Description)
			if len(t.Files) > 0 {
				fmt.Fprintf(&b, "      files: %s\n", strings.Join(t.Files, ", "))
			}
		}
		b.WriteString("\n")
	}

	if len(in.Session.AcceptanceCriteria) > 0 {
		b.WriteString("SESSION ACCEPTANCE CRITERIA (downstream gates that will run after you):\n")
		for _, ac := range in.Session.AcceptanceCriteria {
			fmt.Fprintf(&b, "  - [%s] %s\n", ac.ID, ac.Description)
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(in.SOWSpec) != "" {
		b.WriteString("SOW SPEC EXCERPT:\n")
		spec := in.SOWSpec
		if len(spec) > 6000 {
			spec = spec[:6000] + "\n...[truncated]"
		}
		b.WriteString(spec)
		b.WriteString("\n\n")
	}

	if len(in.PriorGaps) > 0 {
		b.WriteString("GAPS FIXED IN EARLIER ATTEMPTS (verify they stayed fixed, do NOT re-flag them):\n")
		for i, g := range in.PriorGaps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, g)
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(in.ScopeHint) != "" {
		fmt.Fprintf(&b, "SCOPE RESTRICTION: focus EXCLUSIVELY on files under `%s` relative to the repo root. Read files outside this path only when a tool call reveals an import crossing the boundary — flag those as cross-bucket contract issues.\n\n", in.ScopeHint)
	}
	b.WriteString("Explore the repo with read_file / grep / glob / bash. When done, emit ONLY the JSON verdict described in the system prompt and end your turn.\n")
	return b.String()
}

// buildIntegrationSystemPrompt returns the system prompt for the
// integration reviewer. When scopeHint is non-empty, an extra
// paragraph narrows the reviewer to the given repo-relative path
// so chunked-mode recursion can run one bucket at a time.
func buildIntegrationSystemPrompt(scopeHint string) string {
	if strings.TrimSpace(scopeHint) == "" {
		return integrationReviewSystemPrompt
	}
	return integrationReviewSystemPrompt + "\n\nSCOPE: Focus EXCLUSIVELY on files under the repo-root-relative path `" + scopeHint + "`. Read files outside this path only when a tool call reveals an import across the boundary — then flag it as a cross-bucket contract issue."
}

// integrationReviewSystemPrompt is the persistent system prompt
// driving the integration reviewer. Kept tight: workflow + focus
// classes + explicit don't-flag list + output shape.
const integrationReviewSystemPrompt = `You are an integration reviewer running AFTER a team of parallel workers completed a session's tasks. The per-task reviewer already verified each task's individual file output — your job is strictly the CROSS-FILE layer those reviewers structurally cannot see.

FOCUS on these classes of bug:
  - Cross-package import/export contracts: package A imports Foo from B, but B doesn't export Foo
  - tsconfig.json includes/references resolving to zero files (TS18003)
  - package.json main/exports/types pointing to non-existent files
  - turbo.json pipeline deps referencing tasks a package doesn't define
  - Interface drift: A calls B.doThing(x, y) but B.doThing now takes (x, y, z)
  - Helper script targets referenced by package.json scripts but absent from disk

DO NOT flag:
  - In-file polish (missing error handling inside one file, naming)
  - Missing features outside the session's tasks
  - Anything the per-task reviewer already had scope to catch
  - Cosmetic/style issues
  - Anything you can't confirm with a tool call (no speculation)

WORKFLOW:
  1. Start by reading the session's task list + ACs to understand intended output.
  2. Use grep to map imports across packages (e.g. grep -rn "from '@[a-z]" apps packages).
  3. For each cross-package import, verify the target module exports what's imported.
  4. Scan each package.json for scripts referencing files — verify those files exist.
  5. Read each tsconfig.json's include/references — verify they resolve to actual content.
  6. When you find a gap, cite file:line and continue — don't stop at the first bug.

OUTPUT: when done, emit a SINGLE JSON object matching this shape (no prose outside the JSON, no markdown fences):

{"gaps": [{"location": "apps/web/app/page.tsx:12", "kind": "missing-export", "detail": "...", "suggested_followup": "Open packages/ui/src/index.ts and export Button which is imported by apps/web/app/page.tsx:12.", "owning_task": "t-ui-exports"}], "summary": "scanned N packages, verified M imports, found K gaps"}

end_turn immediately after emitting the JSON.`
