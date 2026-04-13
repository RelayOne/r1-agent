// Package plan — fix_dag.go
//
// Root-cause research-and-fix DAG planner.
//
// Why this file exists:
//
// When a stoke SOW session exhausts its repair cascade (a failing
// session spawns S1-cont which also fails, hits maxCascadeDepth,
// and stoke just gives up), a real root cause is often already
// in hand: the semantic judge has named it ("apps/web/tsconfig.json
// extends a base that pins rootDir='./src' but Next.js app-router
// files are under apps/web/app/"). The existing machinery however
// either surrenders at the cap or produces a flat continuation
// list that the next repair worker retries with the same blunt
// approach — it does not translate the diagnosis into a minimal,
// dependency-ordered set of fix actions.
//
// PlanFixDAG closes that gap. At cascade cap, the caller invokes
// this planner: a specialized multi-turn tool-use agent (read_file
// / grep / glob / bash, same cwd-escape sandbox as integration
// review) that (a) reads the sticky-AC diagnoses, (b) verifies the
// root cause via live inspection of the repo, and (c) emits a
// JSON FixDAG whose Tasks carry explicit Dependencies. ApplyFixDAG
// then topologically sorts the DAG and materializes it as a new
// plan.Session the caller promotes via SessionScheduler.AppendSession.
//
// Intra-session task Dependencies are already honored by the
// parallel scheduler's wave builder (runSessionPhase1Parallel).
// The sequential path dispatches in array order, so ApplyFixDAG
// topo-sorts Tasks before returning — making the promoted session
// correct under both dispatch modes.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// FixDAGTask is one node in a promoted fix DAG. Maps 1:1 onto
// plan.Task when the caller promotes the DAG into a Session.
type FixDAGTask struct {
	// ID is unique within the DAG. The caller namespaces it with
	// the parent session ID when building plan.Task IDs.
	ID string `json:"id"`
	// Directive is a one-paragraph instruction, specific enough
	// for a repair worker to execute without further planning.
	Directive string `json:"directive"`
	// Files is the narrow set of files the worker is expected to
	// touch. Optional but strongly encouraged — the scheduler uses
	// it for wave packing.
	Files []string `json:"files,omitempty"`
	// Dependencies lists other FixDAGTask IDs that must finish
	// before this one can start. Leave empty for root tasks.
	Dependencies []string `json:"dependencies,omitempty"`
}

// FixDAG is the planner's full verdict: a research summary plus a
// dependency-ordered DAG of fix tasks. Abandon=true means the
// planner could not find an actionable fix within the current
// session's scope.
type FixDAG struct {
	// ResearchSummary is one paragraph: the root cause the planner
	// identified and how it verified that identification.
	ResearchSummary string `json:"research_summary"`
	// RootCauseFiles is the narrow list of files the planner
	// believes actually contain the bug. May be empty when the fix
	// is structural (new file creation, dep addition).
	RootCauseFiles []string `json:"root_cause_files,omitempty"`
	// Tasks is the fix DAG. Topologically sorted by ApplyFixDAG
	// before promotion.
	Tasks []FixDAGTask `json:"tasks"`
	// Abandon is true when the planner genuinely could not find a
	// fixable root cause at the current scope.
	Abandon bool `json:"abandon,omitempty"`
	// AbandonReason is the planner's prose explanation when Abandon
	// is true. Required when Abandon is true; may be empty otherwise.
	AbandonReason string `json:"abandon_reason,omitempty"`
}

// StickyACContext is one unresolved acceptance criterion plus all
// the diagnostic context the repair loop accumulated for it.
type StickyACContext struct {
	// ACID is the criterion's ID.
	ACID string
	// Description is the human-readable criterion text.
	Description string
	// Command is the criterion's executable check, when present.
	Command string
	// LastOutput is the most recent failure output from the
	// mechanical check.
	LastOutput string
	// SemanticJudgeVerdicts accumulates every semantic-judge
	// reasoning string emitted for this AC during the session's
	// repair loop. Oldest first.
	SemanticJudgeVerdicts []string
}

// FixDAGInput is what PlanFixDAG needs from the failing session's
// context: repo root, session identity, sticky ACs with their
// diagnosis trail, the flat list of repair directives already
// tried, and the raw SOW spec excerpt.
type FixDAGInput struct {
	// RepoRoot is the absolute path to the repository. All tool
	// calls are rooted here with cwd-escape protection.
	RepoRoot string
	// FromSessionID is the ID of the session that exhausted its
	// cascade.
	FromSessionID string
	// FromSessionTitle is the human-readable title of that session.
	FromSessionTitle string
	// StickyACs is the list of ACs that remained failing across
	// the repair cascade.
	StickyACs []StickyACContext
	// RepairHistory is a flat list of repair directives already
	// attempted (oldest first). The planner must NOT re-propose
	// any of these.
	RepairHistory []string
	// SOWSpec is the relevant session spec excerpt (verbatim SOW
	// prose) the planner can cross-reference.
	SOWSpec string

	// UniversalPromptBlock carries the universal coding-standards +
	// known-gotchas block. When non-empty it is appended to the
	// fix-DAG planner's system prompt.
	UniversalPromptBlock string
}

// fixDAGSystemPrompt is the tight structural prompt that defines
// the planner's role, focus, don't-list, abandon conditions, and
// output shape.
const fixDAGSystemPrompt = `You are a root-cause planning agent. A session has exhausted its normal repair cascade with one or more acceptance criteria still failing. Your ONE job: produce a minimal, dependency-ordered DAG of fix tasks that resolves the root cause.

FOCUS:
- Read the failing diagnoses carefully. Semantic judges often name the root cause directly. Verify via tool use and translate into a DAG.
- Use read_file / grep / glob to confirm root-cause files before proposing edits. NEVER propose edits to files you haven't inspected.
- Use bash sparingly, only to verify hypotheses (e.g. ` + "`tsc --showConfig`" + `).
- Produce the NARROWEST DAG that closes the root cause. Each task = one concrete directive + specific file(s) + explicit Dependencies.

DO NOT:
- Propose fixes the repair history already tried.
- Invent scope. Close the stuck ACs, nothing more.
- Submit a flat list when deps exist.

ABANDON WHEN:
- External services/credentials needed.
- SOW ACs are mutually contradictory.
- Can't locate root cause after thorough exploration.

OUTPUT: single JSON object matching FixDAG schema. No prose outside the JSON. end_turn after.

Schema:
{"research_summary": "...", "root_cause_files": ["..."], "tasks": [{"id": "FIX1", "directive": "...", "files": ["..."], "dependencies": []}], "abandon": false, "abandon_reason": ""}`

// PlanFixDAG runs a multi-turn tool-use planning agent that reads
// the sticky-AC diagnoses, explores the repo via read_file / grep
// / glob / bash, identifies the root cause, and emits a FixDAG.
//
// Returns (nil, nil) when prov is nil — the caller falls through
// to its hard-cap behavior. Budget: 10-minute default (the caller's
// ctx deadline wins when it's tighter). Turn cap: 40.
//
// Bash commands run under the same cwd-escape sandbox and
// destructive-command deny list used by the integration reviewer
// (integrationSanitizePath + integrationBashEscapeDeny +
// integrationBashDeny). This file intentionally reuses those
// package-private helpers rather than duplicating them — any
// hardening applied to the integration reviewer propagates here.
func PlanFixDAG(ctx context.Context, prov provider.Provider, model string, in FixDAGInput) (*FixDAG, error) {
	if prov == nil {
		return nil, nil
	}
	if strings.TrimSpace(in.RepoRoot) == "" {
		return nil, fmt.Errorf("fix-dag planner: empty repo root")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// 10-minute default budget; honor a tighter caller deadline.
	budget := 10 * time.Minute
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < budget {
			budget = remaining
		}
	}
	sessionCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	userText := buildFixDAGUserPrompt(in)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})
	messages := []provider.ChatMessage{{Role: "user", Content: userContent}}

	tools := integrationTools()

	const maxTurns = 40
	for turn := 0; turn < maxTurns; turn++ {
		if sessionCtx.Err() != nil {
			return nil, fmt.Errorf("fix-dag planner timed out: %w", sessionCtx.Err())
		}
		sysPrompt := fixDAGSystemPrompt
		if strings.TrimSpace(in.UniversalPromptBlock) != "" {
			sysPrompt += "\n\n" + in.UniversalPromptBlock
		}
		resp, err := prov.Chat(provider.ChatRequest{
			Model:     model,
			System:    sysPrompt,
			Messages:  messages,
			MaxTokens: 3000,
			Tools:     tools,
		})
		if err != nil {
			return nil, fmt.Errorf("fix-dag planner chat: %w", err)
		}
		if resp == nil {
			return nil, fmt.Errorf("fix-dag planner: nil response")
		}

		assistantBlocks := marshalAssistantBlocks(resp.Content)
		messages = append(messages, provider.ChatMessage{Role: "assistant", Content: assistantBlocks})

		toolUses := extractToolUses(resp.Content)
		if len(toolUses) == 0 {
			raw, _ := collectModelText(resp)
			var dag FixDAG
			if _, err := jsonutil.ExtractJSONInto(raw, &dag); err != nil {
				fmt.Printf("  🔬 fix-dag planner: (no JSON verdict) %s\n", firstLine(raw))
				return &FixDAG{
					Abandon:       true,
					AbandonReason: "planner emitted no parseable JSON verdict: " + firstLine(raw),
				}, nil
			}
			return &dag, nil
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
				result = fixDAGBash(sessionCtx, tu.Input, in.RepoRoot)
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

	fmt.Printf("  🔬 fix-dag planner: turn cap reached (%d) — abandoning\n", maxTurns)
	return &FixDAG{
		Abandon:       true,
		AbandonReason: fmt.Sprintf("planner turn cap %d reached without a verdict", maxTurns),
	}, nil
}

// fixDAGBash mirrors integrationBash exactly — same deny list, same
// cwd-escape guard, same timeout shape. Factored as its own symbol
// (rather than calling integrationBash directly) so future tweaks
// to the planner's bash policy (e.g. a tighter timeout, a richer
// output cap) can diverge without touching the integration
// reviewer's copy. Today it is a verbatim delegation.
func fixDAGBash(ctx context.Context, input map[string]interface{}, repoRoot string) string {
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
	// Belt-and-suspenders: make sure bash truly starts from repoRoot even
	// if something in the env tries to chdir elsewhere.
	if _, err := os.Stat(repoRoot); err != nil {
		return fmt.Sprintf("--- bash ---\nerror: repo root unavailable: %v", err)
	}
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

// buildFixDAGUserPrompt renders the opening user message with the
// failing session's identity, the full sticky-AC diagnostic trail,
// the repair history, and the SOW excerpt.
func buildFixDAGUserPrompt(in FixDAGInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository root: %s\n\n", in.RepoRoot)
	fmt.Fprintf(&b, "FAILED SESSION: %s — %s\n", in.FromSessionID, in.FromSessionTitle)
	b.WriteString("The session above exhausted its repair cascade. The criteria below remain failing.\n\n")

	if len(in.StickyACs) > 0 {
		b.WriteString("STICKY ACCEPTANCE CRITERIA (unresolved after full repair cascade):\n")
		for _, ac := range in.StickyACs {
			fmt.Fprintf(&b, "  [%s] %s\n", ac.ACID, ac.Description)
			if strings.TrimSpace(ac.Command) != "" {
				fmt.Fprintf(&b, "    command: %s\n", ac.Command)
			}
			if strings.TrimSpace(ac.LastOutput) != "" {
				out := ac.LastOutput
				if len(out) > 3000 {
					out = out[:3000] + "\n...[truncated]"
				}
				b.WriteString("    last output:\n")
				for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
			for i, v := range ac.SemanticJudgeVerdicts {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				if len(v) > 1500 {
					v = v[:1500] + "...[truncated]"
				}
				fmt.Fprintf(&b, "    judge verdict %d: %s\n", i+1, v)
			}
			b.WriteString("\n")
		}
	}

	if len(in.RepairHistory) > 0 {
		b.WriteString("REPAIR DIRECTIVES ALREADY TRIED (do NOT re-propose these approaches):\n")
		for i, d := range in.RepairHistory {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if len(d) > 400 {
				d = d[:400] + "..."
			}
			fmt.Fprintf(&b, "  %d. %s\n", i+1, d)
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

	b.WriteString("Verify the root cause with read_file / grep / glob / bash. Then emit ONLY the JSON FixDAG verdict described in the system prompt and end your turn.\n")
	return b.String()
}

// ApplyFixDAG materializes a FixDAG into a plan.Session whose Tasks
// have Dependencies set. Task IDs are namespaced as
// parentSessionID+"-fix-"+dag.Tasks[i].ID so the promoted session's
// IDs never collide with the parent's task IDs.
//
// Validation: ApplyFixDAG rejects cycles and dependencies naming
// unknown FixDAGTask IDs. Tasks are topologically sorted (Kahn's
// algorithm, stable within a wave to preserve authoring order) so
// the sequential dispatcher — which does not respect Task.Dependencies
// on its own — still executes in a correct order. The parallel
// dispatcher's wave builder already honors Dependencies; the sort
// leaves its behavior unchanged.
func ApplyFixDAG(dag FixDAG, parentSessionID, parentSessionTitle string) (Session, error) {
	if strings.TrimSpace(parentSessionID) == "" {
		return Session{}, fmt.Errorf("apply fix DAG: empty parent session ID")
	}
	if len(dag.Tasks) == 0 {
		return Session{}, fmt.Errorf("apply fix DAG: no tasks in DAG")
	}

	// Validate IDs + dependency references, and build an id→index map.
	idx := make(map[string]int, len(dag.Tasks))
	for i, t := range dag.Tasks {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return Session{}, fmt.Errorf("apply fix DAG: task %d has empty ID", i)
		}
		if _, dup := idx[id]; dup {
			return Session{}, fmt.Errorf("apply fix DAG: duplicate task ID %q", id)
		}
		idx[id] = i
	}
	for _, t := range dag.Tasks {
		for _, dep := range t.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if _, ok := idx[dep]; !ok {
				return Session{}, fmt.Errorf("apply fix DAG: task %q depends on unknown ID %q", t.ID, dep)
			}
			if dep == t.ID {
				return Session{}, fmt.Errorf("apply fix DAG: task %q depends on itself", t.ID)
			}
		}
	}

	// Kahn's topo sort. Stable within a ready-set: tasks become
	// available in authoring order.
	inDegree := make([]int, len(dag.Tasks))
	adj := make([][]int, len(dag.Tasks))
	for i, t := range dag.Tasks {
		for _, dep := range t.Dependencies {
			if dep == "" {
				continue
			}
			d := idx[dep]
			adj[d] = append(adj[d], i)
			inDegree[i]++
		}
	}
	queue := make([]int, 0, len(dag.Tasks))
	for i, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, i)
		}
	}
	order := make([]int, 0, len(dag.Tasks))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		for _, m := range adj[n] {
			inDegree[m]--
			if inDegree[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	if len(order) != len(dag.Tasks) {
		// Remaining nodes with in-degree > 0 form one or more cycles.
		var cyc []string
		for i, deg := range inDegree {
			if deg > 0 {
				cyc = append(cyc, dag.Tasks[i].ID)
			}
		}
		return Session{}, fmt.Errorf("apply fix DAG: cycle detected among tasks %v", cyc)
	}

	// Build the materialized session.
	title := strings.TrimSpace(parentSessionTitle)
	if title == "" {
		title = "root-cause fix from " + parentSessionID
	}
	sess := Session{
		ID:          parentSessionID + "-fix",
		Title:       title,
		Description: "root-cause fix DAG promoted after " + parentSessionID + " exhausted its repair cascade",
	}
	// Namespace every FixDAGTask ID -> plan.Task ID and remap deps.
	prefix := parentSessionID + "-fix-"
	for _, pos := range order {
		t := dag.Tasks[pos]
		deps := make([]string, 0, len(t.Dependencies))
		for _, dep := range t.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			deps = append(deps, prefix+dep)
		}
		sess.Tasks = append(sess.Tasks, Task{
			ID:           prefix + t.ID,
			Description:  t.Directive,
			Dependencies: deps,
			Files:        append([]string(nil), t.Files...),
		})
	}
	return sess, nil
}
