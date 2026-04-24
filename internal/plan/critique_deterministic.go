// Package plan — critique_deterministic.go
//
// Rule-based SOW critique that runs on the parsed plan.SOW struct
// BEFORE (and potentially instead of) the LLM critique pass. The
// rules here catch the 90% of issues that don't require semantic
// judgment: schema validity, dep graph correctness, AC command
// hygiene, session sizing, required-field presence.
//
// Rules operate on typed fields only. There is no grep over user
// prose. By the time this runs, the generator has turned prose into
// a structured SOW — deterministic rules on structured data are
// robust, not brittle.
//
// Output shape matches SOWCritique so downstream callers
// (RefineSOW, CritiqueAndRefine) don't need to care which producer
// generated the findings.
//
// Splits cleanly with LLM critique:
//   - Deterministic (here): schema / graph / hygiene — <100ms
//   - LLM (sow_critique.go):  coverage / cohesion / task-AC match
//
// Runtime: on a 401-task SOW with 21 sessions, this pass typically
// completes in <50ms. The LLM pass it replaces was ~25 minutes.

package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Critique verdict and severity string literals. These appear on
// CritiqueIssue / SOWCritique as untyped strings (the shape is JSON-
// serialized for downstream callers), so we keep them as string
// constants rather than enums to preserve the wire format.
const (
	critiqueVerdictReject = "reject"
	critiqueVerdictRefine = "refine"
	critiqueVerdictShip   = "ship"

	critiqueSevBlocking = "blocking"

	stackReactNative = "react-native"
)

// CritiqueDeterministic runs every rule-based check on the SOW and
// returns a SOWCritique populated with findings. Caller decides
// whether to run the LLM pass based on HasBlocking / verdict.
//
// Never errors; a malformed SOW just produces more findings. Nil
// input returns a {Verdict:"reject"} critique naming the nil.
func CritiqueDeterministic(sow *SOW) *SOWCritique {
	c := &SOWCritique{
		Dimensions: map[string]int{},
	}
	if sow == nil {
		c.Verdict = critiqueVerdictReject
		c.Summary = "nil SOW"
		c.Issues = append(c.Issues, CritiqueIssue{
			Severity:    critiqueSevBlocking,
			Description: "SOW is nil",
			Fix:         "ensure ConvertProseToSOW produced a non-nil SOW",
		})
		return c
	}

	// Accumulate findings from each rule group. Each group appends
	// to c.Issues and optionally adjusts the dimension score.
	critiqueStructural(sow, c)
	critiqueDeps(sow, c)
	critiqueAcceptanceCriteria(sow, c)
	critiqueSessionShape(sow, c)
	critiqueStack(sow, c)

	// Derive verdict + overall score from findings.
	blocking, major, minor := countSeverities(c.Issues)
	switch {
	case blocking > 0:
		c.Verdict = critiqueVerdictReject
		c.OverallScore = max(0, 50-blocking*10)
	case major > 3:
		c.Verdict = critiqueVerdictRefine
		c.OverallScore = max(50, 80-major*3)
	case major > 0 || minor > 5:
		c.Verdict = critiqueVerdictRefine
		c.OverallScore = max(70, 90-major*2-minor)
	default:
		c.Verdict = critiqueVerdictShip
		c.OverallScore = max(85, 100-minor)
	}
	c.Summary = fmt.Sprintf("deterministic critique: %d blocking, %d major, %d minor across %d sessions / %d tasks",
		blocking, major, minor, len(sow.Sessions), totalTasks(sow))
	return c
}

// critiqueStructural validates the top-level schema: SOW has
// sessions, sessions have IDs, every task has an ID + description.
func critiqueStructural(sow *SOW, c *SOWCritique) {
	if len(sow.Sessions) == 0 {
		c.Issues = append(c.Issues, CritiqueIssue{
			Severity:    critiqueSevBlocking,
			Description: "SOW has no sessions",
			Fix:         "generator produced an empty session list; re-run prose conversion",
		})
		return
	}
	seenSID := map[string]int{}
	seenTID := map[string]string{} // task ID → session ID
	for _, s := range sow.Sessions {
		if strings.TrimSpace(s.ID) == "" {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    critiqueSevBlocking,
				SessionID:   "(missing)",
				Description: "session with empty ID",
				Fix:         "assign a unique ID like S1, S2 to every session",
			})
			continue
		}
		seenSID[s.ID]++
		if seenSID[s.ID] > 1 {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "major",
				SessionID:   s.ID,
				Description: "duplicate session ID",
				Fix:         "make session IDs unique across the SOW",
			})
		}
		if strings.TrimSpace(s.Title) == "" {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "minor",
				SessionID:   s.ID,
				Description: "session has no title",
				Fix:         "add a human-readable title for operator context",
			})
		}
		for _, t := range s.Tasks {
			if strings.TrimSpace(t.ID) == "" {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    critiqueSevBlocking,
					SessionID:   s.ID,
					TaskID:      "(missing)",
					Description: "task with empty ID",
					Fix:         "assign unique task IDs like T1, T2 across the entire SOW",
				})
				continue
			}
			if prev, dup := seenTID[t.ID]; dup {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    critiqueSevBlocking,
					SessionID:   s.ID,
					TaskID:      t.ID,
					Description: fmt.Sprintf("duplicate task ID — also in session %s", prev),
					Fix:         "task IDs must be unique across the WHOLE SOW, not just per-session",
				})
			}
			seenTID[t.ID] = s.ID
			if strings.TrimSpace(t.Description) == "" {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    "major",
					SessionID:   s.ID,
					TaskID:      t.ID,
					Description: "task has no description",
					Fix:         "every task needs a specific one-sentence description of what it produces",
				})
			}
		}
	}
}

// critiqueDeps checks for self-loops, dangling refs, and cycles.
func critiqueDeps(sow *SOW, c *SOWCritique) {
	known := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			known[t.ID] = true
		}
	}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			for _, dep := range t.Dependencies {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if dep == t.ID {
					c.Issues = append(c.Issues, CritiqueIssue{
						Severity:    critiqueSevBlocking,
						SessionID:   s.ID,
						TaskID:      t.ID,
						Description: "self-loop dependency (task lists its own ID)",
						Fix:         "remove the self-reference from dependencies",
					})
					continue
				}
				if !known[dep] {
					c.Issues = append(c.Issues, CritiqueIssue{
						Severity:    "major",
						SessionID:   s.ID,
						TaskID:      t.ID,
						Description: fmt.Sprintf("dangling dependency: references unknown task %s", dep),
						Fix:         "either remove the dep or add the referenced task back to the SOW",
					})
				}
			}
		}
	}
	// Cycle detection across the full task graph.
	all := make([]Task, 0)
	for _, s := range sow.Sessions {
		all = append(all, s.Tasks...)
	}
	if cycle := detectCycle(all); cycle != "" {
		c.Issues = append(c.Issues, CritiqueIssue{
			Severity:    critiqueSevBlocking,
			Description: "dependency cycle detected: " + cycle,
			Fix:         "break the cycle by dropping the back-edge from the latest task in the cycle",
		})
	}
}

// critiqueAcceptanceCriteria applies the hygiene rules the LLM
// critique prompt lists (lines 27-54 of sow_critique.go): no
// unset-env refs, no || echo ok, no long-running processes, no
// browser E2E, etc. These are substring checks on the Command
// field — not user prose — so they're reliable.
func critiqueAcceptanceCriteria(sow *SOW, c *SOWCritique) {
	type badPattern struct {
		pat      string
		severity string
		reason   string
		fix      string
	}
	// Known-bad substrings in AC commands.
	patterns := []badPattern{
		{"|| echo ok", critiqueSevBlocking, "fallback swallows real failures", "remove the || echo ok; let the command fail"},
		{"|| echo 'ok'", critiqueSevBlocking, "fallback swallows real failures", "remove the fallback"},
		{"|| true", critiqueSevBlocking, "|| true turns every exit code into success", "remove the || true; let the command fail"},
		{"$REPO_URL", critiqueSevBlocking, "unset env var; SOW runs against cwd, no remote clone exists", "remove the git clone; use pnpm install + build at repo root"},
		{"$(mktemp", critiqueSevBlocking, "ACs run in the repo root, not a tmp dir", "rewrite to run in the current workspace"},
		{"git clone", critiqueSevBlocking, "AC should not clone — the workspace is already present", "remove the clone step; verify the already-present tree"},
		{"next dev", critiqueSevBlocking, "long-running process never exits; AC command must terminate", "use 'next build' or 'next lint' instead"},
		{"expo start", critiqueSevBlocking, "long-running process; AC command must terminate", "use 'expo doctor' or a build command instead"},
		{"playwright", critiqueSevBlocking, "browser E2E requires browser binaries + display server not available in the build agent", "replace with unit / integration tests at the API layer"},
		{"cypress", critiqueSevBlocking, "browser E2E not supported in the build agent", "replace with unit tests or Playwright-less integration tests"},
		{"puppeteer", critiqueSevBlocking, "browser automation requires Chromium not available in the build agent", "replace with unit / integration tests"},
	}
	// 'vitest' without ' run' also a problem (dev mode hangs), check
	// separately so we don't false-positive on 'vitest run'.
	checkVitestMode := func(cmd string) bool {
		lower := strings.ToLower(cmd)
		if !strings.Contains(lower, "vitest") {
			return false
		}
		// Allow 'vitest run' explicitly.
		return !strings.Contains(lower, "vitest run")
	}
	// Vite dev-server detection: flag bare 'vite' invocations without
	// 'build' / 'preview' / 'optimize' (dev mode hangs). Separate
	// from vitest — '"vite "' substring match would otherwise reject
	// valid 'vite build' commands which acceptance.go treats as a
	// legitimate build (codex P2).
	checkViteMode := func(cmd string) bool {
		lower := strings.ToLower(cmd)
		if !strings.Contains(lower, "vite") || strings.Contains(lower, "vitest") {
			return false
		}
		for _, ok := range []string{"vite build", "vite preview", "vite optimize"} {
			if strings.Contains(lower, ok) {
				return false
			}
		}
		return true
	}

	for _, s := range sow.Sessions {
		if len(s.AcceptanceCriteria) == 0 {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "major",
				SessionID:   s.ID,
				Description: "session has no acceptance criteria",
				Fix:         "every session needs at least one verifiable AC (command or file_exists)",
			})
			continue
		}
		if len(s.AcceptanceCriteria) > 8 {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "minor",
				SessionID:   s.ID,
				Description: fmt.Sprintf("session has %d ACs (>8); likely over-specified", len(s.AcceptanceCriteria)),
				Fix:         "keep 3-5 load-bearing ACs; cut checks that duplicate build/test coverage",
			})
		}
		for _, ac := range s.AcceptanceCriteria {
			cmd := strings.TrimSpace(ac.Command)
			fileExists := strings.TrimSpace(ac.FileExists)
			// ContentMatch parses to zero-value when the SOW emits
			// the tolerated string form (see sow.go UnmarshalJSON);
			// a zero-value ContentMatch has empty File + Pattern and
			// is unrunnable. Treat as "no verification" regardless
			// of whether the struct pointer is nil. (codex P2.)
			hasContentMatch := ac.ContentMatch != nil && strings.TrimSpace(ac.ContentMatch.File) != ""
			if cmd == "" && fileExists == "" && !hasContentMatch {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    "major",
					SessionID:   s.ID,
					Description: fmt.Sprintf("AC %s has no runnable check (command / file_exists / content_match all empty or malformed)", ac.ID),
					Fix:         "every AC needs a runnable verification; a whitespace-only command or empty content_match doesn't count",
				})
				continue
			}
			lower := strings.ToLower(cmd)
			for _, p := range patterns {
				if strings.Contains(lower, p.pat) {
					c.Issues = append(c.Issues, CritiqueIssue{
						Severity:    p.severity,
						SessionID:   s.ID,
						Description: fmt.Sprintf("AC %s command uses %q: %s", ac.ID, p.pat, p.reason),
						Fix:         p.fix,
					})
				}
			}
			if checkVitestMode(cmd) {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    critiqueSevBlocking,
					SessionID:   s.ID,
					Description: fmt.Sprintf("AC %s uses 'vitest' without 'run' — dev mode hangs", ac.ID),
					Fix:         "change to 'vitest run' so it exits after one pass",
				})
			}
			if checkViteMode(cmd) {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    critiqueSevBlocking,
					SessionID:   s.ID,
					Description: fmt.Sprintf("AC %s uses 'vite' dev-server — long-running, never exits", ac.ID),
					Fix:         "change to 'vite build' or 'vite preview' (which has its own timeout handling)",
				})
			}
		}
	}
}

// critiqueSessionShape catches sessions with zero tasks, sessions
// with very large task counts (likely need splitting), and sessions
// without Outputs declarations (limits parallel scheduling).
func critiqueSessionShape(sow *SOW, c *SOWCritique) {
	for _, s := range sow.Sessions {
		if len(s.Tasks) == 0 {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "major",
				SessionID:   s.ID,
				Description: "session has zero tasks",
				Fix:         "either add tasks or drop the session",
			})
			continue
		}
		if len(s.Tasks) > 30 {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "minor",
				SessionID:   s.ID,
				Description: fmt.Sprintf("session has %d tasks (>30); consider splitting", len(s.Tasks)),
				Fix:         "the session sizer will try to split at dispatch; consider pre-splitting by feature surface",
			})
		}
		if len(s.Outputs) == 0 && s.ID != firstSessionID(sow) {
			// Not a hard error — the fuzzy DAG resolver falls back to
			// declaration order. But without Outputs, downstream
			// sessions can't form explicit deps and parallelism
			// suffers. Flag as minor.
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "minor",
				SessionID:   s.ID,
				Description: "session has no outputs declared; downstream sessions can't form explicit deps → reduced parallelism",
				Fix:         "add 2-4 short output artifact names (e.g. 'web auth flow', 'resident roster')",
			})
		}
	}
}

// critiqueStack verifies the stack fields look plausible for the
// declared framework. Catches the common "framework=next but no
// monorepo.manager" case.
func critiqueStack(sow *SOW, c *SOWCritique) {
	if sow.Stack.Language == "" {
		c.Issues = append(c.Issues, CritiqueIssue{
			Severity:    "minor",
			Description: "stack.language is empty",
			Fix:         "set language (typescript | python | go | rust) so downstream tooling picks right defaults",
		})
	}
	if sow.Stack.Framework == "next" || sow.Stack.Framework == stackReactNative {
		if sow.Stack.Monorepo == nil || sow.Stack.Monorepo.Manager == "" {
			c.Issues = append(c.Issues, CritiqueIssue{
				Severity:    "minor",
				Description: fmt.Sprintf("framework=%s suggests a package manager should be declared", sow.Stack.Framework),
				Fix:         "set stack.monorepo.manager to pnpm / npm / yarn",
			})
		}
	}
	// Infra env vars referenced by sessions must exist in stack.infra.
	infraNames := map[string]bool{}
	for _, i := range sow.Stack.Infra {
		infraNames[strings.ToLower(i.Name)] = true
	}
	for _, s := range sow.Sessions {
		for _, needed := range s.InfraNeeded {
			if !infraNames[strings.ToLower(needed)] {
				c.Issues = append(c.Issues, CritiqueIssue{
					Severity:    "major",
					SessionID:   s.ID,
					Description: fmt.Sprintf("infra_needed references %q but stack.infra has no such entry", needed),
					Fix:         "either add the infra block to stack.infra or remove the infra_needed reference",
				})
			}
		}
	}
}

// countSeverities tallies findings by severity. Stable order: no
// external map iteration matters here.
func countSeverities(issues []CritiqueIssue) (blocking, major, minor int) {
	for _, i := range issues {
		switch i.Severity {
		case critiqueSevBlocking:
			blocking++
		case "major":
			major++
		case "minor":
			minor++
		}
	}
	return
}

func totalTasks(sow *SOW) int {
	n := 0
	for _, s := range sow.Sessions {
		n += len(s.Tasks)
	}
	return n
}

func firstSessionID(sow *SOW) string {
	if len(sow.Sessions) == 0 {
		return ""
	}
	return sow.Sessions[0].ID
}

// FormatCritique renders the critique for operator-facing output.
// Stable ordering (severity → session → issue description) so two
// runs on the same input produce identical text.
func FormatCritique(c *SOWCritique) string {
	if c == nil {
		return "(no critique)"
	}
	issues := append([]CritiqueIssue(nil), c.Issues...)
	sort.Slice(issues, func(i, j int) bool {
		wa, wb := sevWeight(issues[i].Severity), sevWeight(issues[j].Severity)
		if wa != wb {
			return wa < wb
		}
		if issues[i].SessionID != issues[j].SessionID {
			return issues[i].SessionID < issues[j].SessionID
		}
		return issues[i].Description < issues[j].Description
	})
	var b strings.Builder
	fmt.Fprintf(&b, "verdict=%s score=%d  %s\n", c.Verdict, c.OverallScore, c.Summary)
	for _, i := range issues {
		loc := ""
		switch {
		case i.TaskID != "" && i.SessionID != "":
			loc = fmt.Sprintf("[%s/%s] ", i.SessionID, i.TaskID)
		case i.SessionID != "":
			loc = fmt.Sprintf("[%s] ", i.SessionID)
		}
		fmt.Fprintf(&b, "  %-8s %s%s\n", i.Severity, loc, i.Description)
		if i.Fix != "" {
			fmt.Fprintf(&b, "            fix: %s\n", i.Fix)
		}
	}
	return b.String()
}

func sevWeight(s string) int {
	switch s {
	case critiqueSevBlocking:
		return 0
	case "major":
		return 1
	case "minor":
		return 2
	}
	return 3
}

