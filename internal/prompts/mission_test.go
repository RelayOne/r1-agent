package prompts

import (
	"strings"
	"testing"
)

func testContext() MissionContext {
	return MissionContext{
		MissionID: "m-1",
		Title:     "JWT Auth",
		Intent:    "Add JWT authentication to the API",
		Phase:     "executing",
		CriteriaBlock: `## Acceptance Criteria (1/3)
- [x] JWT tokens issued on login
- [ ] Invalid tokens return 401
- [ ] Rate limiting returns 429`,
		GapsBlock: `## Open Gaps (1)
- [blocking] No tests for rate limiting`,
		ResearchBlock: `## Research Context
### JWT Auth
Use golang-jwt/jwt/v5 for token parsing`,
		HandoffBlock: `## Previous Agent (agent-1)
Implemented JWT generation. Login endpoint working.`,
		StatusBlock: `## Convergence Status
- Criteria: 1/3 satisfied
- Open gaps: 1 (1 blocking)`,
	}
}

// --- Research Prompt ---

func TestBuildMissionResearchPrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionResearchPrompt(ctx)

	checks := []string{
		"m-1",                    // mission ID
		"JWT Auth",               // title
		"JWT authentication",     // intent
		"Acceptance Criteria",    // criteria block
		"Research Phase",         // phase header
		"Analyze the intent",     // step 1
		"Research the codebase",  // step 2
		"Completeness check",     // step 4
		"Prior Research",         // research block
		"Handoff Context",        // handoff block
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("research prompt missing %q", check)
		}
	}
}

func TestBuildMissionResearchPromptMinimal(t *testing.T) {
	ctx := MissionContext{
		MissionID: "m-2",
		Title:     "Quick Fix",
		Intent:    "Fix the bug",
	}
	prompt := BuildMissionResearchPrompt(ctx)
	if !strings.Contains(prompt, "Quick Fix") {
		t.Error("should include title")
	}
	if strings.Contains(prompt, "Prior Research") {
		t.Error("should not include empty research section")
	}
}

// --- Plan Prompt ---

func TestBuildMissionPlanPrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionPlanPrompt(ctx)

	checks := []string{
		"Planning Phase",        // phase header
		"JWT Auth",              // title
		"Acceptance Criteria",   // criteria
		"Convergence Status",    // status
		"ship_blockers",         // output format
		"tasks",                 // output format
		"verification",          // verification items
		"Research Findings",     // research block
		"Known Gaps",            // gaps block
		"Handoff Context",       // handoff block
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("plan prompt missing %q", check)
		}
	}
}

// --- Execute Prompt ---

func TestBuildMissionExecutePrompt(t *testing.T) {
	ctx := testContext()
	verification := []string{
		"GET /api/auth returns 200 with valid JWT",
		"GET /api/auth returns 401 without token",
	}
	prompt := BuildMissionExecutePrompt(ctx, "Implement JWT token generation", verification)

	checks := []string{
		"implementation agent",                // role
		"JWT Auth",                            // title
		"Implement JWT token generation",      // task
		"Verification Requirements",            // verification header
		"GET /api/auth returns 200",            // verification item
		"No stubs, no TODOs",                  // rule
		"Do NOT run git add",                   // rule
		"Open Gaps",                            // gaps
		"Research Context",                     // research
		"Prior Agent Context",                  // handoff
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("execute prompt missing %q", check)
		}
	}
}

func TestBuildMissionExecutePromptNoVerification(t *testing.T) {
	ctx := MissionContext{MissionID: "m-1", Title: "T", Intent: "I"}
	prompt := BuildMissionExecutePrompt(ctx, "Do the thing", nil)
	if strings.Contains(prompt, "Verification Requirements") {
		t.Error("should not include verification section when empty")
	}
}

// --- Validate Prompt ---

func TestBuildMissionValidatePrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionValidatePrompt(ctx)

	checks := []string{
		"validation agent",              // role
		"Adversarial Validation",        // task header
		"NOT here to confirm",           // adversarial framing
		"User intent is fully satisfied", // gate 1
		"ENTIRE system",                 // gate 2 - whole system, not just changes
		"No TODOs",                      // gate 3 - engineering standards
		"Pre-existing failures",         // gate 2 - pre-existing are your problem
		"Do not rationalize",            // anti-excuse framing
		"verdict",                       // output format
		"criteria_status",               // output format
		"gaps",                          // output format
		"Do NOT modify",                 // read-only rule
		"Previously Identified Gaps",    // gaps block
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("validate prompt missing %q", check)
		}
	}
}

// --- Consensus Prompt ---

func TestBuildMissionConsensusPrompt(t *testing.T) {
	ctx := testContext()
	validationReport := `{"verdict": "complete", "score": 0.85}`
	prompt := BuildMissionConsensusPrompt(ctx, validationReport)

	checks := []string{
		"independent adversarial reviewer", // role
		"DISPROVE Completeness",            // adversarial task header
		"Validation Report",                // report section
		`"verdict": "complete"`,            // included report
		"agree_with_validator",             // output format
		"missed_by_validator",              // output format
		"Do NOT modify",                    // read-only rule
		"rubber-stamp",                     // independence instruction
		"Anti-rationalization",             // anti-excuse protocol
		"Is this REALLY completely done",   // challenge question
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("consensus prompt missing %q", check)
		}
	}
}

// --- No Forbidden Git Operations ---

func TestMissionPromptsNoGitMutations(t *testing.T) {
	ctx := testContext()

	// Validate and consensus prompts should be read-only.
	// They must not contain git mutation commands except in prohibition context.
	readOnlyPrompts := []struct {
		name   string
		prompt string
	}{
		{"validate", BuildMissionValidatePrompt(ctx)},
		{"consensus", BuildMissionConsensusPrompt(ctx, "report")},
	}

	forbidden := []string{"git rebase", "git stash", "git reset"}
	for _, p := range readOnlyPrompts {
		for _, f := range forbidden {
			if strings.Contains(strings.ToLower(p.prompt), f) {
				t.Errorf("%s prompt contains forbidden git operation %q", p.name, f)
			}
		}
	}

	// Execute prompt should mention "Do NOT" with git operations (prohibition is fine)
	execPrompt := BuildMissionExecutePrompt(ctx, "task", nil)
	if !strings.Contains(execPrompt, "Do NOT run git") {
		t.Error("execute prompt should prohibit git operations")
	}
}

// --- Conditional Frontend Prompts ---

func TestExecutePromptFrontendRulesWhenHasFrontend(t *testing.T) {
	ctx := testContext()
	ctx.HasFrontend = true
	ctx.UIFramework = "react"
	ctx.TestFramework = "vitest"
	ctx.HasStorybook = true

	prompt := BuildMissionExecutePrompt(ctx, "Build dashboard", nil)
	checks := []string{
		"REQUIRED",                          // UX rules are required
		"alt text",                          // accessibility
		"keyboard-accessible",               // accessibility
		"responsive",                        // responsive design
		"loading AND error states",          // loading states
		"ErrorBoundary",                     // React-specific
		"key props",                         // React-specific
		"Storybook story",                   // Storybook
		"vitest",                            // test framework
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("frontend execute prompt missing %q", check)
		}
	}
}

func TestExecutePromptNoFrontendRulesForBackend(t *testing.T) {
	ctx := testContext()
	ctx.HasFrontend = false

	prompt := BuildMissionExecutePrompt(ctx, "Add JWT auth", nil)
	if strings.Contains(prompt, "UX Quality Rules") {
		t.Error("backend project should not get UX rules section")
	}
	if strings.Contains(prompt, "ErrorBoundary") {
		t.Error("backend project should not mention React ErrorBoundary")
	}
}

func TestValidatePromptFrontendGateWhenHasFrontend(t *testing.T) {
	ctx := testContext()
	ctx.HasFrontend = true
	ctx.UIFramework = "react"
	ctx.HasStorybook = true

	prompt := BuildMissionValidatePrompt(ctx)
	checks := []string{
		"Gate 3a",              // UX gate present
		"alt attributes",       // accessibility
		"media queries",        // responsive
		"ErrorBoundary",        // React-specific
		"Storybook stories",    // Storybook
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("frontend validate prompt missing %q", check)
		}
	}
}

func TestValidatePromptNoFrontendGateForBackend(t *testing.T) {
	ctx := testContext()
	ctx.HasFrontend = false

	prompt := BuildMissionValidatePrompt(ctx)
	if strings.Contains(prompt, "Gate 3a") {
		t.Error("backend project should not have UX gate in validation")
	}
	if strings.Contains(prompt, "ErrorBoundary") {
		t.Error("backend project should not mention ErrorBoundary in validation")
	}
}

func TestConsensusPromptMentionsUXForFrontend(t *testing.T) {
	ctx := testContext()
	ctx.HasFrontend = true

	prompt := BuildMissionConsensusPrompt(ctx, `{"verdict":"complete"}`)
	if !strings.Contains(prompt, "accessible") {
		t.Error("frontend consensus prompt should mention accessibility")
	}
	if !strings.Contains(prompt, "Responsive") || !strings.Contains(prompt, "screen reader") {
		t.Error("frontend consensus prompt should challenge on responsiveness and screen readers")
	}
}

// --- Agentic Discovery Prompts ---

func TestBuildMissionDiscoveryPrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionDiscoveryPrompt(ctx)

	// Should contain mission info
	if !strings.Contains(prompt, ctx.Title) {
		t.Error("discovery prompt should contain mission title")
	}
	if !strings.Contains(prompt, ctx.Intent) {
		t.Error("discovery prompt should contain mission intent")
	}
	// Should contain discovery protocol sections
	for _, section := range []string{
		"Consumer/Producer Mapping",
		"Reachability Analysis",
		"Cross-Surface Verification",
		"Quality Verification",
	} {
		if !strings.Contains(prompt, section) {
			t.Errorf("discovery prompt should contain %q section", section)
		}
	}
	// Should instruct on output format
	if !strings.Contains(prompt, "FILE:") {
		t.Error("discovery prompt should explain FILE: output format")
	}
	if !strings.Contains(prompt, "GAP:") {
		t.Error("discovery prompt should explain GAP: output format")
	}
}

func TestBuildMissionDiscoveryPromptIncludesResearch(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionDiscoveryPrompt(ctx)
	if !strings.Contains(prompt, "Prior Research") {
		t.Error("discovery prompt should include prior research when available")
	}
}

func TestBuildMissionValidateDiscoveryPrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionValidateDiscoveryPrompt(ctx)

	// Should contain mission info
	if !strings.Contains(prompt, ctx.Title) {
		t.Error("validate discovery prompt should contain mission title")
	}
	// Should contain validation discovery protocol
	for _, section := range []string{
		"THE WORK IS NOT DONE",
		"Cross-surface validation",
		"Consumer/Producer contract verification",
		"Trace every criterion with evidence",
	} {
		if !strings.Contains(prompt, section) {
			t.Errorf("validate discovery prompt should contain %q", section)
		}
	}
	// Should reference gaps
	if !strings.Contains(prompt, "Previously Found Gaps") {
		t.Error("validate discovery prompt should show prior gaps")
	}
	// Should explain surfaces
	for _, surface := range []string{"API", "Web", "CLI", "MCP"} {
		if !strings.Contains(prompt, surface) {
			t.Errorf("validate discovery prompt should mention %s surface", surface)
		}
	}
}

func TestBuildMissionValidateDiscoveryPromptNoGaps(t *testing.T) {
	ctx := MissionContext{
		MissionID: "m-2",
		Title:     "Feature",
		Intent:    "Test",
	}
	prompt := BuildMissionValidateDiscoveryPrompt(ctx)
	// Should NOT include gaps section when empty
	if strings.Contains(prompt, "Previously Found Gaps") {
		t.Error("validate discovery prompt should not show gaps section when no gaps exist")
	}
}

func TestDiscoveryPromptsIncludeMCPTools(t *testing.T) {
	ctx := testContext()

	mcpTools := []string{
		"search_symbols",
		"get_dependencies",
		"search_content",
		"get_file_symbols",
		"impact_analysis",
		"find_symbol_usages",
		"trace_entry_points",
	}

	discovery := BuildMissionDiscoveryPrompt(ctx)
	for _, tool := range mcpTools {
		if !strings.Contains(discovery, tool) {
			t.Errorf("discovery prompt should reference MCP tool %q", tool)
		}
	}

	validate := BuildMissionValidateDiscoveryPrompt(ctx)
	for _, tool := range mcpTools {
		if !strings.Contains(validate, tool) {
			t.Errorf("validate discovery prompt should reference MCP tool %q", tool)
		}
	}

	execute := BuildMissionExecutePrompt(ctx, "task", nil)
	for _, tool := range mcpTools {
		if !strings.Contains(execute, tool) {
			t.Errorf("execute prompt should reference MCP tool %q", tool)
		}
	}
}

func TestDiscoveryPromptsAntiRationalization(t *testing.T) {
	ctx := testContext()

	discovery := BuildMissionDiscoveryPrompt(ctx)
	validate := BuildMissionValidateDiscoveryPrompt(ctx)

	for _, phrase := range []string{
		"Do NOT rationalize",
		"out of scope",
	} {
		if !strings.Contains(discovery, phrase) {
			t.Errorf("discovery prompt should contain anti-rationalization phrase %q", phrase)
		}
		if !strings.Contains(validate, phrase) {
			t.Errorf("validate prompt should contain anti-rationalization phrase %q", phrase)
		}
	}
}

func TestValidatePromptIncludesToolsSection(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionValidatePrompt(ctx)

	for _, tool := range []string{"Read", "Glob", "Grep", "Bash"} {
		if !strings.Contains(prompt, tool) {
			t.Errorf("validate prompt should reference tool %q", tool)
		}
	}
	if !strings.Contains(prompt, "Codebase Tools") {
		t.Error("validate prompt should have Codebase Tools section")
	}
}

func TestConsensusPromptIncludesToolsSection(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionConsensusPrompt(ctx, `{"verdict":"complete"}`)

	for _, tool := range []string{"Read", "Glob", "Grep", "Bash"} {
		if !strings.Contains(prompt, tool) {
			t.Errorf("consensus prompt should reference tool %q", tool)
		}
	}
	if !strings.Contains(prompt, "Codebase Tools") {
		t.Error("consensus prompt should have Codebase Tools section")
	}
}

func TestDiscoveryPromptsAllSurfaces(t *testing.T) {
	ctx := testContext()

	// Prompts use bold markdown: **API**:
	surfaces := []string{"API", "Web", "Mobile", "Desktop", "CLI", "MCP"}

	discovery := BuildMissionDiscoveryPrompt(ctx)
	validate := BuildMissionValidateDiscoveryPrompt(ctx)

	for _, surface := range surfaces {
		bold := "**" + surface + "**:"
		if !strings.Contains(discovery, bold) {
			t.Errorf("discovery prompt should reference %s surface (looking for %q)", surface, bold)
		}
		if !strings.Contains(validate, bold) {
			t.Errorf("validate prompt should reference %s surface (looking for %q)", surface, bold)
		}
	}
}
