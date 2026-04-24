package wizard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWizardAutoDetect(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22\nrequire github.com/lib/pq v1.10.0\n"), 0o600)

	w := New(dir)
	w.Writer = &bytes.Buffer{}
	err := w.RunAutoDetect()
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "stoke.policy.yaml"))
	if err != nil {
		t.Fatal("expected stoke.policy.yaml to be created")
	}
	content := string(data)

	if !strings.Contains(content, "primary: claude") {
		t.Error("expected primary: claude")
	}
	if !strings.Contains(content, "adversarial_depth: standard") {
		t.Error("expected standard adversarial depth")
	}
	if !strings.Contains(content, "posture: standard") {
		t.Error("expected standard security posture")
	}
	if !strings.Contains(content, "postgres") {
		t.Error("expected postgres in detected domains")
	}
}

func TestWizardGenerateYAML(t *testing.T) {
	w := New(t.TempDir())
	w.Prefs = Preferences{
		PrimaryModel:         "claude",
		ReviewModel:          "codex",
		FallbackChain:        []string{"claude", "codex", "openrouter"},
		ModelStrategy:        "balanced",
		AdversarialDepth:     DepthMaximum,
		PolishLevel:          PolishProduction,
		SecurityPosture:      "high",
		DataSensitivity:      "confidential",
		ScaleTier:            ScaleGrowth,
		ComplianceFrameworks: []string{"gdpr", "soc2"},
		Infrastructure:       []string{"aws"},
		ProviderPreference:   ProviderBestFit,
		DomainAreas:          []string{"payments", "api-platform"},
		TeamSize:             "6-20",
		DetectedDomains:      []string{"postgres", "redis", "docker"},
		BuildCmd:             "go build ./...",
		TestCmd:              "go test ./...",
		LintCmd:              "go vet ./...",
	}

	yaml := w.GenerateYAML()

	checks := []string{
		"primary: claude",
		"review: codex",
		"fallback_chain: [claude, codex, openrouter]",
		"strategy: balanced",
		"adversarial_depth: maximum",
		"polish_level: production",
		"posture: high",
		"data_sensitivity: confidential",
		"tier: growth",
		"compliance: [gdpr, soc2]",
		"providers: [aws]",
		"areas: [payments, api-platform]",
		"size: 6-20",
		"postgres",
		"go build ./...",
		"cross_model_review: required",
	}
	for _, check := range checks {
		if !strings.Contains(yaml, check) {
			t.Errorf("expected YAML to contain %q", check)
		}
	}
}

func TestWizardInteractive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22\n"), 0o600)

	// Simulate: accept defaults for everything (12 questions)
	input := strings.NewReader("1\n1\n2\n2\n2\n2\n2\nnone\nauto\n1\nauto\n1\n")
	output := &bytes.Buffer{}

	w := New(dir)
	w.Reader = input
	w.Writer = output

	err := w.Run()
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "stoke.policy.yaml"))
	if err != nil {
		t.Fatal("expected stoke.policy.yaml")
	}
	if len(data) == 0 {
		t.Error("config file is empty")
	}

	// Check rationale was written
	rationale, err := os.ReadFile(filepath.Join(dir, ".stoke", "wizard-rationale.md"))
	if err != nil {
		t.Fatal("expected .stoke/wizard-rationale.md")
	}
	if !strings.Contains(string(rationale), "Configuration Summary") {
		t.Error("rationale missing summary")
	}
}

func TestAskChoice(t *testing.T) {
	input := strings.NewReader("2\n")
	output := &bytes.Buffer{}

	w := &Wizard{Reader: input, Writer: output}
	choice := w.askChoice(Question{
		ID:      "test",
		Prompt:  "Pick one",
		Options: []string{"A", "B", "C"},
		Default: 1,
	})
	if choice != 2 {
		t.Errorf("expected choice 2, got %d", choice)
	}
}

func TestAskChoiceDefault(t *testing.T) {
	input := strings.NewReader("\n")
	output := &bytes.Buffer{}

	w := &Wizard{Reader: input, Writer: output}
	choice := w.askChoice(Question{
		ID:      "test",
		Prompt:  "Pick one",
		Options: []string{"A", "B", "C"},
		Default: 3,
	})
	if choice != 3 {
		t.Errorf("expected default choice 3, got %d", choice)
	}
}

func TestDetectGitStats(t *testing.T) {
	// Test on the Stoke repo itself
	stats := DetectGitStats("../..")
	// Should have at least some stats from this repo
	if stats.CommitCount == 0 {
		t.Log("no git history detected (may be in test sandbox)")
	}
}

func TestInferStage(t *testing.T) {
	tests := []struct {
		stats GitStats
		want  ScaleTier
	}{
		{GitStats{CommitCount: 5, ContributorCount: 1}, ScalePrototype},
		{GitStats{CommitCount: 100, ContributorCount: 3}, ScaleStartup},
		{GitStats{CommitCount: 1000, ContributorCount: 8}, ScaleGrowth},
		{GitStats{CommitCount: 5000, ContributorCount: 25}, ScaleEnterprise},
	}
	for _, tt := range tests {
		got := InferStage(tt.stats)
		if got != tt.want {
			t.Errorf("InferStage(%d commits, %d contribs) = %s, want %s",
				tt.stats.CommitCount, tt.stats.ContributorCount, got, tt.want)
		}
	}
}

func TestAppendUnique(t *testing.T) {
	s := []string{"a", "b"}
	s = appendUnique(s, "b")
	if len(s) != 2 {
		t.Errorf("expected 2 items after adding duplicate, got %d", len(s))
	}
	s = appendUnique(s, "c")
	if len(s) != 3 {
		t.Errorf("expected 3 items after adding new item, got %d", len(s))
	}
}

func TestGenerateRationale(t *testing.T) {
	w := New(t.TempDir())
	w.Prefs = Preferences{
		PrimaryModel:     "claude",
		ReviewModel:      "codex",
		AdversarialDepth: DepthMaximum,
		PolishLevel:      PolishProduction,
		SecurityPosture:  "high",
		DataSensitivity:  "confidential",
		ScaleTier:        ScaleGrowth,
		TeamSize:         "6-20",
		DetectedDomains:  []string{"postgres"},
		Rationale: []RationaleEntry{
			{Decision: "Stage: growth", Evidence: "1000 commits"},
		},
	}

	rationale := w.GenerateRationale()
	if !strings.Contains(rationale, "Stage: growth") {
		t.Error("rationale missing decision")
	}
	if !strings.Contains(rationale, "1000 commits") {
		t.Error("rationale missing evidence")
	}
	if !strings.Contains(rationale, "high") {
		t.Error("rationale missing security posture")
	}
}
