package engine

import "testing"

// TestAddCtx_S33_DualEmitsStokeAndR1Build verifies §S3-3 of
// work-r1-rename.md: when a WorkerLogContext carries a non-empty
// StokeBuild, the resulting JSONL entry stamps BOTH `stoke_build`
// (legacy) AND `r1_build` (canonical) with the identical value. This
// lets CloudSwarm / r1-server consume either key during the 30-day
// rename window.
func TestAddCtx_S33_DualEmitsStokeAndR1Build(t *testing.T) {
	const want = "abcdef1"
	entry := map[string]any{}
	addCtx(entry, &WorkerLogContext{
		RunID:      "run-s33",
		StokeBuild: want,
	})

	for _, k := range []string{"stoke_build", "r1_build"} {
		got, ok := entry[k]
		if !ok {
			t.Errorf("key %q missing from worker-log entry: %+v", k, entry)
			continue
		}
		if s, _ := got.(string); s != want {
			t.Errorf("key %q = %q, want %q", k, s, want)
		}
	}
}

// TestAddCtx_S33_BothKeysAbsentWhenBuildEmpty confirms the dual-emit
// is gated behind a non-empty StokeBuild — neither key should appear
// when the context carries no build info (preserves the "compact
// JSONL" contract).
func TestAddCtx_S33_BothKeysAbsentWhenBuildEmpty(t *testing.T) {
	entry := map[string]any{}
	addCtx(entry, &WorkerLogContext{RunID: "run-empty"})
	for _, k := range []string{"stoke_build", "r1_build"} {
		if _, present := entry[k]; present {
			t.Errorf("key %q must be absent when StokeBuild is empty: %+v", k, entry)
		}
	}
}
