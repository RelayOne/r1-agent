// Package antitrunclobe provides the cortex-side anti-truncation
// Lobe.
//
// STATUS: BLOCKED on cortex-core (spec build order < 9). The cortex
// package (internal/cortex/) has not yet been merged into this
// worktree. Until it is, this package ships only the
// cortex-independent core (Detector) so the agentloop wiring + the
// supervisor rules can call it directly. When cortex-core lands, a
// thin Lobe wrapper around Detector will satisfy the cortex.Lobe
// interface (KindDeterministic) and publish SevCritical Workspace
// Notes for each Detector finding. The wrapper is intentionally
// trivial — Detector does the actual work.
//
// Why ship the Detector now even though the Lobe is BLOCKED:
//
//  1. The supervisor rules (internal/supervisor/rules/antitrunc/)
//     need a deterministic detection helper that doesn't reach into
//     cortex.
//  2. The agentloop wiring (internal/agentloop/antitrunc.go) needs
//     the same helper at the gate composition site.
//  3. When cortex finally lands, the only delta needed here is a
//     ~30-line Lobe constructor that calls Detector and publishes
//     Notes — the heavy lifting is already tested.
//
// Interfaces this Lobe will eventually implement (per cortex-concerns
// spec §"AntiTruncLobe"):
//
//	type Lobe interface {
//	    Name() string
//	    Kind() LobeKind        // KindDeterministic
//	    Run(ctx, in LobeInput) error
//	}
//
// When cortex lands the Lobe constructor signature will be:
//
//	func NewAntiTruncLobe(ws *cortex.Workspace, planPath string, specGlob string) cortex.Lobe
//
// The constructor is documented here so the operator can grep for it
// once the cortex import becomes valid.
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
