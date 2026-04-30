package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/skillmfr"
)

// SeedBundledSkillPacks loads checked-in skill packs from packRoot into the
// manifest registry. Relative IR/proof refs are rewritten to absolute paths so
// deterministic invocation keeps working after registration.
func (b *Backends) SeedBundledSkillPacks(packRoot string) (int, int, error) {
	if b == nil || b.ManifestRegistry == nil {
		return 0, 0, fmt.Errorf("stoke-mcp: manifest registry not initialized")
	}
	if packRoot == "" {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(packRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("read bundled pack root %s: %w", packRoot, err)
	}

	registered := 0
	skipped := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packPath := filepath.Join(packRoot, entry.Name())
		pack, err := skillmfr.LoadPack(packPath)
		if err != nil {
			return registered, skipped, fmt.Errorf("load bundled pack %s: %w", packPath, err)
		}
		for _, manifest := range pack.Manifests {
			if _, exists := b.ManifestRegistry.Get(manifest.Name); exists {
				skipped++
				continue
			}
			manifest = absolutizeManifestRefs(packPath, manifest)
			if err := b.ManifestRegistry.Register(manifest); err != nil {
				return registered, skipped, fmt.Errorf("register bundled pack manifest %s: %w", manifest.Name, err)
			}
			registered++
		}
	}
	return registered, skipped, nil
}

func absolutizeManifestRefs(packPath string, manifest skillmfr.Manifest) skillmfr.Manifest {
	if !manifest.UseIR {
		return manifest
	}
	manifestDir := filepath.Join(packPath, manifest.Name)
	if manifest.IRRef != "" && !filepath.IsAbs(manifest.IRRef) {
		manifest.IRRef = filepath.Join(manifestDir, manifest.IRRef)
	}
	if manifest.CompileProofRef != "" && !filepath.IsAbs(manifest.CompileProofRef) {
		manifest.CompileProofRef = filepath.Join(manifestDir, manifest.CompileProofRef)
	}
	return manifest
}
