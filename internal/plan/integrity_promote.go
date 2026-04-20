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

	// Drive synthesis from structured Issues (which know the correct
	// TargetFile per category) rather than parsing directive text.
	// Falls back to Directives when no Issues exist (belt-and-
	// suspenders for ecosystems that only populated Directives).
	var tasks []Task
	var acs []AcceptanceCriterion
	if len(report.Issues) > 0 {
		for i, iss := range report.Issues {
			tid := fmt.Sprintf("%s-I%d", src.ID, i+1)
			target := iss.TargetFile
			if target == "" {
				target = iss.SourceFile
			}
			desc := fmt.Sprintf("[%s/%s] %s — edit %s. %s",
				iss.Ecosystem, iss.Category, iss.Detail, target, iss.Fix)
			// Files: both target (the file the fix must edit) AND
			// source (the file whose content triggered detection).
			// Keeping the source keeps it visible to the next
			// integrity-gate pass — without it, a manifest fix would
			// leave the original .ts/.rs/etc. invisible to the
			// ecosystem's Owns() and the next pass would miss any
			// still-unresolved findings. target first so the hash AC
			// and scope checker see it as the primary file.
			files := []string{target}
			if iss.SourceFile != "" && iss.SourceFile != target {
				files = append(files, iss.SourceFile)
			}
			tasks = append(tasks, Task{
				ID:          tid,
				Description: desc,
				Files:       files,
			})
			before := snapshotHash(projectRoot, target)
			preview := before
			if len(preview) > 12 {
				preview = preview[:12]
			}
			acs = append(acs, AcceptanceCriterion{
				ID:          fmt.Sprintf("%s-ac-%d", id, i+1),
				Description: fmt.Sprintf("%s changed after integrity fix (pre-hash %s)", target, preview),
				// Portable SHA-256: sha256sum (Linux/coreutils),
				// shasum -a 256 (macOS), openssl dgst (universal).
				Command: fmt.Sprintf(
					`h="$( { sha256sum -- %q 2>/dev/null || shasum -a 256 -- %q 2>/dev/null || openssl dgst -sha256 %q 2>/dev/null; } | awk '{for(i=1;i<=NF;i++) if(length($i)==64) {print $i; exit}}' )"; [ -z "$h" ] && h='(absent)'; [ "$h" != %q ]`,
					target, target, target, before),
			})
		}
	} else {
		for i, d := range report.Directives {
			tid := fmt.Sprintf("%s-I%d", src.ID, i+1)
			tasks = append(tasks, Task{
				ID:          tid,
				Description: d,
				Files:       extractFilesFromDirective(d, src),
			})
		}
	}
	if len(acs) == 0 {
		// No structured issues — fall back to a repo-diff gate so the
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
	// H-80: teach the integrity-fix worker that its ACs check hash
	// deltas, NOT spec conformance. When the reviewer examines a
	// flagged file, it should read the SOW's intent first; if the
	// file's current content satisfies that intent (e.g., a barrel
	// index.ts was modified to re-export a sibling's new module,
	// which is the CORRECT behavior), the integrity-fix doesn't
	// need to write a revert — it just produces a NOTES.txt line
	// explaining why the change is legitimate. H-76 soft-passes
	// meta-session failures, so documenting + not-reverting
	// preserves rung promotion without breaking ladder forward
	// progress on legitimate cross-session barrel updates.
	description := "Resolve the integrity findings listed in the task descriptions. Do not expand scope.\n\n" +
		"H-80 INTENT-FIRST RULE: before reverting or rewriting a flagged file, read the SOW's intent for it. " +
		"If the current file content is CONSISTENT with SOW intent (e.g., barrel file re-exporting a sibling module that a later session legitimately added), DO NOT REVERT. " +
		"Instead append one line to NOTES.txt at the project root:\n" +
		"  <flagged-file>: integrity-fix-override; reason=<why this change is consistent with SOW intent>\n" +
		"The integrity hash AC will still fail mechanically on that file — H-76 soft-passes meta-session failures so the ladder still promotes. " +
		"Only revert when the flagged change actually violates SOW intent (accidental edits, scope drift, over-reach).\n\n" +
		"When a worker is uncertain, prefer the NOTES.txt documentation route: an explained legitimate change is safer than an unexplained revert that regresses downstream sessions."
	s := Session{
		ID:                 id,
		Title:              title,
		Description:        description,
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
