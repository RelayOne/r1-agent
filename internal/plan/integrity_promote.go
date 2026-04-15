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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// synthIntegrityFixSession constructs a new Session that, when
// dispatched, resolves the issues found in report. The session's
// tasks are one-per-directive so the decomp / retry machinery can
// pick off individual fixes without retrying the whole batch.
//
// projectRoot is used to snapshot each referenced file's content
// hash BEFORE the fix runs, so the synthesized acceptance check
// (below) can require an actual mutation rather than merely that
// the file exists — the file already exists when this function
// runs (the source session just wrote it), so `test -s` would
// pass without work being done.
func synthIntegrityFixSession(projectRoot string, src Session, report *IntegrityReport) Session {
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

	// Per-task AC: the target file's content hash must DIFFER from
	// the pre-fix snapshot we capture right now. `test -s <file>`
	// would be a tautology — the file already exists (the source
	// session just wrote it), so the worker could exit without
	// touching anything and still pass. Hash-diff forces a real
	// mutation (or deletion, which also changes the hash result).
	//
	// Missing files are snapshotted as "(absent)"; any post-fix
	// state (file created, or still missing) differs from that
	// sentinel when real work happens. When the file can't be
	// read for any other reason we fall back to a repo-level
	// `git diff` gate so the session still can't pass no-op.
	var acs []AcceptanceCriterion
	for i, t := range tasks {
		for _, f := range t.Files {
			before := snapshotHash(projectRoot, f)
			acs = append(acs, AcceptanceCriterion{
				ID:          fmt.Sprintf("%s-ac-%d", id, i+1),
				Description: fmt.Sprintf("file %s changed after integrity fix (pre-hash %s)", f, before[:min(12, len(before))]),
				Command:     fmt.Sprintf(`h="$( (sha256sum -- %q 2>/dev/null || echo '(absent)') | awk '{print $1}')"; [ "$h" != %q ]`, f, before),
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

	// DAG wiring: we want any session that was going to consume
	// src.Outputs to now block on THIS fix session finishing first.
	// BuildSessionDAG picks the first producer of each artifact; if
	// this fix also produces src.Outputs, the session DAG will
	// still route consumers to src (it was declared earlier). We
	// mint a distinct output artifact (src.ID + "-integrity-ok")
	// and leave the caller to splice it into downstream sessions'
	// Inputs. The actual splicing happens in the scheduler — see
	// the call site in session_scheduler_parallel.go which, after
	// promoting this session, walks sessions that haven't started
	// yet and appends the fix output to their Inputs when they
	// declared any of src.Outputs.
	fixOutput := fmt.Sprintf("%s-integrity-ok", src.ID)
	s := Session{
		ID:                 id,
		Title:              title,
		Description:        "Resolve the integrity findings listed in the task descriptions. Do not expand scope.",
		Tasks:              tasks,
		AcceptanceCriteria: acs,
		Preempt:            true,
		Inputs:             src.Outputs,
		Outputs:            append([]string{fixOutput}, src.Outputs...),
	}
	return s
}

// FixGateOutputArtifact returns the synthetic output artifact the
// integrity fix session will emit for a given source session. The
// scheduler uses this to splice an edge from the fix into any
// downstream session that declared an input consuming one of
// src.Outputs — preventing downstream work from unblocking on a
// source whose outputs the integrity gate has already flagged as
// broken.
func FixGateOutputArtifact(sourceSessionID string) string {
	return fmt.Sprintf("%s-integrity-ok", sourceSessionID)
}

// snapshotHash returns the SHA-256 of the named file at the time of
// the call, or the sentinel string "(absent)" when the file is
// missing or unreadable. Stable across hash tools (all Unixes in
// stoke's supported set have coreutils sha256sum available).
func snapshotHash(projectRoot, rel string) string {
	abs := rel
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectRoot, rel)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return "(absent)"
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
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
