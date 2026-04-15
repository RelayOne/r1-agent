// Package plan — integrity_promote.go
//
// Builds a fix session from an IntegrityReport. The shape mirrors
// the existing task-escalation fix sessions: marked as Preempt so
// the parallel scheduler prioritizes it, explicitly flagged as a
// "decomp overflow"-adjacent origin in the title so operators can
// see why it was created, and scoped to the exact files the source
// session wrote (no infrastructure, no acceptance criteria beyond
// what the integrity gate already verified).
package plan

import (
	"fmt"
	"strings"
)

// synthIntegrityFixSession constructs a new Session that, when
// dispatched, resolves the issues found in report. The session's
// tasks are one-per-directive so the decomp / retry machinery can
// pick off individual fixes without retrying the whole batch.
func synthIntegrityFixSession(src Session, report *IntegrityReport) Session {
	id := fmt.Sprintf("%s-integrity-fix", src.ID)
	title := fmt.Sprintf("integrity fix from %s", src.Title)
	if src.Title == "" {
		title = fmt.Sprintf("integrity fix from %s", src.ID)
	}

	var tasks []Task
	for i, d := range report.Directives {
		tid := fmt.Sprintf("%s-I%d", src.ID, i+1)
		// Reuse the source's file set as the task's declared files
		// so the worker reads them for context. Per-directive files
		// are embedded in the directive text itself (the gate's
		// directive shape includes the file path).
		tasks = append(tasks, Task{
			ID:          tid,
			Description: d,
			Files:       extractFilesFromDirective(d, src),
		})
	}

	// Per-task AC: each directive's target file must exist + be
	// non-empty after the fix runs. Deterministic check — the next
	// integrity-gate pass (automatic after this session completes)
	// is the real semantic verification, but a write-happened gate
	// here catches the case where the worker produces no output.
	var acs []AcceptanceCriterion
	for i, t := range tasks {
		for _, f := range t.Files {
			acs = append(acs, AcceptanceCriterion{
				ID:          fmt.Sprintf("%s-ac-%d", id, i+1),
				Description: fmt.Sprintf("file %s exists and is non-empty after fix", f),
				Command:     fmt.Sprintf(`test -s %q`, f),
			})
			break // one AC per task (first file)
		}
	}
	if len(acs) == 0 {
		// No declared files — fall back to a repo-diff check so the
		// session cannot pass without writing SOMETHING.
		acs = append(acs, AcceptanceCriterion{
			ID:          fmt.Sprintf("%s-ac-diff", id),
			Description: "session produced at least one file change",
			Command:     `git diff --quiet HEAD -- . && exit 1 || exit 0`,
		})
	}

	s := Session{
		ID:                 id,
		Title:              title,
		Description:        "Resolve the integrity findings listed in the task descriptions. Do not expand scope.",
		Tasks:              tasks,
		AcceptanceCriteria: acs,
		Preempt:            true,
		Inputs:             src.Outputs,
		Outputs:            src.Outputs,
	}
	return s
}

// extractFilesFromDirective parses the directive text for the first
// file-path-shaped token and returns it. Integrity-gate directives
// all embed the source file in a consistent position near the start;
// fallback to the source session's file union when parsing fails.
func extractFilesFromDirective(directive string, src Session) []string {
	// Heuristic: the first "relative/path/with/slashes.ext" in the
	// directive is the target file.
	tokens := strings.Fields(strings.ReplaceAll(directive, ",", " "))
	for _, t := range tokens {
		t = strings.Trim(t, `"'`+"`;:()[]{}.")
		if !strings.Contains(t, "/") {
			continue
		}
		if !strings.Contains(t, ".") {
			continue
		}
		// Rough filename filter.
		if strings.ContainsAny(t, " ") {
			continue
		}
		return []string{t}
	}
	// Fallback: union of source session's files.
	seen := map[string]struct{}{}
	var out []string
	for _, task := range src.Tasks {
		for _, f := range task.Files {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	return out
}
