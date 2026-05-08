// Package antitrunclobe provides the cortex-side anti-truncation Lobe.
//
// The Lobe satisfies the real cortex.Lobe contract from
// internal/cortex/lobe.go:27 (ID, Description, Kind, Run) and publishes
// SevCritical Notes into the production cortex.Workspace whenever the
// Detector finds an anti-truncation signal. See lobe_wrapper.go for the
// AntiTruncLobe type and audit/scan-governance-gaps.md item #2 for the
// background on why the wrapper used to ship shadow types.
//
// This file (lobe.go) ships the cortex-independent Detector itself so
// the supervisor rules (internal/supervisor/rules/antitrunc/) and the
// agentloop wiring (internal/agentloop/antitrunc.go) can call it
// directly without a cortex import.
package antitrunclobe

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/antitrunc"
)

// Detector is the cortex-independent core that scans assistant
// history, plan files, spec files, and recent commits for
// anti-truncation signals. The (forthcoming) cortex Lobe wraps this.
//
// Inputs:
//
//   - History — the conversation's assistant turns (text only). The
//     Detector concatenates them and runs MatchTruncation across
//     the result. (Plan + spec scans are independent of history.)
//   - PlanPath — same semantics as antitrunc.Gate.PlanPath.
//   - SpecGlob — a glob pattern (filepath.Match-style) resolved
//     relative to the cwd; matching files are spec-scanned.
//   - GitLog — pluggable function returning recent commit bodies
//     (mirrors Gate.CommitLookbackFn). nil = skip commit scan.
type Detector struct {
	History  []string
	PlanPath string
	SpecGlob string
	GitLog   func(n int) ([]string, error)
}

// Run scans inputs and returns every anti-truncation Finding. Run
// has no side effects — it does NOT publish Notes or write to the
// audit directory; the caller (the cortex Lobe wrapper, the
// supervisor rules, the gate) decides how to surface findings.
//
// Run is safe to call repeatedly; the results are deterministic
// given the same inputs.
func (d *Detector) Run(ctx context.Context) []antitrunc.Finding {
	var findings []antitrunc.Finding

	// 1. Truncation phrases across all assistant history (not just
	//    the last turn — the cortex Lobe sees the whole conversation
	//    while the gate sees only the latest).
	for _, t := range d.History {
		for _, m := range antitrunc.MatchTruncation(t) {
			findings = append(findings, antitrunc.Finding{
				Source:   "assistant_output",
				PhraseID: m.PhraseID,
				Snippet:  m.Snippet,
				Detail:   m.Snippet,
			})
		}
	}

	// 2. Plan unchecked items.
	if d.PlanPath != "" {
		if data, err := os.ReadFile(d.PlanPath); err == nil {
			rep := antitrunc.ScopeReportFromText(d.PlanPath, string(data))
			if rep.Total > 0 && !rep.IsComplete() {
				findings = append(findings, antitrunc.Finding{
					Source:  "plan_unchecked",
					Snippet: "",
					Detail:  formatPlanFinding(rep),
				})
			}
		}
	}

	// 3. Spec-glob unchecked items, in-progress only.
	if d.SpecGlob != "" {
		matches, _ := filepath.Glob(d.SpecGlob)
		sort.Strings(matches)
		for _, sp := range matches {
			data, err := os.ReadFile(sp)
			if err != nil {
				continue
			}
			rep := antitrunc.ScopeReportFromText(sp, string(data))
			if rep.Status != "in-progress" && rep.Status != "in_progress" {
				continue
			}
			if rep.Total > 0 && !rep.IsComplete() {
				findings = append(findings, antitrunc.Finding{
					Source: "spec_unchecked",
					Detail: formatPlanFinding(rep),
				})
			}
		}
	}

	// 4. Commit log scan.
	if d.GitLog != nil {
		bodies, err := d.GitLog(5)
		if err == nil {
			for _, body := range bodies {
				for _, m := range antitrunc.MatchFalseCompletion(body) {
					findings = append(findings, antitrunc.Finding{
						Source:   "commit_body",
						PhraseID: m.PhraseID,
						Snippet:  m.Snippet,
						Detail:   m.Snippet,
					})
				}
			}
		}
	}

	return findings
}

// formatPlanFinding renders a ScopeReport as a human-readable detail
// string used in Workspace Notes and audit logs.
func formatPlanFinding(rep antitrunc.ScopeReport) string {
	var b strings.Builder
	b.WriteString(rep.Path)
	b.WriteString(": ")
	if rep.Status != "" {
		b.WriteString("STATUS=")
		b.WriteString(rep.Status)
		b.WriteString(" ")
	}
	// 2/5 unchecked
	unchecked := rep.Total - rep.Done
	if unchecked > 0 {
		b.WriteString(itoa(unchecked))
		b.WriteString("/")
		b.WriteString(itoa(rep.Total))
		b.WriteString(" unchecked")
	}
	return b.String()
}

// itoa is a tiny stdlib-free formatter so we don't need to pull in
// strconv just for one site. Mirrors strconv.Itoa for non-negative
// values; negatives stringify as "0".
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
