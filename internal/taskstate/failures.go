package taskstate

// FailureCode is a typed reason for attempt failure.
// Each code maps to a concrete, machine-detectable deception pattern.
// Free-text excuses are not failure codes.
type FailureCode string

const (
	// --- Diff evidence ---
	FailureNoDiff                  FailureCode = "NO_DIFF"
	FailureWrongFiles              FailureCode = "WRONG_FILES"
	FailureProtectedPathTouched    FailureCode = "PROTECTED_PATH_TOUCHED"

	// --- Build/test/lint ---
	FailureBuildFailed             FailureCode = "BUILD_FAILED"
	FailureTestsFailed             FailureCode = "TESTS_FAILED"
	FailureLintFailed              FailureCode = "LINT_FAILED"

	// --- Review ---
	FailureReviewRejected          FailureCode = "REVIEW_REJECTED"

	// --- Deception patterns (from enforcer hooks) ---
	FailurePlaceholderCode         FailureCode = "PLACEHOLDER_CODE"
	FailureTypeBypass              FailureCode = "TYPE_BYPASS"        // @ts-ignore, as any, # type: ignore
	FailureEmptyCatch              FailureCode = "EMPTY_CATCH"
	FailureWeakTest                FailureCode = "WEAK_TEST"          // test that always passes
	FailureTautologicalTest        FailureCode = "TAUTOLOGICAL_TEST"  // expect(true).toBe(true)
	FailureSkippedTest             FailureCode = "SKIPPED_TEST"       // .skip, .only
	FailureNoEvidenceForFixedClaim FailureCode = "NO_EVIDENCE_FOR_FIXED_CLAIM"
	FailureSelfGrantedSkip         FailureCode = "SELF_GRANTED_SKIP"  // model claims "pre-existing" or "out of scope"

	// --- Sandbox/auth/MCP ---
	FailureSandboxViolation        FailureCode = "SANDBOX_VIOLATION"
	FailureMCPViolation            FailureCode = "MCP_VIOLATION"
	FailureAuthModeViolation       FailureCode = "AUTH_MODE_VIOLATION"

	// --- Operational ---
	FailureRepeatedFailure         FailureCode = "REPEATED_FAILURE"
	FailureTimeout                 FailureCode = "TIMEOUT"
)

// FailureDetail is one specific instance of a failure.
type FailureDetail struct {
	Code    FailureCode `json:"code"`
	File    string      `json:"file,omitempty"`
	Line    int         `json:"line,omitempty"`
	Message string      `json:"message"`
	Raw     string      `json:"raw,omitempty"`   // exact error output
	FixHint string      `json:"fix_hint,omitempty"`
}

// Fingerprint returns a stable string for dedup.
// Same fingerprint twice = escalate to human.
func Fingerprint(codes []FailureCode, primaryFile string) string {
	if len(codes) == 0 {
		return "unknown"
	}
	fp := string(codes[0])
	if primaryFile != "" {
		fp += ":" + primaryFile
	}
	for _, c := range codes[1:] {
		fp += "+" + string(c)
	}
	return fp
}
