// r1_antitrunc.go — MCP handler for stoke_antitrunc_verify.
//
// External agents (or the test harness) call this tool to query the
// repo's anti-truncation enforcement state. The handler performs the
// same checks as `r1 antitrunc verify` but returns a structured JSON
// response instead of human-readable text.
//
// Implementation note: we re-implement the verify logic here (rather
// than calling cmd/r1) because importing cmd/r1 from internal/mcp
// would invert the dependency direction. The shared core lives in
// internal/antitrunc/ so both surfaces stay in sync.

package mcp

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/antitrunc"
)

// antitruncTaskClaimRE mirrors cmd/r1/antitrunc_cmd.go's taskClaimRE
// — kept here so internal/mcp doesn't import the cmd package.
var antitruncTaskClaimRE = regexp.MustCompile(`(?i)\b(?:TASK-?(\d+)|spec\s+(\d+)|item\s+(\d+))\b`)
var antitruncDoneClaimRE = regexp.MustCompile(`(?i)\b(?:done|complete|completed|finished|shipped|ready)\b`)

// antitruncVerdict is the per-commit verdict shape returned by the
// MCP tool. Names map 1:1 with cmd/r1/antitrunc_cmd.go's
// commitClassification so consumers see the same vocabulary across
// CLI and MCP.
type antitruncVerdict struct {
	SHA       string `json:"sha"`
	Subject   string `json:"subject"`
	Verdict   string `json:"verdict"`
	Detail    string `json:"detail"`
	TaskClaim string `json:"task_claim,omitempty"`
}

func (s *StokeServer) handleAntiTruncVerify(args map[string]interface{}) (string, error) {
	repoRoot, _ := args["repo_root"].(string)
	if repoRoot == "" {
		repoRoot = "."
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("abs(%s): %w", repoRoot, err)
	}

	n := 20
	if raw, ok := args["n"]; ok {
		switch v := raw.(type) {
		case float64:
			n = int(v)
		case int:
			n = v
		}
	}
	planPath, _ := args["plan_path"].(string)
	if planPath == "" {
		planPath = "plans/build-plan.md"
	}
	specGlob, _ := args["spec_glob"].(string)
	if specGlob == "" {
		specGlob = "specs/*.md"
	}

	commits, err := readAntiTruncChanges(abs, n)
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}

	planRep, _ := antitrunc.ScopeReportFromFile(filepath.Join(abs, planPath))
	specReps := readAntiTruncSpecGlob(abs, specGlob)

	results := make([]antitruncVerdict, 0, len(commits))
	lyingCount := 0
	for _, c := range commits {
		v := classifyAntiTruncChange(c, planRep, specReps)
		results = append(results, v)
		if v.Verdict == "lying" {
			lyingCount++
		}
	}

	out := map[string]any{
		"results": results,
		"plan": map[string]any{
			"path":  planRep.Path,
			"done":  planRep.Done,
			"total": planRep.Total,
		},
		"lying_count":  lyingCount,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"phrase_count": len(antitrunc.PhraseIDs()),
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type antitruncChange struct {
	SHA     string
	Subject string
	Body    string
}

func readAntiTruncChanges(repo string, n int) ([]antitruncChange, error) {
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
	var commits []antitruncChange
	for _, raw := range strings.Split(string(out), "\x1e") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		c := antitruncChange{SHA: parts[0], Subject: parts[1]}
		if len(parts) == 3 {
			c.Body = parts[2]
		}
		commits = append(commits, c)
	}
	return commits, nil
}

func readAntiTruncSpecGlob(repo, glob string) []antitrunc.ScopeReport {
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

func classifyAntiTruncChange(c antitruncChange, planRep antitrunc.ScopeReport, specReps []antitrunc.ScopeReport) antitruncVerdict {
	text := c.Subject + "\n" + c.Body
	out := antitruncVerdict{SHA: c.SHA, Subject: c.Subject, Verdict: "unverified"}

	taskMatches := antitruncTaskClaimRE.FindStringSubmatch(text)
	hasDone := antitruncDoneClaimRE.MatchString(text)
	if !hasDone && len(taskMatches) == 0 {
		out.Detail = "no completion claim"
		return out
	}

	if matches := antitrunc.MatchFalseCompletion(text); len(matches) > 0 {
		if (planRep.Total > 0 && !planRep.IsComplete()) || anyAntiTruncIncompleteSpec(specReps) {
			out.Verdict = "lying"
			out.Detail = fmt.Sprintf("false-completion phrase %q hit AND scope incomplete", matches[0].PhraseID)
			out.TaskClaim = matches[0].Snippet
			return out
		}
	}

	if len(taskMatches) > 0 {
		idxStr := firstNonEmptyAntitruncMcp(taskMatches[1:])
		if idxStr != "" {
			out.TaskClaim = "spec/task " + idxStr
			if found, ok := findAntiTruncSpecByIndex(specReps, idxStr); ok {
				if found.IsComplete() {
					out.Verdict = "verified"
					out.Detail = fmt.Sprintf("spec %s checklist complete (%d/%d)", idxStr, found.Done, found.Total)
				} else {
					out.Verdict = "lying"
					out.Detail = fmt.Sprintf("spec %s claimed done but %d/%d items unchecked",
						idxStr, found.Total-found.Done, found.Total)
				}
				return out
			}
			out.Detail = fmt.Sprintf("spec/task %s claim, no matching spec file found", idxStr)
			return out
		}
	}

	out.Detail = "completion claim without task identifier"
	return out
}

func firstNonEmptyAntitruncMcp(xs []string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func anyAntiTruncIncompleteSpec(specReps []antitrunc.ScopeReport) bool {
	for _, r := range specReps {
		if r.Total > 0 && !r.IsComplete() && (r.Status == "in-progress" || r.Status == "in_progress") {
			return true
		}
	}
	return false
}

func findAntiTruncSpecByIndex(specReps []antitrunc.ScopeReport, idx string) (antitrunc.ScopeReport, bool) {
	for _, r := range specReps {
		base := filepath.Base(r.Path)
		if strings.HasSuffix(base, idx+".md") || strings.Contains(base, "-"+idx+".") || strings.Contains(base, "_"+idx+".") {
			return r, true
		}
	}
	return antitrunc.ScopeReport{}, false
}
