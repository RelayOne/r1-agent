package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/skillmfr"
)

type skillPackInstallResult struct {
	PackName          string
	SourcePath        string
	CanonicalLinkPath string
	LegacyLinkPath    string
	InstalledCount    int
	InstalledPacks    []string
}

type skillPackUninstallResult struct {
	PackName          string
	CanonicalLinkPath string
	LegacyLinkPath    string
	RemovedCount      int
	RemovedPaths      []string
}

type skillPackListResult struct {
	PackCount int
	Packs     []skillPackListEntry
}

type skillPackListEntry struct {
	PackName           string
	SourcePath         string
	CanonicalLinkPath  string
	LegacyLinkPath     string
	CanonicalInstalled bool
	LegacyInstalled    bool
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
		fmt.Fprintln(os.Stderr, "skills pack: expected subcommand: install|list|uninstall")
		os.Exit(2)
	}
	switch args[0] {
	case "install":
		runSkillsPackInstallCmd(args[1:])
	case "list":
		runSkillsPackListCmd(args[1:])
	case "uninstall":
		runSkillsPackUninstallCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skills pack: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func runSkillsPackInstallCmd(args []string) {
	repoRoot, packName := parseSkillPackArgs("skills pack install", args)
	result, err := installSkillPack(repoRoot, packName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack install: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "pack: %s\nsource: %s\ninstalled: %d\npacks: %s\ncanonical_link: %s\nlegacy_link: %s\n",
		result.PackName,
		result.SourcePath,
		result.InstalledCount,
		strings.Join(result.InstalledPacks, ","),
		result.CanonicalLinkPath,
		result.LegacyLinkPath,
	)
}

func runSkillsPackUninstallCmd(args []string) {
	repoRoot, packName := parseSkillPackArgs("skills pack uninstall", args)
	result, err := uninstallSkillPack(repoRoot, packName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack uninstall: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "pack: %s\nremoved: %d\npaths: %s\ncanonical_link: %s\nlegacy_link: %s\n",
		result.PackName,
		result.RemovedCount,
		strings.Join(result.RemovedPaths, ","),
		result.CanonicalLinkPath,
		result.LegacyLinkPath,
	)
}

func runSkillsPackListCmd(args []string) {
	repoRoot := parseSkillPackListArgs(args)
	result, err := listInstalledSkillPacks(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack list: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "pack_count: %d\n", result.PackCount)
	for _, pack := range result.Packs {
		fmt.Fprintf(os.Stdout,
			"pack: %s\nsource: %s\ncanonical_link: %s\ncanonical_installed: %t\nlegacy_link: %s\nlegacy_installed: %t\n",
			pack.PackName,
			pack.SourcePath,
			pack.CanonicalLinkPath,
			pack.CanonicalInstalled,
			pack.LegacyLinkPath,
			pack.LegacyInstalled,
		)
	}
}

func parseSkillPackArgs(name string, args []string) (string, string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	repoRoot := fs.String("repo", ".", "repository root")
	packName := fs.String("pack", "", "pack name under repo or user .r1|.stoke/skills/packs/")
	fs.Parse(args)
	if *packName == "" {
		fmt.Fprintf(os.Stderr, "%s: --pack is required\n", name)
		os.Exit(2)
	}
	return *repoRoot, *packName
}

func parseSkillPackListArgs(args []string) string {
	fs := flag.NewFlagSet("skills pack list", flag.ExitOnError)
	repoRoot := fs.String("repo", ".", "repository root")
	fs.Parse(args)
	return *repoRoot
}

func installSkillPack(repoRoot, packName string) (*skillPackInstallResult, error) {
	if packName == "" {
		return nil, fmt.Errorf("pack name required")
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	installedPacks := make(map[string]string)
	if err := installSkillPackRecursive(repoAbs, packName, installedPacks, nil); err != nil {
		return nil, err
	}
	sourcePath, err := resolveSkillPackSource(repoAbs, packName)
	if err != nil {
		return nil, err
	}
	canonicalLink := filepath.Join(repoAbs, r1dir.Canonical, "skills", packName)
	legacyLink := filepath.Join(repoAbs, r1dir.Legacy, "skills", packName)
	packs := make([]string, 0, len(installedPacks))
	for installedPack := range installedPacks {
		packs = append(packs, installedPack)
	}
	sort.Strings(packs)

	return &skillPackInstallResult{
		PackName:          packName,
		SourcePath:        sourcePath,
		CanonicalLinkPath: canonicalLink,
		LegacyLinkPath:    legacyLink,
		InstalledCount:    len(installedPacks) * 2,
		InstalledPacks:    packs,
	}, nil
}

func uninstallSkillPack(repoRoot, packName string) (*skillPackUninstallResult, error) {
	if packName == "" {
		return nil, fmt.Errorf("pack name required")
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	canonicalLink := filepath.Join(repoAbs, r1dir.Canonical, "skills", packName)
	legacyLink := filepath.Join(repoAbs, r1dir.Legacy, "skills", packName)
	linkPaths := []string{canonicalLink, legacyLink}

	removable := make([]string, 0, len(linkPaths))
	for _, linkPath := range linkPaths {
		ok, err := removableSkillPackLink(linkPath)
		if err != nil {
			return nil, err
		}
		if ok {
			removable = append(removable, linkPath)
		}
	}
	for _, linkPath := range removable {
		if err := os.Remove(linkPath); err != nil {
			return nil, fmt.Errorf("remove %q: %w", linkPath, err)
		}
	}
	return &skillPackUninstallResult{
		PackName:          packName,
		CanonicalLinkPath: canonicalLink,
		LegacyLinkPath:    legacyLink,
		RemovedCount:      len(removable),
		RemovedPaths:      removable,
	}, nil
}

func listInstalledSkillPacks(repoRoot string) (*skillPackListResult, error) {
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	packs := map[string]skillPackListEntry{}
	for _, rootName := range []string{r1dir.Canonical, r1dir.Legacy} {
		skillRoot := filepath.Join(repoAbs, rootName, "skills")
		if err := collectInstalledSkillPacks(skillRoot, rootName == r1dir.Canonical, packs); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(packs))
	for name := range packs {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]skillPackListEntry, 0, len(names))
	for _, name := range names {
		rows = append(rows, packs[name])
	}
	return &skillPackListResult{
		PackCount: len(rows),
		Packs:     rows,
	}, nil
}

func resolveSkillPackSource(repoRoot, packName string) (string, error) {
	candidates := skillPackCandidates(repoRoot, packName)
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("stat pack %q: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("skill pack %q not found under %s", packName, strings.Join(candidates, ", "))
}

func skillPackCandidates(repoRoot, packName string) []string {
	candidates := []string{
		filepath.Join(repoRoot, r1dir.Canonical, "skills", "packs", packName),
		filepath.Join(repoRoot, r1dir.Legacy, "skills", "packs", packName),
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, r1dir.Canonical, "skills", "packs", packName),
			filepath.Join(home, r1dir.Legacy, "skills", "packs", packName),
		)
	}
	return candidates
}

func installSkillPackRecursive(repoRoot, packName string, installed map[string]string, stack []string) error {
	if _, ok := installed[packName]; ok {
		return nil
	}
	for _, active := range stack {
		if active == packName {
			cycle := append(append([]string{}, stack...), packName)
			return fmt.Errorf("skill pack dependency cycle: %s", strings.Join(cycle, " -> "))
		}
	}
	sourcePath, err := resolveSkillPackSource(repoRoot, packName)
	if err != nil {
		return err
	}
	pack, err := skillmfr.LoadPack(sourcePath)
	if err != nil {
		return err
	}
	stack = append(stack, packName)
	for _, dependency := range pack.Meta.Dependencies {
		if err := installSkillPackRecursive(repoRoot, dependency, installed, stack); err != nil {
			return fmt.Errorf("install dependency %q for pack %q: %w", dependency, packName, err)
		}
	}
	canonicalLink := filepath.Join(repoRoot, r1dir.Canonical, "skills", packName)
	legacyLink := filepath.Join(repoRoot, r1dir.Legacy, "skills", packName)
	if err := ensureSkillPackLink(canonicalLink, sourcePath); err != nil {
		return err
	}
	if err := ensureSkillPackLink(legacyLink, sourcePath); err != nil {
		return err
	}
	installed[pack.Meta.Name] = sourcePath
	return nil
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

func removableSkillPackLink(linkPath string) (bool, error) {
	info, err := os.Lstat(linkPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %q: %w", linkPath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, fmt.Errorf("target %q exists and is not a symlink", linkPath)
	}
	return true, nil
}

func collectInstalledSkillPacks(skillRoot string, canonical bool, packs map[string]skillPackListEntry) error {
	repoRoot := filepath.Dir(filepath.Dir(skillRoot))
	entries, err := os.ReadDir(skillRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read skill root %q: %w", skillRoot, err)
	}
	for _, entry := range entries {
		if entry.Name() == "packs" {
			continue
		}
		linkPath := filepath.Join(skillRoot, entry.Name())
		packName, sourcePath, ok, err := readInstalledSkillPackLink(linkPath)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		row := packs[packName]
		if row.PackName == "" {
			row = skillPackListEntry{
				PackName:          packName,
				SourcePath:        sourcePath,
				CanonicalLinkPath: filepath.Join(repoRoot, r1dir.Canonical, "skills", packName),
				LegacyLinkPath:    filepath.Join(repoRoot, r1dir.Legacy, "skills", packName),
			}
		}
		if row.SourcePath != "" && row.SourcePath != sourcePath {
			return fmt.Errorf("installed pack %q points to multiple sources: %q and %q", packName, row.SourcePath, sourcePath)
		}
		if canonical {
			row.CanonicalInstalled = true
			row.CanonicalLinkPath = linkPath
		} else {
			row.LegacyInstalled = true
			row.LegacyLinkPath = linkPath
		}
		packs[packName] = row
	}
	return nil
}

func readInstalledSkillPackLink(linkPath string) (string, string, bool, error) {
	info, err := os.Lstat(linkPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("stat %q: %w", linkPath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", "", false, nil
	}
	sourcePath, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve symlink %q: %w", linkPath, err)
	}
	pack, err := skillmfr.LoadPack(sourcePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("load pack from %q: %w", sourcePath, err)
	}
	if pack.Meta.Name == "" {
		return "", "", false, fmt.Errorf("pack at %q has empty name", sourcePath)
	}
	if filepath.Base(linkPath) != pack.Meta.Name {
		return "", "", false, fmt.Errorf("pack link %q points to pack %q", linkPath, pack.Meta.Name)
	}
	return pack.Meta.Name, sourcePath, true, nil
}
