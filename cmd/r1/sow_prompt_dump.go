// SOW prompt dry-run / dumper.
//
// --dump-task-prompts writes the exact system + user prompts Stoke
// would send for every task in the SOW, without calling any LLM.
// Lets the user verify that spec extraction, canonical identifiers,
// and task framing are actually injecting the right content before
// spending on a real native-runner run.
//
// Output layout: .stoke/prompt-dump/<session_id>-<task_id>.txt
// Each file contains:
//
//	SYSTEM PROMPT
//	====================
//	...
//
//	USER PROMPT
//	====================
//	...
//
//	SUPERVISOR EXPECTATIONS
//	====================
//	crates/foo/src/lib.rs:
//	  - must contain: "pub struct Foo"
//	  - must contain: "pub mod bar"
//
//	SPEC EXCERPT (what extractTaskSpecExcerpt pulled)
//	====================
//	...
//
// Also writes .stoke/prompt-dump/_summary.txt with a one-line
// entry per task showing: task ID, excerpt size, expectation count.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/plan"
)

// dumpTaskPrompts walks every session and task in a SOW, builds the
// prompts as execNativeTask would, and writes them to disk. Returns
// the number of files written.
func dumpTaskPrompts(repoRoot string, sow *plan.SOW, rawSOW string) (int, error) {
	dumpDir := filepath.Join(repoRoot, ".stoke", "prompt-dump")
	if err := os.RemoveAll(dumpDir); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("clean dump dir: %w", err)
	}
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		return 0, fmt.Errorf("create dump dir: %w", err)
	}

	var summary strings.Builder
	fmt.Fprintf(&summary, "Task prompt dump for SOW: %s (%s)\n", sow.Name, sow.ID)
	fmt.Fprintf(&summary, "Sessions: %d, total tasks: %d, raw SOW: %d bytes\n\n", len(sow.Sessions), countSOWTasks(sow), len(rawSOW))
	fmt.Fprintf(&summary, "%-20s %-30s %8s %8s %12s\n", "session", "task", "sys_size", "usr_size", "expectations")
	fmt.Fprintf(&summary, "%s\n", strings.Repeat("-", 80))

	count := 0
	for _, session := range sow.Sessions {
		for _, task := range session.Tasks {
			sys, usr := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{
				RawSOW:  rawSOW,
				RepoRoot: repoRoot,
			})

			// Build the supervisor expectations the real run would use.
			sup := autoExtractTaskSupervisor(repoRoot, rawSOW, session, task, 3)
			expectationLines := renderSupervisorExpectations(sup)

			// Extract the excerpt separately so the dump shows exactly
			// what extractTaskSpecExcerpt pulled — useful for debugging
			// "why didn't my task see the right section".
			excerpt := extractTaskSpecExcerpt(rawSOW, session, task, specExcerptConfig{})

			// Write the per-task file.
			filename := sanitizeForFilename(session.ID) + "-" + sanitizeForFilename(task.ID) + ".txt"
			outPath := filepath.Join(dumpDir, filename)
			var b strings.Builder
			b.WriteString("SYSTEM PROMPT\n")
			b.WriteString("====================\n")
			b.WriteString(sys)
			b.WriteString("\n\n")
			b.WriteString("USER PROMPT\n")
			b.WriteString("====================\n")
			b.WriteString(usr)
			b.WriteString("\n\n")
			b.WriteString("SUPERVISOR EXPECTATIONS (auto-extracted from SOW)\n")
			b.WriteString("====================\n")
			if expectationLines == "" {
				b.WriteString("(none — task has no content_match criteria and auto-extractor found no identifiers to verify)\n")
			} else {
				b.WriteString(expectationLines)
			}
			b.WriteString("\n")
			b.WriteString("SPEC EXCERPT (from extractTaskSpecExcerpt)\n")
			b.WriteString("====================\n")
			if excerpt == "" {
				b.WriteString("(empty — no paragraphs in the SOW matched this task's files or identifiers)\n")
			} else {
				b.WriteString(excerpt)
				b.WriteString("\n")
			}
			if err := os.WriteFile(outPath, []byte(b.String()), 0o600); err != nil {
				return count, fmt.Errorf("write %s: %w", outPath, err)
			}

			expectationCount := 0
			if sup != nil {
				for _, e := range sup.Expectations {
					expectationCount += len(e.MustContain) + len(e.MustNotContain)
				}
			}
			fmt.Fprintf(&summary, "%-20s %-30s %8d %8d %12d\n",
				truncateColumn(session.ID, 20),
				truncateColumn(task.ID, 30),
				len(sys), len(usr), expectationCount)

			count++
		}
	}

	if err := os.WriteFile(filepath.Join(dumpDir, "_summary.txt"), []byte(summary.String()), 0o600); err != nil {
		return count, fmt.Errorf("write summary: %w", err)
	}
	return count, nil
}

// renderSupervisorExpectations produces a human-readable block
// describing what the midturn supervisor will check. Returns ""
// when the spec is nil (no expectations at all).
func renderSupervisorExpectations(sup *specSupervisorSpec) string {
	if sup == nil {
		return ""
	}
	// Sort file keys for stable output.
	files := make([]string, 0, len(sup.Expectations))
	indices := make(map[string]int, len(sup.Expectations))
	for i, e := range sup.Expectations {
		files = append(files, e.File)
		indices[e.File] = i
	}
	sort.Strings(files)

	var b strings.Builder
	for _, file := range files {
		exp := sup.Expectations[indices[file]]
		fmt.Fprintf(&b, "%s:\n", file)
		if len(exp.MustContain) == 0 && len(exp.MustNotContain) == 0 {
			b.WriteString("  (no expectations)\n")
			continue
		}
		for _, must := range exp.MustContain {
			fmt.Fprintf(&b, "  - must contain: %q\n", must)
		}
		for _, forbid := range exp.MustNotContain {
			fmt.Fprintf(&b, "  - must NOT contain: %q\n", forbid)
		}
	}
	return b.String()
}

// dumpPromptsCollisionRisk returns "" when the SOW's session+task
// IDs are safe for --dump-task-prompts file naming, and a non-empty
// reason string otherwise. dumpTaskPrompts writes
// `<session.ID>-<task.ID>.txt` per task; empty IDs collapse to
// "unknown" via sanitizeForFilename and duplicate (session, task)
// pairs overwrite each other, so dumping a SOW with either pattern
// silently loses prompts.
func dumpPromptsCollisionRisk(sow *plan.SOW) string {
	seen := map[string]bool{}
	for _, s := range sow.Sessions {
		if s.ID == "" {
			return "session with empty ID"
		}
		for _, t := range s.Tasks {
			if t.ID == "" {
				return fmt.Sprintf("session %s has a task with empty ID", s.ID)
			}
			key := s.ID + "\x00" + t.ID
			if seen[key] {
				return fmt.Sprintf("duplicate (%s, %s) task pair", s.ID, t.ID)
			}
			seen[key] = true
		}
	}
	return ""
}

// sanitizeForFilename strips characters that aren't safe on disk.
// Preserves alphanumerics, dash, underscore, dot; replaces anything
// else with underscore.
func sanitizeForFilename(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// truncateColumn pads or truncates a string to fit a fixed column
// width — used for the summary table.
func truncateColumn(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
