package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skillmfr"
)

type skillPackInstallResult struct {
	PackName          string
	SourcePath        string
	CanonicalLinkPath string
	LegacyLinkPath    string
	InstalledCount    int
}

func skillsCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "skills: expected subcommand: pack")
		os.Exit(2)
	}
	switch args[0] {
	case "pack":
		skillsPackCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skills: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func skillsPackCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "skills pack: expected subcommand: install")
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		runSkillsPackInstallCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skills pack: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func runSkillsPackInstallCmd(args []string) {
	fs := flag.NewFlagSet("skills pack install", flag.ExitOnError)
	repoRoot := fs.String("repo", ".", "repository root")
	packName := fs.String("pack", "", "pack name under <repo>/.r1|.stoke/skills/packs/")
	fs.Parse(args)
	if *packName == "" {
		fmt.Fprintln(os.Stderr, "skills pack install: --pack is required")
		os.Exit(2)
	}
	result, err := installSkillPack(*repoRoot, *packName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack install: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "pack: %s\nsource: %s\ninstalled: %d\ncanonical_link: %s\nlegacy_link: %s\n",
		result.PackName, result.SourcePath, result.InstalledCount, result.CanonicalLinkPath, result.LegacyLinkPath)
}

func installSkillPack(repoRoot, packName string) (*skillPackInstallResult, error) {
	if packName == "" {
		return nil, fmt.Errorf("pack name required")
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	sourcePath, err := resolveSkillPackSource(repoAbs, packName)
	if err != nil {
		return nil, err
	}
	pack, err := skillmfr.LoadPack(sourcePath)
	if err != nil {
		return nil, err
	}

	canonicalLink := filepath.Join(repoAbs, r1dir.Canonical, "skills", packName)
	legacyLink := filepath.Join(repoAbs, r1dir.Legacy, "skills", packName)
	if err := ensureSkillPackLink(canonicalLink, sourcePath); err != nil {
		return nil, err
	}
	if err := ensureSkillPackLink(legacyLink, sourcePath); err != nil {
		return nil, err
	}

	return &skillPackInstallResult{
		PackName:          pack.Meta.Name,
		SourcePath:        sourcePath,
		CanonicalLinkPath: canonicalLink,
		LegacyLinkPath:    legacyLink,
		InstalledCount:    2,
	}, nil
}

func resolveSkillPackSource(repoRoot, packName string) (string, error) {
	candidates := []string{
		filepath.Join(repoRoot, r1dir.Canonical, "skills", "packs", packName),
		filepath.Join(repoRoot, r1dir.Legacy, "skills", "packs", packName),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("stat pack %q: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("skill pack %q not found under %s or %s", packName, candidates[0], candidates[1])
}

func ensureSkillPackLink(linkPath, sourcePath string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(linkPath), err)
	}
	relTarget, err := filepath.Rel(filepath.Dir(linkPath), sourcePath)
	if err != nil {
		return fmt.Errorf("relative link %q -> %q: %w", linkPath, sourcePath, err)
	}
	info, err := os.Lstat(linkPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("target %q exists and is not a symlink", linkPath)
		}
		current, err := os.Readlink(linkPath)
		if err != nil {
			return fmt.Errorf("readlink %q: %w", linkPath, err)
		}
		if current == relTarget {
			return nil
		}
		return fmt.Errorf("target %q already points to %q, want %q", linkPath, current, relTarget)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", linkPath, err)
	}
	if err := os.Symlink(relTarget, linkPath); err != nil {
		return fmt.Errorf("symlink %q -> %q: %w", linkPath, relTarget, err)
	}
	return nil
}
