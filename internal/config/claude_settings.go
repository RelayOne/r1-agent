package config

import "encoding/json"

// ClaudeSettings represents the per-worktree settings.json written for Claude Code, controlling permissions and sandbox.
type ClaudeSettings struct {
	APIKeyHelper                 *string            `json:"apiKeyHelper"` // pointer: nil marshals to JSON null
	DisableBypassPermissionsMode string             `json:"disableBypassPermissionsMode,omitempty"`
	Permissions                  PermissionSettings `json:"permissions,omitempty"`
	Sandbox                      *SandboxSettings   `json:"sandbox,omitempty"`
}

// PermissionSettings defines the tool allow/deny rules for a Claude Code session.
type PermissionSettings struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// SandboxSettings controls the fail-closed sandbox configuration for Claude Code execution.
type SandboxSettings struct {
	Enabled                  bool              `json:"enabled"`
	FailIfUnavailable        bool              `json:"failIfUnavailable,omitempty"`
	AutoAllowBashIfSandboxed bool              `json:"autoAllowBashIfSandboxed,omitempty"`
	AllowUnsandboxedCommands bool              `json:"allowUnsandboxedCommands,omitempty"`
	Filesystem               SandboxFilesystem `json:"filesystem,omitempty"`
	Network                  SandboxNetwork    `json:"network,omitempty"`
}

// SandboxFilesystem defines filesystem read/write restrictions for the sandbox.
type SandboxFilesystem struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
	DenyRead   []string `json:"denyRead,omitempty"`
	AllowRead  []string `json:"allowRead,omitempty"`
}

// SandboxNetwork defines allowed network domains for the sandbox.
type SandboxNetwork struct {
	AllowedDomains []string `json:"allowedDomains,omitempty"`
}

// ClaudeSettingsOptions holds the inputs needed to construct a ClaudeSettings value for a specific phase and mode.
type ClaudeSettingsOptions struct {
	Mode                  string
	Phase                 PhasePolicy
	SandboxEnabled        bool
	SandboxAllowedDomains []string
	SandboxAllowWrite     []string
	SandboxAllowRead      []string
}

// BuildClaudeSettings constructs a ClaudeSettings from the given options, applying sandbox and permission rules.
func BuildClaudeSettings(opts ClaudeSettingsOptions) ClaudeSettings {
	settings := ClaudeSettings{
		DisableBypassPermissionsMode: "disable",
		Permissions: PermissionSettings{
			Allow: append([]string(nil), opts.Phase.AllowedRules...),
			Deny:  append([]string(nil), opts.Phase.DeniedRules...),
		},
	}

	if opts.Mode == "mode1" {
		// Explicitly suppress repo-supplied apiKeyHelper in subscription mode.
		// nil pointer marshals to JSON null, which tells Claude Code "no helper".
		settings.APIKeyHelper = nil
	}

	if opts.SandboxEnabled {
		settings.Sandbox = &SandboxSettings{
			Enabled:                  true,
			FailIfUnavailable:        true,
			AutoAllowBashIfSandboxed: false,
			AllowUnsandboxedCommands: false,
			Filesystem: SandboxFilesystem{
				AllowWrite: append([]string(nil), opts.SandboxAllowWrite...),
				AllowRead:  append([]string(nil), opts.SandboxAllowRead...),
			},
			Network: SandboxNetwork{AllowedDomains: append([]string(nil), opts.SandboxAllowedDomains...)},
		}
	}

	return settings
}

// MarshalClaudeSettings serializes the settings to indented JSON suitable for writing to settings.json.
func MarshalClaudeSettings(settings ClaudeSettings) ([]byte, error) {
	return json.MarshalIndent(settings, "", "  ")
}
