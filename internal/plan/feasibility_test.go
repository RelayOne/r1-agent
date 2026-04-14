package plan

import (
	"context"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/websearch"
)

func TestFeasibilityShippableWhenNoServices(t *testing.T) {
	sow := &SOW{Sessions: []Session{{ID: "S1", Tasks: []Task{{ID: "T1", Description: "Build a local counter"}}}}}
	rep := EvaluateFeasibility(context.Background(), sow, "local only", nil)
	if !rep.AllShippable {
		t.Fatalf("self-contained SOW should be shippable; refusals=%v", rep.Refusals)
	}
	if len(rep.UncoveredServices) != 0 {
		t.Fatal("no services should be uncovered")
	}
}

func TestFeasibilityShippableWhenSOWCoversDocs(t *testing.T) {
	rawSOW := `## Guesty integration
Endpoint reference: https://docs.guesty.com/reference
` + "```" + `
GET /api/v1/listings
` + "```" + `
Fields: ` + "`listing_id`, `check_in`"
	sow := &SOW{
		Sessions: []Session{{Tasks: []Task{{ID: "T1", Description: "connect to Guesty"}}}},
	}
	rep := EvaluateFeasibility(context.Background(), sow, rawSOW, nil)
	if !rep.AllShippable {
		t.Fatalf("SOW with guesty docs should be shippable; refusals=%v", rep.Refusals)
	}
	if len(rep.FetchedDocsForTaskBrief) == 0 {
		t.Fatal("SOW-provided docs should still be surfaced for task briefings")
	}
}

func TestFeasibilityRefusesWhenSOWThinAndNoSearcher(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{{Tasks: []Task{{ID: "T1", Description: "integrates with Guesty"}}}},
	}
	rep := EvaluateFeasibility(context.Background(), sow, "connect to guesty", nil)
	if rep.AllShippable {
		t.Fatal("thin SOW + no searcher must refuse")
	}
	if len(rep.UncoveredServices) == 0 {
		t.Fatal("guesty should appear in UncoveredServices")
	}
	if len(rep.Refusals) == 0 {
		t.Fatal("refusal message required")
	}
}

func TestFeasibilityShippableWhenSearcherReturnsResults(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{{Tasks: []Task{{ID: "T1", Description: "integrates with Guesty API"}}}},
	}
	srv := &fakeSearcherWithResults{results: []websearch.Result{
		{URL: "https://docs.guesty.com/reference", Title: "Guesty API", Body: "POST /listings"},
	}}
	rep := EvaluateFeasibility(context.Background(), sow, "connect to guesty", srv)
	if !rep.AllShippable {
		t.Fatalf("searcher provided docs; should be shippable; refusals=%v", rep.Refusals)
	}
	brief, ok := rep.FetchedDocsForTaskBrief["guesty"]
	if !ok || !strings.Contains(brief, "POST /listings") {
		t.Fatalf("expected guesty docs in briefing map; got %q", brief)
	}
}

func TestFormatReportSurfacesRefusal(t *testing.T) {
	rep := &FeasibilityReport{
		AllShippable: false,
		UncoveredServices: []ExternalServiceDocs{
			{Service: ExternalService{Name: "guesty"}},
		},
		Refusals:    []string{"SOW references 1 external service(s) without usable documentation:\n  - guesty\n"},
		Suggestions: []string{"Paste the API reference into the SOW"},
	}
	out := rep.FormatReport()
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "guesty") {
		t.Fatalf("format should name the service + REFUSED: %s", out)
	}
	if !strings.Contains(out, "Paste the API reference") {
		t.Fatalf("format should include suggestions: %s", out)
	}
}

func TestFormatReportBriefOnShippable(t *testing.T) {
	rep := &FeasibilityReport{AllShippable: true}
	out := rep.FormatReport()
	if !strings.Contains(out, "self-contained") {
		t.Fatalf("empty-services shippable should say self-contained: %s", out)
	}
}
