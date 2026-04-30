package skill

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
)

func TestCloudSwarmFlagshipsPackSeed(t *testing.T) {
	packRoot := filepath.Join("..", "..", ".stoke", "skills", "packs", "cloudswarm-flagships")

	loaded, err := skillmfr.LoadPack(packRoot)
	if err != nil {
		t.Fatalf("LoadPack(%q): %v", packRoot, err)
	}
	if loaded.Meta.Name != "cloudswarm-flagships" {
		t.Fatalf("pack name = %q, want cloudswarm-flagships", loaded.Meta.Name)
	}
	if len(loaded.Manifests) != 3 {
		t.Fatalf("manifest count = %d, want 3", len(loaded.Manifests))
	}

	manifests := map[string]skillmfr.Manifest{}
	names := make([]string, 0, len(loaded.Manifests))
	for _, manifest := range loaded.Manifests {
		manifests[manifest.Name] = manifest
		names = append(names, manifest.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"betbuddies_group_runtime", "dentist_outreach_runtime", "invoice_processor_runtime"}) {
		t.Fatalf("manifest names = %v, want betbuddies_group_runtime + dentist_outreach_runtime + invoice_processor_runtime", names)
	}

	betBuddies := manifests["betbuddies_group_runtime"]
	if !betBuddies.UseIR {
		t.Fatal("betbuddies_group_runtime should enable deterministic runtime via useIR")
	}
	wantBetBuddiesRecommended := []string{"betbuddies-group", "betting-pool", "settlement-approval", "flagship-flow"}
	if !reflect.DeepEqual(betBuddies.RecommendedFor, wantBetBuddiesRecommended) {
		t.Fatalf("betbuddies recommendedFor = %v, want %v", betBuddies.RecommendedFor, wantBetBuddiesRecommended)
	}

	invoice := manifests["invoice_processor_runtime"]
	if !invoice.UseIR {
		t.Fatal("invoice_processor_runtime should enable deterministic runtime via useIR")
	}
	wantInvoiceRecommended := []string{"invoice-processor", "invoice-ingestion", "email-reconciliation", "flagship-flow"}
	if !reflect.DeepEqual(invoice.RecommendedFor, wantInvoiceRecommended) {
		t.Fatalf("invoice recommendedFor = %v, want %v", invoice.RecommendedFor, wantInvoiceRecommended)
	}

	dentist := manifests["dentist_outreach_runtime"]
	if !dentist.UseIR {
		t.Fatal("dentist_outreach_runtime should enable deterministic runtime via useIR")
	}
	wantDentistRecommended := []string{"dentist-outreach", "sales-outreach", "lead-generation", "flagship-flow"}
	if !reflect.DeepEqual(dentist.RecommendedFor, wantDentistRecommended) {
		t.Fatalf("dentist recommendedFor = %v, want %v", dentist.RecommendedFor, wantDentistRecommended)
	}
}
