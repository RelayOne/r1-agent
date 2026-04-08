# 04 — Phase 2: Configuration Wizard

This phase creates `internal/wizard` — a configuration wizard that runs on first use and produces `.stoke/config.yaml`. The default mode is **detect first, ask only what can't be inferred**, following Vercel's "Detected X — is this correct?" pattern.

## Why this exists

From research [P72]: The best configuration wizards ask almost nothing. `npm init` asks 9 questions, `cargo init` asks zero, and `go mod init` takes a single argument. Tools like Vercel reduce 50 potential questions to 3–5 by aggressive auto-detection.

The wizard's value is not in the questions it asks but in the questions it **doesn't have to ask** because it inferred the answer from the repo.

## Architecture

```
stoke wizard --auto       (default, runs detection + presents proposal)
   │
   ├─ Phase 1: Static detection (file existence + manifest parsing)
   │  └─ uses internal/skillselect.DetectProfile (already built)
   │
   ├─ Phase 2: Heuristic inference (project maturity, scale, security posture)
   │  └─ internal/wizard.detect.go:InferMaturity, InferScale, InferSecurity
   │
   ├─ Phase 3: Optional research convergence (AI-powered, opt-in)
   │  └─ uses provider.AnthropicProvider to ask Claude "what config is best for this repo?"
   │  └─ only if --research flag or research_mode: true in user defaults
   │
   ├─ Phase 4: Proposal display (TUI via huh)
   │  └─ Shows detected stack, recommended skills, config summary
   │  └─ User can accept, modify (drops into question flow), or reject
   │
   └─ Phase 5: Output
      ├─ .stoke/config.yaml
      ├─ .stoke/skills/  (copies relevant skills from library)
      └─ .stoke/wizard-rationale.md (decision log)
```

## Package structure

```
internal/wizard/
  wizard.go       — main entrypoint, mode dispatcher
  questions.go    — question definitions, organized by category
  detect.go       — Phase 2 heuristics (maturity, scale, security)
  research.go     — Phase 3 research convergence (optional)
  proposal.go     — Phase 4 TUI proposal display
  output.go       — Phase 5 file writers
  rationale.go    — generates the decision log
  wizard_test.go
```

---

## Step 1: Add huh dependency

`huh` is the recommended library from the Charm ecosystem (Stoke already uses Bubble Tea from the same vendor). Add to `go.mod`:

```bash
go get github.com/charmbracelet/huh@latest
```

If for some reason huh can't be added (network restrictions during build), fall back to a stdin-based question loop. The interface should be the same.

---

## Step 2: Define WizardMode and Result types

**File:** `internal/wizard/wizard.go`

```go
// Package wizard implements an interactive configuration wizard for new Stoke
// projects. It detects the technology stack, infers project maturity, and
// produces a complete .stoke/config.yaml plus selected skill files.
//
// The default mode is auto-detect-then-confirm: scan the repo, propose a
// complete configuration, and ask the user only to approve or modify.
package wizard

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "time"

    "github.com/ericmacdougall/stoke/internal/skill"
    "github.com/ericmacdougall/stoke/internal/skillselect"
)

// Mode determines how the wizard interacts with the user.
type Mode string

const (
    ModeAuto        Mode = "auto"        // detect everything, present proposal, confirm
    ModeInteractive Mode = "interactive" // ask all questions
    ModeHybrid      Mode = "hybrid"      // detect what's detectable, ask the rest
    ModeYes         Mode = "yes"         // accept all defaults, no prompts (CI-safe)
)

// Opts configures a wizard run.
type Opts struct {
    ProjectRoot string
    Mode        Mode
    Research    bool          // enable AI-powered research convergence (Phase 3)
    Provider    Provider      // model provider for research (nil disables research)
    Stdin       *os.File      // for testing
    Stdout      *os.File
    Stderr      *os.File
}

// Provider is a minimal interface so wizard doesn't import provider package directly.
type Provider interface {
    Chat(ctx context.Context, system, user string) (string, error)
}

// Result is the output of a wizard run.
type Result struct {
    Profile      *skillselect.RepoProfile
    Maturity     MaturityClassification
    Config       Config
    Skills       []string         // skill names selected for installation
    Confidence   map[string]float64
    Rationale    []RationaleEntry
    GeneratedAt  time.Time
}

// RationaleEntry documents a single decision made during the wizard.
type RationaleEntry struct {
    Field      string
    Value      string
    Source     string  // "detected", "inferred", "user", "default", "research"
    Evidence   string
    Confidence float64
}

// Config is the structured representation of .stoke/config.yaml as produced
// by the wizard. It includes everything the wizard collects.
type Config struct {
    Project        ProjectConfig        `yaml:"project"`
    Models         ModelsConfig         `yaml:"models"`
    Quality        QualityConfig        `yaml:"quality"`
    Security       SecurityConfig       `yaml:"security"`
    Infrastructure InfrastructureConfig `yaml:"infrastructure"`
    Scale          ScaleConfig          `yaml:"scale"`
    Domains        []string             `yaml:"domains"`
    Skills         SkillsConfig         `yaml:"skills"`
    Team           TeamConfig           `yaml:"team"`
    Risk           RiskConfig           `yaml:"risk"`
}

type ProjectConfig struct {
    Name     string `yaml:"name"`
    Stage    string `yaml:"stage"`     // prototype|mvp|growth|scale|mature
    Monorepo bool   `yaml:"monorepo"`
}

type ModelsConfig struct {
    Strategy      string   `yaml:"strategy"`        // best_quality|cost_optimized|speed|balanced
    Subscriptions []string `yaml:"subscriptions"`    // claude-max, chatgpt-pro, etc.
    Execution     string   `yaml:"execution"`       // cli|api|hybrid
    Architect     string   `yaml:"architect"`        // claude|codex|gemini
    Editor        string   `yaml:"editor"`
    Reviewer      string   `yaml:"reviewer"`
}

type QualityConfig struct {
    Verification     string `yaml:"verification"`      // light|standard|thorough|maximum
    CodeQuality      string `yaml:"code_quality"`      // relaxed|standard|strict
    TestRequirements string `yaml:"test_requirements"` // minimal|standard|comprehensive
    ReviewMode       string `yaml:"review_mode"`       // self|cross_model|multi|human
    HonestyEnforce   string `yaml:"honesty_enforcement"` // light|strict|maximum
}

type SecurityConfig struct {
    Posture        string   `yaml:"posture"`         // basic|standard|high|regulated
    Compliance     []string `yaml:"compliance"`       // pipeda, casl, gdpr, pci_dss, soc2, coppa
    DataSensitivity string  `yaml:"data_sensitivity"` // public|internal|confidential|restricted
}

type InfrastructureConfig struct {
    Providers   []string `yaml:"providers"`
    IaC         string   `yaml:"iac"`         // terraform|pulumi|cdk|none
    Preference  string   `yaml:"preference"`   // agnostic|optimized|single|opensource_only|best_fit
    Credits     string   `yaml:"credits"`      // gcp|aws|azure|none
}

type ScaleConfig struct {
    Expected   string `yaml:"expected"`     // small|medium|large|very_large
    Latency    string `yaml:"latency"`      // tolerant|standard|sensitive
    DataVolume string `yaml:"data_volume"`  // small|medium|large|very_large
}

type SkillsConfig struct {
    Enabled      bool     `yaml:"enabled"`
    AlwaysOn     []string `yaml:"always_on"`
    AutoDetect   bool     `yaml:"auto_detect"`
    TokenBudget  int      `yaml:"token_budget"`
    ResearchFeed bool     `yaml:"research_feed"`
}

type TeamConfig struct {
    Size         string `yaml:"size"`           // solo|2-5|6-20|20+
    OpenSource   bool   `yaml:"open_source"`
    FeatureFlags bool   `yaml:"feature_flags"`
    I18n         string `yaml:"i18n"`           // none|bilingual|multi
}

type RiskConfig struct {
    Autonomy   string `yaml:"autonomy"`        // conservative|standard|permissive|yolo
    BlastRadius string `yaml:"blast_radius"`   // none|read_only|staging|limited_prod|full_prod
}

// Run executes the wizard with the given options.
func Run(ctx context.Context, opts Opts) (*Result, error) {
    if opts.ProjectRoot == "" {
        opts.ProjectRoot = "."
    }
    if opts.Mode == "" {
        opts.Mode = ModeAuto
    }
    if opts.Stdin == nil {
        opts.Stdin = os.Stdin
    }
    if opts.Stdout == nil {
        opts.Stdout = os.Stdout
    }
    if opts.Stderr == nil {
        opts.Stderr = os.Stderr
    }

    result := &Result{
        Confidence:  make(map[string]float64),
        GeneratedAt: time.Now(),
    }

    // Phase 1: Static detection
    profile, err := skillselect.DetectProfile(opts.ProjectRoot)
    if err != nil {
        return nil, fmt.Errorf("detect profile: %w", err)
    }
    result.Profile = profile

    // Phase 2: Heuristic inference
    result.Maturity = InferMaturity(opts.ProjectRoot, profile)
    suggestion := buildDefaultConfig(opts.ProjectRoot, profile, result.Maturity)
    result.Config = suggestion
    addRationale(result, profile, result.Maturity)

    // Phase 3: Optional research convergence
    if opts.Research && opts.Provider != nil {
        if err := runResearchConvergence(ctx, opts.Provider, result); err != nil {
            // Best-effort; do not fail the wizard
            fmt.Fprintf(opts.Stderr, "[wizard] research convergence failed: %v\n", err)
        }
    }

    // Phase 4: Mode-specific user interaction
    switch opts.Mode {
    case ModeYes:
        // Accept everything
    case ModeAuto, ModeHybrid:
        if err := presentProposal(opts, result); err != nil {
            return nil, err
        }
    case ModeInteractive:
        if err := runInteractiveQuestions(opts, result); err != nil {
            return nil, err
        }
    }

    // Phase 5: Pick skills based on final config
    result.Skills = selectSkillsFromConfig(result.Config, profile)

    // Phase 6: Write output
    if err := writeOutput(opts.ProjectRoot, result); err != nil {
        return nil, fmt.Errorf("write output: %w", err)
    }

    return result, nil
}
```

---

## Step 3: Implement maturity inference

**File:** `internal/wizard/detect.go`

```go
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
type MaturityClassification struct {
    Stage      string             // prototype|mvp|growth|scale|mature
    Score      int                // 0-100
    Breakdown  map[string]int     // signal → contributing score
}

// InferMaturity scans the repo for signals of project maturity. The signals
// follow the framework from research [P75]:
//
//   Git activity (commits, contributors)         15%
//   PR/review process                              15%
//   Test coverage / test file presence             15%
//   CI/CD sophistication                            15%
//   Documentation                                   10%
//   Security posture                                10%
//   Dependency management                           10%
//   Monitoring/observability                        10%
//
// Each signal scores 0-100 and contributes its weight to the composite score.
// The composite is mapped to stages: 0-20 prototype, 21-40 mvp, 41-70 growth, 71-100 mature.
func InferMaturity(root string, profile *skillselect.RepoProfile) MaturityClassification {
    breakdown := make(map[string]int)
    total := 0

    // Git activity
    gitScore := scoreGitActivity(root)
    breakdown["git_activity"] = gitScore
    total += gitScore * 15 / 100

    // PR/review process (proxy: presence of CODEOWNERS, PR templates, branch protection)
    reviewScore := scoreReviewProcess(root)
    breakdown["review_process"] = reviewScore
    total += reviewScore * 15 / 100

    // Tests
    testScore := scoreTests(root, profile)
    breakdown["tests"] = testScore
    total += testScore * 15 / 100

    // CI/CD
    ciScore := scoreCI(root, profile)
    breakdown["ci_cd"] = ciScore
    total += ciScore * 15 / 100

    // Documentation
    docScore := scoreDocs(root)
    breakdown["docs"] = docScore
    total += docScore * 10 / 100

    // Security
    secScore := scoreSecurity(root, profile)
    breakdown["security"] = secScore
    total += secScore * 10 / 100

    // Dependencies
    depScore := scoreDependencies(root, profile)
    breakdown["dependencies"] = depScore
    total += depScore * 10 / 100

    // Observability
    obsScore := scoreObservability(root, profile)
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
    // Count commits and contributors using git
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
    return strings.Count(string(out), "\n")
}

func scoreReviewProcess(root string) int {
    score := 0
    if exists(filepath.Join(root, "CODEOWNERS")) || exists(filepath.Join(root, ".github/CODEOWNERS")) || exists(filepath.Join(root, "docs/CODEOWNERS")) {
        score += 30
    }
    if exists(filepath.Join(root, ".github/PULL_REQUEST_TEMPLATE.md")) || exists(filepath.Join(root, ".github/pull_request_template.md")) {
        score += 20
    }
    if exists(filepath.Join(root, ".github/ISSUE_TEMPLATE")) {
        score += 10
    }
    if exists(filepath.Join(root, "CONTRIBUTING.md")) {
        score += 20
    }
    if exists(filepath.Join(root, ".github/workflows")) {
        score += 20
    }
    return min(score, 100)
}

func scoreTests(root string, profile *skillselect.RepoProfile) int {
    // Count test files of any extension
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
        // Test file detection
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
    if score > 100 {
        score = 100
    }
    return score
}

func scoreCI(root string, profile *skillselect.RepoProfile) int {
    score := 0
    if profile.HasCI {
        score += 50
    }
    // Check for sophistication: matrix builds, deployment, security scanning
    if exists(filepath.Join(root, ".github/workflows")) {
        entries, _ := os.ReadDir(filepath.Join(root, ".github/workflows"))
        if len(entries) >= 3 {
            score += 20
        }
        // Look for security scan / deploy keywords
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
    return min(score, 100)
}

func scoreDocs(root string) int {
    score := 0
    if exists(filepath.Join(root, "README.md")) {
        score += 30
    }
    if exists(filepath.Join(root, "docs")) {
        score += 30
    }
    if exists(filepath.Join(root, "ARCHITECTURE.md")) || exists(filepath.Join(root, "docs/architecture.md")) || exists(filepath.Join(root, "docs/architecture")) {
        score += 20
    }
    if exists(filepath.Join(root, "CHANGELOG.md")) {
        score += 10
    }
    if exists(filepath.Join(root, "CONTRIBUTING.md")) {
        score += 10
    }
    return min(score, 100)
}

func scoreSecurity(root string, profile *skillselect.RepoProfile) int {
    score := 0
    if exists(filepath.Join(root, "SECURITY.md")) {
        score += 20
    }
    if exists(filepath.Join(root, ".github/dependabot.yml")) || exists(filepath.Join(root, ".github/dependabot.yaml")) {
        score += 20
    }
    if exists(filepath.Join(root, ".github/workflows")) {
        entries, _ := os.ReadDir(filepath.Join(root, ".github/workflows"))
        for _, e := range entries {
            data, _ := os.ReadFile(filepath.Join(root, ".github/workflows", e.Name()))
            if strings.Contains(string(data), "security") || strings.Contains(string(data), "codeql") {
                score += 30
                break
            }
        }
    }
    if exists(filepath.Join(root, ".pre-commit-config.yaml")) {
        score += 15
    }
    if exists(filepath.Join(root, ".gitleaks.toml")) || exists(filepath.Join(root, ".trufflehog.yml")) {
        score += 15
    }
    return min(score, 100)
}

func scoreDependencies(root string, profile *skillselect.RepoProfile) int {
    score := 0
    // Lockfile presence
    lockfiles := []string{
        "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb",
        "go.sum", "Cargo.lock", "Pipfile.lock", "poetry.lock", "uv.lock",
    }
    for _, l := range lockfiles {
        if exists(filepath.Join(root, l)) {
            score += 30
            break
        }
    }
    // Renovate / Dependabot
    if exists(filepath.Join(root, ".github/dependabot.yml")) || exists(filepath.Join(root, "renovate.json")) {
        score += 40
    }
    // SBOM
    if exists(filepath.Join(root, "sbom.json")) || exists(filepath.Join(root, ".sbom")) {
        score += 30
    }
    return min(score, 100)
}

func scoreObservability(root string, profile *skillselect.RepoProfile) int {
    score := 0
    // Check for OpenTelemetry, Sentry, Datadog, Prometheus deps
    obsKeywords := []string{"opentelemetry", "@opentelemetry", "sentry", "datadog", "newrelic", "prometheus"}
    if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
        content := strings.ToLower(string(data))
        for _, kw := range obsKeywords {
            if strings.Contains(content, kw) {
                score += 20
            }
        }
    }
    if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
        content := strings.ToLower(string(data))
        for _, kw := range obsKeywords {
            if strings.Contains(content, kw) {
                score += 20
            }
        }
    }
    return min(score, 100)
}

// buildDefaultConfig produces a starting Config from the detected profile and maturity.
func buildDefaultConfig(root string, profile *skillselect.RepoProfile, maturity MaturityClassification) Config {
    name := filepath.Base(root)
    if abs, err := filepath.Abs(root); err == nil {
        name = filepath.Base(abs)
    }

    cfg := Config{
        Project: ProjectConfig{
            Name:     name,
            Stage:    maturity.Stage,
            Monorepo: profile.HasMonorepo,
        },
        Models: ModelsConfig{
            Strategy:  "balanced",
            Execution: "hybrid",
            Architect: "claude",
            Editor:    "codex",
            Reviewer:  "codex",
        },
        Quality: defaultQuality(maturity.Stage),
        Security: defaultSecurity(maturity.Stage, profile),
        Infrastructure: InfrastructureConfig{
            Providers:  profile.CloudProviders,
            IaC:        detectIaC(profile),
            Preference: "best_fit",
        },
        Scale: defaultScale(maturity.Stage),
        Domains: detectDomains(profile),
        Skills: SkillsConfig{
            Enabled:      true,
            AutoDetect:   true,
            TokenBudget:  3000,
            ResearchFeed: true,
            AlwaysOn:     []string{"agent-discipline"},
        },
        Team: TeamConfig{
            Size: detectTeamSize(root),
        },
        Risk: defaultRisk(maturity.Stage),
    }
    return cfg
}

func defaultQuality(stage string) QualityConfig {
    switch stage {
    case "prototype":
        return QualityConfig{
            Verification:     "light",
            CodeQuality:      "relaxed",
            TestRequirements: "minimal",
            ReviewMode:       "self",
            HonestyEnforce:   "strict", // ALWAYS strict on honesty, even prototypes
        }
    case "mvp":
        return QualityConfig{
            Verification:     "standard",
            CodeQuality:      "standard",
            TestRequirements: "standard",
            ReviewMode:       "self",
            HonestyEnforce:   "strict",
        }
    case "growth":
        return QualityConfig{
            Verification:     "thorough",
            CodeQuality:      "strict",
            TestRequirements: "standard",
            ReviewMode:       "cross_model",
            HonestyEnforce:   "strict",
        }
    default: // scale, mature
        return QualityConfig{
            Verification:     "maximum",
            CodeQuality:      "strict",
            TestRequirements: "comprehensive",
            ReviewMode:       "multi",
            HonestyEnforce:   "maximum",
        }
    }
}

func defaultSecurity(stage string, profile *skillselect.RepoProfile) SecurityConfig {
    sec := SecurityConfig{
        Posture:         "standard",
        DataSensitivity: "internal",
    }
    if stage == "prototype" {
        sec.Posture = "basic"
    }
    if stage == "scale" || stage == "mature" {
        sec.Posture = "high"
    }
    // Compliance auto-detection from profile
    for _, fw := range profile.Frameworks {
        if fw == "stripe" || fw == "hedera" {
            sec.Compliance = append(sec.Compliance, "pci_dss")
            sec.DataSensitivity = "restricted"
        }
    }
    // Default Canadian compliance for any user-facing project
    if hasUserFacing(profile) {
        sec.Compliance = append(sec.Compliance, "pipeda", "casl")
    }
    return sec
}

func detectIaC(profile *skillselect.RepoProfile) string {
    for _, t := range profile.InfraTools {
        switch t {
        case "terraform":
            return "terraform"
        case "cdk":
            return "cdk"
        case "pulumi":
            return "pulumi"
        }
    }
    return "none"
}

func defaultScale(stage string) ScaleConfig {
    switch stage {
    case "prototype":
        return ScaleConfig{Expected: "small", Latency: "tolerant", DataVolume: "small"}
    case "mvp":
        return ScaleConfig{Expected: "small", Latency: "standard", DataVolume: "small"}
    case "growth":
        return ScaleConfig{Expected: "medium", Latency: "standard", DataVolume: "medium"}
    default:
        return ScaleConfig{Expected: "large", Latency: "sensitive", DataVolume: "large"}
    }
}

func detectDomains(profile *skillselect.RepoProfile) []string {
    var domains []string
    for _, fw := range profile.Frameworks {
        switch fw {
        case "stripe":
            domains = append(domains, "payments")
        case "react-native", "expo":
            domains = append(domains, "mobile")
        case "tauri", "electron":
            domains = append(domains, "desktop")
        case "hedera":
            domains = append(domains, "crypto", "payments")
        }
    }
    for _, proto := range profile.Protocols {
        if proto == "websocket" {
            domains = append(domains, "real-time")
        }
        if proto == "graphql" {
            domains = append(domains, "graphql-api")
        }
    }
    if hasUserFacing(profile) {
        domains = append(domains, "web-app")
    }
    return dedupStrings(domains)
}

func hasUserFacing(profile *skillselect.RepoProfile) bool {
    for _, fw := range profile.Frameworks {
        switch fw {
        case "react", "nextjs", "vue", "sveltekit", "angular", "react-native", "remix":
            return true
        }
    }
    return false
}

func detectTeamSize(root string) string {
    contributors := countGitContributors(root)
    switch {
    case contributors >= 20:
        return "20+"
    case contributors >= 6:
        return "6-20"
    case contributors >= 2:
        return "2-5"
    default:
        return "solo"
    }
}

func defaultRisk(stage string) RiskConfig {
    switch stage {
    case "prototype":
        return RiskConfig{Autonomy: "yolo", BlastRadius: "none"}
    case "mvp":
        return RiskConfig{Autonomy: "permissive", BlastRadius: "none"}
    case "growth":
        return RiskConfig{Autonomy: "standard", BlastRadius: "staging"}
    default:
        return RiskConfig{Autonomy: "conservative", BlastRadius: "limited_prod"}
    }
}

func dedupStrings(in []string) []string {
    seen := map[string]bool{}
    out := make([]string, 0, len(in))
    for _, v := range in {
        if !seen[v] {
            seen[v] = true
            out = append(out, v)
        }
    }
    return out
}

func exists(p string) bool {
    _, err := os.Stat(p)
    return err == nil
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

func addRationale(r *Result, profile *skillselect.RepoProfile, m MaturityClassification) {
    r.Rationale = append(r.Rationale, RationaleEntry{
        Field: "project.stage", Value: m.Stage, Source: "inferred",
        Evidence:   fmt.Sprintf("Composite maturity score: %d. Breakdown: %+v", m.Score, m.Breakdown),
        Confidence: 0.85,
    })
    for _, lang := range profile.Languages {
        conf := profile.Confidence[lang+".manifest"]
        if conf == 0 {
            conf = 0.95
        }
        r.Rationale = append(r.Rationale, RationaleEntry{
            Field: "language", Value: lang, Source: "detected",
            Confidence: conf,
        })
    }
    for _, db := range profile.Databases {
        r.Rationale = append(r.Rationale, RationaleEntry{
            Field: "database", Value: db, Source: "detected", Confidence: 0.95,
        })
    }
}
```

---

## Step 4: Implement question flow

**File:** `internal/wizard/questions.go`

```go
package wizard

import (
    "fmt"
    "github.com/charmbracelet/huh"
)

// runInteractiveQuestions runs the full question flow when the user wants
// to manually configure each section. Pre-fills with detected values.
func runInteractiveQuestions(opts Opts, r *Result) error {
    // Group 1: project identity (mostly auto-detected, just confirm)
    projectGroup := huh.NewGroup(
        huh.NewInput().Title("Project name").Value(&r.Config.Project.Name),
        huh.NewSelect[string]().
            Title("Project stage").
            Description(fmt.Sprintf("Auto-detected: %s (score %d/100)", r.Maturity.Stage, r.Maturity.Score)).
            Options(
                huh.NewOption("Prototype", "prototype"),
                huh.NewOption("MVP", "mvp"),
                huh.NewOption("Growth", "growth"),
                huh.NewOption("Scale", "scale"),
                huh.NewOption("Mature", "mature"),
            ).
            Value(&r.Config.Project.Stage),
    )

    // Group 2: models
    modelGroup := huh.NewGroup(
        huh.NewSelect[string]().Title("Model strategy").
            Options(
                huh.NewOption("Balanced (architect/editor split, cross-model review)", "balanced"),
                huh.NewOption("Best quality (most capable model always)", "best_quality"),
                huh.NewOption("Cost-optimized (route by complexity)", "cost_optimized"),
                huh.NewOption("Speed-optimized (fastest, accept quality tradeoff)", "speed"),
            ).
            Value(&r.Config.Models.Strategy),
        huh.NewMultiSelect[string]().Title("Available subscriptions").
            Options(
                huh.NewOption("Claude Max", "claude-max"),
                huh.NewOption("Claude Pro", "claude-pro"),
                huh.NewOption("Claude API key", "claude-api"),
                huh.NewOption("ChatGPT Pro / Codex", "chatgpt-pro"),
                huh.NewOption("Gemini API", "gemini-api"),
                huh.NewOption("OpenRouter", "openrouter"),
            ).
            Value(&r.Config.Models.Subscriptions),
        huh.NewSelect[string]().Title("Execution backend").
            Options(
                huh.NewOption("Hybrid (CLI when available, API fallback) [recommended]", "hybrid"),
                huh.NewOption("Claude Code CLI only", "cli"),
                huh.NewOption("Direct API only (no CLI dependency)", "api"),
            ).
            Value(&r.Config.Models.Execution),
    )

    // Group 3: quality and verification depth
    qualityGroup := huh.NewGroup(
        huh.NewSelect[string]().Title("Verification rigor").
            Options(
                huh.NewOption("Light (build + lint)", "light"),
                huh.NewOption("Standard (+ unit tests, scope check)", "standard"),
                huh.NewOption("Thorough (+ cross-model review, convergence)", "thorough"),
                huh.NewOption("Maximum (+ critic, speculative, mutation)", "maximum"),
            ).
            Value(&r.Config.Quality.Verification),
        huh.NewSelect[string]().Title("Code quality enforcement").
            Options(
                huh.NewOption("Relaxed (allow TODOs, prototyping)", "relaxed"),
                huh.NewOption("Standard (no TODOs in commits, doc comments)", "standard"),
                huh.NewOption("Strict (+ complexity limits, full coverage gates)", "strict"),
            ).
            Value(&r.Config.Quality.CodeQuality),
        huh.NewSelect[string]().Title("Honesty enforcement (anti-deception)").
            Description("Stoke's primary differentiator. Strict is recommended for any non-prototype work.").
            Options(
                huh.NewOption("Light (basic test running)", "light"),
                huh.NewOption("Strict (mutation testing, AST diff, hidden tests) [recommended]", "strict"),
                huh.NewOption("Maximum (+ multi-agent adversarial review)", "maximum"),
            ).
            Value(&r.Config.Quality.HonestyEnforce),
        huh.NewSelect[string]().Title("Review mode").
            Options(
                huh.NewOption("Self-review", "self"),
                huh.NewOption("Cross-model (Claude writes, Codex reviews)", "cross_model"),
                huh.NewOption("Multi-reviewer (specialized personas)", "multi"),
                huh.NewOption("Human-gated (proposes, human approves)", "human"),
            ).
            Value(&r.Config.Quality.ReviewMode),
    )

    // Group 4: security and compliance
    securityGroup := huh.NewGroup(
        huh.NewSelect[string]().Title("Security posture").
            Options(
                huh.NewOption("Basic (dependency scan, no hardcoded secrets)", "basic"),
                huh.NewOption("Standard (+ input validation, OWASP Top 10)", "standard"),
                huh.NewOption("High (+ security review every PR, supply chain)", "high"),
                huh.NewOption("Regulated (+ SOC 2, audit logging, classification)", "regulated"),
            ).
            Value(&r.Config.Security.Posture),
        huh.NewMultiSelect[string]().Title("Privacy compliance").
            Description("Auto-detected from project characteristics. Review and adjust.").
            Options(
                huh.NewOption("None", "none"),
                huh.NewOption("PIPEDA / BC PIPA (Canadian)", "pipeda"),
                huh.NewOption("CASL (Canadian commercial messages)", "casl"),
                huh.NewOption("GDPR (EU)", "gdpr"),
                huh.NewOption("CCPA / CPRA (California)", "ccpa"),
                huh.NewOption("COPPA (children's data)", "coppa"),
                huh.NewOption("HIPAA (health data)", "hipaa"),
                huh.NewOption("PCI DSS (payment card data)", "pci_dss"),
                huh.NewOption("SOC 2", "soc2"),
            ).
            Value(&r.Config.Security.Compliance),
        huh.NewSelect[string]().Title("Data sensitivity").
            Options(
                huh.NewOption("Public (open source, no sensitive data)", "public"),
                huh.NewOption("Internal (company data, standard confidentiality)", "internal"),
                huh.NewOption("Confidential (PII, financial, health)", "confidential"),
                huh.NewOption("Restricted (payment cards, credentials, keys)", "restricted"),
            ).
            Value(&r.Config.Security.DataSensitivity),
    )

    // Group 5: infrastructure
    infraGroup := huh.NewGroup(
        huh.NewMultiSelect[string]().Title("Cloud providers").
            Options(
                huh.NewOption("GCP", "gcp"),
                huh.NewOption("AWS", "aws"),
                huh.NewOption("Azure", "azure"),
                huh.NewOption("Cloudflare", "cloudflare"),
                huh.NewOption("Fly.io", "fly"),
                huh.NewOption("Vercel", "vercel"),
                huh.NewOption("Netlify", "netlify"),
                huh.NewOption("Self-hosted", "self_hosted"),
            ).
            Value(&r.Config.Infrastructure.Providers),
        huh.NewSelect[string]().Title("Infrastructure as Code").
            Options(
                huh.NewOption("Terraform", "terraform"),
                huh.NewOption("Pulumi", "pulumi"),
                huh.NewOption("CDK (AWS)", "cdk"),
                huh.NewOption("None / manual", "none"),
            ).
            Value(&r.Config.Infrastructure.IaC),
        huh.NewSelect[string]().Title("Provider preference philosophy").
            Options(
                huh.NewOption("Provider-agnostic (avoid vendor lock-in)", "agnostic"),
                huh.NewOption("Provider-optimized (use best native tools)", "optimized"),
                huh.NewOption("Single provider (all-in)", "single"),
                huh.NewOption("Open source only (no managed services)", "opensource_only"),
                huh.NewOption("Best fit per service", "best_fit"),
            ).
            Value(&r.Config.Infrastructure.Preference),
        huh.NewSelect[string]().Title("Cloud credits").
            Options(
                huh.NewOption("None", "none"),
                huh.NewOption("GCP credits (optimize for GCP services)", "gcp"),
                huh.NewOption("AWS credits (optimize for AWS services)", "aws"),
                huh.NewOption("Azure credits", "azure"),
            ).
            Value(&r.Config.Infrastructure.Credits),
    )

    // Group 6: scale
    scaleGroup := huh.NewGroup(
        huh.NewSelect[string]().Title("Expected scale").
            Options(
                huh.NewOption("Small (< 1K req/min)", "small"),
                huh.NewOption("Medium (1K-10K req/min)", "medium"),
                huh.NewOption("Large (10K-100K req/min)", "large"),
                huh.NewOption("Very Large (100K+ req/min, multi-region)", "very_large"),
            ).
            Value(&r.Config.Scale.Expected),
    )

    // Group 7: risk tolerance
    riskGroup := huh.NewGroup(
        huh.NewSelect[string]().Title("Agent autonomy").
            Options(
                huh.NewOption("Conservative (every action requires approval)", "conservative"),
                huh.NewOption("Standard (sandboxed auto-execution with hooks)", "standard"),
                huh.NewOption("Permissive (auto-approval, security hooks only)", "permissive"),
                huh.NewOption("YOLO (minimal hooks, prototype only)", "yolo"),
            ).
            Value(&r.Config.Risk.Autonomy),
        huh.NewSelect[string]().Title("Production blast radius").
            Options(
                huh.NewOption("None (local dev, no prod access)", "none"),
                huh.NewOption("Read-only (can read prod, never modify)", "read_only"),
                huh.NewOption("Staging (can modify staging environments)", "staging"),
                huh.NewOption("Limited prod (specific allowlisted ops)", "limited_prod"),
                huh.NewOption("Full prod (write access, requires human gates)", "full_prod"),
            ).
            Value(&r.Config.Risk.BlastRadius),
    )

    form := huh.NewForm(projectGroup, modelGroup, qualityGroup, securityGroup, infraGroup, scaleGroup, riskGroup)
    return form.Run()
}
```

---

## Step 5: Proposal display + output writers

**File:** `internal/wizard/proposal.go`

```go
package wizard

import (
    "fmt"

    "github.com/charmbracelet/huh"
)

// presentProposal shows the auto-generated config and asks for approval.
// If the user wants to modify, drops into runInteractiveQuestions.
func presentProposal(opts Opts, r *Result) error {
    // Build proposal text
    proposal := renderProposal(r)
    fmt.Fprintln(opts.Stdout, proposal)

    var choice string
    err := huh.NewSelect[string]().
        Title("Configuration proposal").
        Options(
            huh.NewOption("Accept all", "accept"),
            huh.NewOption("Modify some answers", "modify"),
            huh.NewOption("Cancel", "cancel"),
        ).
        Value(&choice).
        Run()
    if err != nil {
        return err
    }
    switch choice {
    case "accept":
        return nil
    case "modify":
        return runInteractiveQuestions(opts, r)
    case "cancel":
        return fmt.Errorf("wizard cancelled by user")
    }
    return nil
}

func renderProposal(r *Result) string {
    var s string
    s += "\n┌────────────────────────────────────────────────────────────┐\n"
    s += "│                Stoke Configuration Proposal                │\n"
    s += "├────────────────────────────────────────────────────────────┤\n"
    s += fmt.Sprintf("│ Project: %-50s │\n", r.Config.Project.Name)
    s += fmt.Sprintf("│ Stage:   %-50s │\n", fmt.Sprintf("%s (score %d/100)", r.Config.Project.Stage, r.Maturity.Score))
    s += "├────────────────────────────────────────────────────────────┤\n"
    s += "│ Detected stack:                                            │\n"
    s += fmt.Sprintf("│   Languages:    %-43s │\n", joinTrunc(r.Profile.Languages, 43))
    s += fmt.Sprintf("│   Frameworks:   %-43s │\n", joinTrunc(r.Profile.Frameworks, 43))
    s += fmt.Sprintf("│   Databases:    %-43s │\n", joinTrunc(r.Profile.Databases, 43))
    s += fmt.Sprintf("│   Cloud:        %-43s │\n", joinTrunc(r.Profile.CloudProviders, 43))
    s += "├────────────────────────────────────────────────────────────┤\n"
    s += fmt.Sprintf("│ Quality:        %-43s │\n", r.Config.Quality.Verification)
    s += fmt.Sprintf("│ Honesty:        %-43s │\n", r.Config.Quality.HonestyEnforce)
    s += fmt.Sprintf("│ Security:       %-43s │\n", r.Config.Security.Posture)
    s += fmt.Sprintf("│ Compliance:     %-43s │\n", joinTrunc(r.Config.Security.Compliance, 43))
    s += fmt.Sprintf("│ Models:         %-43s │\n", r.Config.Models.Strategy)
    s += "├────────────────────────────────────────────────────────────┤\n"
    s += fmt.Sprintf("│ Skills selected: %d                                       │\n", len(r.Skills))
    s += "└────────────────────────────────────────────────────────────┘\n"
    return s
}

func joinTrunc(items []string, max int) string {
    s := ""
    for i, item := range items {
        if i > 0 {
            s += ", "
        }
        s += item
        if len(s) > max {
            return s[:max-3] + "..."
        }
    }
    if s == "" {
        return "(none)"
    }
    return s
}
```

**File:** `internal/wizard/output.go`

```go
package wizard

import (
    "fmt"
    "os"
    "path/filepath"

    "gopkg.in/yaml.v3"
)

// writeOutput creates the .stoke/ directory, writes config.yaml, copies skill
// files from the library, and writes the rationale document.
func writeOutput(root string, r *Result) error {
    stokeDir := filepath.Join(root, ".stoke")
    if err := os.MkdirAll(stokeDir, 0755); err != nil {
        return err
    }

    // Write config.yaml
    cfgPath := filepath.Join(stokeDir, "config.yaml")
    data, err := yaml.Marshal(r.Config)
    if err != nil {
        return fmt.Errorf("marshal config: %w", err)
    }
    if err := os.WriteFile(cfgPath, data, 0644); err != nil {
        return fmt.Errorf("write config: %w", err)
    }

    // Copy selected skills from the library to .stoke/skills/
    skillsDir := filepath.Join(stokeDir, "skills")
    if err := os.MkdirAll(skillsDir, 0755); err != nil {
        return err
    }
    if err := copySkills(r.Skills, skillsDir); err != nil {
        // Best-effort: log but don't fail
        fmt.Fprintf(os.Stderr, "[wizard] copy skills: %v\n", err)
    }

    // Write rationale document
    rationalePath := filepath.Join(stokeDir, "wizard-rationale.md")
    if err := os.WriteFile(rationalePath, []byte(renderRationale(r)), 0644); err != nil {
        return err
    }

    return nil
}

// copySkills copies skill directories from the library locations to the project
// .stoke/skills/. Library lookup order:
//   1. ~/.stoke/skills/<n>/
//   2. (no fallback — if not found, log a warning)
func copySkills(names []string, destDir string) error {
    home, _ := os.UserHomeDir()
    libraryDirs := []string{
        filepath.Join(home, ".stoke", "skills"),
    }
    for _, name := range names {
        for _, lib := range libraryDirs {
            src := filepath.Join(lib, name)
            if _, err := os.Stat(src); err == nil {
                dst := filepath.Join(destDir, name)
                if err := copyDir(src, dst); err != nil {
                    return fmt.Errorf("copy %s: %w", name, err)
                }
                break
            }
        }
    }
    return nil
}

func copyDir(src, dst string) error {
    if err := os.MkdirAll(dst, 0755); err != nil {
        return err
    }
    entries, err := os.ReadDir(src)
    if err != nil {
        return err
    }
    for _, e := range entries {
        srcPath := filepath.Join(src, e.Name())
        dstPath := filepath.Join(dst, e.Name())
        if e.IsDir() {
            if err := copyDir(srcPath, dstPath); err != nil {
                return err
            }
        } else {
            data, err := os.ReadFile(srcPath)
            if err != nil {
                return err
            }
            if err := os.WriteFile(dstPath, data, 0644); err != nil {
                return err
            }
        }
    }
    return nil
}

func renderRationale(r *Result) string {
    s := "# Stoke Configuration Rationale\n\n"
    s += fmt.Sprintf("Generated: %s\n\n", r.GeneratedAt.Format("2006-01-02 15:04:05"))
    s += "## Decisions\n\n"
    for _, e := range r.Rationale {
        s += fmt.Sprintf("### %s = %s\n", e.Field, e.Value)
        s += fmt.Sprintf("- **Source:** %s\n", e.Source)
        if e.Confidence > 0 {
            s += fmt.Sprintf("- **Confidence:** %.0f%%\n", e.Confidence*100)
        }
        if e.Evidence != "" {
            s += fmt.Sprintf("- **Evidence:** %s\n", e.Evidence)
        }
        s += "\n"
    }
    s += "## Skills selected\n\n"
    for _, name := range r.Skills {
        s += fmt.Sprintf("- %s\n", name)
    }
    return s
}

// selectSkillsFromConfig converts the final config back into a skill list.
// This is what gets installed into .stoke/skills/.
func selectSkillsFromConfig(cfg Config, profile *skillselect.RepoProfile) []string {
    skills := []string{"agent-discipline"}
    skills = append(skills, profile.Languages...)
    skills = append(skills, profile.Frameworks...)
    skills = append(skills, profile.Databases...)
    skills = append(skills, profile.MessageQueues...)
    for _, c := range profile.CloudProviders {
        skills = append(skills, "cloud-"+c)
    }
    skills = append(skills, profile.Protocols...)
    skills = append(skills, cfg.Domains...)

    // Always-on quality skills
    skills = append(skills, "code-quality", "testing", "security", "error-handling")

    // Compliance skills
    for _, c := range cfg.Security.Compliance {
        skills = append(skills, "compliance-"+c)
    }

    // Risk-tolerance based skills
    if cfg.Risk.BlastRadius == "limited_prod" || cfg.Risk.BlastRadius == "full_prod" {
        skills = append(skills, "production-safety")
    }

    return dedupStrings(skills)
}
```

---

## Step 6: Optional research convergence

**File:** `internal/wizard/research.go`

```go
package wizard

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
)

// runResearchConvergence asks the configured AI provider for additional config
// recommendations based on the detected profile. This is opt-in (--research flag)
// and best-effort.
func runResearchConvergence(ctx context.Context, p Provider, r *Result) error {
    profile, _ := json.Marshal(r.Profile)
    maturity, _ := json.Marshal(r.Maturity)

    system := `You are a configuration advisor for Stoke, an AI coding orchestrator.
Given a detected technology profile and maturity classification, recommend
configuration adjustments. Be concise. Output JSON only with this schema:

{
  "stage_correction": "prototype|mvp|growth|scale|mature|null",
  "additional_skills": ["skill_name", ...],
  "additional_compliance": ["pipeda", "casl", ...],
  "warnings": ["warning text", ...]
}

Do not include explanations outside the JSON.`

    user := fmt.Sprintf("Profile: %s\nMaturity: %s\n\nWhat config adjustments do you recommend?", profile, maturity)

    response, err := p.Chat(ctx, system, user)
    if err != nil {
        return err
    }

    // Parse response (be lenient — strip markdown fences)
    response = strings.TrimSpace(response)
    response = strings.TrimPrefix(response, "```json")
    response = strings.TrimPrefix(response, "```")
    response = strings.TrimSuffix(response, "```")
    response = strings.TrimSpace(response)

    var rec struct {
        StageCorrection      string   `json:"stage_correction"`
        AdditionalSkills     []string `json:"additional_skills"`
        AdditionalCompliance []string `json:"additional_compliance"`
        Warnings             []string `json:"warnings"`
    }
    if err := json.Unmarshal([]byte(response), &rec); err != nil {
        return fmt.Errorf("parse research response: %w", err)
    }

    if rec.StageCorrection != "" && rec.StageCorrection != "null" {
        if rec.StageCorrection != r.Config.Project.Stage {
            r.Rationale = append(r.Rationale, RationaleEntry{
                Field: "project.stage", Value: rec.StageCorrection, Source: "research",
                Evidence: "AI advisor suggested correction from " + r.Config.Project.Stage,
                Confidence: 0.75,
            })
            r.Config.Project.Stage = rec.StageCorrection
        }
    }
    for _, c := range rec.AdditionalCompliance {
        r.Config.Security.Compliance = append(r.Config.Security.Compliance, c)
        r.Rationale = append(r.Rationale, RationaleEntry{
            Field: "security.compliance", Value: c, Source: "research", Confidence: 0.7,
        })
    }
    r.Config.Security.Compliance = dedupStrings(r.Config.Security.Compliance)

    return nil
}
```

---

## Step 7: CLI command

**File:** `cmd/stoke/main.go`

Add a new command:

```go
case "wizard":
    return runWizardCmd(args[1:])
```

```go
func runWizardCmd(args []string) error {
    opts := wizard.Opts{
        ProjectRoot: ".",
        Mode:        wizard.ModeAuto,
    }
    for _, arg := range args {
        switch arg {
        case "--auto":
            opts.Mode = wizard.ModeAuto
        case "--interactive":
            opts.Mode = wizard.ModeInteractive
        case "--hybrid":
            opts.Mode = wizard.ModeHybrid
        case "--yes", "-y":
            opts.Mode = wizard.ModeYes
        case "--research":
            opts.Research = true
        }
    }
    ctx := context.Background()
    result, err := wizard.Run(ctx, opts)
    if err != nil {
        return err
    }
    fmt.Printf("Wizard complete. Wrote .stoke/config.yaml with %d skills.\n", len(result.Skills))
    return nil
}
```

---

## Validation gate for Phase 2

1. `go vet ./...` clean, `go test ./internal/wizard/...` passes
2. `go build ./cmd/stoke` succeeds
3. Run `stoke wizard --auto` on a sample repo (try the Stoke repo itself) — should produce `.stoke/config.yaml` and `.stoke/wizard-rationale.md` without errors
4. Run `stoke wizard --interactive` and click through every group — should not crash
5. Run `stoke wizard --yes` — should produce config without prompts (CI-safe)
6. Inspect the generated `wizard-rationale.md` and verify it explains every decision
7. Append phase 2 entry to `STOKE-IMPL-NOTES.md`

## Now go to `05-phase3-hub.md`.
