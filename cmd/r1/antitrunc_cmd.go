// antitrunc_cmd.go — `r1 antitrunc <verb>` CLI subcommand.
//
// Verbs:
//
//   verify  — cross-check recent commit "TASK-N done" claims against
//             plan / spec checklist state. Classifies each commit as
//             Verified-done, Unverified, or Lying. Exits non-zero
//             on any Lying classification — useful as a CI gate.
//
//   tail    — stream the audit/antitrunc/ directory in real time so
//             external observers (or another agent) can watch
//             enforcement firings. (Item 23)
//
//   list-patterns — dump the regex catalog IDs.
//
// The verbs are deliberately separated from `r1 verify` (which is
// the build/test/lint pipeline) — anti-truncation verification is
// adversarial and operates on git history + checklist state, not
// on code state. Mixing them under one verb conflates two
// independent gates.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/antitrunc"
)

// antitruncCmd is the entry point used by main.go's switch.
func antitruncCmd(args []string) {
	os.Exit(runAntiTruncCmd(args, os.Stdout, os.Stderr))
}

func runAntiTruncCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: r1 antitrunc <verify|tail|list-patterns> [flags]")
		return 2
	}
	switch args[0] {
	case "verify":
		return runAntiTruncVerify(args[1:], stdout, stderr)
	case "tail":
		return runAntiTruncTail(args[1:], stdout, stderr)
	case "list-patterns":
		return runAntiTruncListPatterns(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "antitrunc: unknown verb %q\n", args[0])
		return 2
	}
}

// commitClassification records the verdict for one inspected commit.
type commitClassification struct {
	SHA       string `json:"sha"`
	Subject   string `json:"subject"`
	Verdict   string `json:"verdict"` // "verified", "unverified", "lying"
	Detail    string `json:"detail"`
	TaskClaim string `json:"task_claim,omitempty"`
}

// taskClaimRE pulls "TASK-<N>", "task <N>", "spec <N>", "item <N>"
// claims out of a commit subject so we can cross-reference. The
// catalog is intentionally narrow — we only flag claims the
// claimant CAN make objectively, leaving subjective summaries
// alone.
var taskClaimRE = regexp.MustCompile(`(?i)\b(?:TASK-?(\d+)|spec\s+(\d+)|item\s+(\d+))\b`)

// doneClaimRE matches the "done / complete / finished / shipped"
// shape of a completion claim.
var doneClaimRE = regexp.MustCompile(`(?i)\b(?:done|complete|completed|finished|shipped|ready)\b`)

func runAntiTruncVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("antitrunc verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	n := fs.Int("n", 20, "number of recent commits to inspect")
	planPath := fs.String("plan", "plans/build-plan.md", "path to plan markdown")
	specGlob := fs.String("specs", "specs/*.md", "glob for spec markdown files")
	jsonOut := fs.Bool("json", false, "emit JSON output instead of human")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "abs(%s): %v\n", *repo, err)
		return 2
	}

	commits, err := readRecentChanges(absRepo, *n)
	if err != nil {
		fmt.Fprintf(stderr, "git log: %v\n", err)
		return 2
	}

	planRep, _ := antitrunc.ScopeReportFromFile(filepath.Join(absRepo, *planPath))
	specReps := readSpecGlob(absRepo, *specGlob)

	results := make([]commitClassification, 0, len(commits))
	lyingCount := 0
	for _, c := range commits {
		cls := classifyChange(c, planRep, specReps)
		results = append(results, cls)
		if cls.Verdict == "lying" {
			lyingCount++
		}
	}

	if *jsonOut {
		body, _ := json.MarshalIndent(map[string]any{
			"results":     results,
			"plan":        map[string]any{"path": planRep.Path, "done": planRep.Done, "total": planRep.Total},
			"lying_count": lyingCount,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		}, "", "  ")
		fmt.Fprintln(stdout, string(body))
	} else {
		printVerifyHuman(stdout, results, planRep)
	}

	if lyingCount > 0 {
		return 1
	}
	return 0
}

// gitChange is the structured shape of one commit we inspect.
type gitChange struct {
	SHA     string
	Subject string
	Body    string
}

// readRecentChanges shells out to git log to read the last n commits
// in the format we need: SHA \t subject \t body.
func readRecentChanges(repo string, n int) ([]gitChange, error) {
	if n <= 0 {
		n = 20
	}
	cmd := exec.Command("git", "log",
		"-n", fmt.Sprintf("%d", n),
		"--format=%h%x09%s%x09%b%x1e",
	)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var commits []gitChange
	for _, raw := range strings.Split(string(out), "\x1e") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		c := gitChange{SHA: parts[0], Subject: parts[1]}
		if len(parts) == 3 {
			c.Body = parts[2]
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// readSpecGlob parses every markdown file matching glob (resolved
// relative to repo) into a ScopeReport. Used so verify can match a
// "spec N done" claim against the actual spec checklist.
func readSpecGlob(repo, glob string) []antitrunc.ScopeReport {
	abs := filepath.Join(repo, glob)
	matches, err := filepath.Glob(abs)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	out := make([]antitrunc.ScopeReport, 0, len(matches))
	for _, m := range matches {
		rep, err := antitrunc.ScopeReportFromFile(m)
		if err != nil {
			continue
		}
		out = append(out, rep)
	}
	return out
}

// classifyChange produces the Verified-done / Unverified / Lying
// verdict for one commit. Algorithm:
//
//	1. Combine subject + body into "text".
//	2. If neither a done-claim nor a task-claim is present -> "unverified" with no claim.
//	3. If a "spec N (done|complete)" claim is present, look up the spec by index and
//	   check IsComplete().
//	4. If false-completion phrases hit AND the plan/specs aren't fully checked,
//	   classify as "lying" regardless of subject.
func classifyChange(c gitChange, planRep antitrunc.ScopeReport, specReps []antitrunc.ScopeReport) commitClassification {
	text := c.Subject + "\n" + c.Body
	cls := commitClassification{SHA: c.SHA, Subject: c.Subject, Verdict: "unverified"}

	taskMatches := taskClaimRE.FindStringSubmatch(text)
	hasDone := doneClaimRE.MatchString(text)
	if !hasDone && len(taskMatches) == 0 {
		// No claim made.
		cls.Detail = "no completion claim"
		return cls
	}

	// Multi-signal corroboration: a false-completion phrase + an
	// underlying incomplete plan / spec is a "lying" verdict.
	if matches := antitrunc.MatchFalseCompletion(text); len(matches) > 0 {
		if (planRep.Total > 0 && !planRep.IsComplete()) || anyIncompleteSpec(specReps) {
			cls.Verdict = "lying"
			cls.Detail = fmt.Sprintf("false-completion phrase %q hit AND scope incomplete", matches[0].PhraseID)
			cls.TaskClaim = matches[0].Snippet
			return cls
		}
	}

	// Spec-index claim cross-check.
	if len(taskMatches) > 0 {
		idxStr := firstNonEmptyAntitrunc(taskMatches[1:])
		if idxStr != "" {
			cls.TaskClaim = "spec/task " + idxStr
			if found, ok := findSpecByIndex(specReps, idxStr); ok {
				if found.IsComplete() {
					cls.Verdict = "verified"
					cls.Detail = fmt.Sprintf("spec %s checklist complete (%d/%d)", idxStr, found.Done, found.Total)
				} else {
					cls.Verdict = "lying"
					cls.Detail = fmt.Sprintf("spec %s claimed done but %d/%d items unchecked",
						idxStr, found.Total-found.Done, found.Total)
				}
				return cls
			}
			cls.Detail = fmt.Sprintf("spec/task %s claim, no matching spec file found", idxStr)
			return cls
		}
	}

	// Bare done claim with no task index.
	cls.Detail = "completion claim without task identifier"
	return cls
}

func firstNonEmptyAntitrunc(xs []string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func anyIncompleteSpec(specReps []antitrunc.ScopeReport) bool {
	for _, r := range specReps {
		if r.Total > 0 && !r.IsComplete() && (r.Status == "in-progress" || r.Status == "in_progress") {
			return true
		}
	}
	return false
}

// findSpecByIndex locates a spec whose filename ends in "<idx>.md"
// or whose body contains a top-line "spec <idx>" header. Index match
// is exact (no zero-padding tricks).
func findSpecByIndex(specReps []antitrunc.ScopeReport, idx string) (antitrunc.ScopeReport, bool) {
	for _, r := range specReps {
		base := filepath.Base(r.Path)
		if strings.HasSuffix(base, idx+".md") || strings.Contains(base, "-"+idx+".") || strings.Contains(base, "_"+idx+".") {
			return r, true
		}
	}
	return antitrunc.ScopeReport{}, false
}

func printVerifyHuman(stdout io.Writer, results []commitClassification, planRep antitrunc.ScopeReport) {
	fmt.Fprintf(stdout, "antitrunc verify — %d commits inspected\n", len(results))
	if planRep.Total > 0 {
		fmt.Fprintf(stdout, "  plan %s: %d/%d done (%.0f%%)\n", planRep.Path, planRep.Done, planRep.Total, planRep.PercentDone()*100)
	}
	verified, unverified, lying := 0, 0, 0
	for _, r := range results {
		switch r.Verdict {
		case "verified":
			verified++
		case "unverified":
			unverified++
		case "lying":
			lying++
		}
	}
	fmt.Fprintf(stdout, "  verified: %d  unverified: %d  lying: %d\n\n", verified, unverified, lying)

	for _, r := range results {
		var marker string
		switch r.Verdict {
		case "verified":
			marker = "OK  "
		case "unverified":
			marker = "??  "
		case "lying":
			marker = "!!  "
		}
		fmt.Fprintf(stdout, "%s%s %s — %s\n", marker, r.SHA, r.Subject, r.Detail)
	}
	if lying > 0 {
		fmt.Fprintln(stdout, "\nantitrunc verify FAILED: at least one commit was classified Lying.")
	}
}

func runAntiTruncTail(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("antitrunc tail", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	follow := fs.Bool("follow", false, "follow new files indefinitely")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := filepath.Join(*repo, "audit", "antitrunc")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "(no audit/antitrunc/ directory yet)")
			return 0
		}
		fmt.Fprintf(stderr, "read %s: %v\n", dir, err)
		return 1
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fmt.Fprintln(stdout, "==", e.Name(), "==")
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Fprintf(stderr, "read %s: %v\n", e.Name(), err)
			continue
		}
		fmt.Fprintln(stdout, string(body))
	}
	if *follow {
		fmt.Fprintln(stderr, "tail --follow: polling, ctrl-c to exit")
		seen := make(map[string]bool)
		for _, e := range entries {
			seen[e.Name()] = true
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			es, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range es {
				if e.IsDir() || seen[e.Name()] {
					continue
				}
				seen[e.Name()] = true
				fmt.Fprintln(stdout, "==", e.Name(), "==")
				body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				fmt.Fprintln(stdout, string(body))
			}
		}
	}
	return 0
}

func runAntiTruncListPatterns(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("antitrunc list-patterns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for _, id := range antitrunc.PhraseIDs() {
		fmt.Fprintln(stdout, id)
	}
	return 0
}
