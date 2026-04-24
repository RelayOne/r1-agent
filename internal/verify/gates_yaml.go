// Package verify — gates_yaml.go
//
// The R1 site advertises a gates preset in `.stoke/gates.yaml` with a
// composite score across build/test/lint/review/scope axes. The
// presets ship under `.stoke/gates.d/{default,strict,fast}.yaml`; a
// caller can either load a single preset file directly or point the
// loader at the top-level `.stoke/gates.yaml` symlink when that lands.
//
// Contents:
//
//   - GatesPreset typed schema covering name, description, gate
//     thresholds, and composite weights
//   - LoadGatesYAML: read + parse + validate a single preset file
//   - LoadGatesPresetDir: read every *.yaml file in a directory,
//     returning a keyed map for selection by name
//
// The loader is a self-contained library: callers read a preset
// and enforce its thresholds however they choose. The existing
// verify.Pipeline keeps its Rubric-driven path; a preset-aware
// renderer can consume LoadGatesYAML directly without changing
// the Pipeline surface.
package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// GatesPreset is a named bundle of per-gate thresholds and composite
// weights. The site's demo drags a single "strictness" slider — the
// underlying data model is three presets (or more) each pinning
// concrete per-gate thresholds and weights so the composite score is
// reproducible.
//
// YAML shape:
//
//	preset: default
//	description: Balanced defaults for general-purpose code tasks.
//	composite:
//	  threshold: 0.80
//	  weights:
//	    build: 2.0
//	    tests: 2.0
//	    lint: 1.0
//	    review: 1.5
//	    scope: 1.0
//	gates:
//	  - id: build
//	    threshold: 1.0
//	    blocker: true
//	  - id: tests
//	    threshold: 1.0
//	    blocker: true
//	  - id: lint
//	    threshold: 0.9
//	    blocker: false
//	  - id: review
//	    threshold: 0.8
//	    blocker: true
//	  - id: scope
//	    threshold: 1.0
//	    blocker: true
type GatesPreset struct {
	// Preset is the short name, e.g. "default", "strict", "fast".
	// Lookup by name is case-insensitive but the stored value
	// preserves the author's casing.
	Preset string `yaml:"preset"`

	// Description is free-form prose shown in the TUI picker and
	// the `stoke verify --gates` help output.
	Description string `yaml:"description,omitempty"`

	// Composite holds the overall pass threshold and per-axis
	// weights. Present whenever the preset renders a single
	// composite score; omit only when every gate is a hard gate.
	Composite CompositeScore `yaml:"composite"`

	// Gates is the ordered list of individual gates with their
	// per-gate thresholds. The engine enforces each blocker gate
	// as a hard pass/fail; non-blocker gates contribute to the
	// composite score only.
	Gates []GateSpec `yaml:"gates"`
}

// CompositeScore holds the overall pass threshold and per-axis
// weights used to compute the composite score reported by the
// gates preset demo.
type CompositeScore struct {
	// Threshold is the composite score in [0,1] the preset
	// requires to pass. A preset with no composite uses 0.0 to
	// indicate "pass iff every blocker gate passes" — the
	// weighted average then contributes no additional gating.
	Threshold float64 `yaml:"threshold"`

	// Weights maps gate ID → weight. Unlisted gate IDs default
	// to 1.0. Negative weights are rejected by Validate.
	Weights map[string]float64 `yaml:"weights,omitempty"`
}

// GateSpec is one row in the preset's gates list.
type GateSpec struct {
	// ID is the gate identifier (e.g. "build", "tests", "lint",
	// "review", "scope"). IDs must be unique within a preset.
	ID string `yaml:"id"`

	// Threshold is the per-gate score in [0,1] required to pass
	// this gate. For pass/fail gates (build, tests), use 1.0.
	Threshold float64 `yaml:"threshold"`

	// Blocker marks a gate as a hard gate: failure fails the
	// preset regardless of composite score. Typically build,
	// tests, and scope are blockers.
	Blocker bool `yaml:"blocker,omitempty"`
}

// Validate checks the preset shape: non-empty preset name,
// non-empty gates list, unique gate IDs, thresholds in [0,1],
// non-negative weights, and a composite threshold in [0,1].
// Empty gates is REJECTED — a preset without gates would silently
// pass every artifact.
func (p GatesPreset) Validate() error {
	if strings.TrimSpace(p.Preset) == "" {
		return fmt.Errorf("verify: gates preset has empty preset name")
	}
	if len(p.Gates) == 0 {
		return fmt.Errorf("verify: gates preset %q has no gates (would silently pass every artifact)", p.Preset)
	}
	seen := map[string]bool{}
	for i, g := range p.Gates {
		if strings.TrimSpace(g.ID) == "" {
			return fmt.Errorf("verify: gates preset %q gate %d has empty ID", p.Preset, i)
		}
		if seen[g.ID] {
			return fmt.Errorf("verify: gates preset %q has duplicate gate ID %q", p.Preset, g.ID)
		}
		seen[g.ID] = true
		if g.Threshold < 0 || g.Threshold > 1 {
			return fmt.Errorf("verify: gates preset %q gate %q threshold %v not in [0,1]", p.Preset, g.ID, g.Threshold)
		}
	}
	if p.Composite.Threshold < 0 || p.Composite.Threshold > 1 {
		return fmt.Errorf("verify: gates preset %q composite threshold %v not in [0,1]", p.Preset, p.Composite.Threshold)
	}
	for id, w := range p.Composite.Weights {
		if w < 0 {
			return fmt.Errorf("verify: gates preset %q weight for %q is negative (%v)", p.Preset, id, w)
		}
	}
	return nil
}

// WeightFor returns the composite weight for a gate ID, defaulting
// to 1.0 when the preset does not pin an explicit weight.
func (p GatesPreset) WeightFor(gateID string) float64 {
	if w, ok := p.Composite.Weights[gateID]; ok {
		return w
	}
	return 1.0
}

// GateIDs returns the ordered list of gate IDs declared in the
// preset. Order matches the source YAML.
func (p GatesPreset) GateIDs() []string {
	out := make([]string, 0, len(p.Gates))
	for _, g := range p.Gates {
		out = append(out, g.ID)
	}
	return out
}

// LoadGatesYAML reads a gates preset YAML file, parses it, and
// validates the result. Returns a populated GatesPreset on success
// or a wrapped error on any of the three failure modes: missing
// file, malformed YAML, semantically invalid preset.
func LoadGatesYAML(path string) (*GatesPreset, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("verify: LoadGatesYAML: empty path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is operator-supplied configuration.
	if err != nil {
		return nil, fmt.Errorf("verify: read gates yaml %q: %w", path, err)
	}
	var preset GatesPreset
	if err := yaml.Unmarshal(raw, &preset); err != nil {
		return nil, fmt.Errorf("verify: parse gates yaml %q: %w", path, err)
	}
	if err := preset.Validate(); err != nil {
		return nil, err
	}
	return &preset, nil
}

// LoadGatesPresetDir reads every *.yaml (and *.yml) file in dir,
// parses each as a GatesPreset, and returns a map keyed by the
// preset's Preset field (case-insensitive). Parse failures and
// validation failures are surfaced; an empty directory returns an
// empty map, not an error.
func LoadGatesPresetDir(dir string) (map[string]*GatesPreset, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("verify: LoadGatesPresetDir: empty dir")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("verify: read gates preset dir %q: %w", dir, err)
	}
	// Sort for deterministic error-order and deterministic duplicate detection.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make(map[string]*GatesPreset, len(names))
	for _, n := range names {
		preset, err := LoadGatesYAML(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(preset.Preset)
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("verify: duplicate gates preset name %q (file %q)", preset.Preset, n)
		}
		out[key] = preset
	}
	return out, nil
}
