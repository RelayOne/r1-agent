package skill

import (
	"path/filepath"
	"reflect"
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
	if len(loaded.Manifests) != 1 {
		t.Fatalf("manifest count = %d, want 1", len(loaded.Manifests))
	}

	manifest := loaded.Manifests[0]
	if manifest.Name != "invoice_processor_runtime" {
		t.Fatalf("manifest name = %q, want invoice_processor_runtime", manifest.Name)
	}
	if !manifest.UseIR {
		t.Fatal("invoice_processor_runtime should enable deterministic runtime via useIR")
	}
	wantRecommended := []string{"invoice-processor", "invoice-ingestion", "email-reconciliation", "flagship-flow"}
	if !reflect.DeepEqual(manifest.RecommendedFor, wantRecommended) {
		t.Fatalf("recommendedFor = %v, want %v", manifest.RecommendedFor, wantRecommended)
	}
}
