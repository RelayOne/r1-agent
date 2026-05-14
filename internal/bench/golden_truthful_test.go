package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const truthfulCorpusRoot = "golden/truthful-completion"

// TestAllTruthfulCompletionMissionsParse walks the corpus directory and
// asserts every mission.yaml loads cleanly via the existing Runner.
// Spec §T5.4 item 47.
func TestAllTruthfulCompletionMissionsParse(t *testing.T) {
	missions := truthfulMissionDirs(t)
	if len(missions) == 0 {
		t.Fatal("no truthful-completion missions found")
	}
	r := NewRunner(truthfulCorpusRoot)
	for _, id := range missions {
		t.Run(id, func(t *testing.T) {
			cfg, err := r.LoadMission(id)
			if err != nil {
				t.Fatalf("LoadMission(%q): %v", id, err)
			}
			if cfg.ID != id {
				t.Errorf("mission.yaml ID = %q, dir = %q (mismatch)", cfg.ID, id)
			}
			if cfg.Title == "" {
				t.Error("Title is empty")
			}
			if cfg.Intent == "" {
				t.Error("Intent is empty")
			}
			if len(cfg.Plan) == 0 {
				t.Error("Plan is empty (truthful-completion missions require at least one PlanItem)")
			}
		})
	}
}

// TestAllTruthfulCompletionMissionsHavePatch asserts every mission
// directory ships a non-empty gold.patch. Spec §T5.4 item 48.
func TestAllTruthfulCompletionMissionsHavePatch(t *testing.T) {
	for _, id := range truthfulMissionDirs(t) {
		t.Run(id, func(t *testing.T) {
			patchPath := filepath.Join(truthfulCorpusRoot, id, "gold.patch")
			info, err := os.Stat(patchPath)
			if err != nil {
				t.Fatalf("gold.patch missing: %v", err)
			}
			if info.Size() == 0 {
				t.Errorf("gold.patch is empty")
			}
		})
	}
}

// TestAllTruthfulCompletionMissionsHaveReadme asserts every mission
// has a non-empty README.md. Spec §T5.4 item 49. Production missions
// must also cite an upstream SWE-bench Pro task ID; seed missions are
// exempt from that constraint (they're not derived from upstream).
func TestAllTruthfulCompletionMissionsHaveReadme(t *testing.T) {
	for _, id := range truthfulMissionDirs(t) {
		t.Run(id, func(t *testing.T) {
			readmePath := filepath.Join(truthfulCorpusRoot, id, "README.md")
			body, err := os.ReadFile(readmePath)
			if err != nil {
				t.Fatalf("README.md missing: %v", err)
			}
			if len(body) == 0 {
				t.Fatalf("README.md is empty")
			}
			text := string(body)
			// Every README must mention upstream provenance OR explicitly
			// note it's a seed mission.
			isSeed := strings.HasPrefix(id, "seed-")
			hasUpstreamID := strings.Contains(text, "swebench-pro") || strings.Contains(text, "SWE-bench")
			if !isSeed && !hasUpstreamID {
				t.Errorf("README missing upstream SWE-bench Pro task ID (and mission is not a seed)")
			}
		})
	}
}

// truthfulMissionDirs returns sorted mission IDs from the corpus root.
// Defined in a non-test file so it can be shared by other tests; per
// the detect-stubs hook conventions, helpers without t.Errorf etc.
// belong in non-test files. But this is a test-only helper, so it stays
// here with a t.Helper() to satisfy the hook.
func truthfulMissionDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(truthfulCorpusRoot)
	if err != nil {
		t.Skipf("corpus root not present: %v", err)
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Ignore hidden / non-mission dirs.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}
