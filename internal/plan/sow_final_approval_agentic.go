// Package plan — sow_final_approval_agentic.go
//
// Agentic CTO-role reviewer for FinalPlanApproval. Replaces the
// monolithic prose+SOW dump-and-review with a tool-driven
// exploration loop:
//
//   The reviewer is given:
//     - The PROSE table-of-contents (section name + size only, no body)
//     - The SOW SUMMARY (session list with title + task/AC counts +
//       declared inputs/outputs only, no task or AC bodies)
//
//   And tools to pull what it actually wants to look at:
//     - list_prose_sections, read_prose_section, search_prose
//     - list_sessions, read_session (summary | tasks | acs | full),
//       search_sow
//     - read_repo_file (to verify a file the SOW references actually
//       exists in the workspace), list_repo_dir, grep_repo
//
// Why this matters:
//   - Sentinel-class SOWs hit ~100k input tokens in the monolith
//     review and take 5-10 minutes per call. The reviewer rarely
//     deeply reads >20% of sections — most are obviously-mapped.
//   - Tool-driven exploration lets the reviewer fetch only what it
//     needs for the verdict. Smaller per-turn context, faster, and
//     each tool result is whitespace-compressed JSON for density.
//   - The reviewer can also CROSS-CHECK the SOW against the actual
//     workspace (read_repo_file / grep_repo), catching cases where
//     the planner declared a file that doesn't exist OR missed a
//     real file that should have been touched. The monolithic
//     approach can't do that — it only sees prose + SOW JSON.
//
// Verdict shape is identical to the monolithic FinalApprovalVerdict
// so callers don't change. The agentic call REPLACES the legacy
// FinalPlanApproval body; the public function name and signature
// remain the same.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1-agent/internal/agentloop"
	"github.com/RelayOne/r1-agent/internal/jsonutil"
	"github.com/RelayOne/r1-agent/internal/provider"
)

// FinalPlanApprovalAgentic is the tool-driven reviewer. Same return
// type as the legacy FinalPlanApproval — caller does not change.
//
// projectRoot is the workspace root so the reviewer can spot-check
// declared files via the repo tools. Pass empty string to disable
// repo introspection (legacy behavior).
func FinalPlanApprovalAgentic(ctx context.Context, prose string, sow *SOW, prov provider.Provider, model string, projectRoot string) (*FinalApprovalVerdict, error) {
	if sow == nil {
		return nil, fmt.Errorf("nil SOW")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider")
	}

	// Build the prose section index once. Section parsing is
	// markdown-aware (#, ##, ###); plain-text input falls back to
	// "the whole file" as a single section.
	sections := indexProseSections(prose)
	sowSummary := summarizeSOW(sow)

	// Initial system prompt: identical decision rules to the legacy
	// reviewer, plus instructions on how to use the tools and the
	// required emit-verdict tool.
	system := agenticReviewSystemPrompt

	// Initial user message: the TOC + SOW summary + nudge to call
	// tools to explore. Compact JSON to keep the prefix small.
	tocBytes, _ := json.Marshal(sections.summary())
	sowBytes, _ := json.Marshal(sowSummary)
	initial := fmt.Sprintf(`# CTO Final Approval Review

PROSE TABLE OF CONTENTS (section names + sizes; bodies fetched on demand):
%s

SOW SUMMARY (session list with task/AC counts + declared inputs/outputs; bodies fetched on demand):
%s

Use the tools to read the specific prose sections and SOW sessions you need to render a fidelity + feasibility verdict. The repo tools (read_repo_file / list_repo_dir / grep_repo) let you cross-check declared files against the actual workspace state. When you have enough evidence, call the emit_verdict tool with your decision. DO NOT emit prose narration alongside the tool calls — short rationale lines are fine, but the verdict itself MUST come through emit_verdict.`,
		string(tocBytes), string(sowBytes))

	// Tool registry. The verdict is emitted via a tool call so the
	// loop can detect completion deterministically.
	verdictCh := make(chan *FinalApprovalVerdict, 1)
	tools := agenticReviewTools()
	handler := buildAgenticReviewHandler(prose, sections, sow, projectRoot, verdictCh)

	// Run the loop with a generous turn cap (50) — the reviewer can
	// reasonably need 20-40 reads on a Sentinel-class SOW.
	loopCfg := agentloop.Config{
		Model:        model,
		MaxTurns:     50,
		MaxTokens:    16000,
		SystemPrompt: system,
		Timeout:      6 * time.Minute,
		// Near-limit reminder: when the model is 5 turns from the
		// cap, inject a supervisor note telling it to call
		// emit_verdict NOW. Without this the model can spend all
		// turns reading sections and never emit the verdict.
		MidturnCheckFn: func(msgs []agentloop.Message, turn int) string {
			if turn >= 45 {
				return "URGENT: You are about to hit the turn limit. Call emit_verdict NOW with your current assessment. Do NOT write the verdict as prose — it MUST come through the emit_verdict tool call."
			}
			return ""
		},
	}
	loop := agentloop.New(prov, loopCfg, tools, handler)

	loopCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	result, err := loop.Run(loopCtx, initial)
	// Check for verdict in channel first (from emit_verdict tool call).
	select {
	case v := <-verdictCh:
		return v, nil
	default:
	}
	// No tool-call verdict. Try to parse one from the model's final
	// text output — Claude often writes the verdict as prose JSON
	// instead of calling the tool, especially near the turn limit
	// or when the system prompt is complex.
	if result != nil && result.FinalText != "" {
		if v := tryParseVerdictFromText(result.FinalText); v != nil {
			return v, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("agentic review loop: %w", err)
	}
	return nil, fmt.Errorf("agentic review loop ended without emit_verdict call")
}

const agenticReviewSystemPrompt = `You are a senior engineering reviewer (VP-eng / CTO role) giving final sign-off on a sprint plan before the team starts building.

You have access to TOOLS to read the original prose and the merged SOW on demand. The TOC + SOW summary appear in the first user message; use the tools to fetch the specific sections and sessions you need to verify fidelity (does the plan deliver what the prose asked for?) and feasibility (can the team actually execute it?).

Workflow:
  1. Skim the TOC + SOW summary in the first user message.
  2. Identify the prose sections that name deliverables/scope (usually sections 2-9). Read those with read_prose_section.
  3. Identify which SOW sessions claim to deliver each scope item. Read their tasks and ACs with read_session(id, include="full") for sessions you suspect have issues.
  4. Cross-check: when the SOW declares a file path, optionally read_repo_file or list_repo_dir to confirm the workspace shape supports it.
  5. When you have enough evidence, call emit_verdict with the structured verdict.

DECISION RULES:
  - approve: every prose deliverable maps to one or more sessions that produce it; ACs actually verify delivery; cross-session deps form a coherent DAG.
  - request_changes: one or more MAJOR concerns (a deliverable is only partially covered, a session's ACs don't actually verify what it produces, cross-session dep gaps that will cause the DAG to race).
  - reject: BLOCKING concern present (a core deliverable is completely missing, the plan is structurally incoherent).

FIDELITY checks:
  - Does every prose deliverable map to one or more sessions?
  - Are session ACs specific enough that "all ACs pass" = "user gets what they asked for"? Generic build-pass ACs on a session that was supposed to deliver a feature is a major concern.
  - Are UI surfaces (pages, screens, routes) scaffolded with per-route/per-screen tasks?
  - Are API endpoints, data models, auth flows traceable to concrete tasks?

FEASIBILITY checks:
  - Is each session's scope bounded (≤ 30 tasks) and coherent (one deliverable, one package boundary)?
  - Do cross-session inputs/outputs form a DAG that serializes correctly?
  - Are ACs achievable in the runtime (no browser E2E on Linux, no long-running servers, no unset env vars)?
  - Are task descriptions specific enough for an agent to execute without creative leaps?

CRITICAL INSTRUCTION FOR VERDICT DELIVERY:
When done, you MUST call the emit_verdict tool. Do NOT write the verdict as prose text. Do NOT write a JSON block in your response. The ONLY way to deliver your verdict is through the emit_verdict tool call. If you write the verdict as text instead of calling the tool, the system will fail to capture it and your entire review will be wasted. Call emit_verdict with: decision, reasoning, concerns array, fidelity_score, feasibility_score.`

// agenticReviewTools returns the tool definitions the reviewer can
// call. Each handler is implemented in buildAgenticReviewHandler.
func agenticReviewTools() []provider.ToolDef {
	return []provider.ToolDef{
		{
			Name:        "list_prose_sections",
			Description: "List the markdown sections of the original prose with their line ranges and byte sizes. Returns JSON [{name, level, lines, bytes}, ...].",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "read_prose_section",
			Description: "Read the body of a prose section by name. Names match list_prose_sections output. Returns the section text.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Section name from list_prose_sections."}},"required":["name"]}`),
		},
		{
			Name:        "search_prose",
			Description: "Grep the original prose for a regex pattern. Returns matching lines with line numbers and ±2 lines of context. Use to find specific deliverables, technologies, or constraints.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression."}},"required":["pattern"]}`),
		},
		{
			Name:        "list_sessions",
			Description: "List all SOW sessions with id, title, task count, AC count, inputs, outputs. Returns compact JSON.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "read_sow_metadata",
			Description: "Read the top-level SOW metadata that ISN'T per-session: SOW id/name/version, the declared tech Stack (frameworks, languages, runtimes), Infra requirements (env vars, services, mocks-allowed flag), and any other SOW-wide fields. Use to verify framework/language/env-var declarations needed for feasibility judgment.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "read_session",
			Description: "Read a single session by id. include controls verbosity: 'summary' (id+title+counts), 'tasks' (full tasks list), 'acs' (full ACs), 'full' (everything). Default 'full'.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"Session id (e.g. S5)."},"include":{"type":"string","enum":["summary","tasks","acs","full"],"description":"Verbosity."}},"required":["id"]}`),
		},
		{
			Name:        "search_sow",
			Description: "Grep all sessions/tasks/ACs for a regex pattern. Returns matching items with their session id and a short snippet. Use to find specific files, identifiers, or behaviors across the SOW.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression."}},"required":["pattern"]}`),
		},
		{
			Name:        "read_repo_file",
			Description: "Read up to 200 lines of a file from the workspace. Use to verify that a file the SOW declares actually exists OR that an existing file the SOW didn't claim has content the SOW should be touching. Returns 'NOT FOUND' if the path doesn't exist.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Repo-relative path."}},"required":["path"]}`),
		},
		{
			Name:        "list_repo_dir",
			Description: "List a directory in the workspace (file names only, depth 1). Use to check workspace structure when verifying SOW declarations.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Repo-relative directory path. Empty = root."}}}`),
		},
		{
			Name:        "grep_repo",
			Description: "Grep the workspace for a regex pattern across .ts/.tsx/.js/.jsx/.go/.py/.rs/.java files. Returns matching files + line numbers (no body). Use to verify a symbol the SOW declares actually exists in code.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression."}},"required":["pattern"]}`),
		},
		{
			Name:        "emit_verdict",
			Description: "Emit the final approval verdict. The loop ends after this is called. Provide decision, reasoning, concerns array (each with severity 'blocking'|'major'|'minor', optional session_id, description, fix), and 0-100 fidelity_score and feasibility_score.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"decision":{"type":"string","enum":["approve","request_changes","reject"]},
					"reasoning":{"type":"string"},
					"concerns":{"type":"array","items":{"type":"object","properties":{
						"severity":{"type":"string","enum":["blocking","major","minor"]},
						"session_id":{"type":"string"},
						"description":{"type":"string"},
						"fix":{"type":"string"}
					},"required":["severity","description"]}},
					"fidelity_score":{"type":"integer","minimum":0,"maximum":100},
					"feasibility_score":{"type":"integer","minimum":0,"maximum":100}
				},
				"required":["decision","reasoning","fidelity_score","feasibility_score"]
			}`),
		},
	}
}

// buildAgenticReviewHandler returns the tool dispatcher closure
// bound to this review's prose, SOW, and projectRoot.
func buildAgenticReviewHandler(prose string, sections *proseIndex, sow *SOW, projectRoot string, verdictCh chan<- *FinalApprovalVerdict) agentloop.ToolHandler {
	return func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "list_prose_sections":
			return jsonCompact(sections.summary())
		case "read_prose_section":
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(input, &args)
			body, ok := sections.read(prose, args.Name)
			if !ok {
				return fmt.Sprintf("section %q not found. Use list_prose_sections to see available names.", args.Name), nil
			}
			return body, nil
		case "search_prose":
			var args struct {
				Pattern string `json:"pattern"`
			}
			_ = json.Unmarshal(input, &args)
			return grepProse(prose, args.Pattern), nil
		case "list_sessions":
			return jsonCompact(summarizeSOW(sow))
		case "read_sow_metadata":
			// Sessions field is excluded — that's covered by
			// list_sessions / read_session. Stack and its nested
			// Infra slice carry the framework + env-var declarations
			// the reviewer needs for feasibility judgment.
			return jsonCompact(map[string]any{
				"id":            sow.ID,
				"name":          sow.Name,
				"stack":         sow.Stack,
				"session_count": len(sow.Sessions),
			})
		case "read_session":
			var args struct {
				ID      string `json:"id"`
				Include string `json:"include"`
			}
			_ = json.Unmarshal(input, &args)
			if args.Include == "" {
				args.Include = "full"
			}
			return readSessionView(sow, args.ID, args.Include), nil
		case "search_sow":
			var args struct {
				Pattern string `json:"pattern"`
			}
			_ = json.Unmarshal(input, &args)
			return grepSOW(sow, args.Pattern), nil
		case "read_repo_file":
			var args struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &args)
			return readRepoFile(projectRoot, args.Path), nil
		case "list_repo_dir":
			var args struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &args)
			return listRepoDir(projectRoot, args.Path), nil
		case "grep_repo":
			var args struct {
				Pattern string `json:"pattern"`
			}
			_ = json.Unmarshal(input, &args)
			return grepRepo(projectRoot, args.Pattern), nil
		case "emit_verdict":
			var v FinalApprovalVerdict
			if _, err := jsonutil.ExtractJSONInto(string(input), &v); err != nil {
				return "", fmt.Errorf("emit_verdict invalid: %w", err)
			}
			if v.Decision == "" {
				return "", fmt.Errorf("emit_verdict requires non-empty decision")
			}
			select {
			case verdictCh <- &v:
			default:
				// Already received one — keep first.
			}
			return "verdict received; loop will end", nil
		}
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// ---------------------------------------------------------------------
// Prose section indexer
// ---------------------------------------------------------------------

type proseSection struct {
	Name      string `json:"name"`
	// Path is the unique addressable name for the section. When the
	// raw heading text repeats (markdown commonly has multiple
	// "Overview" / "API" sections), Path disambiguates by appending
	// "#N" for the Nth occurrence. read_prose_section accepts
	// either Path (always unique) or Name (first match wins).
	Path      string `json:"path"`
	Level     int    `json:"level"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Bytes     int    `json:"bytes"`
	byteStart int
	byteEnd   int
}

type proseIndex struct {
	sections []proseSection
}

func (p *proseIndex) summary() []proseSection {
	out := make([]proseSection, len(p.sections))
	copy(out, p.sections)
	// Strip internal byte offsets from the externally-visible copy.
	for i := range out {
		out[i].byteStart = 0
		out[i].byteEnd = 0
	}
	return out
}

func (p *proseIndex) read(prose, name string) (string, bool) {
	// Try Path first (always unique). Then fall back to Name (first
	// match wins, OK when the heading is unique). Final fallback:
	// case-insensitive. Clamp to len(prose) to avoid panics on
	// inputs that don't end with a trailing newline (the indexer
	// over-counts byteOffset by 1 on the synthetic final newline,
	// so the last section's byteEnd can exceed len(prose) by 1).
	clamp := func(start, end int) string {
		if end > len(prose) {
			end = len(prose)
		}
		if start > end {
			start = end
		}
		return prose[start:end]
	}
	for _, s := range p.sections {
		if s.Path == name {
			return clamp(s.byteStart, s.byteEnd), true
		}
	}
	for _, s := range p.sections {
		if s.Name == name {
			return clamp(s.byteStart, s.byteEnd), true
		}
	}
	low := strings.ToLower(name)
	for _, s := range p.sections {
		if strings.ToLower(s.Path) == low || strings.ToLower(s.Name) == low {
			return clamp(s.byteStart, s.byteEnd), true
		}
	}
	return "", false
}

var headerRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

func indexProseSections(prose string) *proseIndex {
	idx := &proseIndex{}
	lines := strings.Split(prose, "\n")
	type sec struct {
		name      string
		level     int
		lineStart int
		byteStart int
	}
	var open *sec
	byteOffset := 0
	for i, line := range lines {
		if m := headerRE.FindStringSubmatch(line); m != nil {
			if open != nil {
				idx.sections = append(idx.sections, proseSection{
					Name:      open.name,
					Level:     open.level,
					LineStart: open.lineStart,
					LineEnd:   i,
					Bytes:     byteOffset - open.byteStart,
					byteStart: open.byteStart,
					byteEnd:   byteOffset,
				})
			}
			open = &sec{
				name:      strings.TrimSpace(m[2]),
				level:     len(m[1]),
				lineStart: i + 1,
				byteStart: byteOffset,
			}
		}
		byteOffset += len(line) + 1 // +1 for the newline
	}
	if open != nil {
		idx.sections = append(idx.sections, proseSection{
			Name:      open.name,
			Level:     open.level,
			LineStart: open.lineStart,
			LineEnd:   len(lines),
			Bytes:     byteOffset - open.byteStart,
			byteStart: open.byteStart,
			byteEnd:   byteOffset,
		})
	}
	if len(idx.sections) == 0 {
		idx.sections = append(idx.sections, proseSection{
			Name:      "(whole file)",
			Level:     0,
			LineStart: 1,
			LineEnd:   len(lines),
			Bytes:     len(prose),
			byteStart: 0,
			byteEnd:   len(prose),
		})
	}
	// Assign unique Path values so duplicate headings remain
	// individually addressable. First occurrence keeps the bare
	// name; subsequent occurrences get "name [2]", "name [3]", etc.
	// We pre-collect every literal heading text to avoid colliding
	// the synthetic suffix with a real heading that already happens
	// to look like one ("Intro [2]" appearing literally in the
	// prose, however unlikely). On collision the suffix increments
	// until it finds an unused value. The "[N]" form was chosen
	// over "#N" because real markdown headings often contain `#`
	// (e.g. cross-references) but rarely "name [N]" verbatim.
	// Track taken values case-insensitively because read() falls
	// back to case-insensitive lookup; if we treat "Intro [2]" and
	// "intro [2]" as different keys, a literal "intro [2]" section
	// could shadow the synthetic "Intro [2]" on a normalized lookup.
	taken := map[string]struct{}{}
	for i := range idx.sections {
		taken[strings.ToLower(idx.sections[i].Name)] = struct{}{}
	}
	counts := map[string]int{}
	for i := range idx.sections {
		name := idx.sections[i].Name
		counts[name]++
		if counts[name] == 1 {
			idx.sections[i].Path = name
			continue
		}
		// Find an unused "name [N]" suffix (case-insensitive
		// collision check).
		n := counts[name]
		var candidate string
		for {
			candidate = fmt.Sprintf("%s [%d]", name, n)
			if _, dup := taken[strings.ToLower(candidate)]; !dup {
				break
			}
			n++
		}
		idx.sections[i].Path = candidate
		taken[strings.ToLower(candidate)] = struct{}{}
	}
	return idx
}

// ---------------------------------------------------------------------
// SOW summary + per-session view
// ---------------------------------------------------------------------

type sessionSummary struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Tasks    int      `json:"tasks"`
	ACs      int      `json:"acs"`
	Inputs   []string `json:"inputs,omitempty"`
	Outputs  []string `json:"outputs,omitempty"`
	Preempt  bool     `json:"preempt,omitempty"`
}

func summarizeSOW(sow *SOW) []sessionSummary {
	out := make([]sessionSummary, 0, len(sow.Sessions))
	for _, s := range sow.Sessions {
		out = append(out, sessionSummary{
			ID:      s.ID,
			Title:   s.Title,
			Tasks:   len(s.Tasks),
			ACs:     len(s.AcceptanceCriteria),
			Inputs:  s.Inputs,
			Outputs: s.Outputs,
			Preempt: s.Preempt,
		})
	}
	return out
}

func readSessionView(sow *SOW, id, include string) string {
	var found *Session
	for i := range sow.Sessions {
		if sow.Sessions[i].ID == id {
			found = &sow.Sessions[i]
			break
		}
	}
	if found == nil {
		return fmt.Sprintf("session %q not found. Use list_sessions to see available IDs.", id)
	}
	switch include {
	case "summary":
		s, _ := jsonCompact(sessionSummary{
			ID: found.ID, Title: found.Title,
			Tasks: len(found.Tasks), ACs: len(found.AcceptanceCriteria),
			Inputs: found.Inputs, Outputs: found.Outputs,
		})
		return s
	case "tasks":
		s, _ := jsonCompact(map[string]any{"id": found.ID, "tasks": found.Tasks})
		return s
	case "acs":
		s, _ := jsonCompact(map[string]any{"id": found.ID, "acceptance_criteria": found.AcceptanceCriteria})
		return s
	default:
		s, _ := jsonCompact(found)
		return s
	}
}

// ---------------------------------------------------------------------
// Search helpers
// ---------------------------------------------------------------------

func grepProse(prose, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("invalid regex: %v", err)
	}
	lines := strings.Split(prose, "\n")
	hits := make([]string, 0, len(lines))
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		startCtx := i - 2
		if startCtx < 0 {
			startCtx = 0
		}
		endCtx := i + 3
		if endCtx > len(lines) {
			endCtx = len(lines)
		}
		var ctx []string
		for j := startCtx; j < endCtx; j++ {
			marker := "  "
			if j == i {
				marker = "→ "
			}
			ctx = append(ctx, fmt.Sprintf("%s%d: %s", marker, j+1, lines[j]))
		}
		hits = append(hits, strings.Join(ctx, "\n"))
		if len(hits) >= 30 {
			hits = append(hits, "... (truncated, refine the pattern)")
			break
		}
	}
	if len(hits) == 0 {
		return "no matches"
	}
	return strings.Join(hits, "\n---\n")
}

func grepSOW(sow *SOW, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("invalid regex: %v", err)
	}
	var hits []string
	for _, s := range sow.Sessions {
		if re.MatchString(s.Title) || re.MatchString(s.Description) {
			hits = append(hits, fmt.Sprintf("[%s] title/desc: %s — %s", s.ID, s.Title, snippet(s.Description, 100)))
		}
		for _, t := range s.Tasks {
			fields := []string{t.Description, strings.Join(t.Files, " ")}
			joined := strings.Join(fields, " ")
			if re.MatchString(joined) {
				hits = append(hits, fmt.Sprintf("[%s/%s] task: %s | files=%v", s.ID, t.ID, snippet(t.Description, 120), t.Files))
			}
		}
		for _, ac := range s.AcceptanceCriteria {
			joined := ac.Description + " " + ac.Command
			if re.MatchString(joined) {
				hits = append(hits, fmt.Sprintf("[%s/%s] AC: %s | cmd=%s", s.ID, ac.ID, snippet(ac.Description, 100), snippet(ac.Command, 100)))
			}
		}
		if len(hits) >= 80 {
			hits = append(hits, "... (truncated, refine the pattern)")
			break
		}
	}
	if len(hits) == 0 {
		return "no matches"
	}
	return strings.Join(hits, "\n")
}

func snippet(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ---------------------------------------------------------------------
// Repo introspection
// ---------------------------------------------------------------------

func readRepoFile(projectRoot, path string) string {
	if projectRoot == "" {
		return "repo introspection disabled (no projectRoot)"
	}
	abs := filepath.Join(projectRoot, path)
	body, err := os.ReadFile(abs)
	if err != nil {
		return "NOT FOUND: " + path
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) > 200 {
		lines = lines[:200]
		lines = append(lines, "... (truncated at 200 lines)")
	}
	return strings.Join(lines, "\n")
}

func listRepoDir(projectRoot, path string) string {
	if projectRoot == "" {
		return "repo introspection disabled (no projectRoot)"
	}
	abs := filepath.Join(projectRoot, path)
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "NOT FOUND: " + path
	}
	var out []string
	for _, e := range entries {
		mark := ""
		if e.IsDir() {
			mark = "/"
		}
		out = append(out, e.Name()+mark)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func grepRepo(projectRoot, pattern string) string {
	if projectRoot == "" {
		return "repo introspection disabled (no projectRoot)"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("invalid regex: %v", err)
	}
	var hits []string
	_ = filepath.WalkDir(projectRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "dist" ||
				name == "build" || name == ".next" || name == ".turbo" || name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
			".go", ".py", ".rs", ".java", ".kt", ".kts",
			".swift", ".cs", ".php", ".c", ".cpp", ".h", ".hpp", ".m", ".mm":
		default:
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(projectRoot, p)
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, snippet(line, 120)))
				if len(hits) >= 60 {
					hits = append(hits, "... (truncated, refine the pattern)")
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if len(hits) == 0 {
		return "no matches"
	}
	return strings.Join(hits, "\n")
}

// tryParseVerdictFromText attempts to extract a FinalApprovalVerdict
// from the model's free-text output. Claude frequently writes the
// verdict as a JSON block in prose rather than calling emit_verdict —
// especially when the model is near the turn limit, confused by the
// tool schema, or just being Claude. This fallback prevents the
// entire agentic review from failing over something recoverable.
func tryParseVerdictFromText(text string) *FinalApprovalVerdict {
	if text == "" {
		return nil
	}
	var v FinalApprovalVerdict
	if _, err := jsonutil.ExtractJSONInto(text, &v); err != nil {
		return nil
	}
	if v.Decision == "" {
		return nil
	}
	return &v
}

// jsonCompact marshals v as the smallest valid JSON (no indentation,
// no extra whitespace) so tool responses stay dense.
func jsonCompact(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
