package baseline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureAllPass(t *testing.T) {
	snap, err := Capture(context.Background(), t.TempDir(), Commands{
		Build: "true",  // always exits 0
		Test:  "true",
		Lint:  "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.AllPass {
		t.Error("all commands should pass")
	}
	if len(snap.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(snap.Results))
	}
	for _, r := range snap.Results {
		if !r.Pass {
			t.Errorf("%s should pass", r.Name)
		}
		if r.ExitCode != 0 {
			t.Errorf("%s exit code = %d", r.Name, r.ExitCode)
		}
		if r.Duration <= 0 {
			t.Errorf("%s duration should be positive", r.Name)
		}
	}
	if snap.ContentHash == "" {
		t.Error("content hash should be set")
	}
	if snap.CapturedAt.IsZero() {
		t.Error("captured_at should be set")
	}
}

func TestCaptureWithFailures(t *testing.T) {
	snap, err := Capture(context.Background(), t.TempDir(), Commands{
		Build: "true",
		Test:  "false",  // always exits 1
		Lint:  "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.AllPass {
		t.Error("should not all pass when test fails")
	}
	failures := snap.Failures()
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	if failures[0].Name != "test" {
		t.Errorf("expected test failure, got %s", failures[0].Name)
	}
	if failures[0].ExitCode == 0 {
		t.Error("exit code should be non-zero")
	}
}

func TestCaptureSkipsEmptyCommands(t *testing.T) {
	snap, err := Capture(context.Background(), t.TempDir(), Commands{
		Build: "true",
		// Test and Lint are empty — should be skipped
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Results) != 1 {
		t.Errorf("expected 1 result (skipping empty), got %d", len(snap.Results))
	}
	if snap.Results[0].Name != "build" {
		t.Errorf("expected build, got %s", snap.Results[0].Name)
	}
}

func TestCaptureEmptyRepoRoot(t *testing.T) {
	_, err := Capture(context.Background(), "", Commands{Build: "true"})
	if err == nil {
		t.Error("should reject empty repo root")
	}
}

func TestCaptureTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	snap, err := Capture(ctx, t.TempDir(), Commands{
		Build: "sleep 10", // will be killed by timeout
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.AllPass {
		t.Error("timed-out command should not pass")
	}
}

func TestCaptureRecordsOutput(t *testing.T) {
	snap, err := Capture(context.Background(), t.TempDir(), Commands{
		Build: "echo hello-world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Results[0].Output == "" {
		t.Error("output should be captured")
	}
}

func TestVerifyIsSameAsCapture(t *testing.T) {
	cmds := Commands{Build: "true", Test: "true"}
	snap, _ := Verify(context.Background(), t.TempDir(), cmds)
	if snap == nil {
		t.Fatal("Verify should return a snapshot")
	}
	if !snap.AllPass {
		t.Error("all commands should pass")
	}
}

func TestCompareAllSame(t *testing.T) {
	cmds := Commands{Build: "true", Test: "true"}
	before, _ := Capture(context.Background(), t.TempDir(), cmds)
	after, _ := Capture(context.Background(), t.TempDir(), cmds)

	diff := Compare(before, after)
	if diff.HasAnyFailure() {
		t.Error("no failures expected")
	}
	if len(diff.StillPassing) != 2 {
		t.Errorf("expected 2 still passing, got %d", len(diff.StillPassing))
	}
}

func TestComparePreExisting(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "false"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "false"})

	diff := Compare(before, after)
	if !diff.HasAnyFailure() {
		t.Error("should have failures")
	}
	if len(diff.PreExisting) != 1 {
		t.Errorf("expected 1 pre-existing, got %d", len(diff.PreExisting))
	}
	if diff.PreExisting[0].Name != "test" {
		t.Errorf("expected test pre-existing, got %s", diff.PreExisting[0].Name)
	}
	if diff.AllFixed() {
		t.Error("pre-existing failures remain — should not be all-fixed")
	}
}

func TestCompareIntroducedFailure(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "true"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "false"})

	diff := Compare(before, after)
	if len(diff.Introduced) != 1 {
		t.Errorf("expected 1 introduced, got %d", len(diff.Introduced))
	}
	if diff.Introduced[0].Name != "test" {
		t.Errorf("expected test introduced, got %s", diff.Introduced[0].Name)
	}
}

func TestCompareFixed(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "false", Test: "true"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "true"})

	diff := Compare(before, after)
	if len(diff.Fixed) != 1 {
		t.Errorf("expected 1 fixed, got %d", len(diff.Fixed))
	}
	if diff.Fixed[0].Name != "build" {
		t.Errorf("expected build fixed, got %s", diff.Fixed[0].Name)
	}
	if !diff.AllFixed() {
		t.Error("all pre-existing failures were fixed")
	}
}

func TestPreExistingFailures(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "false", Test: "false"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "false", Test: "true"})

	pe := PreExistingFailures(before, after)
	if len(pe) != 1 {
		t.Errorf("expected 1 pre-existing, got %d", len(pe))
	}
	if pe[0].Name != "build" {
		t.Errorf("expected build, got %s", pe[0].Name)
	}
}

func TestNewFailures(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "true", Lint: "true"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "false", Lint: "true"})

	nf := NewFailures(before, after)
	if len(nf) != 1 {
		t.Errorf("expected 1 new, got %d", len(nf))
	}
}

func TestSaveAndLoad(t *testing.T) {
	snap, _ := Capture(context.Background(), t.TempDir(), Commands{Build: "true", Test: "true"})

	path := filepath.Join(t.TempDir(), "baselines", "m-1.json")
	if err := snap.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Results) != len(snap.Results) {
		t.Errorf("results mismatch: %d vs %d", len(loaded.Results), len(snap.Results))
	}
	if loaded.ContentHash != snap.ContentHash {
		t.Error("content hash mismatch")
	}
	if loaded.AllPass != snap.AllPass {
		t.Error("all_pass mismatch")
	}
}

func TestLoadNonExistent(t *testing.T) {
	_, err := Load("/nonexistent/path.json")
	if err == nil {
		t.Error("should fail on missing file")
	}
}

func TestFailureSummary(t *testing.T) {
	snap := &Snapshot{
		Results: []CommandResult{
			{Name: "build", Pass: true},
			{Name: "test", Pass: false, Command: "go test ./...", ExitCode: 1, Output: "FAIL main_test.go"},
		},
	}
	summary := snap.FailureSummary()
	if summary == "" || summary == "all checks pass" {
		t.Error("should have failure summary")
	}
}

func TestFailureSummaryAllPass(t *testing.T) {
	snap := &Snapshot{
		Results: []CommandResult{
			{Name: "build", Pass: true},
		},
	}
	if snap.FailureSummary() != "all checks pass" {
		t.Error("should say all pass")
	}
}

func TestAutoDetectGo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o600)

	cmds := AutoDetect(dir)
	if cmds.Build != "go build ./..." {
		t.Errorf("Build = %q", cmds.Build)
	}
	if cmds.Test != "go test ./..." {
		t.Errorf("Test = %q", cmds.Test)
	}
	if cmds.Lint != "go vet ./..." {
		t.Errorf("Lint = %q", cmds.Lint)
	}
}

func TestAutoDetectEmpty(t *testing.T) {
	cmds := AutoDetect(t.TempDir())
	if cmds.Build != "" || cmds.Test != "" || cmds.Lint != "" {
		t.Error("should detect nothing in empty dir")
	}
}

func TestDiffSummary(t *testing.T) {
	dir := t.TempDir()
	before, _ := Capture(context.Background(), dir, Commands{Build: "false", Test: "true"})
	after, _ := Capture(context.Background(), dir, Commands{Build: "true", Test: "false"})

	diff := Compare(before, after)
	summary := diff.Summary()
	if summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestDiffSummaryNoCommands(t *testing.T) {
	diff := Compare(&Snapshot{}, &Snapshot{})
	if diff.Summary() != "no verification commands configured" {
		t.Errorf("unexpected summary: %s", diff.Summary())
	}
}

func TestSnapshotJSON(t *testing.T) {
	snap := &Snapshot{
		CapturedAt:  time.Now(),
		RepoRoot:    "/repo",
		Commands:    Commands{Build: "make", Test: "make test"},
		Results:     []CommandResult{{Name: "build", Pass: true, ExitCode: 0}},
		AllPass:     true,
		ContentHash: "abc123",
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RepoRoot != "/repo" {
		t.Error("RepoRoot mismatch")
	}
	if decoded.ContentHash != "abc123" {
		t.Error("ContentHash mismatch")
	}
}

func TestContentHashDeterministic(t *testing.T) {
	results := []CommandResult{
		{Name: "build", ExitCode: 0, Output: "ok"},
		{Name: "test", ExitCode: 1, Output: "FAIL"},
	}
	h1 := hashResults(results)
	h2 := hashResults(results)
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
}

func TestContentHashDiffers(t *testing.T) {
	r1 := []CommandResult{{Name: "build", ExitCode: 0, Output: "ok"}}
	r2 := []CommandResult{{Name: "build", ExitCode: 1, Output: "FAIL"}}
	if hashResults(r1) == hashResults(r2) {
		t.Error("different results should have different hashes")
	}
}
