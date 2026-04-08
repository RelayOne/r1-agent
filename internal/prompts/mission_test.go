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
		"semantic_search",
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

// --- Decomposition Prompt ---

func TestBuildDecompositionPrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildDecompositionPrompt(ctx, "implement", "Add JWT authentication to the API", 0, 4)

	checks := []string{
		"decomposition agent",          // role
		"JWT Auth",                     // title
		"implement",                    // work type
		"Add JWT authentication",       // scope
		"minimum viable scope",         // key instruction
		"directed acyclic graph",       // DAG requirement
		"No cycles",                    // cycle prohibition
		"depends_on",                   // dependency field
		"critical path",               // critical path identification
		"Parallel safety",             // parallel safety
		"research",                    // work type: research
		"implement",                   // work type: implement
		"test",                        // work type: test
		"review",                      // work type: review
		"validate",                    // work type: validate
		"action",                      // JSON output: action field
		"decompose",                   // JSON output: decompose action
		"execute",                     // JSON output: execute action
		"Research Context",            // includes research when available
		"Handoff Context",             // includes handoff when available
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("decomposition prompt missing %q", check)
		}
	}
}

func TestBuildDecompositionPromptMaxDepth(t *testing.T) {
	ctx := testContext()

	// At max depth (depth >= maxDepth - 1), should force execute
	prompt := BuildDecompositionPrompt(ctx, "implement", "Small task", 3, 4)
	if !strings.Contains(prompt, "FORCED EXECUTE MODE") {
		t.Error("at max depth, decomposition prompt should force execute mode")
	}
	if strings.Contains(prompt, "minimum viable scope") {
		t.Error("at max depth, decomposition prompt should NOT include decomposition rules")
	}

	// Also test exact boundary: depth == maxDepth - 1
	prompt2 := BuildDecompositionPrompt(ctx, "implement", "Small task", 2, 3)
	if !strings.Contains(prompt2, "FORCED EXECUTE MODE") {
		t.Error("at depth == maxDepth-1, should force execute mode")
	}

	// Below max depth should allow decomposition
	prompt3 := BuildDecompositionPrompt(ctx, "implement", "Big task", 0, 3)
	if strings.Contains(prompt3, "FORCED EXECUTE MODE") {
		t.Error("below max depth, should NOT force execute mode")
	}
	if !strings.Contains(prompt3, "minimum viable scope") {
		t.Error("below max depth, should include decomposition rules")
	}
}

// --- Work Node Prompt ---

func TestBuildWorkNodePrompt(t *testing.T) {
	ctx := testContext()
	prompt := BuildWorkNodePrompt(ctx, "implement", "Implement JWT validation in auth/jwt.go", "Add JWT auth to API", "w-1: researching JWT libraries\nw-3: writing JWT tests")

	checks := []string{
		"focused execution agent",     // role
		"JWT Auth",                    // title
		"Implement JWT validation",    // scope
		"Add JWT auth to API",         // parent context
		"researching JWT libraries",   // sibling context
		"writing JWT tests",           // sibling context
		"Do NOT duplicate",            // sibling overlap prevention
		"EXACTLY",                     // scope discipline
		"search_symbols",              // MCP tool
		"get_dependencies",            // MCP tool
		"impact_analysis",             // MCP tool
		"find_symbol_usages",          // MCP tool
		"Anti-Rationalization",        // anti-rationalization
		"Do NOT expand beyond",        // scope discipline
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("work node prompt missing %q", check)
		}
	}
}

func TestBuildWorkNodePromptTypes(t *testing.T) {
	ctx := testContext()

	tests := []struct {
		nodeType string
		expected []string
	}{
		{
			"research",
			[]string{"Research Instructions", "verified sources", "UNVERIFIED", "Do NOT write code"},
		},
		{
			"implement",
			[]string{"Implementation Instructions", "no stubs, no TODOs", "Maximum Complete Effort", "production-grade"},
		},
		{
			"test",
			[]string{"Test Instructions", "ADVERSARIAL", "edge cases", "error paths", "concurrent access"},
		},
		{
			"review",
			[]string{"Review Instructions", "security issues", "Do NOT modify files", "file:line"},
		},
		{
			"validate",
			[]string{"Validation Instructions", "entry point", "end-to-end", "verdict"},
		},
	}

	for _, tt := range tests {
		prompt := BuildWorkNodePrompt(ctx, tt.nodeType, "Do the thing", "", "")
		for _, exp := range tt.expected {
			if !strings.Contains(prompt, exp) {
				t.Errorf("work node prompt (type=%s) missing %q", tt.nodeType, exp)
			}
		}
	}
}

// --- Monitor Prompt ---

func TestBuildMonitorPrompt(t *testing.T) {
	ctx := testContext()
	childResults := []string{
		"Child 1: Implemented JWT validation function in auth/jwt.go",
		"Child 2: Wrote tests for JWT validation in auth/jwt_test.go",
	}
	prompt := BuildMonitorPrompt(ctx, "Add JWT auth to the API", childResults)

	checks := []string{
		"monitor agent",               // role
		"JWT Auth",                    // title
		"Add JWT auth to the API",     // parent scope
		"Child 1",                     // child result 1
		"Child 2",                     // child result 2
		"Implemented JWT validation",  // child content
		"Wrote tests for JWT",         // child content
		"Completeness Check",          // step 1
		"Gap Detection",               // step 2 — the critical gap check
		"Fell Through Cracks",         // gap language
		"Integration Verification",    // step 3
		"Integration glue",            // gap: integration
		"Missing wiring",              // gap: wiring
		"Interface mismatches",        // gap: interface mismatch
		"complete",                    // status: complete
		"incomplete",                  // status: incomplete
		"fix_tasks",                   // fix tasks in output
		"evidence",                    // evidence requirement
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("monitor prompt missing %q", check)
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

// --- Convergence Spec Requirements ---

func TestResearchPromptResearchOverRecall(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionResearchPrompt(ctx)

	checks := []string{
		"STALE CACHE",               // training data not trusted
		"CURRENT documentation",     // verify against current docs
		"UNVERIFIED",                // mark unverified facts
		"source citations",          // require citations
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("research prompt missing research-over-recall mandate: %q", check)
		}
	}
}

func TestExecutePromptConvergencePhilosophy(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionExecutePrompt(ctx, "Build feature", nil)

	checks := []string{
		"Maximum Complete Effort",     // max effort philosophy
		"Scope Expansion",             // scope expansion, not defense
		"No Excuses, No Deferrals",    // no excuses
		"pre-existing issue",          // fix pre-existing
		"ADVERSARIAL tests",           // adversarial testing
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("execute prompt missing convergence philosophy: %q", check)
		}
	}
}

func TestValidatePromptFullEngineeringStandards(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionValidatePrompt(ctx)

	// Must cover all major engineering standard categories from the spec
	categories := []string{
		"RBAC",                    // security: authorization
		"CORS",                    // security: CORS
		"CSP headers",             // security: CSP
		"Rate limiting",           // security: rate limiting
		"CSRF",                    // security: CSRF
		"CVEs",                    // security: dependency scanning
		"Audit trail",             // security: audit
		"Idempotency",             // reliability
		"Circuit breakers",        // reliability
		"Graceful degradation",    // reliability
		"Graceful shutdown",       // reliability
		"Timeouts",                // reliability
		"Connection pooling",      // reliability
		"Transaction boundaries",  // reliability
		"Structured logging",      // observability
		"Distributed tracing",     // observability
		"Design patterns",         // architecture
		"Migrations",              // database
		"N+1",                     // database
		"OpenAPI",                 // API design
		"Cursor-based pagination", // API design
	}
	for _, cat := range categories {
		if !strings.Contains(prompt, cat) {
			t.Errorf("validate prompt missing engineering standard: %q", cat)
		}
	}
}

func TestValidatePromptEvidenceRequirements(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionValidatePrompt(ctx)

	checks := []string{
		"Evidence-Based Done Claims",
		"Vague affirmations",
		"AUTOMATICALLY REJECTED",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("validate prompt missing evidence requirement: %q", check)
		}
	}
}

func TestConsensusPromptWhyNotProtocol(t *testing.T) {
	ctx := testContext()
	prompt := BuildMissionConsensusPrompt(ctx, "report")

	checks := []string{
		"Why Not",                                // Why Not protocol
		"avoidance of effort",                    // effort check
		"penetration tester",                     // security challenge
		"10x load",                               // scalability challenge
		"Fresh-Context Adversarial Review",       // fresh context
		"sunk-cost bias",                         // no bias
		"Anti-Hallucination Requirements",        // anti-hallucination
		"file:line",                              // evidence requirement
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("consensus prompt missing spec requirement: %q", check)
		}
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

// --- Micro-Convergence Validation Prompt Tests ---

func TestBuildNodeValidationPrompt(t *testing.T) {
	prompt := BuildNodeValidationPrompt("implement", "add auth login", "implemented login in auth.go:10")
	checks := []string{
		"adversarial validator",
		"implement",
		"add auth login",
		"implemented login in auth.go:10",
		"gaps",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("node validation prompt missing %q", c)
		}
	}
}

func TestBuildNodeValidationPromptTypes(t *testing.T) {
	types := map[string]string{
		"implement": "error paths",
		"test":      "edge cases",
		"research":  "verified against actual code",
		"review":    "file:line citations",
	}
	for nodeType, expected := range types {
		prompt := BuildNodeValidationPrompt(nodeType, "scope", "output")
		if !strings.Contains(prompt, expected) {
			t.Errorf("node validation for type %q should contain %q", nodeType, expected)
		}
	}
}

func TestBuildDecompositionValidationPrompt(t *testing.T) {
	prompt := BuildDecompositionValidationPrompt("add auth", "1. implement login\n2. add tests")
	checks := []string{
		"adversarial validator",
		"decomposition",
		"add auth",
		"implement login",
		"Completeness",
		"Minimum Scope",
		"Dependencies",
		"Overlap",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("decomposition validation prompt missing %q", c)
		}
	}
}

func TestBuildResearchValidationPrompt(t *testing.T) {
	prompt := BuildResearchValidationPrompt("find auth endpoints", "found login at api/auth.go:15")
	checks := []string{
		"adversarial validator",
		"research",
		"find auth endpoints",
		"found login at api/auth.go:15",
		"Accuracy",
		"Completeness",
		"Verification",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("research validation prompt missing %q", c)
		}
	}
}

func TestBuildPlanValidationPrompt(t *testing.T) {
	prompt := BuildPlanValidationPrompt("implement auth system", "1. Add login endpoint\n2. Add tests")
	checks := []string{
		"adversarial validator",
		"plan",
		"implement auth system",
		"Add login endpoint",
		"Completeness",
		"Ordering",
		"Feasibility",
		"Risk",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("plan validation prompt missing %q", c)
		}
	}
}
