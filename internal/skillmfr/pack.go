// Package skillmfr — pack.go
//
// R1 Actium-Studio skill-pack seed (work-r1-actium-studio-skills.md
// phase R1S-1.5). Defines the on-disk layout of a skill pack and
// provides a Loader that reads the pack directory, parses its
// `pack.yaml` metadata, and returns each manifest validated against
// the existing skillmfr.Manifest rules.
//
// On-disk layout this loader understands:
//
//     <packRoot>/
//       pack.yaml                  -- pack metadata (PackMeta below)
//       README.md                  -- operator docs (not parsed)
//       <skill_name>/
//         manifest.json            -- skillmfr.Manifest JSON
//         SKILL.md                 -- optional operator-facing body
//       <skill_name_2>/
//         manifest.json
//         ...
//
// Why JSON for the manifest, not SKILL.md YAML frontmatter? The
// existing `skill.parseSkill` only extracts name/description/
// triggers/allowed-tools/keywords from frontmatter — it cannot
// represent typed JSON Schema fields. skillmfr.Manifest already
// round-trips through encoding/json, so a plain manifest.json is
// the lowest-friction loadable form that satisfies the work
// order's "must pass skillmfr.Manifest validation" conformance.
// Operators who want prose docs keep a SKILL.md alongside; the
// loader doesn't require it.
//
// Scope of this file intentionally stays minimal: LoadPack +
// PackMeta + a typed error for bad input. Discovery /
// registry-integration lives in the command layer
// (cmd/stoke/skills_pack.go in phase R1S-1.4).
package skillmfr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// PackMeta mirrors pack.yaml. Fields are optional except Name and
// Version so a pack without a dependency pin still loads — the
// dispatcher checks the pins when it decides whether to activate
// the pack.
type PackMeta struct {
	// Name is the pack identifier, e.g. "actium-studio".
	Name string `yaml:"name" json:"name"`

	// Version is the pack's own semver. Independent of any skill's
	// version: a pack can republish the same skill version with a
	// new README.
	Version string `yaml:"version" json:"version"`

	// Description is the one-line operator-facing summary.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// MinR1Version is the minimum R1/stoke binary version this pack
	// knows how to talk to. Opaque string (semver comparison is the
	// caller's problem). Empty means unspecified.
	MinR1Version string `yaml:"min_r1_version,omitempty" json:"min_r1_version,omitempty"`

	// UpstreamAPIVersion pins the external service API version the
	// pack's manifests target (e.g. Actium Studio v1). Diagnostic
	// only — the HTTP client sets the actual content-negotiation
	// header.
	UpstreamAPIVersion string `yaml:"upstream_api_version,omitempty" json:"upstream_api_version,omitempty"`

	// SkillCount is the number of manifests shipped in this pack.
	// Declared in the metadata so operators can sanity-check
	// discovery ("pack claims 53, loader found 47" → something is
	// unfinished). The LoadPack function returns both values and
	// does not crash on mismatch.
	SkillCount int `yaml:"skill_count,omitempty" json:"skill_count,omitempty"`

	// Dependencies lists other pack names this pack composes with.
	// Currently informational; pack activation logic lives above.
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

// LoadedPack is the result of a successful LoadPack call.
type LoadedPack struct {
	// Meta is the parsed pack.yaml.
	Meta PackMeta

	// Manifests is the list of validated manifests keyed by name.
	// Order is alphabetical by Name for reproducibility in tests.
	Manifests []Manifest

	// Root is the absolute path of the pack directory on disk, for
	// diagnostics.
	Root string
}

// ErrPackInvalid is the umbrella error for pack-loading failures.
// Wrapped at each call site with specific context so callers can
// errors.Is against it.
var ErrPackInvalid = errors.New("skillmfr: pack invalid")

// LoadPack reads a skill-pack directory at packRoot, parses its
// pack.yaml, iterates every immediate subdirectory looking for a
// manifest.json, and returns the aggregated LoadedPack.
//
// Failure modes (all wrap ErrPackInvalid):
//   - packRoot not a directory
//   - pack.yaml missing or malformed
//   - a subdirectory contains manifest.json but it fails Validate
//
// A subdirectory without manifest.json is SILENTLY SKIPPED —
// packs are allowed to ship README-only docs subdirs (e.g.
// `examples/`) next to real skill dirs.
func LoadPack(packRoot string) (*LoadedPack, error) {
	info, err := os.Stat(packRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: stat %q: %v", ErrPackInvalid, packRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %q is not a directory", ErrPackInvalid, packRoot)
	}

	metaPath := filepath.Join(packRoot, "pack.yaml")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read pack.yaml: %v", ErrPackInvalid, err)
	}
	var meta PackMeta
	if err := yaml.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("%w: parse pack.yaml: %v", ErrPackInvalid, err)
	}
	if meta.Name == "" {
		return nil, fmt.Errorf("%w: pack.yaml missing name", ErrPackInvalid)
	}
	if meta.Version == "" {
		return nil, fmt.Errorf("%w: pack.yaml missing version", ErrPackInvalid)
	}

	entries, err := os.ReadDir(packRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: readdir %q: %v", ErrPackInvalid, packRoot, err)
	}
	var manifests []Manifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(packRoot, entry.Name(), "manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("%w: read %s: %v", ErrPackInvalid, manifestPath, err)
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%w: parse %s: %v", ErrPackInvalid, manifestPath, err)
		}
		if err := m.Validate(); err != nil {
			return nil, fmt.Errorf("%w: validate %s: %v", ErrPackInvalid, manifestPath, err)
		}
		manifests = append(manifests, m)
	}

	sort.SliceStable(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})

	abs, absErr := filepath.Abs(packRoot)
	if absErr != nil {
		abs = packRoot
	}
	return &LoadedPack{
		Meta:      meta,
		Manifests: manifests,
		Root:      abs,
	}, nil
}

// RegisterPack loads a pack via LoadPack and registers every
// manifest with the supplied registry. Returns the number of
// manifests registered + any error (wrapping ErrPackInvalid or a
// registry error). On registry error, the partial state is left
// as-is — callers decide whether to roll back.
func RegisterPack(r *Registry, packRoot string) (int, error) {
	if r == nil {
		return 0, fmt.Errorf("skillmfr: RegisterPack: nil registry")
	}
	pack, err := LoadPack(packRoot)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, m := range pack.Manifests {
		if err := r.Register(m); err != nil {
			return count, fmt.Errorf("skillmfr: RegisterPack %q skill %q: %w",
				pack.Meta.Name, m.Name, err)
		}
		count++
	}
	return count, nil
}
