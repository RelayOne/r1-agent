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
		"validation agent",          // role
		"Adversarial Validation",    // task header
		"NOT here to confirm",       // adversarial framing
		"Unsatisfied criteria",      // check 1
		"Missing tests",             // check 2
		"Stubs and TODOs",          // check 3
		"Security issues",           // check 4
		"Tautological tests",        // check 6
		"verdict",                   // output format
		"criteria_status",           // output format
		"gaps",                      // output format
		"Do NOT modify",             // read-only rule
		"Previously Identified Gaps", // gaps block
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
		"independent reviewer",             // role
		"Independent Consensus Review",     // task header
		"Validation Report",                // report section
		`"verdict": "complete"`,            // included report
		"agree_with_validator",             // output format
		"additional_gaps",                  // output format
		"Do NOT modify",                    // read-only rule
		"rubber-stamp",                     // independence instruction
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
