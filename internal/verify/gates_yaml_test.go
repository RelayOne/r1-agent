package verify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGatesYAML_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.yaml")
	content := `preset: default
description: Balanced defaults.
composite:
  threshold: 0.8
  weights:
    build: 2.0
    tests: 2.0
    lint: 1.0
gates:
  - id: build
    threshold: 1.0
    blocker: true
  - id: tests
    threshold: 1.0
    blocker: true
  - id: lint
    threshold: 0.9
    blocker: false
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := LoadGatesYAML(path)
	if err != nil {
		t.Fatalf("LoadGatesYAML: %v", err)
	}
	if p.Preset != "default" {
		t.Errorf("preset = %q, want default", p.Preset)
	}
	if p.Composite.Threshold != 0.8 {
		t.Errorf("composite threshold = %v, want 0.8", p.Composite.Threshold)
	}
	if p.WeightFor("build") != 2.0 {
		t.Errorf("WeightFor(build) = %v, want 2.0", p.WeightFor("build"))
	}
	// Unlisted gate IDs default to 1.0.
	if p.WeightFor("unknown") != 1.0 {
		t.Errorf("WeightFor(unknown) = %v, want 1.0", p.WeightFor("unknown"))
	}
	if len(p.Gates) != 3 {
		t.Errorf("gates count = %d, want 3", len(p.Gates))
	}
	ids := p.GateIDs()
	if len(ids) != 3 || ids[0] != "build" || ids[1] != "tests" || ids[2] != "lint" {
		t.Errorf("GateIDs preserved order mismatch: %v", ids)
	}
}

func TestLoadGatesYAML_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	_, err := LoadGatesYAML(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	// The loader wraps the os error so os.IsNotExist still works.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestLoadGatesYAML_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	// Unclosed bracket + tabbed indent — guaranteed parse failure.
	content := "preset: bad\ncomposite:\n\tthreshold: [0.8, 0.9\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadGatesYAML(path)
	if err == nil {
		t.Fatal("expected error for malformed yaml")
	}
	if !strings.Contains(err.Error(), "parse gates yaml") {
		t.Errorf("expected parse error wrapper, got %v", err)
	}
}

func TestLoadGatesYAML_EmptyPath(t *testing.T) {
	_, err := LoadGatesYAML("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestGatesPresetValidate(t *testing.T) {
	cases := []struct {
		name    string
		preset  GatesPreset
		wantErr string
	}{
		{
			name: "empty preset name",
			preset: GatesPreset{
				Gates: []GateSpec{{ID: "build", Threshold: 1.0}},
			},
			wantErr: "empty preset name",
		},
		{
			name:    "no gates",
			preset:  GatesPreset{Preset: "bad"},
			wantErr: "no gates",
		},
		{
			name: "empty gate ID",
			preset: GatesPreset{
				Preset: "bad",
				Gates:  []GateSpec{{Threshold: 1.0}},
			},
			wantErr: "empty ID",
		},
		{
			name: "duplicate gate ID",
			preset: GatesPreset{
				Preset: "bad",
				Gates: []GateSpec{
					{ID: "build", Threshold: 1.0},
					{ID: "build", Threshold: 0.8},
				},
			},
			wantErr: "duplicate gate ID",
		},
		{
			name: "threshold out of range",
			preset: GatesPreset{
				Preset: "bad",
				Gates:  []GateSpec{{ID: "build", Threshold: 1.5}},
			},
			wantErr: "not in [0,1]",
		},
		{
			name: "composite threshold out of range",
			preset: GatesPreset{
				Preset:    "bad",
				Composite: CompositeScore{Threshold: 2.0},
				Gates:     []GateSpec{{ID: "build", Threshold: 1.0}},
			},
			wantErr: "composite threshold",
		},
		{
			name: "negative weight",
			preset: GatesPreset{
				Preset: "bad",
				Composite: CompositeScore{
					Threshold: 0.5,
					Weights:   map[string]float64{"build": -1.0},
				},
				Gates: []GateSpec{{ID: "build", Threshold: 1.0}},
			},
			wantErr: "negative",
		},
		{
			name: "valid",
			preset: GatesPreset{
				Preset:    "ok",
				Composite: CompositeScore{Threshold: 0.5},
				Gates:     []GateSpec{{ID: "build", Threshold: 1.0}},
			},
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.preset.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoadGatesPresetDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("default.yaml", "preset: default\ncomposite:\n  threshold: 0.8\ngates:\n  - id: build\n    threshold: 1.0\n")
	write("strict.yaml", "preset: Strict\ncomposite:\n  threshold: 0.95\ngates:\n  - id: build\n    threshold: 1.0\n")
	write("README.md", "# ignored") // non-yaml files ignored by the loader
	write("nested.txt", "ignored")

	presets, err := LoadGatesPresetDir(dir)
	if err != nil {
		t.Fatalf("LoadGatesPresetDir: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("presets count = %d, want 2 (keys=%v)", len(presets), keysOf(presets))
	}
	// Lookup is lower-cased.
	if _, ok := presets["default"]; !ok {
		t.Error("expected key 'default'")
	}
	if _, ok := presets["strict"]; !ok {
		t.Error("expected key 'strict'")
	}
	// Value preserves original casing.
	if presets["strict"].Preset != "Strict" {
		t.Errorf("preset casing = %q, want Strict", presets["strict"].Preset)
	}
}

func TestLoadGatesPresetDir_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	body := "preset: dup\ncomposite:\n  threshold: 0.5\ngates:\n  - id: build\n    threshold: 1.0\n"
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write b: %v", err)
	}
	_, err := LoadGatesPresetDir(dir)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate preset error, got %v", err)
	}
}

func TestLoadGatesPresetDir_MissingDir(t *testing.T) {
	_, err := LoadGatesPresetDir(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

// Check the committed preset files in .stoke/gates.d/ actually load
// and validate. If a future edit breaks the schema, this guards it.
func TestCommittedPresetsLoad(t *testing.T) {
	// Find repo root by walking up until we see go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	gatesDir := filepath.Join(root, ".stoke", "gates.d")
	// The gates.d/ directory is a required committed artifact — the
	// R1 site advertises it and the loader's contract depends on it.
	// If this test cannot find the directory, that is a regression
	// the test MUST surface via a hard failure.
	if _, err := os.Stat(gatesDir); err != nil {
		t.Fatalf("required committed gates.d not found at %s: %v", gatesDir, err)
	}
	presets, err := LoadGatesPresetDir(gatesDir)
	if err != nil {
		t.Fatalf("LoadGatesPresetDir(%s): %v", gatesDir, err)
	}
	for _, want := range []string{"default", "strict", "fast"} {
		if _, ok := presets[want]; !ok {
			t.Errorf("missing committed preset %q (loaded keys: %v)", want, keysOf(presets))
		}
	}
}

func keysOf(m map[string]*GatesPreset) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
