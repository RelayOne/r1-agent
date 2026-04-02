package config

import "encoding/json"

type ClaudeSettings struct {
	APIKeyHelper                 *string            `json:"apiKeyHelper"` // pointer: nil marshals to JSON null
	DisableBypassPermissionsMode string             `json:"disableBypassPermissionsMode,omitempty"`
	Permissions                  PermissionSettings `json:"permissions,omitempty"`
	Sandbox                      *SandboxSettings   `json:"sandbox,omitempty"`
}

type PermissionSettings struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type SandboxSettings struct {
	Enabled                  bool              `json:"enabled"`
	FailIfUnavailable        bool              `json:"failIfUnavailable,omitempty"`
	AutoAllowBashIfSandboxed bool              `json:"autoAllowBashIfSandboxed,omitempty"`
	AllowUnsandboxedCommands bool              `json:"allowUnsandboxedCommands,omitempty"`
	Filesystem               SandboxFilesystem `json:"filesystem,omitempty"`
	Network                  SandboxNetwork    `json:"network,omitempty"`
}

type SandboxFilesystem struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
	DenyRead   []string `json:"denyRead,omitempty"`
	AllowRead  []string `json:"allowRead,omitempty"`
}

type SandboxNetwork struct {
	AllowedDomains []string `json:"allowedDomains,omitempty"`
}

type ClaudeSettingsOptions struct {
	Mode                  string
	Phase                 PhasePolicy
	SandboxEnabled        bool
	SandboxAllowedDomains []string
	SandboxAllowWrite     []string
	SandboxAllowRead      []string
}

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

func MarshalClaudeSettings(settings ClaudeSettings) ([]byte, error) {
	return json.MarshalIndent(settings, "", "  ")
}
