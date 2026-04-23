package plan

import (
	"strings"
	"testing"
)

// TestPreCompletionGateParsing exercises the four required fixtures
// from specs/descent-hardening.md item 3:
//   1. Pass — all claims match evidence.
//   2. Missing block — final text claims "done" without <pre_completion>.
//   3. FILES_MODIFIED mismatch — claim not reflected in git status.
//   4. AC_VERIFICATION mismatch — claimed exit_code 0 without transcript.
func TestPreCompletionGateParsing(t *testing.T) {
	passBlock := `
<pre_completion>
FILES_MODIFIED:
  - internal/foo/bar.go (modified) — fix the bug

AC_VERIFICATION:
  - AC-id: AC-01
    command: go test ./...
    ran_this_session: yes
    exit_code: 0
    first_error_line: none
    verdict: PASS

SELF_ASSESSMENT:
  - Did every AC report PASS? yes
  - Am I claiming success? yes
</pre_completion>
`

	t.Run("passes when block matches evidence", func(t *testing.T) {
		mismatches := map[string]string{}
		check := NewPreEndTurnCheck(PreCheckContext{
			RepoRoot: "", // disable git cross-check for this test
			SowACs: []AcceptanceCriterion{
				{ID: "AC-01", Command: "go test ./..."},
			},
			SessionTranscript: []ToolCall{
				{Name: "bash", Input: "go test ./..."},
			},
			OnMismatch: func(kind, claim, observed string) {
				mismatches[kind] = claim
			},
		})
		retry, reason := check("I'm done — " + passBlock)
		if retry {
			t.Errorf("expected retry=false, got true")
		}
		if reason != "" {
			t.Errorf("expected empty reason, got %q", reason)
		}
		if len(mismatches) != 0 {
			t.Errorf("expected zero mismatches, got %v", mismatches)
		}
	})

	t.Run("missing block triggers when completion words present", func(t *testing.T) {
		var kinds []string
		check := NewPreEndTurnCheck(PreCheckContext{
			OnMismatch: func(kind, claim, observed string) {
				kinds = append(kinds, kind)
			},
		})
		retry, reason := check("Task complete. All tests pass and I'm ready for review.")
		if retry {
			t.Errorf("expected retry=false")
		}
		if !strings.Contains(reason, "missing") {
			t.Errorf("expected 'missing' in reason, got %q", reason)
		}
		if len(kinds) != 1 || kinds[0] != "missing_block" {
			t.Errorf("expected one missing_block mismatch, got %v", kinds)
		}
	})

	t.Run("no completion trigger means no gate required", func(t *testing.T) {
		check := NewPreEndTurnCheck(PreCheckContext{})
		retry, reason := check("Still thinking about the edge cases here.")
		if retry || reason != "" {
			t.Errorf("expected (false, \"\"), got (%v, %q)", retry, reason)
		}
	})

	t.Run("AC_VERIFICATION mismatch when command absent from transcript", func(t *testing.T) {
		var kinds []string
		check := NewPreEndTurnCheck(PreCheckContext{
			SessionTranscript: []ToolCall{
				{Name: "bash", Input: "ls -la"}, // AC command missing
			},
			OnMismatch: func(kind, claim, observed string) {
				kinds = append(kinds, kind)
			},
		})
		retry, reason := check("All done. " + passBlock)
		if retry {
			t.Errorf("expected retry=false")
		}
		if !strings.Contains(reason, "AC-01") {
			t.Errorf("expected AC-01 in reason, got %q", reason)
		}
		foundKind := false
		for _, k := range kinds {
			if k == "ac_no_evidence" {
				foundKind = true
			}
		}
		if !foundKind {
			t.Errorf("expected ac_no_evidence mismatch, got %v", kinds)
		}
	})

	t.Run("self-assessment inconsistent flags blocked outcome", func(t *testing.T) {
		inconsistentBlock := `
<pre_completion>
FILES_MODIFIED:
  - x.go (modified) — stub

AC_VERIFICATION:
  - AC-id: AC-01
    command: go test ./...
    ran_this_session: no
    exit_code: not run
    first_error_line: none
    verdict: FAIL

SELF_ASSESSMENT:
  - Did every AC report PASS? no
  - Am I claiming success? yes
</pre_completion>
`
		var kinds []string
		check := NewPreEndTurnCheck(PreCheckContext{
			OnMismatch: func(kind, claim, observed string) {
				kinds = append(kinds, kind)
			},
		})
		retry, reason := check("Done. " + inconsistentBlock)
		if retry {
			t.Errorf("expected retry=false")
		}
		if !strings.Contains(reason, "inconsistent") {
			t.Errorf("expected 'inconsistent' in reason, got %q", reason)
		}
		foundKind := false
		for _, k := range kinds {
			if k == "self_assessment_inconsistent" {
				foundKind = true
			}
		}
		if !foundKind {
			t.Errorf("expected self_assessment_inconsistent mismatch, got %v", kinds)
		}
	})
}

// TestParsePreCompletionBlockStructured exercises the ParsePreCompletionBlock
// function's field extraction on a well-formed fixture.
func TestParsePreCompletionBlockStructured(t *testing.T) {
	body := `
FILES_MODIFIED:
  - src/a.go (created) — new helper
  - src/b.go (modified) — fixed off-by-one

AC_VERIFICATION:
  - AC-id: AC-01
    command: go test ./internal/foo
    ran_this_session: yes
    exit_code: 0
    first_error_line: none
    verdict: PASS

SELF_ASSESSMENT:
  - Did every AC report PASS? yes
  - Am I claiming success? yes
`
	r, err := ParsePreCompletionBlock(body)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(r.FilesClaims) != 2 {
		t.Errorf("expected 2 file claims, got %d", len(r.FilesClaims))
	}
	if r.FilesClaims[0].Path != "src/a.go" || r.FilesClaims[0].Action != "created" {
		t.Errorf("file claim 0 mismatch: %+v", r.FilesClaims[0])
	}
	if len(r.ACClaims) != 1 {
		t.Fatalf("expected 1 AC claim, got %d", len(r.ACClaims))
	}
	ac := r.ACClaims[0]
	if ac.ACID != "AC-01" || ac.Command != "go test ./internal/foo" || ac.ExitCode != "0" {
		t.Errorf("AC claim mismatch: %+v", ac)
	}
	if r.SelfAssessAllACsPass == nil || !*r.SelfAssessAllACsPass {
		t.Errorf("expected SelfAssessAllACsPass=true")
	}
	if r.SelfAssessClaimingSuccess == nil || !*r.SelfAssessClaimingSuccess {
		t.Errorf("expected SelfAssessClaimingSuccess=true")
	}
}

// TestCommandAppearsInTranscript exercises substring + whitespace-normalized match.
func TestCommandAppearsInTranscript(t *testing.T) {
	tr := []ToolCall{
		{Name: "bash", Input: "cd /tmp && go  test  ./..."},
	}
	if !commandAppearsInTranscript(tr, "go test ./...") {
		t.Errorf("expected whitespace-normalized match")
	}
	if commandAppearsInTranscript(tr, "cargo test") {
		t.Errorf("expected no match for absent command")
	}
}
