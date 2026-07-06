package main

import (
	"errors"
	"fmt"
	"log"
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
	// Checked-in / caller-chosen roots are trusted by provenance.
	return b.seedPackRoots([]packSeedRoot{{path: packRoot, trusted: true}})
}

// packSeedRoot pairs a pack-registry root with whether it is trusted by
// provenance. Repo-bundled roots are trusted (they ship in the checkout you
// are running); user-home / external roots are NOT — an attacker who can drop
// a directory there could otherwise register arbitrary skills, so those must
// carry a valid signature from a trusted key (see skillmfr.VerifyPackTrusted)
// or be explicitly opted in via R1_ALLOW_UNSIGNED_SKILL_PACKS.
type packSeedRoot struct {
	path    string
	trusted bool
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
	return b.seedPackRoots(packRegistrySeedRoots(repoRoot))
}

// SeedSkillPackRoots loads skill packs from the given roots into the manifest
// registry. Relative IR/proof refs are rewritten to absolute paths so
// deterministic invocation keeps working after registration.
func (b *Backends) SeedSkillPackRoots(packRoots []string) (int, int, error) {
	// Back-compat surface: an explicit root list is treated as trusted
	// provenance (the caller named the paths). External-registry hardening is
	// applied via SeedPackRegistries, which marks user-home roots untrusted.
	roots := make([]packSeedRoot, 0, len(packRoots))
	for _, p := range packRoots {
		roots = append(roots, packSeedRoot{path: p, trusted: true})
	}
	return b.seedPackRoots(roots)
}

func (b *Backends) seedPackRoots(packRoots []packSeedRoot) (int, int, error) {
	if b == nil || b.ManifestRegistry == nil {
		return 0, 0, fmt.Errorf("r1-mcp: manifest registry not initialized")
	}
	if len(packRoots) == 0 {
		return 0, 0, nil
	}

	policy := skillmfr.LoadPackTrustPolicyFromEnv()
	registered := 0
	skipped := 0
	for _, root := range packRoots {
		packRoot := root.path
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
			if root.trusted {
				// Provenance-trusted: unsigned is fine, but a present
				// signature must still be cryptographically valid.
				if _, err := skillmfr.VerifyPackSignatureIfPresent(packPath); err != nil {
					return registered, skipped, fmt.Errorf("verify bundled pack %s: %w", packPath, err)
				}
			} else {
				// External/untrusted root: require a valid signature from a
				// trusted key (fail-closed). Unsigned or untrusted-key packs
				// are SKIPPED (not activated) rather than crashing seeding; a
				// present-but-broken signature is a hard error.
				if _, err := skillmfr.VerifyPackTrusted(packPath, policy); err != nil {
					if errors.Is(err, skillmfr.ErrPackUnsigned) || errors.Is(err, skillmfr.ErrPackUntrusted) {
						skipped += skippedManifestCount(packPath)
						log.Printf("r1-mcp: skipping untrusted external skill pack %s: %v (set %s or %s to allow)",
							packPath, err, skillmfr.EnvTrustedPackKeys, skillmfr.EnvAllowUnsignedPacks)
						continue
					}
					return registered, skipped, fmt.Errorf("verify external pack %s: %w", packPath, err)
				}
			}
			pack, err := skillmfr.LoadPack(packPath)
			if err != nil {
				return registered, skipped, fmt.Errorf("load bundled pack %s: %w", packPath, err)
			}
			// R1S-1.2 (audit A039): the actium-studio pack registers
			// only when studio_config.enabled is true, per the contract
			// in internal/config/studio.go — studio.* skills must not
			// be advertised while uninvokable.
			if !b.StudioConfig.Enabled && packHasStudioManifests(pack.Manifests) {
				skipped += len(pack.Manifests)
				continue
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

// packRegistrySeedRoots returns the pack-registry roots with per-root trust:
// repo-bundled roots (under repoRoot) are provenance-trusted; user-home roots
// are not and must be signed by a trusted key.
func packRegistrySeedRoots(repoRoot string) []packSeedRoot {
	roots := packRegistryRoots(repoRoot)
	repoAbs, _ := filepath.Abs(repoRoot)
	out := make([]packSeedRoot, 0, len(roots))
	for _, r := range roots {
		trusted := repoAbs != "" && strings.HasPrefix(r, repoAbs+string(os.PathSeparator))
		out = append(out, packSeedRoot{path: r, trusted: trusted})
	}
	return out
}

// skippedManifestCount best-effort counts the manifests an untrusted pack
// WOULD have registered, so the skipped tally stays manifest-granular. Falls
// back to 1 (dir-level) when the pack cannot be loaded.
func skippedManifestCount(packPath string) int {
	if pack, err := skillmfr.LoadPack(packPath); err == nil && len(pack.Manifests) > 0 {
		return len(pack.Manifests)
	}
	return 1
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

// packHasStudioManifests reports whether any manifest in the pack
// belongs to the Actium Studio surface (studio.* names). Used to gate
// registration on studio_config.enabled (audit A039).
func packHasStudioManifests(manifests []skillmfr.Manifest) bool {
	for _, m := range manifests {
		if strings.HasPrefix(m.Name, "studio.") {
			return true
		}
	}
	return false
}
