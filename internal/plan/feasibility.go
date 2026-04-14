// Package plan — feasibility.go
//
// The feasibility gate runs after the SOW is converted + critiqued +
// refined but BEFORE the first task dispatches. It enforces the
// shippability contract:
//
//   1. Every referenced external service must have usable API
//      documentation — in the SOW or fetched via web search. No
//      mocks are synthesized by the harness under any circumstance.
//
//   2. Every task's acceptance-criteria command must be achievable
//      in the current runtime capability. iOS build commands on a
//      Linux runtime get rewritten to static-only scope or the gate
//      refuses.
//
// The gate is a hard stop. If infeasibilities remain after the
// configured number of refine iterations, stoke exits non-zero with
// an operator-facing report. The only override is the explicit
// --force flag, which preserves human authority while making the
// override visible.

package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/websearch"
)

// FeasibilityReport is the gate's output.
type FeasibilityReport struct {
	// AllShippable is true when every task is feasible and every
	// external service is covered by docs. When false, Refusals
	// explains why.
	AllShippable bool
	// DocCoverage is one entry per external service referenced by
	// the SOW.
	DocCoverage []ExternalServiceDocs
	// UncoveredServices names every service the gate could not
	// find documentation for. Populated when DocCoverage has
	// entries with Covered=false.
	UncoveredServices []ExternalServiceDocs
	// Refusals is the operator-facing set of refusal reasons,
	// each a human sentence. Empty when AllShippable is true.
	Refusals []string
	// Suggestions is the operator-facing set of next-step
	// suggestions. Parallel to Refusals.
	Suggestions []string
	// FetchedDocsForTaskBrief is a map service-name → combined
	// excerpt text that the dispatcher should inject into task
	// briefings for tasks that reference the service. Populated
	// from WebResults + SOWEvidence so workers see the actual
	// documentation content, not just the fact that it exists.
	FetchedDocsForTaskBrief map[string]string
}

// EvaluateFeasibility runs the full gate: external-service doc
// coverage check. Runtime-capability checks happen at the session
// level once dispatch starts (they're per-session, and the session
// scheduler already hosts smoketest integration); EvaluateFeasibility
// is focused on the up-front refusals — things we can know BEFORE
// writing a single line of code.
//
// When searcher is nil, uncovered services stay uncovered — no
// synthesis, no guessing. The gate refuses.
func EvaluateFeasibility(ctx context.Context, sow *SOW, rawSOW string, searcher websearch.Searcher) *FeasibilityReport {
	rep := &FeasibilityReport{
		FetchedDocsForTaskBrief: map[string]string{},
	}
	services := DetectExternalServices(sow, rawSOW)
	if len(services) == 0 {
		rep.AllShippable = true
		return rep
	}
	rep.DocCoverage = CheckExternalDocs(ctx, services, rawSOW, searcher)

	for _, doc := range rep.DocCoverage {
		if !doc.Covered {
			rep.UncoveredServices = append(rep.UncoveredServices, doc)
			continue
		}
		// Build the briefing-injection text for this service.
		var b strings.Builder
		if doc.BundledDoc != "" {
			fmt.Fprintf(&b, "Bundled API reference for %s (verified; shipped with stoke):\n\n%s\n", doc.Service.Name, doc.BundledDoc)
		}
		if doc.SOWProvides {
			fmt.Fprintf(&b, "Documentation for %s is provided inline in the SOW (evidence: %s).\n", doc.Service.Name, doc.SOWEvidence)
		}
		for _, r := range doc.WebResults {
			fmt.Fprintf(&b, "\n--- %s (%s) ---\n%s\n", r.Title, r.URL, firstNonEmpty(r.Body, r.Excerpt))
		}
		if txt := strings.TrimSpace(b.String()); txt != "" {
			rep.FetchedDocsForTaskBrief[doc.Service.Name] = txt
		}
	}

	if len(rep.UncoveredServices) > 0 {
		rep.Refusals = append(rep.Refusals, RefusalReason(rep.UncoveredServices))
		rep.Suggestions = append(rep.Suggestions,
			"Paste each service's API reference into the SOW, OR",
			"Set TAVILY_API_KEY or WEBSEARCH_COMMAND for auto-fetch, OR",
			"Provide docs on disk with --docs-dir <path>, OR",
			"Pass --force to proceed without docs (no mocks will be synthesized)")
		rep.AllShippable = false
		return rep
	}
	rep.AllShippable = true
	return rep
}

// FormatReport renders the report for the operator-facing banner
// printed at gate time. When AllShippable is true, returns a brief
// confirmation. When false, returns the full refusal message.
func (r *FeasibilityReport) FormatReport() string {
	if r == nil {
		return ""
	}
	if r.AllShippable {
		if len(r.DocCoverage) == 0 {
			return "  ✔ feasibility gate: no external services referenced; build is self-contained"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "  ✔ feasibility gate: %d external service(s), all covered by documentation\n", len(r.DocCoverage))
		for _, d := range r.DocCoverage {
			src := "SOW"
			switch {
			case d.BundledDoc != "":
				src = "bundled"
			case !d.SOWProvides && len(d.WebResults) > 0:
				src = "web"
			case !d.SOWProvides:
				src = "model training"
			}
			fmt.Fprintf(&b, "     - %s: docs from %s\n", d.Service.Name, src)
		}
		return b.String()
	}
	var b strings.Builder
	b.WriteString("\n  ⛔ FEASIBILITY GATE REFUSED — SOW is not shippable as written.\n\n")
	for _, r := range r.Refusals {
		b.WriteString(r)
		b.WriteString("\n")
	}
	if len(r.Suggestions) > 0 {
		b.WriteString("\nOperator next steps:\n")
		for _, s := range r.Suggestions {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
