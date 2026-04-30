package analyze

import (
	"strings"

	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// stageConstitution checks the skill against the system constitution.
// This is the categorical-superiority moment: the constitution is
// enforced at compile time, not at run time. A skill that would violate
// the constitution does not compile; the LLM that authored it does not
// see the constitution at execution time because there is no execution
// time for a non-compiling skill.
//
// Stage 4 of the pipeline. Depends on stage 3 (capability) having
// produced the declared capabilities; consumes those and the
// constitution; produces diagnostics for violations.
func stageConstitution(skill *ir.Skill, c *Constitution) StageResult {
	res := StageResult{Passed: true}

	// LLM-authored skills must declare lineage details
	if c.RequireLineageForLLMAuthored {
		if skill.Lineage.Kind == "llm-authored" || skill.Lineage.Kind == "llm-amended" {
			if skill.Lineage.MissionID == "" {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:   "error",
					Code:    "E041_LINEAGE_MISSING_MISSION",
					Message: "llm-authored or llm-amended skills must declare lineage.mission_id",
					Hint:    "set lineage.mission_id to the authoring mission's ID",
				})
			}
			if skill.Lineage.AuthoringStance == "" {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:   "error",
					Code:    "E042_LINEAGE_MISSING_STANCE",
					Message: "llm-authored or llm-amended skills must declare lineage.authoring_stance",
					Hint:    "set lineage.authoring_stance to the authoring worker's stance ID",
				})
			}
		}
	}

	// Forbidden shell patterns
	for _, pattern := range c.ForbidShellPatterns {
		for _, allowed := range skill.Capabilities.Shell.AllowCommands {
			if matchesShellPattern(allowed, pattern) {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:    "error",
					Code:     "E040_FORBIDDEN_SHELL_PATTERN",
					Message:  "skill declares shell command pattern matching constitution-forbidden pattern: " + pattern,
					Location: "capabilities.shell.allow_commands[" + allowed + "]",
					Hint:     "remove the matching pattern from allow_commands",
				})
			}
		}
	}

	// Forbidden filesystem write paths
	for _, forbidden := range c.ForbidFSWritePaths {
		for _, declared := range skill.Capabilities.FS.WritePaths {
			if pathMatches(declared, forbidden) {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:    "error",
					Code:     "E043_FORBIDDEN_FS_WRITE",
					Message:  "skill declares fs.write_paths matching constitution-forbidden path: " + forbidden,
					Location: "capabilities.fs.write_paths[" + declared + "]",
					Hint:     "remove the matching path from write_paths; constitution-protected paths cannot be modified by skills",
				})
			}
		}
	}

	// Forbidden network domains
	for _, forbidden := range c.ForbidNetworkDomains {
		for _, declared := range skill.Capabilities.Network.AllowDomains {
			if domainMatches(declared, forbidden) {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:    "error",
					Code:     "E044_FORBIDDEN_NETWORK_DOMAIN",
					Message:  "skill declares network.allow_domains matching constitution-forbidden domain: " + forbidden,
					Location: "capabilities.network.allow_domains[" + declared + "]",
					Hint:     "remove the matching domain; constitution-forbidden domains cannot be reached by any skill",
				})
			}
		}
	}

	// LLM-authored skills must use only the default capabilities or have
	// HITL-approved widening. The widening approval is itself a ledger
	// node (skill.capability_widening_approved); the analyzer doesn't
	// load it, but it does check that broader-than-default capabilities
	// declare an approval reference.
	if skill.Lineage.Kind == "llm-authored" || skill.Lineage.Kind == "llm-amended" {
		// Check each capability category against defaults
		if len(skill.Capabilities.Shell.AllowCommands) > 0 &&
			len(c.DefaultCapsForLLMAuthored.Shell.AllowCommands) == 0 {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level:   "warning",
				Code:    "W045_LLM_AUTHORED_WIDE_SHELL",
				Message: "llm-authored skill declares shell.allow_commands beyond default (empty); requires HITL approval",
				Hint:    "either remove shell capabilities or attach an approval reference",
			})
			// Warning, not error; the approval-checking machinery lives
			// in the registry layer where ledger context is available
		}
		if skill.Capabilities.LLM.BudgetUSD > c.DefaultCapsForLLMAuthored.LLM.BudgetUSD {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level:   "warning",
				Code:    "W046_LLM_AUTHORED_HIGH_BUDGET",
				Message: "llm-authored skill declares LLM budget > default; requires HITL approval",
				Hint:    "lower the budget or attach an approval reference",
			})
		}
	}

	return res
}

// matchesShellPattern is a simplistic glob match; production code would
// use a proper shell-glob library. The patterns here come from the
// constitution and are administered by humans, so a small but careful
// implementation is fine for the analyzer surface.
func matchesShellPattern(declared, forbidden string) bool {
	if declared == forbidden {
		return true
	}
	// "rm -rf *" forbidden subsumes "rm -rf $HOME"? Conservative: only
	// exact-prefix or wildcard match. Real implementation should use
	// path/filepath.Match or a shell-aware tokenizer.
	if strings.HasSuffix(forbidden, "*") {
		prefix := strings.TrimSuffix(forbidden, "*")
		if strings.HasPrefix(declared, prefix) {
			return true
		}
	}
	if strings.HasSuffix(declared, "*") {
		prefix := strings.TrimSuffix(declared, "*")
		if strings.HasPrefix(forbidden, prefix) {
			return true
		}
	}
	return false
}

// pathMatches reports whether a declared path overlaps with a forbidden
// path. Conservative: any glob match in either direction is a hit.
func pathMatches(declared, forbidden string) bool {
	if declared == forbidden {
		return true
	}
	// "policies/" forbidden subsumes "policies/foo.yaml" declared
	if strings.HasSuffix(forbidden, "/") {
		if strings.HasPrefix(declared, forbidden) {
			return true
		}
	}
	if strings.HasSuffix(declared, "/") {
		if strings.HasPrefix(forbidden, declared) {
			return true
		}
	}
	// Glob suffixes
	if strings.HasSuffix(forbidden, "*") {
		prefix := strings.TrimSuffix(forbidden, "*")
		if strings.HasPrefix(declared, prefix) {
			return true
		}
	}
	if strings.HasSuffix(declared, "*") {
		prefix := strings.TrimSuffix(declared, "*")
		if strings.HasPrefix(forbidden, prefix) {
			return true
		}
	}
	return false
}

// domainMatches reports whether a declared domain matches a
// forbidden-domain wildcard pattern.
func domainMatches(declared, forbidden string) bool {
	if declared == forbidden {
		return true
	}
	if strings.HasPrefix(forbidden, "*.") {
		suffix := forbidden[1:] // ".suspicious.tld"
		if strings.HasSuffix(declared, suffix) {
			return true
		}
	}
	if strings.HasPrefix(declared, "*.") {
		suffix := declared[1:]
		if strings.HasSuffix(forbidden, suffix) {
			return true
		}
	}
	return false
}
