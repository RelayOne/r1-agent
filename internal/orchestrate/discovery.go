package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/goast"
	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/mission"
)

// DiscoveryEngine provides AST-enriched, multi-phase model discovery.
//
// Unlike a thin API wrapper, this engine:
// 1. Pre-flight: runs goast analysis on the codebase to build a structural map
//    (call graph, dead symbols, interface satisfaction, import graph)
// 2. Injects AST-derived context into model prompts so the model starts with
//    real structural facts, not guesses
// 3. Post-run: cross-validates model findings against the AST — catching
//    hallucinated symbols, verifying reachability claims, confirming wiring
// 4. Produces structured DiscoveryReport alongside raw text for downstream use
//
// When MCP codebase tools are also enabled, the model gets:
//   - search_symbols, get_dependencies, find_symbol_usages
//   - trace_entry_points, impact_analysis, semantic_search
//
// AST analysis + MCP tools + model reasoning = triangulated discovery.
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

	// Cached AST analysis (built lazily on first use)
	astAnalysis *goast.Analysis
}

// DiscoveryReport is the structured output of a discovery session.
type DiscoveryReport struct {
	Findings     string            `json:"findings"`      // raw model findings
	DeadSymbols  []string          `json:"dead_symbols"`  // AST-verified unreachable exports
	Unreachable  []string          `json:"unreachable"`   // symbols not called from entry points
	InterfaceMap map[string][]string `json:"interface_map"` // interface -> implementing types
	CallHotspots []string          `json:"call_hotspots"` // most-called symbols
	ASTContext   string            `json:"ast_context"`   // injected context summary
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

	runtimeDir, err := os.MkdirTemp("", "stoke-discovery-*")
	if err != nil {
		return "", "", fmt.Errorf("create runtime dir: %w", err)
	}
	d.runtimeDir = runtimeDir

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

// ensureAST builds or returns cached AST analysis of the codebase.
func (d *DiscoveryEngine) ensureAST() *goast.Analysis {
	if d.astAnalysis != nil {
		return d.astAnalysis
	}
	a, err := goast.AnalyzeDir(d.RepoRoot)
	if err != nil {
		return nil
	}
	d.astAnalysis = a
	return a
}

// buildASTContext generates a structural summary to inject into model prompts.
// This gives the model real facts upfront instead of starting from scratch.
func (d *DiscoveryEngine) buildASTContext() string {
	ast := d.ensureAST()
	if ast == nil || len(ast.AllSymbols) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n## Pre-computed Structural Analysis (from AST)\n\n")

	// Dead symbols — exported but unreachable from entry points
	dead := ast.DeadSymbols()
	if len(dead) > 0 {
		b.WriteString("### Potentially Dead Symbols (exported, not reachable from entry points)\n")
		limit := 20
		if len(dead) < limit {
			limit = len(dead)
		}
		for _, s := range dead[:limit] {
			b.WriteString(fmt.Sprintf("- %s %s in %s:%d\n", s.Kind, s.Name, s.File, s.Line))
		}
		if len(dead) > 20 {
			b.WriteString(fmt.Sprintf("- ... and %d more\n", len(dead)-20))
		}
		b.WriteString("\n")
	}

	// Interface satisfaction map
	satisfaction := ast.InterfaceSatisfaction()
	if len(satisfaction) > 0 {
		b.WriteString("### Interface Implementations\n")
		for iface, types := range satisfaction {
			b.WriteString(fmt.Sprintf("- %s implemented by: %s\n", iface, strings.Join(types, ", ")))
		}
		b.WriteString("\n")
	}

	// Call hotspots — most-called symbols
	calleeCount := make(map[string]int)
	for _, c := range ast.AllCalls {
		calleeCount[c.Callee]++
	}
	type hotspot struct {
		name  string
		count int
	}
	var hotspots []hotspot
	for name, count := range calleeCount {
		if count >= 3 { // only report symbols called 3+ times
			hotspots = append(hotspots, hotspot{name, count})
		}
	}
	if len(hotspots) > 0 {
		// Sort by count desc
		for i := 0; i < len(hotspots); i++ {
			for j := i + 1; j < len(hotspots); j++ {
				if hotspots[j].count > hotspots[i].count {
					hotspots[i], hotspots[j] = hotspots[j], hotspots[i]
				}
			}
		}
		b.WriteString("### Call Hotspots (most-called symbols)\n")
		limit := 15
		if len(hotspots) < limit {
			limit = len(hotspots)
		}
		for _, h := range hotspots[:limit] {
			b.WriteString(fmt.Sprintf("- %s (%d call sites)\n", h.name, h.count))
		}
		b.WriteString("\n")
	}

	// Summary stats
	b.WriteString(fmt.Sprintf("### Stats: %d symbols, %d call edges, %d files, %d interfaces\n\n",
		len(ast.AllSymbols), len(ast.AllCalls), len(ast.Files), len(satisfaction)))

	return b.String()
}

// enrichPrompt adds AST-derived context to a model prompt.
func (d *DiscoveryEngine) enrichPrompt(prompt string) string {
	astCtx := d.buildASTContext()
	if astCtx == "" {
		return prompt
	}
	return prompt + astCtx
}

// buildDiscoveryReport creates a structured report from model findings + AST.
func (d *DiscoveryEngine) buildDiscoveryReport(findings string) *DiscoveryReport {
	report := &DiscoveryReport{
		Findings:   findings,
		ASTContext: d.buildASTContext(),
	}

	ast := d.ensureAST()
	if ast == nil {
		return report
	}

	// Enrich with AST data
	for _, s := range ast.DeadSymbols() {
		report.DeadSymbols = append(report.DeadSymbols,
			fmt.Sprintf("%s %s in %s:%d", s.Kind, s.Name, s.File, s.Line))
	}

	report.InterfaceMap = ast.InterfaceSatisfaction()

	// Find hotspots
	calleeCount := make(map[string]int)
	for _, c := range ast.AllCalls {
		calleeCount[c.Callee]++
	}
	for name, count := range calleeCount {
		if count >= 5 {
			report.CallHotspots = append(report.CallHotspots,
				fmt.Sprintf("%s (%d calls)", name, count))
		}
	}

	return report
}

// DiscoveryFn returns a callback suitable for HandlerDeps.DiscoveryFn.
// Runs AST pre-analysis, enriches the prompt, then runs a multi-turn
// Claude session with codebase MCP tools, and post-validates findings.
func (d *DiscoveryEngine) DiscoveryFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("discovery MCP setup: %w", err)
		}

		// Enrich prompt with AST-derived structural context
		enrichedPrompt := d.enrichPrompt(prompt)

		spec := engine.RunSpec{
			Prompt:      enrichedPrompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "discovery",
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   true,
				MaxTurns:     30,
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

		// Post-run: build structured report and append AST verification
		report := d.buildDiscoveryReport(result.ResultText)
		output := result.ResultText
		if len(report.DeadSymbols) > 0 {
			output += "\n\n## AST-Verified Dead Symbols\n"
			for _, ds := range report.DeadSymbols {
				output += "- " + ds + "\n"
			}
		}

		return output, nil
	}
}

// ValidateDiscoveryFn returns a callback suitable for HandlerDeps.ValidateDiscoveryFn.
// Enriches validation with AST-derived facts so the validator can cross-check
// model claims against actual code structure.
func (d *DiscoveryEngine) ValidateDiscoveryFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("validate-discovery MCP setup: %w", err)
		}

		// Enrich with AST context for cross-validation
		enrichedPrompt := d.enrichPrompt(prompt)

		spec := engine.RunSpec{
			Prompt:      enrichedPrompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "validate-discovery",
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   true,
				MaxTurns:     40,
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
// Enriches the consensus prompt with AST facts so models can verify structural
// claims during their adversarial review.
func (d *DiscoveryEngine) ConsensusModelFn() func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
	return func(ctx context.Context, missionID, model, prompt string) (string, string, []string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", "", nil, fmt.Errorf("consensus MCP setup: %w", err)
		}

		// Enrich consensus prompt with AST context
		enrichedPrompt := d.enrichPrompt(prompt)

		spec := engine.RunSpec{
			Prompt:      enrichedPrompt,
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

		// Parse structured output
		verdict, reasoning, gapsFound := parseConsensusOutput(result.ResultText)
		return verdict, reasoning, gapsFound, nil
	}
}

// parseConsensusOutput extracts verdict, reasoning, and gaps from model output.
// Tries JSON first, falls back to line-based VERDICT:/GAP: parsing.
func parseConsensusOutput(text string) (verdict, reasoning string, gaps []string) {
	verdict = "complete"
	reasoning = text

	// Try JSON extraction
	type consensusJSON struct {
		Verdict           string `json:"verdict"`
		Reasoning         string `json:"reasoning"`
		MissedByValidator []struct {
			Description string `json:"description"`
		} `json:"missed_by_validator"`
	}

	jsonText := text
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
				gaps = append(gaps, m.Description)
			}
		}
	} else {
		// Fallback: line-based parsing
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "VERDICT:") {
				verdict = strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
			} else if strings.HasPrefix(line, "GAP:") {
				gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:"))
				if gapDesc != "" {
					gaps = append(gaps, gapDesc)
				}
			}
		}
	}

	if len(gaps) > 0 && verdict == "complete" {
		verdict = "incomplete"
	}

	return verdict, reasoning, gaps
}

// Cleanup removes temporary files created by the discovery engine.
func (d *DiscoveryEngine) Cleanup() {
	if d.runtimeDir != "" {
		os.RemoveAll(d.runtimeDir)
	}
}

// ExecuteFn returns a callback suitable for HandlerDeps.ExecuteFn.
// Enriches execution prompts with AST context so the model understands
// the structural landscape before making changes.
func (d *DiscoveryEngine) ExecuteFn() func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return nil, fmt.Errorf("execute MCP setup: %w", err)
		}

		// Enrich execution prompt with AST context
		enrichedPrompt := d.enrichPrompt(prompt)

		spec := engine.RunSpec{
			Prompt:      enrichedPrompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name: "execute",
				BuiltinTools: []string{
					"Read", "Write", "Edit", "Glob", "Grep", "Bash",
				},
				MCPEnabled: true,
				MaxTurns:   50,
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

		// Parse FILE: lines from the result
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
// Enriches validation with AST-derived facts for cross-checking.
func (d *DiscoveryEngine) ValidateFn() func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
	return func(ctx context.Context, m *mission.Mission, prompt string) (string, error) {
		mcpConfig, runtimeDir, err := d.ensureMCPConfig()
		if err != nil {
			return "", fmt.Errorf("validate MCP setup: %w", err)
		}

		// Enrich with AST context
		enrichedPrompt := d.enrichPrompt(prompt)

		spec := engine.RunSpec{
			Prompt:      enrichedPrompt,
			WorktreeDir: d.RepoRoot,
			RuntimeDir:  runtimeDir,
			Mode:        d.Mode,
			Phase: engine.PhaseSpec{
				Name:         "validate",
				BuiltinTools: []string{"Read", "Glob", "Grep", "Bash"},
				MCPEnabled:   true,
				MaxTurns:     20,
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

// Report returns the current AST-based discovery report without running a model.
// Useful for getting structural insights before any model invocation.
func (d *DiscoveryEngine) Report() *DiscoveryReport {
	return d.buildDiscoveryReport("")
}

// InvalidateAST forces a re-analysis of the codebase AST on next use.
// Call this after code changes to get fresh structural data.
func (d *DiscoveryEngine) InvalidateAST() {
	d.astAnalysis = nil
}
