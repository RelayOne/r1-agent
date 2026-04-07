package wizard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo creates a bare-minimum git repo in dir so snapshot.Take works.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	// Create a file and commit so HEAD exists.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmds = append(cmds,
		[]string{"git", "add", "."},
		[]string{"git", "commit", "-m", "init"},
	)
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}
}

func TestDefaultConfigValid(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Version == "" {
		t.Error("expected non-empty version")
	}
	if cfg.OperatingMode == "" {
		t.Error("expected non-empty operating_mode")
	}
	if cfg.DefaultModel == "" {
		t.Error("expected non-empty default_model")
	}
	if cfg.Budget.MaxUSD <= 0 {
		t.Error("expected positive max_usd")
	}
	if cfg.Budget.WarningPct <= 0 {
		t.Error("expected positive warning_pct")
	}
	if cfg.Budget.CheckPct <= 0 {
		t.Error("expected positive check_pct")
	}
	if cfg.Budget.EscalatePct <= 0 {
		t.Error("expected positive escalate_pct")
	}
	if cfg.Budget.StopPct <= 0 {
		t.Error("expected positive stop_pct")
	}
	if cfg.Supervisor.Preset == "" {
		t.Error("expected non-empty supervisor preset")
	}
	if !cfg.Skills.Enabled {
		t.Error("expected skills enabled by default")
	}
	if !cfg.Skills.AutoDetect {
		t.Error("expected skills auto_detect by default")
	}
	if cfg.Bus.PropagationMode == "" {
		t.Error("expected non-empty bus propagation_mode")
	}
	if len(cfg.ModelOverrides) == 0 {
		t.Error("expected non-empty model_overrides")
	}
}

func TestInitCreatesDirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	cfg, err := Init(context.Background(), InitOpts{
		RepoRoot: dir,
		Mode:     "yes",
		Preset:   "balanced",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	// Verify directory structure
	for _, sub := range []string{"", "ledger", "bus", "snapshot"} {
		p := filepath.Join(dir, ".stoke", sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", p, err)
		} else if !info.IsDir() {
			t.Errorf("expected %s to be a directory", p)
		}
	}

	// Verify config.yaml was written
	cfgPath := filepath.Join(dir, ".stoke", "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("expected config.yaml: %v", err)
	}

	// Verify snapshot was saved
	snapPath := filepath.Join(dir, ".stoke", "snapshot", "init.json")
	if _, err := os.Stat(snapPath); err != nil {
		t.Errorf("expected snapshot file: %v", err)
	}
}

func TestLoadSaveConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	original := DefaultConfig()
	original.Budget.MaxUSD = 42.5
	original.DefaultModel = "gpt-4"
	original.Supervisor.Preset = "strict"

	if err := SaveConfig(dir, original); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Budget.MaxUSD != 42.5 {
		t.Errorf("expected max_usd 42.5, got %f", loaded.Budget.MaxUSD)
	}
	if loaded.DefaultModel != "gpt-4" {
		t.Errorf("expected default_model gpt-4, got %s", loaded.DefaultModel)
	}
	if loaded.Supervisor.Preset != "strict" {
		t.Errorf("expected preset strict, got %s", loaded.Supervisor.Preset)
	}
	if loaded.Version != original.Version {
		t.Errorf("expected version %s, got %s", original.Version, loaded.Version)
	}
	if loaded.OperatingMode != original.OperatingMode {
		t.Errorf("expected operating_mode %s, got %s", original.OperatingMode, loaded.OperatingMode)
	}
}

func TestSetField(t *testing.T) {
	cfg := DefaultConfig()

	// Set top-level string field
	if err := SetField(cfg, "default_model", "gpt-4"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "gpt-4" {
		t.Errorf("expected gpt-4, got %s", cfg.DefaultModel)
	}

	// Set nested float field
	if err := SetField(cfg, "budget.max_usd", "99.99"); err != nil {
		t.Fatal(err)
	}
	if cfg.Budget.MaxUSD != 99.99 {
		t.Errorf("expected 99.99, got %f", cfg.Budget.MaxUSD)
	}

	// Set nested string field
	if err := SetField(cfg, "supervisor.preset", "strict"); err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.Preset != "strict" {
		t.Errorf("expected strict, got %s", cfg.Supervisor.Preset)
	}

	// Set nested bool field
	if err := SetField(cfg, "skills.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if cfg.Skills.Enabled {
		t.Error("expected skills.enabled false")
	}

	// Set bus propagation mode
	if err := SetField(cfg, "bus.propagation_mode", "verbose"); err != nil {
		t.Fatal(err)
	}
	if cfg.Bus.PropagationMode != "verbose" {
		t.Errorf("expected verbose, got %s", cfg.Bus.PropagationMode)
	}

	// Unknown field should error
	if err := SetField(cfg, "nonexistent.field", "x"); err == nil {
		t.Error("expected error for unknown field")
	}
}

func TestApplyPreset(t *testing.T) {
	tests := []struct {
		preset      string
		wantPreset  string
		wantMaxUSD  float64
		wantBusProp string
	}{
		{"minimal", "minimal", 5.0, "minimal"},
		{"balanced", "balanced", 10.0, "filtered"},
		{"strict", "strict", 25.0, "verbose"},
	}

	for _, tt := range tests {
		cfg := DefaultConfig()
		if err := ApplyPreset(cfg, tt.preset); err != nil {
			t.Errorf("ApplyPreset(%s): %v", tt.preset, err)
			continue
		}
		if cfg.Supervisor.Preset != tt.wantPreset {
			t.Errorf("preset %s: supervisor.preset=%s, want %s", tt.preset, cfg.Supervisor.Preset, tt.wantPreset)
		}
		if cfg.Budget.MaxUSD != tt.wantMaxUSD {
			t.Errorf("preset %s: budget.max_usd=%f, want %f", tt.preset, cfg.Budget.MaxUSD, tt.wantMaxUSD)
		}
		if cfg.Bus.PropagationMode != tt.wantBusProp {
			t.Errorf("preset %s: bus.propagation_mode=%s, want %s", tt.preset, cfg.Bus.PropagationMode, tt.wantBusProp)
		}
	}

	// Unknown preset should error
	cfg := DefaultConfig()
	if err := ApplyPreset(cfg, "unknown"); err == nil {
		t.Error("expected error for unknown preset")
	}
}

func TestInitYesMode(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	cfg, err := Init(context.Background(), InitOpts{
		RepoRoot: dir,
		Mode:     "yes",
		Preset:   "minimal",
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.OperatingMode != "yes" {
		t.Errorf("expected operating_mode 'yes', got %s", cfg.OperatingMode)
	}
	if cfg.Supervisor.Preset != "minimal" {
		t.Errorf("expected preset 'minimal', got %s", cfg.Supervisor.Preset)
	}

	// Verify we can load the written config back
	loaded, err := LoadConfig(filepath.Join(dir, ".stoke"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Supervisor.Preset != "minimal" {
		t.Errorf("loaded preset: got %s, want minimal", loaded.Supervisor.Preset)
	}
}

func TestInitMissingRepoRoot(t *testing.T) {
	_, err := Init(context.Background(), InitOpts{})
	if err == nil {
		t.Error("expected error when repo root is empty")
	}
}
