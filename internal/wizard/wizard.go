// Package wizard implements the `stoke init` project configuration wizard.
//
// It auto-detects the project's technology stack, asks structured questions
// about preferences (models, adversarial depth, polish level, compliance,
// infrastructure), and generates a complete stoke.policy.yaml.
//
// Default mode: research the repo and self-configure from detected context.
// Interactive mode: walk through questions with the user.
package wizard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/convergence"
	"github.com/RelayOne/r1/internal/r1dir"
)

// Preferences captures all user choices from the wizard.
type Preferences struct {
	// Project identity
	ProjectName string `yaml:"project_name,omitempty"`

	// Model routing
	PrimaryModel    string   `yaml:"primary_model"`
	ReviewModel     string   `yaml:"review_model"`
	FallbackChain   []string `yaml:"fallback_chain,omitempty"`
	ModelStrategy   string   `yaml:"model_strategy,omitempty"` // balanced, best-quality, cost-optimized, speed-optimized

	// Adversarial depth
	AdversarialDepth AdversarialDepth `yaml:"adversarial_depth"`

	// Quality
	PolishLevel     PolishLevel `yaml:"polish_level"`
	SecurityPosture string      `yaml:"security_posture"` // basic, standard, high, regulated
	DataSensitivity string      `yaml:"data_sensitivity"` // public, internal, confidential, restricted

	// Scale
	ScaleTier ScaleTier `yaml:"scale_tier"`

	// Compliance
	ComplianceFrameworks []string `yaml:"compliance,omitempty"`

	// Infrastructure
	Infrastructure []string `yaml:"infrastructure,omitempty"`

	// Provider preferences
	ProviderPreference ProviderPref `yaml:"provider_preference"`

	// Domain-specific areas
	DomainAreas []string `yaml:"domain_areas,omitempty"` // ecommerce, payments, mobile, etc.

	// Team
	TeamSize   string `yaml:"team_size,omitempty"`   // solo, 2-5, 6-20, 20+
	OpenSource bool   `yaml:"open_source,omitempty"`

	// Detected domains (auto-populated)
	DetectedDomains []string `yaml:"detected_domains,omitempty"`

	// Git stats (auto-populated)
	GitStats GitStats `yaml:"-"` // not serialized

	// Build commands (auto-detected or manual)
	BuildCmd string `yaml:"build_cmd,omitempty"`
	TestCmd  string `yaml:"test_cmd,omitempty"`
	LintCmd  string `yaml:"lint_cmd,omitempty"`

	// Rationale entries for decision log
	Rationale []RationaleEntry `yaml:"-"`
}

// RationaleEntry records why a configuration decision was made.
type RationaleEntry struct {
	Decision string
	Evidence string
}

// AdversarialDepth controls how aggressive the enforcement system is.
type AdversarialDepth string

const (
	DepthLight    AdversarialDepth = "light"    // critic only
	DepthStandard AdversarialDepth = "standard" // critic + convergence
	DepthMaximum  AdversarialDepth = "maximum"  // critic + convergence + cross-model review
)

// PolishLevel controls which severity findings block merge.
type PolishLevel string

const (
	PolishShipIt       PolishLevel = "ship-it"       // blocking only
	PolishProduction   PolishLevel = "production"     // blocking + major
	PolishPerfectionist PolishLevel = "perfectionist" // all findings
)

// ScaleTier hints at the expected operational scale.
type ScaleTier string

const (
	ScalePrototype  ScaleTier = "prototype"
	ScaleStartup    ScaleTier = "startup"
	ScaleGrowth     ScaleTier = "growth"
	ScaleEnterprise ScaleTier = "enterprise"
)

// ProviderPref indicates model provider preferences.
type ProviderPref string

const (
	ProviderBestFit       ProviderPref = "best-fit"
	ProviderOSSOnly       ProviderPref = "oss-only"
	ProviderAgnostic      ProviderPref = "provider-agnostic"
	ProviderClaudeOnly    ProviderPref = "claude-only"
)

// Question represents a single wizard question.
type Question struct {
	ID          string
	Prompt      string
	Options     []string // numbered options, empty = free text
	Default     int      // default option index (1-based)
	DefaultText string   // default for free text
}

// Wizard runs the interactive configuration wizard.
type Wizard struct {
	ProjectDir string
	Reader     io.Reader
	Writer     io.Writer
	Prefs      Preferences
}

// New creates a wizard for the given project directory.
func New(projectDir string) *Wizard {
	return &Wizard{
		ProjectDir: projectDir,
		Reader:     os.Stdin,
		Writer:     os.Stdout,
	}
}

// Run executes the full wizard flow: detect, ask, generate.
func (w *Wizard) Run() error {
	w.printf("\n  stoke init — project configuration wizard\n\n")

	// Phase 1: Auto-detect
	w.printf("  Scanning project...\n")
	w.autoDetect()

	// Phase 2: Interactive questions
	w.printf("\n")
	w.askQuestions()

	// Phase 3: Generate config
	yaml := w.GenerateYAML()
	outPath := filepath.Join(w.ProjectDir, "r1.policy.yaml")

	// Check for existing config
	if _, err := os.Stat(outPath); err == nil {
		w.printf("\n  stoke.policy.yaml already exists. Overwrite? [y/N] ")
		if !w.confirm() {
			w.printf("  Aborted.\n")
			return nil
		}
	}

	if err := os.WriteFile(outPath, []byte(yaml), 0644); err != nil { // #nosec G306 -- wizard-generated config consumed by user tooling; 0644 is appropriate.
		return fmt.Errorf("write config: %w", err)
	}

	// Write rationale
	if err := w.WriteRationale(); err != nil {
		w.printf("  warning: could not write rationale: %v\n", err)
	}

	w.printf("\n  Written: %s\n", outPath)
	w.printf("  Rationale: .stoke/wizard-rationale.md\n")
	w.printf("  %d convergence rules active (%d base + domain-specific)\n",
		w.countRules(), 66)
	w.printf("\n  Run `stoke build` to start.\n\n")
	return nil
}

// RunAutoDetect runs in non-interactive mode: detect everything, use defaults.
func (w *Wizard) RunAutoDetect() error {
	w.autoDetect()
	w.applyDefaults()

	yaml := w.GenerateYAML()
	outPath := filepath.Join(w.ProjectDir, "r1.policy.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0644); err != nil { // #nosec G306 -- wizard-generated config consumed by user tooling; 0644 is appropriate.
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (w *Wizard) autoDetect() {
	// Detect technology stack
	domains := convergence.DetectDomains(w.ProjectDir)
	for d := range domains {
		w.Prefs.DetectedDomains = append(w.Prefs.DetectedDomains, string(d))
		w.printf("  detected: %s\n", d)
	}

	// Detect build commands
	cmds := config.DetectCommands(w.ProjectDir)
	w.Prefs.BuildCmd = cmds.Build
	w.Prefs.TestCmd = cmds.Test
	w.Prefs.LintCmd = cmds.Lint
	if cmds.Build != "" {
		w.printf("  build: %s\n", cmds.Build)
	}
	if cmds.Test != "" {
		w.printf("  test:  %s\n", cmds.Test)
	}
	if cmds.Lint != "" {
		w.printf("  lint:  %s\n", cmds.Lint)
	}

	// Git history analysis
	w.Prefs.GitStats = DetectGitStats(w.ProjectDir)
	stats := w.Prefs.GitStats
	if stats.CommitCount > 0 {
		w.printf("  commits: %d, contributors: %d\n", stats.CommitCount, stats.ContributorCount)
	}
	if stats.HasCI {
		w.printf("  CI: detected\n")
	}
	if stats.IsMonorepo {
		w.printf("  monorepo: yes\n")
	}
	if stats.HasTests {
		w.printf("  test coverage: %.0f%% of files have tests\n", stats.TestFileRatio*100)
	}

	// Infer stage from git stats (overrides simple file count)
	w.Prefs.ScaleTier = InferStage(stats)
	w.Prefs.TeamSize = InferTeamSize(stats)
	w.printf("  stage: %s (inferred), team: %s\n", w.Prefs.ScaleTier, w.Prefs.TeamSize)

	// Record rationale
	w.Prefs.Rationale = append(w.Prefs.Rationale, RationaleEntry{
		Decision: fmt.Sprintf("Stage: %s", w.Prefs.ScaleTier),
		Evidence: fmt.Sprintf("%d commits, %d contributors, CI=%v, tests=%v",
			stats.CommitCount, stats.ContributorCount, stats.HasCI, stats.HasTests),
	})
	for _, d := range w.Prefs.DetectedDomains {
		w.Prefs.Rationale = append(w.Prefs.Rationale, RationaleEntry{
			Decision: fmt.Sprintf("Domain: %s detected", d),
			Evidence: "Found in project dependency manifests",
		})
	}
}

func (w *Wizard) askQuestions() {
	questions := []struct {
		ask func()
	}{
		{w.askModel},
		{w.askModelStrategy},
		{w.askAdversarialDepth},
		{w.askPolishLevel},
		{w.askSecurityPosture},
		{w.askDataSensitivity},
		{w.askScaleTier},
		{w.askCompliance},
		{w.askInfrastructure},
		{w.askProviderPreference},
		{w.askDomainAreas},
		{w.askTeamSize},
	}
	for _, q := range questions {
		q.ask()
	}
}

func (w *Wizard) askModel() {
	choice := w.askChoice(Question{
		ID:     "model",
		Prompt: "Primary model",
		Options: []string{
			"Claude (recommended)",
			"Claude + Codex cross-review",
			"Multi-provider (Claude → Codex → OpenRouter fallback)",
		},
		Default: 1,
	})
	switch choice {
	case 1:
		w.Prefs.PrimaryModel = providerLabelClaude
		w.Prefs.ReviewModel = providerLabelClaude
	case 2:
		w.Prefs.PrimaryModel = providerLabelClaude
		w.Prefs.ReviewModel = "codex"
	case 3:
		w.Prefs.PrimaryModel = providerLabelClaude
		w.Prefs.ReviewModel = "codex"
		w.Prefs.FallbackChain = []string{providerLabelClaude, "codex", "openrouter"}
	}
}

func (w *Wizard) askAdversarialDepth() {
	choice := w.askChoice(Question{
		ID:     "depth",
		Prompt: "Adversarial enforcement depth",
		Options: []string{
			"Light — critic only (fast, fewer checks)",
			"Standard — critic + convergence gate (recommended)",
			"Maximum — critic + convergence + cross-model review (thorough)",
		},
		Default: 2,
	})
	switch choice {
	case 1:
		w.Prefs.AdversarialDepth = DepthLight
	case 2:
		w.Prefs.AdversarialDepth = DepthStandard
	case 3:
		w.Prefs.AdversarialDepth = DepthMaximum
	}
}

func (w *Wizard) askPolishLevel() {
	choice := w.askChoice(Question{
		ID:     "polish",
		Prompt: "Quality threshold",
		Options: []string{
			"Ship-it — only blocking findings stop merge",
			"Production — blocking + major findings stop merge (recommended)",
			"Perfectionist — all findings stop merge",
		},
		Default: 2,
	})
	switch choice {
	case 1:
		w.Prefs.PolishLevel = PolishShipIt
	case 2:
		w.Prefs.PolishLevel = PolishProduction
	case 3:
		w.Prefs.PolishLevel = PolishPerfectionist
	}
}

func (w *Wizard) askScaleTier() {
	choice := w.askChoice(Question{
		ID:     "scale",
		Prompt: fmt.Sprintf("Scale tier (detected: %s)", w.Prefs.ScaleTier),
		Options: []string{
			"Prototype — move fast, minimal rules",
			"Startup — balanced speed and safety",
			"Growth — full enforcement, performance rules active",
			"Enterprise — maximum enforcement, compliance-ready",
		},
		Default: w.scaleToIndex(),
	})
	switch choice {
	case 1:
		w.Prefs.ScaleTier = ScalePrototype
	case 2:
		w.Prefs.ScaleTier = ScaleStartup
	case 3:
		w.Prefs.ScaleTier = ScaleGrowth
	case 4:
		w.Prefs.ScaleTier = ScaleEnterprise
	}
}

func (w *Wizard) askCompliance() {
	w.printf("  Compliance frameworks (comma-separated, or 'none'):\n")
	w.printf("  Options: none, soc2, gdpr, hipaa, pci-dss, pipeda, casl, ccpa\n")
	w.printf("  [none] > ")
	input := w.readLine()
	if input == "" || strings.EqualFold(input, "none") {
		w.Prefs.ComplianceFrameworks = nil
		return
	}
	parts := strings.Split(input, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" && p != "none" {
			w.Prefs.ComplianceFrameworks = append(w.Prefs.ComplianceFrameworks, p)
		}
	}
}

func (w *Wizard) askInfrastructure() {
	w.printf("  Infrastructure / cloud providers (comma-separated, or 'auto'):\n")
	w.printf("  Options: auto, aws, azure, gcp, digitalocean, cloudflare, self-hosted\n")
	w.printf("  [auto] > ")
	input := w.readLine()
	if input == "" || strings.EqualFold(input, "auto") {
		// Use detected domains
		for _, d := range w.Prefs.DetectedDomains {
			switch d {
			case "aws", "azure", "cloudflare":
				w.Prefs.Infrastructure = append(w.Prefs.Infrastructure, d)
			}
		}
		return
	}
	parts := strings.Split(input, ",")
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" && p != "auto" {
			w.Prefs.Infrastructure = append(w.Prefs.Infrastructure, p)
		}
	}
}

func (w *Wizard) askProviderPreference() {
	choice := w.askChoice(Question{
		ID:     "provider",
		Prompt: "Model provider preference",
		Options: []string{
			"Best-fit — use whatever model is best for each task",
			"Claude-only — only use Anthropic models",
			"Provider-agnostic — prefer models available from multiple providers",
			"OSS-only — only use open-source models",
		},
		Default: 1,
	})
	switch choice {
	case 1:
		w.Prefs.ProviderPreference = ProviderBestFit
	case 2:
		w.Prefs.ProviderPreference = ProviderClaudeOnly
	case 3:
		w.Prefs.ProviderPreference = ProviderAgnostic
	case 4:
		w.Prefs.ProviderPreference = ProviderOSSOnly
	}
}

func (w *Wizard) askModelStrategy() {
	choice := w.askChoice(Question{
		ID:     "model_strategy",
		Prompt: "Model usage strategy",
		Options: []string{
			"Balanced — architect/editor split, cross-model review (recommended)",
			"Best quality — use most capable model always",
			"Cost-optimized — route by task complexity, minimize spend",
			"Speed-optimized — use fastest model, accept quality tradeoff",
		},
		Default: 1,
	})
	strategies := []string{"balanced", "best-quality", "cost-optimized", "speed-optimized"}
	w.Prefs.ModelStrategy = strategies[choice-1]
}

func (w *Wizard) askSecurityPosture() {
	choice := w.askChoice(Question{
		ID:     "security",
		Prompt: "Security posture",
		Options: []string{
			"Basic — dependency scanning, no hardcoded secrets",
			"Standard — Basic + input validation + auth checks + OWASP Top 10",
			"High — Standard + security review + supply chain verification",
			"Regulated — High + SOC 2 evidence + audit logging + data classification",
		},
		Default: 2,
	})
	postures := []string{"basic", "standard", "high", "regulated"}
	w.Prefs.SecurityPosture = postures[choice-1]
}

func (w *Wizard) askDataSensitivity() {
	choice := w.askChoice(Question{
		ID:     "data",
		Prompt: "Data sensitivity level",
		Options: []string{
			"Public — open source, no sensitive data",
			"Internal — company data, standard confidentiality",
			"Confidential — PII, financial data, health data",
			"Restricted — payment card data, credentials, encryption keys",
		},
		Default: 2,
	})
	levels := []string{"public", "internal", "confidential", "restricted"}
	w.Prefs.DataSensitivity = levels[choice-1]
}

func (w *Wizard) askDomainAreas() {
	w.printf("  Domain-specific areas (comma-separated, or 'auto'):\n")
	w.printf("  Options: auto, ecommerce, payments, subscriptions, realtime, search,\n")
	w.printf("           mobile, desktop, api-platform, cli, internal-tooling, ai-ml\n")
	w.printf("  [auto] > ")
	input := w.readLine()
	if input == "" || strings.EqualFold(input, "auto") {
		// Infer from detected domains
		for _, d := range w.Prefs.DetectedDomains {
			switch d {
			case "stripe":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "payments")
			case "react":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "web-app")
			case "tauri":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "desktop")
			case "grpc", "graphql":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "api-platform")
			case "kafka", "rabbitmq":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "realtime")
			case "elasticsearch":
				w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, "search")
			}
		}
		return
	}
	for _, p := range strings.Split(input, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" && p != "auto" {
			w.Prefs.DomainAreas = appendUnique(w.Prefs.DomainAreas, p)
		}
	}
}

func (w *Wizard) askTeamSize() {
	detected := w.Prefs.TeamSize
	choice := w.askChoice(Question{
		ID:     "team",
		Prompt: fmt.Sprintf("Team size (detected: %s)", detected),
		Options: []string{
			"Solo",
			"2-5 developers",
			"6-20 developers",
			"20+ developers",
		},
		Default: w.teamToIndex(),
	})
	sizes := []string{"solo", "2-5", "6-20", "20+"}
	w.Prefs.TeamSize = sizes[choice-1]
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

func (w *Wizard) teamToIndex() int {
	switch w.Prefs.TeamSize {
	case "solo":
		return 1
	case "2-5":
		return 2
	case "6-20":
		return 3
	case "20+":
		return 4
	default:
		return 1
	}
}

// GenerateYAML produces the complete stoke.policy.yaml from preferences.
func (w *Wizard) GenerateYAML() string {
	var b strings.Builder

	b.WriteString("# Generated by stoke init\n")
	b.WriteString("# https://github.com/RelayOne/r1\n\n")

	// Project identity
	b.WriteString("project:\n")
	if w.Prefs.ProjectName != "" {
		b.WriteString(fmt.Sprintf("  name: %s\n", w.Prefs.ProjectName))
	}
	b.WriteString(fmt.Sprintf("  stage: %s\n", w.Prefs.ScaleTier))
	if w.Prefs.GitStats.IsMonorepo {
		b.WriteString("  monorepo: true\n")
	}

	// Model configuration
	b.WriteString("\nmodels:\n")
	b.WriteString(fmt.Sprintf("  primary: %s\n", w.Prefs.PrimaryModel))
	b.WriteString(fmt.Sprintf("  review: %s\n", w.Prefs.ReviewModel))
	if len(w.Prefs.FallbackChain) > 0 {
		b.WriteString("  fallback_chain: [")
		b.WriteString(strings.Join(w.Prefs.FallbackChain, ", "))
		b.WriteString("]\n")
	}
	b.WriteString(fmt.Sprintf("  provider_preference: %s\n", w.Prefs.ProviderPreference))
	if w.Prefs.ModelStrategy != "" {
		b.WriteString(fmt.Sprintf("  strategy: %s\n", w.Prefs.ModelStrategy))
	}

	// Quality & enforcement configuration
	b.WriteString("\nquality:\n")
	b.WriteString(fmt.Sprintf("  adversarial_depth: %s\n", w.Prefs.AdversarialDepth))
	b.WriteString(fmt.Sprintf("  polish_level: %s\n", w.Prefs.PolishLevel))

	// Security
	b.WriteString("\nsecurity:\n")
	b.WriteString(fmt.Sprintf("  posture: %s\n", w.Prefs.SecurityPosture))
	b.WriteString(fmt.Sprintf("  data_sensitivity: %s\n", w.Prefs.DataSensitivity))
	if len(w.Prefs.ComplianceFrameworks) > 0 {
		b.WriteString("  compliance: [")
		b.WriteString(strings.Join(w.Prefs.ComplianceFrameworks, ", "))
		b.WriteString("]\n")
	}

	// Infrastructure
	if len(w.Prefs.Infrastructure) > 0 {
		b.WriteString("\ninfrastructure:\n")
		b.WriteString("  providers: [")
		b.WriteString(strings.Join(w.Prefs.Infrastructure, ", "))
		b.WriteString("]\n")
		b.WriteString(fmt.Sprintf("  preference: %s\n", w.Prefs.ProviderPreference))
	}

	// Scale
	b.WriteString("\nscale:\n")
	b.WriteString(fmt.Sprintf("  tier: %s\n", w.Prefs.ScaleTier))

	// Domains
	b.WriteString("\ndomains:\n")
	b.WriteString("  auto_detect: true\n")
	if len(w.Prefs.DetectedDomains) > 0 {
		b.WriteString("  detected: [")
		b.WriteString(strings.Join(w.Prefs.DetectedDomains, ", "))
		b.WriteString("]\n")
	}
	if len(w.Prefs.DomainAreas) > 0 {
		b.WriteString("  areas: [")
		b.WriteString(strings.Join(w.Prefs.DomainAreas, ", "))
		b.WriteString("]\n")
	}

	// Team
	b.WriteString("\nteam:\n")
	b.WriteString(fmt.Sprintf("  size: %s\n", w.Prefs.TeamSize))
	if w.Prefs.OpenSource {
		b.WriteString("  open_source: true\n")
	}

	// Standard phase configuration
	b.WriteString("\nphases:\n")
	b.WriteString("  plan:\n")
	b.WriteString("    builtin_tools: [Read, Glob, Grep]\n")
	b.WriteString("    denied_rules: []\n")
	b.WriteString("    allowed_rules: [Read, Glob, Grep]\n")
	b.WriteString("    mcp_enabled: false\n")
	b.WriteString("  execute:\n")
	b.WriteString("    builtin_tools: [Read, Edit, Write, Bash, Glob, Grep]\n")
	b.WriteString("    denied_rules:\n")
	b.WriteString("      - \"Bash(rm -rf *)\"\n")
	b.WriteString("      - \"Bash(git push *)\"\n")
	b.WriteString("      - \"Bash(git reset --hard *)\"\n")
	b.WriteString("      - \"Bash(sudo *)\"\n")
	b.WriteString("    allowed_rules:\n")
	b.WriteString("      - Read\n")
	b.WriteString("      - Edit\n")
	// Add detected build/test/lint commands
	if w.Prefs.TestCmd != "" {
		b.WriteString(fmt.Sprintf("      - \"Bash(%s)\"\n", w.Prefs.TestCmd))
	}
	if w.Prefs.LintCmd != "" {
		b.WriteString(fmt.Sprintf("      - \"Bash(%s)\"\n", w.Prefs.LintCmd))
	}
	b.WriteString("    mcp_enabled: true\n")
	b.WriteString("  verify:\n")
	b.WriteString("    builtin_tools: [Read, Glob, Grep, Bash]\n")
	b.WriteString("    denied_rules:\n")
	b.WriteString("      - Edit\n")
	b.WriteString("      - Write\n")
	b.WriteString("      - \"Bash(rm *)\"\n")
	b.WriteString("    allowed_rules:\n")
	b.WriteString("      - Read\n")
	if w.Prefs.TestCmd != "" {
		b.WriteString(fmt.Sprintf("      - \"Bash(%s)\"\n", w.Prefs.TestCmd))
	}
	if w.Prefs.LintCmd != "" {
		b.WriteString(fmt.Sprintf("      - \"Bash(%s)\"\n", w.Prefs.LintCmd))
	}
	b.WriteString("    mcp_enabled: false\n")

	// Files
	b.WriteString("\nfiles:\n")
	b.WriteString("  protected:\n")
	b.WriteString("    - \".claude/\"\n")
	b.WriteString("    - \".stoke/\"\n")
	b.WriteString("    - CLAUDE.md\n")
	b.WriteString("    - \".env*\"\n")
	b.WriteString("    - stoke.policy.yaml\n")

	// Verification
	b.WriteString("\nverification:\n")
	b.WriteString(fmt.Sprintf("  build: %s\n", w.verifyValue(w.Prefs.BuildCmd != "")))
	b.WriteString(fmt.Sprintf("  tests: %s\n", w.verifyValue(w.Prefs.TestCmd != "")))
	b.WriteString(fmt.Sprintf("  lint: %s\n", w.verifyValue(w.Prefs.LintCmd != "")))
	crossReview := w.Prefs.AdversarialDepth == DepthMaximum
	b.WriteString(fmt.Sprintf("  cross_model_review: %s\n", w.verifyValue(crossReview)))
	b.WriteString("  scope_check: required\n")

	// Build commands
	if w.Prefs.BuildCmd != "" || w.Prefs.TestCmd != "" || w.Prefs.LintCmd != "" {
		b.WriteString("\ncommands:\n")
		if w.Prefs.BuildCmd != "" {
			b.WriteString(fmt.Sprintf("  build: %s\n", w.Prefs.BuildCmd))
		}
		if w.Prefs.TestCmd != "" {
			b.WriteString(fmt.Sprintf("  test: %s\n", w.Prefs.TestCmd))
		}
		if w.Prefs.LintCmd != "" {
			b.WriteString(fmt.Sprintf("  lint: %s\n", w.Prefs.LintCmd))
		}
	}

	return b.String()
}

// --- helpers ---

func (w *Wizard) printf(format string, args ...any) {
	fmt.Fprintf(w.Writer, format, args...)
}

func (w *Wizard) readLine() string {
	scanner := bufio.NewScanner(w.Reader)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

func (w *Wizard) confirm() bool {
	input := w.readLine()
	return strings.EqualFold(input, "y") || strings.EqualFold(input, "yes")
}

func (w *Wizard) askChoice(q Question) int {
	w.printf("  %s:\n", q.Prompt)
	for i, opt := range q.Options {
		marker := "  "
		if i+1 == q.Default {
			marker = "→ "
		}
		w.printf("    %s%d. %s\n", marker, i+1, opt)
	}
	w.printf("  [%d] > ", q.Default)
	input := w.readLine()
	if input == "" {
		return q.Default
	}
	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > len(q.Options) {
		return q.Default
	}
	return n
}

func (w *Wizard) scaleToIndex() int {
	switch w.Prefs.ScaleTier {
	case ScalePrototype:
		return 1
	case ScaleStartup:
		return 2
	case ScaleGrowth:
		return 3
	case ScaleEnterprise:
		return 4
	default:
		return 2
	}
}

func (w *Wizard) verifyValue(enabled bool) string {
	if enabled {
		return "required"
	}
	return "disabled"
}

func (w *Wizard) applyDefaults() {
	if w.Prefs.PrimaryModel == "" {
		w.Prefs.PrimaryModel = providerLabelClaude
	}
	if w.Prefs.ReviewModel == "" {
		w.Prefs.ReviewModel = providerLabelClaude
	}
	if w.Prefs.AdversarialDepth == "" {
		w.Prefs.AdversarialDepth = DepthStandard
	}
	if w.Prefs.PolishLevel == "" {
		w.Prefs.PolishLevel = PolishProduction
	}
	if w.Prefs.ProviderPreference == "" {
		w.Prefs.ProviderPreference = ProviderBestFit
	}
	if w.Prefs.ModelStrategy == "" {
		w.Prefs.ModelStrategy = "balanced"
	}
	if w.Prefs.SecurityPosture == "" {
		w.Prefs.SecurityPosture = "standard"
	}
	if w.Prefs.DataSensitivity == "" {
		w.Prefs.DataSensitivity = "internal"
	}
	if w.Prefs.TeamSize == "" {
		w.Prefs.TeamSize = "solo"
	}
}

func (w *Wizard) countRules() int {
	domains := convergence.DetectDomains(w.ProjectDir)
	base := 66 // base + extended + postmortem
	domainRules := convergence.DomainRules(domains)
	return base + len(domainRules)
}

// GenerateRationale produces a decision log explaining why each config
// choice was made. Written to .stoke/wizard-rationale.md.
func (w *Wizard) GenerateRationale() string {
	var b strings.Builder
	b.WriteString("# R1 Configuration Rationale\n\n")
	b.WriteString("## Decisions Made\n\n")
	for _, r := range w.Prefs.Rationale {
		b.WriteString(fmt.Sprintf("### %s\n", r.Decision))
		b.WriteString(fmt.Sprintf("Evidence: %s\n\n", r.Evidence))
	}
	// Summary
	b.WriteString("## Configuration Summary\n\n")
	b.WriteString(fmt.Sprintf("- Stage: %s\n", w.Prefs.ScaleTier))
	b.WriteString(fmt.Sprintf("- Adversarial depth: %s\n", w.Prefs.AdversarialDepth))
	b.WriteString(fmt.Sprintf("- Polish level: %s\n", w.Prefs.PolishLevel))
	b.WriteString(fmt.Sprintf("- Security: %s\n", w.Prefs.SecurityPosture))
	b.WriteString(fmt.Sprintf("- Data sensitivity: %s\n", w.Prefs.DataSensitivity))
	b.WriteString(fmt.Sprintf("- Model: %s (review: %s)\n", w.Prefs.PrimaryModel, w.Prefs.ReviewModel))
	b.WriteString(fmt.Sprintf("- Team: %s\n", w.Prefs.TeamSize))
	b.WriteString(fmt.Sprintf("- Convergence rules: %d\n", w.countRules()))
	if len(w.Prefs.DetectedDomains) > 0 {
		b.WriteString(fmt.Sprintf("- Detected: %s\n", strings.Join(w.Prefs.DetectedDomains, ", ")))
	}
	if len(w.Prefs.ComplianceFrameworks) > 0 {
		b.WriteString(fmt.Sprintf("- Compliance: %s\n", strings.Join(w.Prefs.ComplianceFrameworks, ", ")))
	}
	return b.String()
}

// WriteRationale writes the decision log to .stoke/wizard-rationale.md.
func (w *Wizard) WriteRationale() error {
	stokeDir := r1dir.JoinFor(w.ProjectDir)
	if err := os.MkdirAll(stokeDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stokeDir, "wizard-rationale.md"), // #nosec G306 -- wizard-generated config consumed by user tooling; 0644 is appropriate.
		[]byte(w.GenerateRationale()), 0644)
}
