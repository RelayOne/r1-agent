package wizard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ericmacdougall/stoke/internal/skillselect"
)

// MaturityClassification is the inferred project stage with score breakdown.
// Each of the 8 signals contributes a weighted percentage to the composite score.
type MaturityClassification struct {
	Stage     string         `json:"stage"` // prototype|mvp|growth|mature
	Score     int            `json:"score"` // 0-100 composite
	Breakdown map[string]int `json:"breakdown"`
}

// InferMaturity scans the repo for 8 signals of project maturity:
//
//	Git activity (commits, contributors)       15%
//	PR/review process                          15%
//	Test coverage / test file presence         15%
//	CI/CD sophistication                       15%
//	Documentation                              10%
//	Security posture                           10%
//	Dependency management                      10%
//	Monitoring/observability                   10%
//
// Each signal scores 0-100 and contributes its weight to the composite.
// Composite is mapped: 0-20 prototype, 21-40 mvp, 41-70 growth, 71-100 mature.
func InferMaturity(root string, profile *skillselect.RepoProfile) MaturityClassification {
	breakdown := make(map[string]int)
	total := 0

	gitScore := scoreGitActivity(root)
	breakdown["git_activity"] = gitScore
	total += gitScore * 15 / 100

	reviewScore := scoreReviewProcess(root)
	breakdown["review_process"] = reviewScore
	total += reviewScore * 15 / 100

	testScore := scoreTests(root)
	breakdown["tests"] = testScore
	total += testScore * 15 / 100

	ciScore := scoreCI(root, profile)
	breakdown["ci_cd"] = ciScore
	total += ciScore * 15 / 100

	docScore := scoreDocs(root)
	breakdown["docs"] = docScore
	total += docScore * 10 / 100

	secScore := scoreSecurity(root)
	breakdown["security"] = secScore
	total += secScore * 10 / 100

	depScore := scoreDependencies(root)
	breakdown["dependencies"] = depScore
	total += depScore * 10 / 100

	obsScore := scoreObservability(root)
	breakdown["observability"] = obsScore
	total += obsScore * 10 / 100

	stage := "prototype"
	switch {
	case total >= 71:
		stage = "mature"
	case total >= 41:
		stage = "growth"
	case total >= 21:
		stage = "mvp"
	}

	return MaturityClassification{
		Stage:     stage,
		Score:     total,
		Breakdown: breakdown,
	}
}

func scoreGitActivity(root string) int {
	if !isGitRepo(root) {
		return 0
	}
	commits := countGitCommits(root)
	contributors := countGitContributors(root)

	score := 0
	switch {
	case commits >= 5000:
		score += 50
	case commits >= 500:
		score += 35
	case commits >= 50:
		score += 15
	case commits >= 10:
		score += 5
	}
	switch {
	case contributors >= 20:
		score += 50
	case contributors >= 5:
		score += 30
	case contributors >= 2:
		score += 10
	}
	if score > 100 {
		score = 100
	}
	return score
}

func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

func countGitCommits(root string) int {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func countGitContributors(root string) int {
	cmd := exec.Command("git", "shortlog", "-sn", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return 0
	}
	return strings.Count(lines, "\n") + 1
}

func scoreReviewProcess(root string) int {
	score := 0
	if maturityExists(filepath.Join(root, "CODEOWNERS")) ||
		maturityExists(filepath.Join(root, ".github/CODEOWNERS")) ||
		maturityExists(filepath.Join(root, "docs/CODEOWNERS")) {
		score += 30
	}
	if maturityExists(filepath.Join(root, ".github/PULL_REQUEST_TEMPLATE.md")) ||
		maturityExists(filepath.Join(root, ".github/pull_request_template.md")) {
		score += 20
	}
	if maturityExists(filepath.Join(root, ".github/ISSUE_TEMPLATE")) {
		score += 10
	}
	if maturityExists(filepath.Join(root, "CONTRIBUTING.md")) {
		score += 20
	}
	if maturityExists(filepath.Join(root, ".github/workflows")) {
		score += 20
	}
	return capScore(score)
}

func scoreTests(root string) int {
	testFiles := 0
	sourceFiles := 0
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "target" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".test.tsx") ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.jsx") ||
			strings.HasSuffix(name, ".spec.ts") || strings.HasSuffix(name, ".spec.tsx") ||
			strings.HasSuffix(name, ".spec.js") || strings.HasSuffix(name, ".spec.jsx") ||
			strings.HasSuffix(name, "_test.py") || strings.HasPrefix(name, "test_") {
			testFiles++
		} else if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".ts") ||
			strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".js") ||
			strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".rs") {
			sourceFiles++
		}
		return nil
	})
	if sourceFiles == 0 {
		return 0
	}
	ratio := float64(testFiles) / float64(sourceFiles)
	score := int(ratio * 200) // 0.5 ratio = 100 score
	return capScore(score)
}

func scoreCI(root string, profile *skillselect.RepoProfile) int {
	score := 0
	if profile.HasCI {
		score += 50
	}
	if maturityExists(filepath.Join(root, ".github/workflows")) {
		entries, _ := os.ReadDir(filepath.Join(root, ".github/workflows"))
		if len(entries) >= 3 {
			score += 20
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(root, ".github/workflows", e.Name()))
			content := string(data)
			if strings.Contains(content, "snyk") || strings.Contains(content, "trivy") ||
				strings.Contains(content, "codeql") || strings.Contains(content, "semgrep") {
				score += 15
			}
			if strings.Contains(content, "deploy") {
				score += 15
			}
		}
	}
	return capScore(score)
}

func scoreDocs(root string) int {
	score := 0
	if maturityExists(filepath.Join(root, "README.md")) {
		score += 30
	}
	if maturityExists(filepath.Join(root, "docs")) {
		score += 30
	}
	if maturityExists(filepath.Join(root, "ARCHITECTURE.md")) ||
		maturityExists(filepath.Join(root, "docs/architecture.md")) ||
		maturityExists(filepath.Join(root, "docs/architecture")) {
		score += 20
	}
	if maturityExists(filepath.Join(root, "CHANGELOG.md")) {
		score += 10
	}
	if maturityExists(filepath.Join(root, "CONTRIBUTING.md")) {
		score += 10
	}
	return capScore(score)
}

func scoreSecurity(root string) int {
	score := 0
	if maturityExists(filepath.Join(root, "SECURITY.md")) {
		score += 20
	}
	if maturityExists(filepath.Join(root, ".github/dependabot.yml")) ||
		maturityExists(filepath.Join(root, ".github/dependabot.yaml")) {
		score += 20
	}
	if maturityExists(filepath.Join(root, ".github/workflows")) {
		entries, _ := os.ReadDir(filepath.Join(root, ".github/workflows"))
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(root, ".github/workflows", e.Name()))
			if strings.Contains(string(data), "security") || strings.Contains(string(data), "codeql") {
				score += 30
				break
			}
		}
	}
	if maturityExists(filepath.Join(root, ".pre-commit-config.yaml")) {
		score += 15
	}
	if maturityExists(filepath.Join(root, ".gitleaks.toml")) ||
		maturityExists(filepath.Join(root, ".trufflehog.yml")) {
		score += 15
	}
	return capScore(score)
}

func scoreDependencies(root string) int {
	score := 0
	lockfiles := []string{
		"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb",
		"go.sum", "Cargo.lock", "Pipfile.lock", "poetry.lock", "uv.lock",
	}
	for _, l := range lockfiles {
		if maturityExists(filepath.Join(root, l)) {
			score += 30
			break
		}
	}
	if maturityExists(filepath.Join(root, ".github/dependabot.yml")) ||
		maturityExists(filepath.Join(root, "renovate.json")) {
		score += 40
	}
	if maturityExists(filepath.Join(root, "sbom.json")) ||
		maturityExists(filepath.Join(root, ".sbom")) {
		score += 30
	}
	return capScore(score)
}

func scoreObservability(root string) int {
	score := 0
	obsKeywords := []string{"opentelemetry", "@opentelemetry", "sentry", "datadog", "newrelic", "prometheus"}
	for _, manifest := range []string{"package.json", "go.mod", "pyproject.toml", "requirements.txt"} {
		if data, err := os.ReadFile(filepath.Join(root, manifest)); err == nil {
			content := strings.ToLower(string(data))
			for _, kw := range obsKeywords {
				if strings.Contains(content, kw) {
					score += 20
				}
			}
		}
	}
	return capScore(score)
}

func maturityExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func capScore(score int) int {
	if score > 100 {
		return 100
	}
	return score
}
