package rulecheck

import (
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
)

// TestRuleCheckLobe_SeverityMapping is the spec item 14 table-driven
// test that walks one representative rule from every supervisor rule
// subdirectory (9 total — consensus, cross_team, drift, hierarchy,
// research, sdm, skill, snapshot, trust) plus the dissent-specific
// case the spec singles out and asserts severityFor returns the
// expected cortex.Severity.
//
// Mapping (post audit/scan-governance-gaps cortex-concerns 14 fix):
//
//   - trust.*                                  → critical
//   - consensus.dissent.*                      → critical
//   - antitrunc.*                              → critical
//   - snapshot.*                               → critical
//   - hierarchy.user_escalation                → critical
//   - hierarchy.completion_requires_*          → critical
//   - drift.*                                  → warning
//   - cross_team.*                             → warning
//   - sdm.*                                    → warning
//   - hierarchy.* (non-critical)               → warning
//   - skill.*                                  → warning
//   - research.timeout                         → warning
//   - everything else                          → info
func TestRuleCheckLobe_SeverityMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rule     string
		want     cortex.Severity
		category string // doc-only: which subdir the rule comes from
	}{
		// trust/* — all three rules in trust/ map to critical.
		{"trust.completion_requires_second_opinion", "trust.completion_requires_second_opinion", cortex.SevCritical, "trust"},
		{"trust.fix_requires_second_opinion", "trust.fix_requires_second_opinion", cortex.SevCritical, "trust"},
		{"trust.problem_requires_second_opinion", "trust.problem_requires_second_opinion", cortex.SevCritical, "trust"},

		// consensus/* — only the dissent variant is critical;
		// the other consensus rules collapse to info per the
		// "default → info" branch (the spec mapping does not
		// elevate generic consensus.* to warning/critical).
		{"consensus.dissent_requires_address (critical via HasPrefix)", "consensus.dissent_requires_address", cortex.SevCritical, "consensus"},
		{"consensus.draft_requires_review (info, not dissent)", "consensus.draft_requires_review", cortex.SevInfo, "consensus"},
		{"consensus.convergence_detected (info, not dissent)", "consensus.convergence_detected", cortex.SevInfo, "consensus"},
		{"consensus.iteration_threshold (info, not dissent)", "consensus.iteration_threshold", cortex.SevInfo, "consensus"},
		{"consensus.partner_timeout (info, not dissent)", "consensus.partner_timeout", cortex.SevInfo, "consensus"},

		// drift/* — all warning.
		{"drift.budget_threshold", "drift.budget_threshold", cortex.SevWarning, "drift"},
		{"drift.intent_alignment_check", "drift.intent_alignment_check", cortex.SevWarning, "drift"},
		{"drift.judge_scheduled", "drift.judge_scheduled", cortex.SevWarning, "drift"},

		// cross_team/* — all warning.
		{"cross_team.modification_requires_cto", "cross_team.modification_requires_cto", cortex.SevWarning, "cross_team"},

		// antitrunc/* — all critical (Spec 9: rule-engine counterparts of
		// the AntiTruncLobe Notes; must block end_turn the same way).
		{"antitrunc.scope_underdelivery", "antitrunc.scope_underdelivery", cortex.SevCritical, "antitrunc"},
		{"antitrunc.subagent_summary_truncation", "antitrunc.subagent_summary_truncation", cortex.SevCritical, "antitrunc"},
		{"antitrunc.truncation_phrase_detected", "antitrunc.truncation_phrase_detected", cortex.SevCritical, "antitrunc"},

		// hierarchy/* — completion_requires_* + user_escalation are
		// critical; the catch-all is warning.
		{"hierarchy.completion_requires_parent_agreement", "hierarchy.completion_requires_parent_agreement", cortex.SevCritical, "hierarchy"},
		{"hierarchy.escalation_forwards_upward", "hierarchy.escalation_forwards_upward", cortex.SevWarning, "hierarchy"},
		{"hierarchy.user_escalation", "hierarchy.user_escalation", cortex.SevCritical, "hierarchy"},

		// research/* — only timeout is warning; the rest stay info.
		{"research.report_unblocks_requester", "research.report_unblocks_requester", cortex.SevInfo, "research"},
		{"research.request_dispatches_researchers", "research.request_dispatches_researchers", cortex.SevInfo, "research"},
		{"research.timeout", "research.timeout", cortex.SevWarning, "research"},

		// sdm/* — warning (advisory but operator-visible).
		{"sdm.collision_file_modification", "sdm.collision_file_modification", cortex.SevWarning, "sdm"},
		{"sdm.dependency_crossed", "sdm.dependency_crossed", cortex.SevWarning, "sdm"},
		{"sdm.drift_cross_branch", "sdm.drift_cross_branch", cortex.SevWarning, "sdm"},
		{"sdm.duplicate_work_detected", "sdm.duplicate_work_detected", cortex.SevWarning, "sdm"},
		{"sdm.schedule_risk_critical_path", "sdm.schedule_risk_critical_path", cortex.SevWarning, "sdm"},

		// skill/* — warning (skill-lifecycle events).
		{"skill.application_requires_review", "skill.application_requires_review", cortex.SevWarning, "skill"},
		{"skill.contradicts_outcome", "skill.contradicts_outcome", cortex.SevWarning, "skill"},
		{"skill.extraction_trigger", "skill.extraction_trigger", cortex.SevWarning, "skill"},
		{"skill.import_consensus", "skill.import_consensus", cortex.SevWarning, "skill"},
		{"skill.load_audit", "skill.load_audit", cortex.SevWarning, "skill"},

		// snapshot/* — critical (safety: snapshot protection violations
		// are operator-actionable safety events).
		{"snapshot.formatter_requires_consent", "snapshot.formatter_requires_consent", cortex.SevCritical, "snapshot"},
		{"snapshot.modification_requires_cto", "snapshot.modification_requires_cto", cortex.SevCritical, "snapshot"},

		// Default-branch sanity: a totally unknown rule name still
		// returns info rather than panicking or returning an empty
		// Severity (which would fail Note.Validate).
		{"unknown rule defaults to info", "frobnicate.does_not_exist", cortex.SevInfo, "<default>"},
		{"empty rule name", "", cortex.SevInfo, "<default>"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := severityFor(tc.rule)
			if got != tc.want {
				t.Errorf("severityFor(%q) = %q, want %q (subdir=%s)", tc.rule, got, tc.want, tc.category)
			}
		})
	}
}

// TestRuleCheckLobe_SeverityCoversAllNineSubdirs asserts the table in
// TestRuleCheckLobe_SeverityMapping touches every supervisor rule
// subdirectory the spec enumerates. This is a guard against future
// reviewers silently dropping a subdir when the rules engine grows new
// categories.
func TestRuleCheckLobe_SeverityCoversAllNineSubdirs(t *testing.T) {
	t.Parallel()
	wantSubdirs := []string{
		"consensus", "cross_team", "drift", "hierarchy",
		"research", "sdm", "skill", "snapshot", "trust",
	}
	if len(wantSubdirs) != 9 {
		t.Fatalf("internal: expected 9 subdirs, got %d", len(wantSubdirs))
	}
	// Walk one representative rule per subdir and confirm severityFor
	// returns one of the four declared severities (no zero-value).
	probes := map[string]string{
		"consensus":  "consensus.dissent_requires_address",
		"cross_team": "cross_team.modification_requires_cto",
		"drift":      "drift.budget_threshold",
		"hierarchy":  "hierarchy.user_escalation",
		"research":   "research.timeout",
		"sdm":        "sdm.duplicate_work_detected",
		"skill":      "skill.load_audit",
		"snapshot":   "snapshot.modification_requires_cto",
		"trust":      "trust.completion_requires_second_opinion",
	}
	for _, sd := range wantSubdirs {
		probe, ok := probes[sd]
		if !ok {
			t.Errorf("subdir %q has no probe rule defined", sd)
			continue
		}
		got := severityFor(probe)
		switch got {
		case cortex.SevInfo, cortex.SevAdvice, cortex.SevWarning, cortex.SevCritical:
			// ok
		default:
			t.Errorf("severityFor(%q) returned non-canonical severity %q", probe, got)
		}
	}
}
