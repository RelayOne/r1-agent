package wizard

// WizardConfig is the structured representation of .stoke/config.yaml as
// produced by the wizard. It includes everything collected from detection,
// inference, and user input.
type WizardConfig struct {
	Project        ProjectConfig        `yaml:"project" json:"project"`
	Models         ModelsConfig         `yaml:"models" json:"models"`
	Quality        QualityConfig        `yaml:"quality" json:"quality"`
	Security       SecurityConfig       `yaml:"security" json:"security"`
	Infrastructure InfrastructureConfig `yaml:"infrastructure" json:"infrastructure"`
	Scale          ScaleConfig          `yaml:"scale" json:"scale"`
	Domains        []string             `yaml:"domains" json:"domains"`
	Skills         WizardSkillsConfig   `yaml:"skills" json:"skills"`
	Team           TeamConfig           `yaml:"team" json:"team"`
	Risk           RiskConfig           `yaml:"risk" json:"risk"`
}

// ProjectConfig captures project identity.
type ProjectConfig struct {
	Name     string `yaml:"name" json:"name"`
	Stage    string `yaml:"stage" json:"stage"` // prototype|mvp|growth|mature
	Monorepo bool   `yaml:"monorepo,omitempty" json:"monorepo,omitempty"`
}

// ModelsConfig captures AI model strategy preferences.
type ModelsConfig struct {
	Strategy      string   `yaml:"strategy" json:"strategy"`             // balanced|best_quality|cost_optimized|speed
	Subscriptions []string `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
	Execution     string   `yaml:"execution" json:"execution"`           // cli|api|hybrid
	Architect     string   `yaml:"architect" json:"architect"`
	Editor        string   `yaml:"editor" json:"editor"`
	Reviewer      string   `yaml:"reviewer" json:"reviewer"`
}

// QualityConfig captures verification and enforcement preferences.
type QualityConfig struct {
	Verification     string `yaml:"verification" json:"verification"`           // light|standard|thorough|maximum
	CodeQuality      string `yaml:"code_quality" json:"code_quality"`           // relaxed|standard|strict
	TestRequirements string `yaml:"test_requirements" json:"test_requirements"` // minimal|standard|comprehensive
	ReviewMode       string `yaml:"review_mode" json:"review_mode"`             // self|cross_model|multi|human
	HonestyEnforce   string `yaml:"honesty_enforcement" json:"honesty_enforcement"` // light|strict|maximum
}

// SecurityConfig captures security posture and compliance.
type SecurityConfig struct {
	Posture         string   `yaml:"posture" json:"posture"`                   // basic|standard|high|regulated
	Compliance      []string `yaml:"compliance,omitempty" json:"compliance,omitempty"`
	DataSensitivity string   `yaml:"data_sensitivity" json:"data_sensitivity"` // public|internal|confidential|restricted
}

// InfrastructureConfig captures cloud and IaC preferences.
type InfrastructureConfig struct {
	Providers  []string `yaml:"providers,omitempty" json:"providers,omitempty"`
	IaC        string   `yaml:"iac" json:"iac"` // terraform|pulumi|cdk|none
	Preference string   `yaml:"preference" json:"preference"`
	Credits    string   `yaml:"credits,omitempty" json:"credits,omitempty"`
}

// ScaleConfig captures expected operational scale.
type ScaleConfig struct {
	Expected   string `yaml:"expected" json:"expected"`       // small|medium|large|very_large
	Latency    string `yaml:"latency" json:"latency"`         // tolerant|standard|sensitive
	DataVolume string `yaml:"data_volume" json:"data_volume"` // small|medium|large|very_large
}

// WizardSkillsConfig captures skill injection preferences.
type WizardSkillsConfig struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	AlwaysOn     []string `yaml:"always_on,omitempty" json:"always_on,omitempty"`
	AutoDetect   bool     `yaml:"auto_detect" json:"auto_detect"`
	TokenBudget  int      `yaml:"token_budget" json:"token_budget"`
	ResearchFeed bool     `yaml:"research_feed" json:"research_feed"`
}

// TeamConfig captures team characteristics.
type TeamConfig struct {
	Size         string `yaml:"size" json:"size"`                                       // solo|2-5|6-20|20+
	OpenSource   bool   `yaml:"open_source,omitempty" json:"open_source,omitempty"`
	FeatureFlags bool   `yaml:"feature_flags,omitempty" json:"feature_flags,omitempty"`
	I18n         string `yaml:"i18n,omitempty" json:"i18n,omitempty"`                   // none|bilingual|multi
}

// RiskConfig captures agent autonomy and blast radius preferences.
type RiskConfig struct {
	Autonomy    string `yaml:"autonomy" json:"autonomy"`         // conservative|standard|permissive|yolo
	BlastRadius string `yaml:"blast_radius" json:"blast_radius"` // none|read_only|staging|limited_prod|full_prod
}
