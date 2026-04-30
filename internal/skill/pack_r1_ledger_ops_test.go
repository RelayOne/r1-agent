package skill

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/RelayOne/r1/internal/skillmfr"
)

func TestR1LedgerOpsPackSeed(t *testing.T) {
	packRoot := filepath.Join("..", "..", ".stoke", "skills", "packs", "r1-ledger-ops")

	loaded, err := skillmfr.LoadPack(packRoot)
	if err != nil {
		t.Fatalf("LoadPack(%q): %v", packRoot, err)
	}
	if loaded.Meta.Name != "r1-ledger-ops" {
		t.Fatalf("pack name = %q, want r1-ledger-ops", loaded.Meta.Name)
	}
	if len(loaded.Manifests) != 2 {
		t.Fatalf("manifest count = %d, want 2", len(loaded.Manifests))
	}

	manifests := map[string]skillmfr.Manifest{}
	names := make([]string, 0, len(loaded.Manifests))
	for _, manifest := range loaded.Manifests {
		manifests[manifest.Name] = manifest
		names = append(names, manifest.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"ledger_audit_query_runtime", "metrics_collection_runtime"}) {
		t.Fatalf("manifest names = %v, want ledger_audit_query_runtime + metrics_collection_runtime", names)
	}

	audit := manifests["ledger_audit_query_runtime"]
	if !audit.UseIR {
		t.Fatal("ledger_audit_query_runtime should enable deterministic runtime via useIR")
	}
	wantRecommended := []string{"ledger-audit", "audit-query", "receipts", "honesty", "governance"}
	if !reflect.DeepEqual(audit.RecommendedFor, wantRecommended) {
		t.Fatalf("recommendedFor = %v, want %v", audit.RecommendedFor, wantRecommended)
	}

	metricsRuntime := manifests["metrics_collection_runtime"]
	if !metricsRuntime.UseIR {
		t.Fatal("metrics_collection_runtime should enable deterministic runtime via useIR")
	}
	wantMetricsRecommended := []string{"metrics", "telemetry", "runtime-ops", "costs", "latency"}
	if !reflect.DeepEqual(metricsRuntime.RecommendedFor, wantMetricsRecommended) {
		t.Fatalf("recommendedFor = %v, want %v", metricsRuntime.RecommendedFor, wantMetricsRecommended)
	}
}
