// Package sandguard implements sandbox escape detection for agent environments.
// Inspired by OmO's sandbox monitoring and claw-code's permission enforcement:
//
// Agents running in sandboxed environments may attempt to escape confinement
// via creative tool use. This package detects:
// - Path traversal (../../etc/passwd)
// - Environment variable exfiltration (echo $API_KEY)
// - Network access from restricted contexts
// - Process spawning (exec, system, popen)
// - Symlink attacks
// - Attempts to modify sandbox config itself
//
// Detection is pattern-based with configurable sensitivity levels.
package sandguard

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Threat classifies a detected escape attempt.
type Threat struct {
	Category string `json:"category"` // "path_traversal", "env_exfil", "network", "exec", "symlink", "config"
	Severity string `json:"severity"` // "low", "medium", "high", "critical"
	Detail   string `json:"detail"`
	Input    string `json:"input"` // the offending input
}

// Config controls detection sensitivity.
type Config struct {
	AllowedPaths   []string `json:"allowed_paths"`   // whitelisted path prefixes
	BlockedEnvVars []string `json:"blocked_env_vars"` // env vars that must not be accessed
	AllowNetwork   bool     `json:"allow_network"`
	AllowExec      bool     `json:"allow_exec"`
	AllowSymlinks  bool     `json:"allow_symlinks"`
}

// DefaultConfig returns a strict security configuration.
func DefaultConfig() Config {
	return Config{
		BlockedEnvVars: []string{
			"API_KEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL",
			"AWS_ACCESS", "AWS_SECRET", "GITHUB_TOKEN", "ANTHROPIC_API_KEY",
			"OPENAI_API_KEY", "DATABASE_URL", "PRIVATE_KEY",
		},
		AllowNetwork:  false,
		AllowExec:     false,
		AllowSymlinks: false,
	}
}

// Guard checks inputs for sandbox escape attempts.
type Guard struct {
	config Config
}

// NewGuard creates a sandbox escape detector.
func NewGuard(cfg Config) *Guard {
	return &Guard{config: cfg}
}

// CheckPath validates a file path for traversal attacks.
func (g *Guard) CheckPath(path string) *Threat {
	// Normalize
	cleaned := filepath.Clean(path)

	// Check for path traversal
	if strings.Contains(path, "..") {
		// Check if traversal escapes allowed paths
		if !g.isAllowedPath(cleaned) {
			return &Threat{
				Category: "path_traversal",
				Severity: "high",
				Detail:   "path contains traversal that escapes allowed directories",
				Input:    path,
			}
		}
	}

	// Check for sensitive system paths
	for _, sensitive := range sensitivePaths {
		if strings.HasPrefix(cleaned, sensitive) {
			return &Threat{
				Category: "path_traversal",
				Severity: "critical",
				Detail:   "access to sensitive system path: " + sensitive,
				Input:    path,
			}
		}
	}

	// Check for hidden config files
	base := filepath.Base(cleaned)
	for _, cfg := range sensitiveFiles {
		if base == cfg {
			return &Threat{
				Category: "config",
				Severity: "medium",
				Detail:   "access to sensitive config file: " + cfg,
				Input:    path,
			}
		}
	}

	return nil
}

// CheckCommand validates a shell command for escape attempts.
func (g *Guard) CheckCommand(cmd string) []Threat {
	var threats []Threat

	lower := strings.ToLower(cmd)

	// Exec check
	if !g.config.AllowExec {
		for _, pat := range execPatterns {
			if pat.MatchString(cmd) {
				threats = append(threats, Threat{
					Category: "exec",
					Severity: "high",
					Detail:   "process execution detected",
					Input:    cmd,
				})
				break
			}
		}
	}

	// Network check
	if !g.config.AllowNetwork {
		for _, pat := range networkPatterns {
			if pat.MatchString(lower) {
				threats = append(threats, Threat{
					Category: "network",
					Severity: "high",
					Detail:   "network access detected",
					Input:    cmd,
				})
				break
			}
		}
	}

	// Env exfiltration
	for _, envVar := range g.config.BlockedEnvVars {
		patterns := []string{
			"$" + envVar,
			"${" + envVar + "}",
			"$ENV{" + envVar + "}",
			"os.environ['" + strings.ToLower(envVar) + "']",
			"os.getenv(\"" + envVar + "\")",
			"process.env." + envVar,
		}
		for _, p := range patterns {
			if strings.Contains(cmd, p) || strings.Contains(lower, strings.ToLower(p)) {
				threats = append(threats, Threat{
					Category: "env_exfil",
					Severity: "critical",
					Detail:   "attempt to access blocked env var: " + envVar,
					Input:    cmd,
				})
				break
			}
		}
	}

	// Sandbox config modification
	for _, pat := range configPatterns {
		if pat.MatchString(cmd) {
			threats = append(threats, Threat{
				Category: "config",
				Severity: "critical",
				Detail:   "attempt to modify sandbox configuration",
				Input:    cmd,
			})
			break
		}
	}

	return threats
}

// CheckToolUse validates a tool invocation.
func (g *Guard) CheckToolUse(tool string, args map[string]string) []Threat {
	var threats []Threat

	// Check file paths in args
	for _, val := range args {
		if t := g.CheckPath(val); t != nil {
			threats = append(threats, *t)
		}
	}

	// Check command args
	if cmd, ok := args["command"]; ok {
		threats = append(threats, g.CheckCommand(cmd)...)
	}

	return threats
}

// IsSafe returns true if no threats were detected.
func IsSafe(threats []Threat) bool {
	return len(threats) == 0
}

// MaxSeverity returns the highest severity from a list of threats.
func MaxSeverity(threats []Threat) string {
	severityRank := map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}
	max := 0
	maxStr := ""
	for _, t := range threats {
		if rank := severityRank[t.Severity]; rank > max {
			max = rank
			maxStr = t.Severity
		}
	}
	return maxStr
}

func (g *Guard) isAllowedPath(path string) bool {
	if len(g.config.AllowedPaths) == 0 {
		return false
	}
	abs, _ := filepath.Abs(path)
	for _, allowed := range g.config.AllowedPaths {
		allowedAbs, _ := filepath.Abs(allowed)
		if strings.HasPrefix(abs, allowedAbs) {
			return true
		}
	}
	return false
}

var sensitivePaths = []string{
	"/etc/shadow", "/etc/passwd", "/etc/sudoers",
	"/root", "/proc", "/sys",
	"/var/run/docker.sock",
}

var sensitiveFiles = []string{
	".env", ".env.local", ".env.production",
	"credentials.json", "service-account.json",
	"id_rsa", "id_ed25519",
	".npmrc", ".pypirc",
	"settings.json",
}

var execPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bexec\s*\(`),
	regexp.MustCompile(`\bsystem\s*\(`),
	regexp.MustCompile(`\bpopen\s*\(`),
	regexp.MustCompile(`\bsubprocess\b`),
	regexp.MustCompile(`\bchild_process\b`),
	regexp.MustCompile(`\bos\.system\b`),
	regexp.MustCompile(`\bRuntime\.exec\b`),
}

var networkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bcurl\b`),
	regexp.MustCompile(`\bwget\b`),
	regexp.MustCompile(`\bnc\b`),
	regexp.MustCompile(`\bnetcat\b`),
	regexp.MustCompile(`\bssh\b`),
	regexp.MustCompile(`\bscp\b`),
	regexp.MustCompile(`\bftp\b`),
	regexp.MustCompile(`https?://`),
	regexp.MustCompile(`\bfetch\s*\(`),
	regexp.MustCompile(`\brequests\.\w+\(`),
}

var configPatterns = []*regexp.Regexp{
	regexp.MustCompile(`settings\.json`),
	regexp.MustCompile(`sandbox.*config`),
	regexp.MustCompile(`--no-sandbox`),
	regexp.MustCompile(`--disable-sandbox`),
	regexp.MustCompile(`allowedTools`),
}
