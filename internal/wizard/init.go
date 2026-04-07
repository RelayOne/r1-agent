package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/ericmacdougall/stoke/internal/snapshot"
	"gopkg.in/yaml.v3"
)

// Config is the structured representation of .stoke/config.yaml
// for the Init/LoadConfig/SaveConfig workflow.
type Config struct {
	Version       string            `yaml:"version"`
	OperatingMode string            `yaml:"operating_mode"` // interactive, full_auto
	DefaultModel  string            `yaml:"default_model"`
	ModelOverrides map[string]string `yaml:"model_overrides"` // role -> model
	Budget        BudgetConfig      `yaml:"budget"`
	Supervisor    SupervisorConfig  `yaml:"supervisor"`
	Skills        SkillsConfig      `yaml:"skills"`
	Snapshot      SnapshotConfig    `yaml:"snapshot"`
	Bus           BusConfig         `yaml:"bus"`
}

// BudgetConfig controls cost enforcement thresholds.
type BudgetConfig struct {
	MaxUSD     float64 `yaml:"max_usd"`
	WarningPct float64 `yaml:"warning_pct"`
	CheckPct   float64 `yaml:"check_pct"`
	EscalatePct float64 `yaml:"escalate_pct"`
	StopPct    float64 `yaml:"stop_pct"`
}

// SupervisorConfig controls the supervisor rule preset and overrides.
type SupervisorConfig struct {
	Preset        string                  `yaml:"preset"` // minimal, balanced, strict
	RuleOverrides map[string]RuleOverride `yaml:"rule_overrides,omitempty"`
}

// RuleOverride allows toggling or parameterizing individual supervisor rules.
type RuleOverride struct {
	Enabled    *bool          `yaml:"enabled,omitempty"`
	Parameters map[string]any `yaml:"parameters,omitempty"`
}

// SkillsConfig controls skill injection.
type SkillsConfig struct {
	Enabled    bool     `yaml:"enabled"`
	AutoDetect bool     `yaml:"auto_detect"`
	AlwaysOn   []string `yaml:"always_on,omitempty"`
	Excluded   []string `yaml:"excluded,omitempty"`
}

// SnapshotConfig controls workspace snapshot behavior.
type SnapshotConfig struct {
	FormatterOnSnapshot bool     `yaml:"formatter_on_snapshot"`
	ColdStartAnnotation bool     `yaml:"cold_start_annotation"`
	PromotedPaths       []string `yaml:"promoted_paths,omitempty"`
}

// BusConfig controls event bus propagation.
type BusConfig struct {
	PropagationMode string `yaml:"propagation_mode"` // filtered, verbose, minimal
}

// InitOpts configures the initialization flow.
type InitOpts struct {
	RepoRoot   string
	Mode       string // "auto", "interactive", "yes"
	Preset     string // "minimal", "balanced", "strict"
	GlobalInit bool   // true for --global
}

// Init runs the first-time initialization flow.
// Creates .stoke/ directory, takes snapshot, writes config.yaml,
// initializes ledger and bus directories.
func Init(ctx context.Context, opts InitOpts) (*Config, error) {
	if opts.RepoRoot == "" {
		return nil, fmt.Errorf("wizard: repo root is required")
	}
	if opts.Preset == "" {
		opts.Preset = "balanced"
	}

	stokeDir := filepath.Join(opts.RepoRoot, ".stoke")

	// 1. Create .stoke/ directory and subdirectories
	for _, sub := range []string{"", "ledger", "bus", "snapshot"} {
		dir := filepath.Join(stokeDir, sub)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("wizard: create %s: %w", dir, err)
		}
	}

	// 2. Take snapshot
	snap, err := snapshot.Take(opts.RepoRoot, "init")
	if err != nil {
		return nil, fmt.Errorf("wizard: take snapshot: %w", err)
	}
	snapPath := filepath.Join(stokeDir, "snapshot", "init.json")
	if err := snapshot.Save(snap, snapPath); err != nil {
		return nil, fmt.Errorf("wizard: save snapshot: %w", err)
	}

	// 3. Build and write config
	cfg := DefaultConfig()
	if err := ApplyPreset(cfg, opts.Preset); err != nil {
		return nil, fmt.Errorf("wizard: apply preset: %w", err)
	}
	if opts.Mode != "" {
		cfg.OperatingMode = opts.Mode
	}

	if err := SaveConfig(stokeDir, cfg); err != nil {
		return nil, fmt.Errorf("wizard: save config: %w", err)
	}

	return cfg, nil
}

// LoadConfig reads .stoke/config.yaml.
func LoadConfig(stokeDir string) (*Config, error) {
	path := filepath.Join(stokeDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wizard: read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("wizard: parse config: %w", err)
	}
	return &cfg, nil
}

// SaveConfig writes .stoke/config.yaml.
func SaveConfig(stokeDir string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("wizard: marshal config: %w", err)
	}
	path := filepath.Join(stokeDir, "config.yaml")
	return os.WriteFile(path, data, 0644)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Version:       "1",
		OperatingMode: "interactive",
		DefaultModel:  "claude",
		ModelOverrides: map[string]string{
			"review": "codex",
		},
		Budget: BudgetConfig{
			MaxUSD:      10.0,
			WarningPct:  50.0,
			CheckPct:    75.0,
			EscalatePct: 90.0,
			StopPct:     100.0,
		},
		Supervisor: SupervisorConfig{
			Preset:        "balanced",
			RuleOverrides: map[string]RuleOverride{},
		},
		Skills: SkillsConfig{
			Enabled:    true,
			AutoDetect: true,
			AlwaysOn:   []string{"agent-discipline"},
		},
		Snapshot: SnapshotConfig{
			FormatterOnSnapshot: true,
			ColdStartAnnotation: true,
		},
		Bus: BusConfig{
			PropagationMode: "filtered",
		},
	}
}

// SetField sets a config field by dotted path (e.g. "budget.max_usd").
func SetField(cfg *Config, field string, value string) error {
	parts := strings.Split(field, ".")
	if len(parts) < 1 || len(parts) > 2 {
		return fmt.Errorf("wizard: unsupported field path %q", field)
	}

	v := reflect.ValueOf(cfg).Elem()

	// Find top-level field
	topField := findField(v, parts[0])
	if !topField.IsValid() {
		return fmt.Errorf("wizard: unknown field %q", parts[0])
	}

	if len(parts) == 1 {
		return setReflectValue(topField, value)
	}

	// Navigate into struct
	if topField.Kind() != reflect.Struct {
		return fmt.Errorf("wizard: field %q is not a struct", parts[0])
	}
	subField := findField(topField, parts[1])
	if !subField.IsValid() {
		return fmt.Errorf("wizard: unknown field %q in %q", parts[1], parts[0])
	}
	return setReflectValue(subField, value)
}

// findField finds a struct field by its yaml tag or Go name (case-insensitive).
func findField(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("yaml")
		tagName := strings.Split(tag, ",")[0]
		if tagName == name || strings.EqualFold(sf.Name, name) {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

func setReflectValue(f reflect.Value, value string) error {
	switch f.Kind() {
	case reflect.String:
		f.SetString(value)
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("wizard: invalid float %q: %w", value, err)
		}
		f.SetFloat(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("wizard: invalid bool %q: %w", value, err)
		}
		f.SetBool(b)
	default:
		return fmt.Errorf("wizard: unsupported field type %s", f.Kind())
	}
	return nil
}

// ApplyPreset applies a named supervisor preset (minimal, balanced, strict).
func ApplyPreset(cfg *Config, preset string) error {
	switch preset {
	case "minimal":
		cfg.Supervisor.Preset = "minimal"
		cfg.Budget.MaxUSD = 5.0
		cfg.Skills.AutoDetect = false
		cfg.Bus.PropagationMode = "minimal"
		cfg.Snapshot.FormatterOnSnapshot = false
		cfg.Snapshot.ColdStartAnnotation = false
	case "balanced":
		cfg.Supervisor.Preset = "balanced"
		cfg.Budget.MaxUSD = 10.0
		cfg.Skills.AutoDetect = true
		cfg.Bus.PropagationMode = "filtered"
		cfg.Snapshot.FormatterOnSnapshot = true
		cfg.Snapshot.ColdStartAnnotation = true
	case "strict":
		cfg.Supervisor.Preset = "strict"
		cfg.Budget.MaxUSD = 25.0
		cfg.Skills.AutoDetect = true
		cfg.Bus.PropagationMode = "verbose"
		cfg.Snapshot.FormatterOnSnapshot = true
		cfg.Snapshot.ColdStartAnnotation = true
	default:
		return fmt.Errorf("wizard: unknown preset %q (valid: minimal, balanced, strict)", preset)
	}
	return nil
}
