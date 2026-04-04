package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/mcp"
	"github.com/ericmacdougall/stoke/internal/mission"
)

// DiscoveryEngine provides default implementations of DiscoveryFn and
// ValidateDiscoveryFn that use the Claude engine with MCP codebase tools.
//
// When the model runs a discovery loop, it gets access to:
//   - search_symbols: Find functions, types, classes by name
//   - get_dependencies: Import/dependent tracing
//   - search_content: Semantic content search
//   - get_file_symbols: List symbols in a file
//   - impact_analysis: Transitive dependency impact
//
// The model uses these tools in a multi-turn loop to map the codebase
// against the mission intent, trace consumer/producer relationships,
// and verify cross-surface reachability.
type DiscoveryEngine struct {
	Runner   engine.CommandRunner
	RepoRoot string
	Mode     engine.AuthMode

	// PoolConfigDir and PoolAPIKey for authentication
	PoolConfigDir string
	PoolAPIKey    string
	PoolBaseURL   string

	// mcpConfigPath is lazily created on first use
	mcpConfigPath string
	runtimeDir    string
}

// NewDiscoveryEngine creates a DiscoveryEngine that uses the given runner.
func NewDiscoveryEngine(runner engine.CommandRunner, repoRoot string) *DiscoveryEngine {
	return &DiscoveryEngine{
		Runner:   runner,
		RepoRoot: repoRoot,
		Mode:     engine.AuthModeMode1,
	}
}

// ensureMCPConfig creates the MCP config and runtime directory if needed.
func (d *DiscoveryEngine) ensureMCPConfig() (string, string, error) {
	if d.mcpConfigPath != "" {
		return d.mcpConfigPath, d.runtimeDir, nil
	}

	// Create a runtime directory for discovery sessions
	runtimeDir, err := os.MkdirTemp("", "stoke-discovery-*")
	if err != nil {
		return "", "", fmt.Errorf("create runtime dir: %w", err)
	}
	d.runtimeDir = runtimeDir

	// Find the stoke binary for MCP server
	binaryPath, err := os.Executable()
	if err != nil {
		binaryPath = "stoke" // fallback to PATH lookup
	}

	configPath := filepath.Join(runtimeDir, "mcp-codebase.json")
	if err := mcp.WriteMCPConfig(configPath, binaryPath, d.RepoRoot); err != nil {
		return "", "", fmt.Errorf("write MCP config: %w", err)
	}

	d.mcpConfigPath = configPath
	return configPath, runtimeDir, nil
}

// DiscoveryFn returns a callback suitable for HandlerDeps.DiscoveryFn.
// It runs a multi-turn Claude session with codebase MCP tools enabled.
func (d *DiscoveryEngine) DiscoveryFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("discovery MCP setup: %w", err)
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "discovery",
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   true,
				MaxTurns:     30, // Multi-turn: model needs many turns to trace code
				ReadOnly:     true,
			},
			MCPConfigPath: mcpConfig,
			PoolConfigDir: d.PoolConfigDir,
			PoolAPIKey:    d.PoolAPIKey,
			PoolBaseURL:   d.PoolBaseURL,
		}

		result, err := d.Runner.Run(ctx, spec, nil)
		if err != nil {
			return "", fmt.Errorf("discovery run: %w", err)
		}

		return result.ResultText, nil
	}
}

// ValidateDiscoveryFn returns a callback suitable for HandlerDeps.ValidateDiscoveryFn.
// It runs a multi-turn Claude session for adversarial validation with codebase tools.
func (d *DiscoveryEngine) ValidateDiscoveryFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("validate-discovery MCP setup: %w", err)
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "validate-discovery",
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   true,
				MaxTurns:     40, // Validation needs even more turns to be thorough
				ReadOnly:     true,
			},
			MCPConfigPath: mcpConfig,
			PoolConfigDir: d.PoolConfigDir,
			PoolAPIKey:    d.PoolAPIKey,
			PoolBaseURL:   d.PoolBaseURL,
		}

		result, err := d.Runner.Run(ctx, spec, nil)
		if err != nil {
			return "", fmt.Errorf("validate-discovery run: %w", err)
		}

		return result.ResultText, nil
	}
}

// Cleanup removes temporary files created by the discovery engine.
func (d *DiscoveryEngine) Cleanup() {
	if d.runtimeDir != "" {
		os.RemoveAll(d.runtimeDir)
	}
}
