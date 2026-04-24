package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	Research    bool     // enable AI-powered research convergence
	Provider    Provider // model provider for research (nil disables)
	Stdin       *os.File
	Stdout      *os.File
	Stderr      *os.File
}

// Provider is a minimal interface for AI-powered research convergence.
type Provider interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

// WizardResult is the output of a wizard run.
type WizardResult struct {
	Profile     *skillselect.RepoProfile
	Maturity    MaturityClassification
	Config      WizardConfig
	Skills      []string
	Confidence  map[string]float64
	Rationale   []DetailedRationale
	GeneratedAt time.Time
}

// DetailedRationale documents a single decision with source tracking.
type DetailedRationale struct {
	Field      string  `json:"field"`
	Value      string  `json:"value"`
	Source     string  `json:"source"` // detected|inferred|user|default|research
	Evidence   string  `json:"evidence,omitempty"`
	Confidence float64 `json:"confidence"`
}

// RunWizard executes the wizard with the given options, returning a structured result.
// This is the modern entrypoint for the wizard system.
func RunWizard(ctx context.Context, opts Opts) (*WizardResult, error) {
	if opts.ProjectRoot == "" {
		opts.ProjectRoot = "."
	}
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	result := &WizardResult{
		Confidence:  make(map[string]float64),
		GeneratedAt: time.Now(),
	}

	// Phase 1: Static detection via skillselect
	profile, err := skillselect.DetectProfile(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("detect profile: %w", err)
	}
	result.Profile = profile

	// Phase 2: Heuristic inference
	result.Maturity = InferMaturity(opts.ProjectRoot, profile)
	result.Config = buildDefaultConfig(opts.ProjectRoot, profile, result.Maturity)
	addDetailedRationale(result, profile, result.Maturity)

	// Phase 3: Optional research convergence
	if opts.Research && opts.Provider != nil {
		if err := runResearchConvergence(ctx, opts.Provider, result); err != nil {
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
		// Fall through to proposal for now; interactive mode uses the
		// existing Wizard struct for full question flow
		if err := presentProposal(opts, result); err != nil {
			return nil, err
		}
	}

	// Phase 5: Select skills based on final config
	result.Skills = selectSkillsFromConfig(result.Config, profile)

	// Phase 6: Write output
	if err := writeOutput(opts.ProjectRoot, result); err != nil {
		return nil, fmt.Errorf("write output: %w", err)
	}

	return result, nil
}

// buildDefaultConfig produces a starting WizardConfig from the detected profile and maturity.
func buildDefaultConfig(root string, profile *skillselect.RepoProfile, maturity MaturityClassification) WizardConfig {
	name := filepath.Base(root)
	if abs, err := filepath.Abs(root); err == nil {
		name = filepath.Base(abs)
	}

	return WizardConfig{
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
		Quality:  defaultQuality(maturity.Stage),
		Security: defaultSecurity(maturity.Stage, profile),
		Infrastructure: InfrastructureConfig{
			Providers:  profile.CloudProviders,
			IaC:        detectIaC(profile),
			Preference: "best_fit",
		},
		Scale:   defaultScale(maturity.Stage),
		Domains: detectDomains(profile),
		Skills: WizardSkillsConfig{
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
}

func defaultQuality(stage string) QualityConfig {
	switch stage {
	case "prototype":
		return QualityConfig{
			Verification:     "light",
			CodeQuality:      "relaxed",
			TestRequirements: "minimal",
			ReviewMode:       "self",
			HonestyEnforce:   "strict", // always strict on honesty
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
	default: // mature
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
	if stage == "mature" {
		sec.Posture = "high"
	}
	// Compliance auto-detection from profile
	for _, fw := range profile.Frameworks {
		if fw == "stripe" || fw == "hedera" {
			sec.Compliance = appendIfMissing(sec.Compliance, "pci_dss")
			sec.DataSensitivity = "restricted"
		}
	}
	if hasUserFacing(profile) {
		sec.Compliance = appendIfMissing(sec.Compliance, "pipeda")
		sec.Compliance = appendIfMissing(sec.Compliance, "casl")
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
	return dedupRunStrings(domains)
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

func addDetailedRationale(r *WizardResult, profile *skillselect.RepoProfile, m MaturityClassification) {
	r.Rationale = append(r.Rationale, DetailedRationale{
		Field: "project.stage", Value: m.Stage, Source: "inferred",
		Evidence:   fmt.Sprintf("Composite maturity score: %d. Breakdown: %v", m.Score, m.Breakdown),
		Confidence: 0.85,
	})
	for _, lang := range profile.Languages {
		conf := profile.Confidence[lang+".manifest"]
		if conf == 0 {
			conf = 0.95
		}
		r.Rationale = append(r.Rationale, DetailedRationale{
			Field: "language", Value: lang, Source: "detected", Confidence: conf,
		})
	}
	for _, db := range profile.Databases {
		r.Rationale = append(r.Rationale, DetailedRationale{
			Field: "database", Value: db, Source: "detected", Confidence: 0.95,
		})
	}
	for _, fw := range profile.Frameworks {
		r.Rationale = append(r.Rationale, DetailedRationale{
			Field: "framework", Value: fw, Source: "detected", Confidence: 0.95,
		})
	}
}

// selectSkillsFromConfig converts the final config into a skill list for installation.
func selectSkillsFromConfig(cfg WizardConfig, profile *skillselect.RepoProfile) []string {
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

	return dedupRunStrings(skills)
}

func appendIfMissing(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

func dedupRunStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func joinTrunc(items []string, maxLen int) string {
	s := ""
	for i, item := range items {
		if i > 0 {
			s += ", "
		}
		s += item
		if len(s) > maxLen {
			return s[:maxLen-3] + "..."
		}
	}
	if s == "" {
		return "(none)"
	}
	return s
}

func renderProposal(r *WizardResult) string {
	var b strings.Builder
	b.WriteString("\n+---------------------------------------------------------+\n")
	b.WriteString("|            Stoke Configuration Proposal                  |\n")
	b.WriteString("+---------------------------------------------------------+\n")
	b.WriteString(fmt.Sprintf("| Project: %-48s |\n", r.Config.Project.Name))
	b.WriteString(fmt.Sprintf("| Stage:   %-48s |\n", fmt.Sprintf("%s (score %d/100)", r.Config.Project.Stage, r.Maturity.Score)))
	b.WriteString("+---------------------------------------------------------+\n")
	b.WriteString("| Detected stack:                                         |\n")
	b.WriteString(fmt.Sprintf("|   Languages:  %-43s |\n", joinTrunc(r.Profile.Languages, 43)))
	b.WriteString(fmt.Sprintf("|   Frameworks: %-43s |\n", joinTrunc(r.Profile.Frameworks, 43)))
	b.WriteString(fmt.Sprintf("|   Databases:  %-43s |\n", joinTrunc(r.Profile.Databases, 43)))
	b.WriteString(fmt.Sprintf("|   Cloud:      %-43s |\n", joinTrunc(r.Profile.CloudProviders, 43)))
	b.WriteString("+---------------------------------------------------------+\n")
	b.WriteString(fmt.Sprintf("| Quality:      %-43s |\n", r.Config.Quality.Verification))
	b.WriteString(fmt.Sprintf("| Honesty:      %-43s |\n", r.Config.Quality.HonestyEnforce))
	b.WriteString(fmt.Sprintf("| Security:     %-43s |\n", r.Config.Security.Posture))
	b.WriteString(fmt.Sprintf("| Compliance:   %-43s |\n", joinTrunc(r.Config.Security.Compliance, 43)))
	b.WriteString(fmt.Sprintf("| Models:       %-43s |\n", r.Config.Models.Strategy))
	b.WriteString("+---------------------------------------------------------+\n")
	b.WriteString(fmt.Sprintf("| Skills:       %d selected                                |\n", len(r.Skills)))
	b.WriteString("+---------------------------------------------------------+\n")
	return b.String()
}
