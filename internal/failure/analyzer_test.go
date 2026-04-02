package failure

import (
	"testing"
)

func TestClassifyBuildFailureTS(t *testing.T) {
	a := Analyze(
		"src/auth.ts(45,12): error TS2339: Property 'user' does not exist on type 'Request'.\nsrc/auth.ts(72,5): error TS2322: Type mismatch.",
		"", "")
	if a.Class != BuildFailed {
		t.Errorf("class=%q, want BuildFailed", a.Class)
	}
	if len(a.Specifics) != 2 {
		t.Fatalf("specifics=%d, want 2", len(a.Specifics))
	}
	if a.Specifics[0].File != "src/auth.ts" {
		t.Errorf("file=%q", a.Specifics[0].File)
	}
	if a.Specifics[0].Line != 45 {
		t.Errorf("line=%d", a.Specifics[0].Line)
	}
}

func TestClassifyBuildFailureGo(t *testing.T) {
	a := Analyze("./main.go:12:5: undefined: FooBar\n./main.go:15:2: syntax error: unexpected }", "", "")
	if a.Class != BuildFailed {
		t.Errorf("class=%q", a.Class)
	}
	if len(a.Specifics) < 2 {
		t.Fatalf("specifics=%d, want >=2", len(a.Specifics))
	}
}

func TestClassifyTestFailureJest(t *testing.T) {
	a := Analyze("", "PASS src/utils.test.ts\nFAIL src/auth.test.ts\n  Expected: 429\n  Received: 200", "")
	if a.Class != TestsFailed {
		t.Errorf("class=%q", a.Class)
	}
	if len(a.Specifics) < 1 {
		t.Fatal("expected specifics")
	}
}

func TestClassifyTestFailureGo(t *testing.T) {
	a := Analyze("", "--- FAIL: TestRateLimit (0.05s)\n    rate_test.go:42: expected 429", "")
	if a.Class != TestsFailed {
		t.Errorf("class=%q", a.Class)
	}
}

func TestClassifyPolicyViolation(t *testing.T) {
	a := Analyze("", "", "found @ts-ignore in diff\n  3:1  error  no-ts-ignore")
	if a.Class != PolicyViolation {
		t.Errorf("class=%q, want PolicyViolation", a.Class)
	}
}

func TestClassifyEslintDisable(t *testing.T) {
	a := Analyze("", "", "// eslint-disable-next-line no-unused-vars")
	if a.Class != PolicyViolation {
		t.Errorf("class=%q", a.Class)
	}
}

func TestRetryOnFirstFailure(t *testing.T) {
	a := &Analysis{Class: BuildFailed, Summary: "2 errors"}
	d := ShouldRetry(a, 1, nil)
	if d.Action != Retry {
		t.Errorf("action=%d, want Retry", d.Action)
	}
}

func TestEscalateOnThirdAttempt(t *testing.T) {
	a := &Analysis{Class: BuildFailed, Summary: "errors"}
	d := ShouldRetry(a, 3, nil)
	if d.Action != Escalate {
		t.Errorf("action=%d, want Escalate", d.Action)
	}
}

func TestEscalateOnSameError(t *testing.T) {
	first := &Analysis{Class: BuildFailed, Summary: "type error in auth.ts"}
	second := &Analysis{Class: BuildFailed, Summary: "type error in auth.ts"}
	d := ShouldRetry(second, 2, first)
	if d.Action != Escalate {
		t.Errorf("same error should escalate")
	}
}

func TestPolicyViolationRetryHasConstraint(t *testing.T) {
	a := &Analysis{Class: PolicyViolation}
	d := ShouldRetry(a, 1, nil)
	if d.Action != Retry {
		t.Error("policy violation should retry once")
	}
	if d.Constraint == "" {
		t.Error("expected constraint")
	}
}

func TestTimeoutEscalatesOnSecond(t *testing.T) {
	a := &Analysis{Class: Timeout}
	d1 := ShouldRetry(a, 1, nil)
	if d1.Action != Retry {
		t.Error("first timeout should retry")
	}
	d2 := ShouldRetry(a, 2, nil)
	if d2.Action != Escalate {
		t.Error("second timeout should escalate")
	}
}

func TestRootCauseClusters(t *testing.T) {
	details := []Detail{
		{Message: "TS2339: Property 'user' missing"},
		{Message: "TS2322: Type mismatch"},
		{Message: "TS2339: Property 'session' missing"},
	}
	cause := inferRootCause(details, "")
	if cause == "" {
		t.Error("expected root cause")
	}
}

func TestAnalyzeIncomplete(t *testing.T) {
	a := Analyze("", "", "")
	if a.Class != Incomplete {
		t.Errorf("class=%q, want Incomplete", a.Class)
	}
}
