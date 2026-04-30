package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

type skillPackUpdateResult struct {
	PackName      string
	UpdatedCount  int
	UpdatedPacks  []skillPackUpdateEntry
	PulledGitDirs []string
}

type skillPackUpdateEntry struct {
	PackName          string
	SourcePath        string
	GitRoot           string
	PullStatus        string
	CanonicalLinkPath string
	LegacyLinkPath    string
}

type skillPackListResult struct {
	PackCount int
	Packs     []skillPackListEntry
}

type skillPackInfoResult struct {
	PackName            string
	SourcePath          string
	Version             string
	Description         string
	MinR1Version        string
	UpstreamAPIVersion  string
	DeclaredSkillCount  int
	ManifestCount       int
	Dependencies        []string
	CanonicalLinkPath   string
	LegacyLinkPath      string
	CanonicalInstalled  bool
	LegacyInstalled     bool
	InstalledSourcePath string
}

type skillPackPublishResult struct {
	PackName             string
	Version              string
	SourcePath           string
	CanonicalPublishPath string
	LegacyPublishPath    string
	ManifestCount        int
	DeclaredSkillCount   int
	Dependencies         []string
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
		fmt.Fprintln(os.Stderr, "skills pack: expected subcommand: info|install|list|publish|uninstall|update")
		os.Exit(2)
	}
	switch args[0] {
	case "info":
		runSkillsPackInfoCmd(args[1:])
	case "install":
		runSkillsPackInstallCmd(args[1:])
	case "list":
		runSkillsPackListCmd(args[1:])
	case "publish":
		runSkillsPackPublishCmd(args[1:])
	case "uninstall":
		runSkillsPackUninstallCmd(args[1:])
	case "update":
		runSkillsPackUpdateCmd(args[1:])
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

func runSkillsPackInfoCmd(args []string) {
	repoRoot, packName := parseSkillPackArgs("skills pack info", args)
	result, err := infoSkillPack(repoRoot, packName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack info: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout,
		"pack: %s\nsource: %s\nversion: %s\ndescription: %s\nmin_r1_version: %s\nupstream_api_version: %s\ndeclared_skill_count: %d\nmanifest_count: %d\ndependencies: %s\ncanonical_link: %s\ncanonical_installed: %t\nlegacy_link: %s\nlegacy_installed: %t\ninstalled_source: %s\n",
		result.PackName,
		result.SourcePath,
		result.Version,
		result.Description,
		result.MinR1Version,
		result.UpstreamAPIVersion,
		result.DeclaredSkillCount,
		result.ManifestCount,
		strings.Join(result.Dependencies, ","),
		result.CanonicalLinkPath,
		result.CanonicalInstalled,
		result.LegacyLinkPath,
		result.LegacyInstalled,
		result.InstalledSourcePath,
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

func runSkillsPackUpdateCmd(args []string) {
	repoRoot, packName := parseSkillPackArgs("skills pack update", args)
	result, err := updateSkillPack(repoRoot, packName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack update: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "pack: %s\nupdated: %d\npulled_git_dirs: %s\n",
		result.PackName,
		result.UpdatedCount,
		strings.Join(result.PulledGitDirs, ","),
	)
	for _, pack := range result.UpdatedPacks {
		fmt.Fprintf(os.Stdout,
			"updated_pack: %s\nsource: %s\ngit_root: %s\npull_status: %s\ncanonical_link: %s\nlegacy_link: %s\n",
			pack.PackName,
			pack.SourcePath,
			pack.GitRoot,
			pack.PullStatus,
			pack.CanonicalLinkPath,
			pack.LegacyLinkPath,
		)
	}
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

func runSkillsPackPublishCmd(args []string) {
	repoRoot, packName, destRoot, force := parseSkillPackPublishArgs(args)
	result, err := publishSkillPack(repoRoot, packName, destRoot, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills pack publish: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout,
		"pack: %s\nversion: %s\nsource: %s\npublished_manifest_count: %d\ndeclared_skill_count: %d\ndependencies: %s\ncanonical_publish_path: %s\nlegacy_publish_path: %s\n",
		result.PackName,
		result.Version,
		result.SourcePath,
		result.ManifestCount,
		result.DeclaredSkillCount,
		strings.Join(result.Dependencies, ","),
		result.CanonicalPublishPath,
		result.LegacyPublishPath,
	)
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

func parseSkillPackPublishArgs(args []string) (string, string, string, bool) {
	fs := flag.NewFlagSet("skills pack publish", flag.ExitOnError)
	repoRoot := fs.String("repo", ".", "repository root")
	packName := fs.String("pack", "", "pack name under repo or user .r1|.stoke/skills/packs/")
	destRoot := fs.String("dest-root", "", "destination root that receives .r1/.stoke skill pack copies (defaults to HOME)")
	force := fs.Bool("force", false, "replace an already-published pack in the destination library")
	fs.Parse(args)
	if *packName == "" {
		fmt.Fprintln(os.Stderr, "skills pack publish: --pack is required")
		os.Exit(2)
	}
	return *repoRoot, *packName, *destRoot, *force
}

func infoSkillPack(repoRoot, packName string) (*skillPackInfoResult, error) {
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
	installedSourcePath, canonicalInstalled, legacyInstalled, err := installedSkillPackState(repoAbs, pack.Meta.Name)
	if err != nil {
		return nil, err
	}
	return &skillPackInfoResult{
		PackName:            pack.Meta.Name,
		SourcePath:          sourcePath,
		Version:             pack.Meta.Version,
		Description:         pack.Meta.Description,
		MinR1Version:        pack.Meta.MinR1Version,
		UpstreamAPIVersion:  pack.Meta.UpstreamAPIVersion,
		DeclaredSkillCount:  pack.Meta.SkillCount,
		ManifestCount:       len(pack.Manifests),
		Dependencies:        append([]string(nil), pack.Meta.Dependencies...),
		CanonicalLinkPath:   filepath.Join(repoAbs, r1dir.Canonical, "skills", pack.Meta.Name),
		LegacyLinkPath:      filepath.Join(repoAbs, r1dir.Legacy, "skills", pack.Meta.Name),
		CanonicalInstalled:  canonicalInstalled,
		LegacyInstalled:     legacyInstalled,
		InstalledSourcePath: installedSourcePath,
	}, nil
}

func publishSkillPack(repoRoot, packName, destRoot string, force bool) (*skillPackPublishResult, error) {
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
	destAbs, err := resolveSkillPackPublishRoot(destRoot)
	if err != nil {
		return nil, err
	}
	canonicalPublishPath := filepath.Join(destAbs, r1dir.Canonical, "skills", "packs", pack.Meta.Name)
	legacyPublishPath := filepath.Join(destAbs, r1dir.Legacy, "skills", "packs", pack.Meta.Name)
	for _, publishPath := range []string{canonicalPublishPath, legacyPublishPath} {
		if err := publishSkillPackDir(sourcePath, publishPath, force); err != nil {
			return nil, err
		}
	}
	return &skillPackPublishResult{
		PackName:             pack.Meta.Name,
		Version:              pack.Meta.Version,
		SourcePath:           sourcePath,
		CanonicalPublishPath: canonicalPublishPath,
		LegacyPublishPath:    legacyPublishPath,
		ManifestCount:        len(pack.Manifests),
		DeclaredSkillCount:   pack.Meta.SkillCount,
		Dependencies:         append([]string(nil), pack.Meta.Dependencies...),
	}, nil
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

func updateSkillPack(repoRoot, packName string) (*skillPackUpdateResult, error) {
	if packName == "" {
		return nil, fmt.Errorf("pack name required")
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	updated := make(map[string]skillPackUpdateEntry)
	gitRefresh := make(map[string]skillPackRefreshState)
	if err := updateSkillPackRecursive(repoAbs, packName, updated, gitRefresh, nil); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(updated))
	for name := range updated {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]skillPackUpdateEntry, 0, len(names))
	for _, name := range names {
		rows = append(rows, updated[name])
	}
	pulledGitDirs := make([]string, 0, len(gitRefresh))
	for gitRoot, state := range gitRefresh {
		if state.PullStatus == skillPackPullStatusPulled {
			pulledGitDirs = append(pulledGitDirs, gitRoot)
		}
	}
	sort.Strings(pulledGitDirs)
	return &skillPackUpdateResult{
		PackName:      packName,
		UpdatedCount:  len(rows),
		UpdatedPacks:  rows,
		PulledGitDirs: pulledGitDirs,
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

func resolveSkillPackPublishRoot(destRoot string) (string, error) {
	if strings.TrimSpace(destRoot) != "" {
		abs, err := filepath.Abs(destRoot)
		if err != nil {
			return "", fmt.Errorf("resolve publish root: %w", err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve publish root from HOME: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve publish root from HOME: empty home directory")
	}
	return home, nil
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

func publishSkillPackDir(sourcePath, publishPath string, force bool) error {
	if err := ensurePublishTargetReady(publishPath, force); err != nil {
		return err
	}
	if err := copySkillPackTree(sourcePath, publishPath); err != nil {
		return err
	}
	return nil
}

func ensurePublishTargetReady(path string, force bool) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !force {
			return fmt.Errorf("published pack target %q already exists; rerun with --force to replace it", path)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("published pack target %q is a symlink; remove it manually before publishing", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("published pack target %q exists and is not a directory", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove existing published pack %q: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf("stat published pack target %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	return nil
}

func copySkillPackTree(sourcePath, destPath string) error {
	return filepath.WalkDir(sourcePath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return fmt.Errorf("relative publish path for %q: %w", path, err)
		}
		targetPath := filepath.Join(destPath, rel)
		switch {
		case d.IsDir():
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", targetPath, err)
			}
			return nil
		case d.Type()&os.ModeSymlink != 0:
			return fmt.Errorf("publish pack source %q contains symlink %q", sourcePath, path)
		case !d.Type().IsRegular():
			return fmt.Errorf("publish pack source %q contains unsupported file %q", sourcePath, path)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if err := os.WriteFile(targetPath, payload, 0o644); err != nil {
			return fmt.Errorf("write %q: %w", targetPath, err)
		}
		return nil
	})
}

const (
	skillPackPullStatusPulled            = "pulled"
	skillPackPullStatusSkippedNoGit      = "skipped_no_git"
	skillPackPullStatusSkippedNoUpstream = "skipped_no_upstream"
	skillPackPullStatusSkippedRepoLocal  = "skipped_repo_local"
)

type skillPackRefreshState struct {
	GitRoot    string
	PullStatus string
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

func updateSkillPackRecursive(repoRoot, packName string, updated map[string]skillPackUpdateEntry, gitRefresh map[string]skillPackRefreshState, stack []string) error {
	if _, ok := updated[packName]; ok {
		return nil
	}
	for _, active := range stack {
		if active == packName {
			cycle := append(append([]string{}, stack...), packName)
			return fmt.Errorf("skill pack dependency cycle: %s", strings.Join(cycle, " -> "))
		}
	}
	sourcePath, err := resolveInstalledOrSourceSkillPack(repoRoot, packName)
	if err != nil {
		return err
	}
	refreshState, err := refreshSkillPackSource(repoRoot, sourcePath, gitRefresh)
	if err != nil {
		return err
	}
	pack, err := skillmfr.LoadPack(sourcePath)
	if err != nil {
		return err
	}
	stack = append(stack, packName)
	for _, dependency := range pack.Meta.Dependencies {
		if err := updateSkillPackRecursive(repoRoot, dependency, updated, gitRefresh, stack); err != nil {
			return fmt.Errorf("update dependency %q for pack %q: %w", dependency, packName, err)
		}
	}
	canonicalLink := filepath.Join(repoRoot, r1dir.Canonical, "skills", pack.Meta.Name)
	legacyLink := filepath.Join(repoRoot, r1dir.Legacy, "skills", pack.Meta.Name)
	if err := ensureSkillPackLink(canonicalLink, sourcePath); err != nil {
		return err
	}
	if err := ensureSkillPackLink(legacyLink, sourcePath); err != nil {
		return err
	}
	updated[pack.Meta.Name] = skillPackUpdateEntry{
		PackName:          pack.Meta.Name,
		SourcePath:        sourcePath,
		GitRoot:           refreshState.GitRoot,
		PullStatus:        refreshState.PullStatus,
		CanonicalLinkPath: canonicalLink,
		LegacyLinkPath:    legacyLink,
	}
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

func resolveInstalledOrSourceSkillPack(repoRoot, packName string) (string, error) {
	installedSource, installed, err := resolveInstalledSkillPackSource(repoRoot, packName)
	if err != nil {
		return "", err
	}
	if installed {
		return installedSource, nil
	}
	return resolveSkillPackSource(repoRoot, packName)
}

func resolveInstalledSkillPackSource(repoRoot, packName string) (string, bool, error) {
	linkPaths := []string{
		filepath.Join(repoRoot, r1dir.Canonical, "skills", packName),
		filepath.Join(repoRoot, r1dir.Legacy, "skills", packName),
	}
	var sourcePath string
	found := false
	for _, linkPath := range linkPaths {
		foundPack, resolvedSource, ok, err := readInstalledSkillPackLink(linkPath)
		if err != nil {
			return "", false, err
		}
		if !ok {
			continue
		}
		if foundPack != packName {
			return "", false, fmt.Errorf("pack link %q resolved to %q, want %q", linkPath, foundPack, packName)
		}
		if !found {
			sourcePath = resolvedSource
			found = true
			continue
		}
		if sourcePath != resolvedSource {
			return "", false, fmt.Errorf("installed pack %q points to multiple sources: %q and %q", packName, sourcePath, resolvedSource)
		}
	}
	return sourcePath, found, nil
}

func installedSkillPackState(repoRoot, packName string) (string, bool, bool, error) {
	canonicalLink := filepath.Join(repoRoot, r1dir.Canonical, "skills", packName)
	legacyLink := filepath.Join(repoRoot, r1dir.Legacy, "skills", packName)
	canonicalPack, canonicalSource, canonicalInstalled, err := readInstalledSkillPackLink(canonicalLink)
	if err != nil {
		return "", false, false, err
	}
	if canonicalInstalled && canonicalPack != packName {
		return "", false, false, fmt.Errorf("pack link %q resolved to %q, want %q", canonicalLink, canonicalPack, packName)
	}
	legacyPack, legacySource, legacyInstalled, err := readInstalledSkillPackLink(legacyLink)
	if err != nil {
		return "", false, false, err
	}
	if legacyInstalled && legacyPack != packName {
		return "", false, false, fmt.Errorf("pack link %q resolved to %q, want %q", legacyLink, legacyPack, packName)
	}
	switch {
	case canonicalInstalled && legacyInstalled && canonicalSource != legacySource:
		return "", false, false, fmt.Errorf("installed pack %q points to multiple sources: %q and %q", packName, canonicalSource, legacySource)
	case canonicalInstalled:
		return canonicalSource, true, legacyInstalled, nil
	case legacyInstalled:
		return legacySource, canonicalInstalled, true, nil
	default:
		return "", false, false, nil
	}
}

func refreshSkillPackSource(repoRoot, sourcePath string, gitRefresh map[string]skillPackRefreshState) (skillPackRefreshState, error) {
	if pathWithin(repoRoot, sourcePath) {
		return skillPackRefreshState{PullStatus: skillPackPullStatusSkippedRepoLocal}, nil
	}
	gitRoot, ok, err := gitTopLevel(sourcePath)
	if err != nil {
		return skillPackRefreshState{}, err
	}
	if !ok {
		return skillPackRefreshState{PullStatus: skillPackPullStatusSkippedNoGit}, nil
	}
	if state, ok := gitRefresh[gitRoot]; ok {
		return state, nil
	}
	if pathWithin(repoRoot, gitRoot) {
		state := skillPackRefreshState{GitRoot: gitRoot, PullStatus: skillPackPullStatusSkippedRepoLocal}
		gitRefresh[gitRoot] = state
		return state, nil
	}
	upstreamConfigured, err := gitHasUpstream(gitRoot)
	if err != nil {
		return skillPackRefreshState{}, err
	}
	if !upstreamConfigured {
		state := skillPackRefreshState{GitRoot: gitRoot, PullStatus: skillPackPullStatusSkippedNoUpstream}
		gitRefresh[gitRoot] = state
		return state, nil
	}
	cmd := exec.Command("git", "-C", gitRoot, "pull", "--ff-only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return skillPackRefreshState{}, fmt.Errorf("git pull --ff-only in %q: %w: %s", gitRoot, err, strings.TrimSpace(string(out)))
	}
	state := skillPackRefreshState{GitRoot: gitRoot, PullStatus: skillPackPullStatusPulled}
	gitRefresh[gitRoot] = state
	return state, nil
}

func gitTopLevel(dir string) (string, bool, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "not a git repository") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git rev-parse --show-toplevel in %q: %w: %s", dir, err, strings.TrimSpace(text))
	}
	return strings.TrimSpace(string(out)), true, nil
}

func gitHasUpstream(dir string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "no upstream configured") || strings.Contains(text, "no upstream") {
			return false, nil
		}
		return false, fmt.Errorf("git rev-parse @{upstream} in %q: %w: %s", dir, err, strings.TrimSpace(text))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func pathWithin(root, target string) bool {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)
	rel, err := filepath.Rel(rootClean, targetClean)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
