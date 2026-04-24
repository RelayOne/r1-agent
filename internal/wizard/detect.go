package wizard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// GitStats captures heuristics from git history for auto-detection.
type GitStats struct {
	CommitCount      int
	ContributorCount int
	HasCI            bool
	HasTests         bool
	TestFileRatio    float64 // test files / total files
	IsMonorepo       bool
}

// DetectGitStats extracts project maturity signals from git history.
func DetectGitStats(projectDir string) GitStats {
	var stats GitStats

	// Commit count
	if out, err := exec.Command("git", "-C", projectDir, "rev-list", "--count", "HEAD").Output(); err == nil { // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			stats.CommitCount = n
		}
	}

	// Contributor count
	if out, err := exec.Command("git", "-C", projectDir, "shortlog", "-sn", "--no-merges", "HEAD").Output(); err == nil { // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
		stats.ContributorCount = len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	}

	// CI presence
	ciPaths := []string{
		".github/workflows",
		".gitlab-ci.yml",
		".circleci",
		"Jenkinsfile",
		".travis.yml",
		"azure-pipelines.yml",
	}
	for _, p := range ciPaths {
		if _, err := os.Stat(filepath.Join(projectDir, p)); err == nil {
			stats.HasCI = true
			break
		}
	}

	// Test file detection and ratio
	var totalFiles, testFiles int
	filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			// Skip .git and vendor
			name := info.Name()
			if name == ".git" || name == detectVendor || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".go" || ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" ||
			ext == ".py" || ext == ".rs" || ext == ".java" || ext == ".rb" {
			totalFiles++
			base := filepath.Base(path)
			if strings.Contains(base, "_test.") || strings.Contains(base, ".test.") ||
				strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") ||
				strings.Contains(path, "__tests__") || strings.Contains(path, "/tests/") {
				testFiles++
				stats.HasTests = true
			}
		}
		return nil
	})
	if totalFiles > 0 {
		stats.TestFileRatio = float64(testFiles) / float64(totalFiles)
	}

	// Monorepo detection
	monorepoSignals := []string{"turbo.json", "nx.json", "pnpm-workspace.yaml", "lerna.json"}
	for _, s := range monorepoSignals {
		if _, err := os.Stat(filepath.Join(projectDir, s)); err == nil {
			stats.IsMonorepo = true
			break
		}
	}
	// Multiple go.mod files = Go monorepo
	goModCount := 0
	filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			name := info.Name()
			if name == ".git" || name == detectVendor || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() == "go.mod" {
			goModCount++
		}
		return nil
	})
	if goModCount > 1 {
		stats.IsMonorepo = true
	}

	return stats
}

// InferStage guesses the project stage from git stats.
func InferStage(stats GitStats) ScaleTier {
	switch {
	case stats.CommitCount > 2000 || stats.ContributorCount > 15:
		return ScaleEnterprise
	case stats.CommitCount > 500 || stats.ContributorCount > 5:
		return ScaleGrowth
	case stats.CommitCount > 50 || stats.ContributorCount > 2:
		return ScaleStartup
	default:
		return ScalePrototype
	}
}

// InferTeamSize guesses team size from contributor count.
func InferTeamSize(stats GitStats) string {
	switch {
	case stats.ContributorCount > 20:
		return teamSize20Plus
	case stats.ContributorCount > 5:
		return teamSize6to20
	case stats.ContributorCount > 1:
		return teamSize2to5
	default:
		return teamSizeSolo
	}
}
