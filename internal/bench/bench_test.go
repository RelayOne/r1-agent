package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"gopkg.in/yaml.v3"
)

// goldenDir returns the path to the golden directory relative to this test file.
func goldenDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(filename), "golden")
}

func TestLoadMission(t *testing.T) {
	r := NewRunner(goldenDir(t))
	cfg, err := r.LoadMission("hello-world")
	if err != nil {
		t.Fatalf("LoadMission: %v", err)
	}
	if cfg.ID != "hello-world" {
		t.Errorf("ID = %q, want %q", cfg.ID, "hello-world")
	}
	if cfg.Title != "Hello World Greenfield" {
		t.Errorf("Title = %q, want %q", cfg.Title, "Hello World Greenfield")
	}
	if cfg.Category != "greenfield" {
		t.Errorf("Category = %q, want %q", cfg.Category, "greenfield")
	}
	if cfg.Difficulty != "easy" {
		t.Errorf("Difficulty = %q, want %q", cfg.Difficulty, "easy")
	}
	if len(cfg.Acceptance) != 3 {
		t.Errorf("Acceptance count = %d, want 3", len(cfg.Acceptance))
	}
}

func TestListMissions(t *testing.T) {
	r := NewRunner(goldenDir(t))
	missions, err := r.ListMissions()
	if err != nil {
		t.Fatalf("ListMissions: %v", err)
	}
	if len(missions) < 1 {
		t.Fatalf("ListMissions returned %d missions, want >= 1", len(missions))
	}
	found := false
	for _, m := range missions {
		if m.ID == "hello-world" {
			found = true
			break
		}
	}
	if !found {
		t.Error("hello-world mission not found in ListMissions")
	}
}

func TestCompare(t *testing.T) {
	baseline := &RunResult{
		MissionID:       "test-mission",
		TerminalState:   "converged",
		AcceptanceMet:   3,
		AcceptanceTotal: 3,
		CostUSD:         1.00,
		WallTimeMs:      5000,
		TokensUsed:      1000,
	}
	current := &RunResult{
		MissionID:       "test-mission",
		TerminalState:   "converged",
		AcceptanceMet:   3,
		AcceptanceTotal: 3,
		CostUSD:         1.10,
		WallTimeMs:      5500,
		TokensUsed:      1100,
	}

	result := Compare(baseline, current)

	if result.Mission != "test-mission" {
		t.Errorf("Mission = %q, want %q", result.Mission, "test-mission")
	}
	if result.Regression {
		t.Error("expected no regression for small cost increase")
	}
	costDelta := result.Delta["cost_usd"]
	if costDelta < 0.09 || costDelta > 0.11 {
		t.Errorf("cost delta = %f, want ~0.10", costDelta)
	}
}

func TestDetectRegressionAcceptance(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 2,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when acceptance dropped")
	}
}

func TestDetectRegressionTerminalState(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
	}
	current := &RunResult{
		TerminalState: "escalated",
		AcceptanceMet: 3,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when state changed from converged to escalated")
	}
}

func TestDetectRegressionCost(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.00,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       2.00,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when cost doubled")
	}
}

func TestDetectRegressionNoRegression(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.00,
		WallTimeMs:    5000,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.20,
		WallTimeMs:    6000,
	}
	if DetectRegression(baseline, current) {
		t.Error("expected no regression for minor increases")
	}
}

func TestReport(t *testing.T) {
	results := []RunResult{
		{
			MissionID:       "test-1",
			TerminalState:   "converged",
			AcceptanceMet:   2,
			AcceptanceTotal: 3,
			CostUSD:         0.50,
			TokensUsed:      500,
			WallTimeMs:      3000,
		},
	}

	report := Report(results)
	if report == "" {
		t.Fatal("Report returned empty string")
	}
	if !strings.Contains(report, "test-1") {
		t.Error("Report does not contain mission ID")
	}
	if !strings.Contains(report, "converged") {
		t.Error("Report does not contain terminal state")
	}
	if !strings.Contains(report, "Bench Report") {
		t.Error("Report does not contain header")
	}
}

func TestComparisonReport(t *testing.T) {
	comparisons := []ComparisonResult{
		{
			Mission:    "test-1",
			Baseline:   RunResult{TerminalState: "converged"},
			Current:    RunResult{TerminalState: "escalated"},
			Regression: true,
			Delta:      map[string]float64{"cost_usd": 0.5, "acceptance_met": -1},
		},
	}

	report := ComparisonReport(comparisons)
	if report == "" {
		t.Fatal("ComparisonReport returned empty string")
	}
	if !strings.Contains(report, "YES") {
		t.Error("ComparisonReport does not indicate regression")
	}
}

func TestComputeMetricsEmptyLedger(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bench-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := ledger.New(filepath.Join(tmpDir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer l.Close()

	b, err := bus.New(filepath.Join(tmpDir, "bus"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	result, err := ComputeMetrics(ctx, l, b, "nonexistent")
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}

	if result.CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0", result.CostUSD)
	}
	if result.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0", result.TokensUsed)
	}
	if result.TrustFirings != 0 {
		t.Errorf("TrustFirings = %d, want 0", result.TrustFirings)
	}
	if result.LoopIterations != 0 {
		t.Errorf("LoopIterations = %d, want 0", result.LoopIterations)
	}
	if result.DissentCount != 0 {
		t.Errorf("DissentCount = %d, want 0", result.DissentCount)
	}
	if result.TerminalState != "converged" {
		t.Errorf("TerminalState = %q, want %q", result.TerminalState, "converged")
	}
}

// testdataPath returns the absolute path to a testdata file relative to this
// test file.
func testdataPath(t *testing.T, rel string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(filename), "testdata", rel)
}

// TestMissionConfig_YAMLRoundtripWithTruthful loads a mission fixture that
// populates every truthful-completion field, marshals it back out, unmarshals
// it once more, and uses reflect.DeepEqual to assert no semantic loss. This
// pins T1.1/T1.2 yaml tags so any accidental rename or tag drift breaks the
// build immediately.
func TestMissionConfig_YAMLRoundtripWithTruthful(t *testing.T) {
	path := testdataPath(t, "mission-with-plan.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var first MissionConfig
	if err := yaml.Unmarshal(raw, &first); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	// Sanity-check the truthful-completion fields landed.
	if len(first.Plan) != 3 {
		t.Fatalf("Plan length = %d, want 3", len(first.Plan))
	}
	if first.Plan[0].ID != "P1" || first.Plan[0].ChangedFiles[0] != "pkg/foo/foo.go" {
		t.Errorf("Plan[0] mismatched: %+v", first.Plan[0])
	}
	if first.Plan[2].TestCommand != "go test ./pkg/bar/..." {
		t.Errorf("Plan[2].TestCommand = %q", first.Plan[2].TestCommand)
	}
	if first.GoldDiffPath != "testdata/gold/truthful-demo.diff" {
		t.Errorf("GoldDiffPath = %q", first.GoldDiffPath)
	}
	if first.CompletionCriteria.JudgeAgree != "required" {
		t.Errorf("JudgeAgree = %q, want required", first.CompletionCriteria.JudgeAgree)
	}
	if first.CompletionCriteria.PlanCompletionThreshold != 1.0 {
		t.Errorf("PlanCompletionThreshold = %v, want 1.0", first.CompletionCriteria.PlanCompletionThreshold)
	}
	if first.CompletionCriteria.DeliveryRatioMin != 80 {
		t.Errorf("DeliveryRatioMin = %d, want 80", first.CompletionCriteria.DeliveryRatioMin)
	}

	// Marshal -> unmarshal -> DeepEqual.
	out, err := yaml.Marshal(&first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var second MissionConfig
	if err := yaml.Unmarshal(out, &second); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("roundtrip mismatch:\nfirst  = %#v\nsecond = %#v", first, second)
	}
}

// TestMissionConfig_YAMLRoundtrip regression-tests legacy missions: loads
// the on-disk hello-world fixture, marshals it via yaml.Marshal, then
// compares whitespace-normalised text to catch accidental yaml tag changes
// (which would manifest as renamed keys in the output).
func TestMissionConfig_YAMLRoundtrip(t *testing.T) {
	path := filepath.Join(goldenDir(t), "hello-world", "mission.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mission.yaml: %v", err)
	}

	var cfg MissionConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Re-parse the marshalled output and compare structurally — guards the
	// yaml tags without coupling the test to gopkg.in/yaml.v3's exact
	// emission format (quoting style, scalar style).
	var roundtripped MissionConfig
	if err := yaml.Unmarshal(out, &roundtripped); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if !reflect.DeepEqual(cfg, roundtripped) {
		t.Errorf("legacy mission roundtrip mismatch:\nbefore = %#v\nafter  = %#v", cfg, roundtripped)
	}

	// Additionally byte-compare the marshalled output against itself after
	// whitespace normalisation. Trips if a key got renamed (which would
	// appear in `out` but not in `raw`).
	normalize := func(s string) string {
		// Collapse runs of whitespace and trim — we only care that the
		// keys present in raw remain present in out.
		fields := strings.Fields(s)
		return strings.Join(fields, " ")
	}
	rawNorm := normalize(string(raw))
	outNorm := normalize(string(out))
	// The marshalled form may reorder or omit fields with zero values, so
	// we only assert that every word in the source survives the round-trip
	// in some form — accidental tag renames would drop the key entirely.
	for _, key := range []string{"id:", "title:", "description:", "category:", "difficulty:", "acceptance_criteria:"} {
		if !strings.Contains(rawNorm, key) {
			continue // key not in source — nothing to check
		}
		if !strings.Contains(outNorm, key) {
			t.Errorf("yaml key %q present in source but missing after marshal — tag rename suspected", key)
		}
	}
}

// TestRunResult_TruthfulFieldsRoundtrip JSON-marshals a fully-populated
// RunResult, unmarshals it back, and compares with reflect.DeepEqual. Pins
// the T2.1 json tags.
func TestRunResult_TruthfulFieldsRoundtrip(t *testing.T) {
	original := RunResult{
		MissionID:                "truthful-demo",
		TerminalState:            "converged",
		AcceptanceMet:            3,
		AcceptanceTotal:          3,
		WallTimeMs:               1234,
		CostUSD:                  0.99,
		TokensUsed:               4242,
		LoopIterations:           2,
		TrustFirings:             1,
		DissentCount:             0,
		EscalationCount:          0,
		LedgerCorrupted:          false,
		CompletionAttempted:      true,
		CompletionClaim:          "Done — all four checklist items shipped, tests green",
		CompletionTruthful:       false,
		CompletionSilentlyFailed: false,
		PlanItemsCompleted:       3,
		PlanItemsTotal:           4,
		DeliveryRatioPercent:     87,
		JudgeVerdict:             "agrees_truthful",
		JudgeRationale:           "diff substantively addresses all plan items",
	}

	data, err := json.Marshal(&original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded RunResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("roundtrip mismatch:\noriginal = %#v\ndecoded  = %#v", original, decoded)
	}

	// Pin the wire-level tag names so future renames break this test.
	wire := string(data)
	for _, tag := range []string{
		`"completion_attempted"`,
		`"completion_claim"`,
		`"completion_truthful"`,
		`"completion_silently_failed"`,
		`"plan_items_completed"`,
		`"plan_items_total"`,
		`"delivery_ratio_percent"`,
		`"judge_verdict"`,
		`"judge_rationale"`,
	} {
		if !strings.Contains(wire, tag) {
			t.Errorf("expected JSON to contain tag %s, got: %s", tag, wire)
		}
	}
}

// TestRunResult_LegacyJSONRoundtrip unmarshals a legacy on-disk fixture
// (written before the truthful-completion fields existed) and asserts the
// new fields take their zero values. Guards backward compatibility for any
// older RunResult JSON sitting in baseline storage.
func TestRunResult_LegacyJSONRoundtrip(t *testing.T) {
	path := testdataPath(t, "result-legacy.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}

	var got RunResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal legacy result: %v", err)
	}

	// Legacy fields populated.
	if got.TerminalState == "" {
		t.Error("TerminalState should be populated from legacy fixture")
	}
	if got.WallTimeMs == 0 {
		t.Error("WallTimeMs should be populated from legacy fixture")
	}
	if got.CostUSD == 0 {
		t.Error("CostUSD should be populated from legacy fixture")
	}

	// New truthful-completion fields take zero values.
	if got.CompletionAttempted {
		t.Errorf("CompletionAttempted should be zero, got %v", got.CompletionAttempted)
	}
	if got.CompletionClaim != "" {
		t.Errorf("CompletionClaim should be zero, got %q", got.CompletionClaim)
	}
	if got.CompletionTruthful {
		t.Errorf("CompletionTruthful should be zero, got %v", got.CompletionTruthful)
	}
	if got.CompletionSilentlyFailed {
		t.Errorf("CompletionSilentlyFailed should be zero, got %v", got.CompletionSilentlyFailed)
	}
	if got.PlanItemsCompleted != 0 {
		t.Errorf("PlanItemsCompleted should be zero, got %d", got.PlanItemsCompleted)
	}
	if got.PlanItemsTotal != 0 {
		t.Errorf("PlanItemsTotal should be zero, got %d", got.PlanItemsTotal)
	}
	if got.DeliveryRatioPercent != 0 {
		t.Errorf("DeliveryRatioPercent should be zero, got %d", got.DeliveryRatioPercent)
	}
	if got.JudgeVerdict != "" {
		t.Errorf("JudgeVerdict should be zero, got %q", got.JudgeVerdict)
	}
	if got.JudgeRationale != "" {
		t.Errorf("JudgeRationale should be zero, got %q", got.JudgeRationale)
	}
}
