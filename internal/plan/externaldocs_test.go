package plan

import (
	"context"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/websearch"
)

func TestDetectKnownServiceByAlias(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{
				ID:    "S1",
				Title: "Guesty connection wizard",
				Tasks: []Task{{ID: "T5", Description: "Build Guesty credential form"}},
			},
		},
	}
	rawSOW := "The operator configures their Guesty integration via..."
	got := DetectExternalServices(sow, rawSOW)
	if len(got) == 0 {
		t.Fatal("guesty mention must be detected")
	}
	foundGuesty := false
	for _, s := range got {
		if s.Name == "guesty" {
			foundGuesty = true
			if len(s.MentionedInTaskIDs) == 0 || s.MentionedInTaskIDs[0] != "T5" {
				t.Fatalf("expected T5 in MentionedInTaskIDs; got %+v", s.MentionedInTaskIDs)
			}
		}
	}
	if !foundGuesty {
		t.Fatal("guesty not in detected services")
	}
}

func TestDetectUnknownServiceViaIntegrationHint(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{Tasks: []Task{{ID: "T1", Description: "Integrates with Shopwave API to sync orders"}}},
		},
	}
	got := DetectExternalServices(sow, "")
	foundShopwave := false
	for _, s := range got {
		if strings.EqualFold(s.Name, "shopwave") {
			foundShopwave = true
		}
	}
	if !foundShopwave {
		t.Fatalf("generic integration hint should catch 'Shopwave'; got %+v", got)
	}
}

func TestSOWProvidesDocumentationSufficient(t *testing.T) {
	sow := `## Guesty integration

Docs: https://docs.guesty.com/reference/

Endpoints:

` + "```" + `
GET /api/v1/listings
POST /api/v1/reservations
` + "```" + `

Required fields: ` + "`listing_id`, `check_in`, `check_out`"

	ok, ev := sowProvidesDocumentation(sow, "guesty")
	if !ok {
		t.Fatalf("sufficient guesty docs should pass: evidence=%q", ev)
	}
	if ev == "" {
		t.Fatal("evidence string should not be empty on pass")
	}
}

func TestSOWProvidesDocumentationInsufficient(t *testing.T) {
	sow := "The app integrates with Guesty. That's all the docs."
	if ok, _ := sowProvidesDocumentation(sow, "guesty"); ok {
		t.Fatal("one-liner mention must not count as docs")
	}
}

func TestCheckExternalDocsFallsBackToSearcher(t *testing.T) {
	srv := &fakeSearcherWithResults{results: []websearch.Result{{URL: "https://docs.example", Title: "Example API", Excerpt: "endpoints..."}}}
	services := []ExternalService{{Name: "guesty"}}
	rawSOW := "Connect to Guesty somehow."
	got := CheckExternalDocs(context.Background(), services, rawSOW, srv)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].SOWProvides {
		t.Fatal("one-liner SOW should NOT be marked as SOWProvides")
	}
	if !got[0].Covered {
		t.Fatal("searcher returned results so Covered should be true")
	}
	if len(got[0].WebResults) == 0 {
		t.Fatal("web results should be stored")
	}
}

func TestCheckExternalDocsUncoveredWhenNoSearcher(t *testing.T) {
	services := []ExternalService{{Name: "guesty"}}
	got := CheckExternalDocs(context.Background(), services, "integrates with guesty", nil)
	if got[0].Covered {
		t.Fatal("without docs and without searcher, service must be uncovered")
	}
}

func TestRefusalReasonListsEveryUncoveredService(t *testing.T) {
	uncovered := []ExternalServiceDocs{
		{Service: ExternalService{Name: "guesty", MentionedInTaskIDs: []string{"T5"}}},
		{Service: ExternalService{Name: "stripe", MentionedInTaskIDs: []string{"T10", "T11"}}},
	}
	msg := RefusalReason(uncovered)
	if !strings.Contains(msg, "guesty") || !strings.Contains(msg, "stripe") {
		t.Fatalf("refusal message must list each service: %s", msg)
	}
	if !strings.Contains(msg, "--force") || !strings.Contains(msg, "--docs-dir") {
		t.Fatalf("refusal message must surface operator options: %s", msg)
	}
}

// fakeSearcherWithResults is a tiny stub used by the externaldocs tests.
type fakeSearcherWithResults struct {
	results []websearch.Result
}

func (f *fakeSearcherWithResults) Name() string { return "fake" }
func (f *fakeSearcherWithResults) Search(ctx context.Context, q string, n int) ([]websearch.Result, error) {
	return f.results, nil
}
