package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skillmfr"
)

// SeedBundledSkillPacks loads checked-in skill packs from packRoot into the
// manifest registry. Relative IR/proof refs are rewritten to absolute paths so
// deterministic invocation keeps working after registration.
func (b *Backends) SeedBundledSkillPacks(packRoot string) (int, int, error) {
	return b.SeedSkillPackRoots([]string{packRoot})
}

// SeedPackRegistries loads skill packs from the repo + user pack registries in
// deterministic precedence order:
//  1. <repo>/.r1/skills/packs
//  2. <repo>/.stoke/skills/packs
//  3. <home>/.r1/skills/packs
//  4. <home>/.stoke/skills/packs
//
// First registration wins, so canonical repo packs shadow legacy/user copies.
func (b *Backends) SeedPackRegistries(repoRoot string) (int, int, error) {
	return b.SeedSkillPackRoots(packRegistryRoots(repoRoot))
}

// SeedSkillPackRoots loads skill packs from the given roots into the manifest
// registry. Relative IR/proof refs are rewritten to absolute paths so
// deterministic invocation keeps working after registration.
func (b *Backends) SeedSkillPackRoots(packRoots []string) (int, int, error) {
	if b == nil || b.ManifestRegistry == nil {
		return 0, 0, fmt.Errorf("stoke-mcp: manifest registry not initialized")
	}
	if len(packRoots) == 0 {
		return 0, 0, nil
	}

	registered := 0
	skipped := 0
	for _, packRoot := range packRoots {
		if strings.TrimSpace(packRoot) == "" {
			continue
		}
		entries, err := os.ReadDir(packRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return registered, skipped, fmt.Errorf("read bundled pack root %s: %w", packRoot, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			packPath := filepath.Join(packRoot, entry.Name())
			if _, err := skillmfr.VerifyPackSignatureIfPresent(packPath); err != nil {
				return registered, skipped, fmt.Errorf("verify bundled pack %s: %w", packPath, err)
			}
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
	}
	return registered, skipped, nil
}

func packRegistryRoots(repoRoot string) []string {
	roots := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(root string) {
		if strings.TrimSpace(root) == "" {
			return
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	for _, rootName := range []string{r1dir.Canonical, r1dir.Legacy} {
		add(filepath.Join(repoRoot, rootName, "skills", "packs"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		for _, rootName := range []string{r1dir.Canonical, r1dir.Legacy} {
			add(filepath.Join(home, rootName, "skills", "packs"))
		}
	}
	return roots
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
