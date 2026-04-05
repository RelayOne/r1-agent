package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// ConsensusModelFn returns a callback suitable for HandlerDeps.ConsensusModelFn.
// It runs a model session for each consensus model, passing the adversarial
// consensus prompt. The model must try to disprove completeness.
func (d *DiscoveryEngine) ConsensusModelFn() func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
	return func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", "", nil, fmt.Errorf("consensus MCP setup: %w", err)
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "consensus-" + model,
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   true,
				MaxTurns:     25,
				ReadOnly:     true,
			},
			MCPConfigPath: mcpConfig,
			PoolConfigDir: d.PoolConfigDir,
			PoolAPIKey:    d.PoolAPIKey,
			PoolBaseURL:   d.PoolBaseURL,
		}

		result, err := d.Runner.Run(ctx, spec, nil)
		if err != nil {
			return "", "", nil, fmt.Errorf("consensus run: %w", err)
		}

		// Parse structured output — try JSON first (matches prompt format),
		// then fall back to line-based VERDICT:/GAP: parsing
		verdict := "complete"
		reasoning := result.ResultText
		var gapsFound []string

		// Try to extract JSON from the response
		type consensusJSON struct {
			Verdict          string `json:"verdict"`
			Reasoning        string `json:"reasoning"`
			MissedByValidator []struct {
				Description string `json:"description"`
			} `json:"missed_by_validator"`
		}

		// Find JSON block in output (may be wrapped in markdown code fences)
		jsonText := result.ResultText
		if idx := strings.Index(jsonText, "{"); idx >= 0 {
			if end := strings.LastIndex(jsonText, "}"); end > idx {
				jsonText = jsonText[idx : end+1]
			}
		}

		var parsed consensusJSON
		if err := json.Unmarshal([]byte(jsonText), &parsed); err == nil && parsed.Verdict != "" {
			verdict = parsed.Verdict
			if parsed.Reasoning != "" {
				reasoning = parsed.Reasoning
			}
			for _, m := range parsed.MissedByValidator {
				if m.Description != "" {
					gapsFound = append(gapsFound, m.Description)
				}
			}
		} else {
			// Fallback: line-based parsing
			for _, line := range strings.Split(result.ResultText, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "VERDICT:") {
					verdict = strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
				} else if strings.HasPrefix(line, "GAP:") {
					gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:"))
					if gapDesc != "" {
						gapsFound = append(gapsFound, gapDesc)
					}
				}
			}
		}

		if len(gapsFound) > 0 && verdict == "complete" {
			verdict = "incomplete"
		}

		return verdict, reasoning, gapsFound, nil
	}
}

// Cleanup removes temporary files created by the discovery engine.
func (d *DiscoveryEngine) Cleanup() {
	if d.runtimeDir != "" {
		os.RemoveAll(d.runtimeDir)
	}
}

// ExecuteFn returns a callback suitable for HandlerDeps.ExecuteFn.
// Unlike discovery (read-only, 30 turns), execution has write access
// and runs with a full tool set so the model can implement code changes.
// The MCP codebase tools are still available for the model to understand
// what it's working with during implementation.
func (d *DiscoveryEngine) ExecuteFn() func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return nil, fmt.Errorf("execute MCP setup: %w", err)
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name: "execute",
				BuiltinTools: []string{
					"Read", "Write", "Edit", "Glob", "Grep", "Bash",
				},
				MCPEnabled: true,
				MaxTurns:   50, // Execution needs many turns for implementation + testing
				ReadOnly:   false,
			},
			MCPConfigPath: mcpConfig,
			PoolConfigDir: d.PoolConfigDir,
			PoolAPIKey:    d.PoolAPIKey,
			PoolBaseURL:   d.PoolBaseURL,
		}

		result, err := d.Runner.Run(ctx, spec, nil)
		if err != nil {
			return nil, fmt.Errorf("execute run: %w", err)
		}

		// Parse FILE: lines from the result to identify changed files
		var filesChanged []string
		for _, line := range strings.Split(result.ResultText, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "FILE:") {
				path := strings.TrimSpace(strings.TrimPrefix(line, "FILE:"))
				if path != "" {
					filesChanged = append(filesChanged, path)
				}
			}
		}

		return filesChanged, nil
	}
}

// ValidateFn returns a callback suitable for HandlerDeps.ValidateFn (Layer 3).
// This is a single-shot adversarial validation — less thorough than
// ValidateDiscoveryFn (Layer 4) but still model-driven.
func (d *DiscoveryEngine) ValidateFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("validate MCP setup: %w", err)
		}

		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "validate",
				BuiltinTools: []string{"Read", "Glob", "Grep", "Bash"},
				MCPEnabled:   true,
				MaxTurns:     20, // Single-shot validation needs fewer turns
				ReadOnly:     true,
			},
			MCPConfigPath: mcpConfig,
			PoolConfigDir: d.PoolConfigDir,
			PoolAPIKey:    d.PoolAPIKey,
			PoolBaseURL:   d.PoolBaseURL,
		}

		result, err := d.Runner.Run(ctx, spec, nil)
		if err != nil {
			return "", fmt.Errorf("validate run: %w", err)
		}

		return result.ResultText, nil
	}
}
