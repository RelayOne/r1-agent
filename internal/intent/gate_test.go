package intent

import (
	"strings"
	"testing"
)

func TestClassifyTrivial(t *testing.T) {
	c := Classify("rename foo to bar")
	if c.Class != ClassTrivial {
		t.Errorf("expected trivial, got %s", c.Class)
	}
	if c.Confidence < 0.7 {
		t.Errorf("expected high confidence for trivial, got %f", c.Confidence)
	}
}

func TestClassifyExplicit(t *testing.T) {
	c := Classify("add JWT validation to the login endpoint")
	if c.Class != ClassExplicit {
		t.Errorf("expected explicit, got %s", c.Class)
	}
}

func TestClassifyExploratory(t *testing.T) {
	c := Classify("investigate why the tests are failing on CI")
	if c.Class != ClassExploratory {
		t.Errorf("expected exploratory, got %s", c.Class)
	}

	c2 := Classify("how does the authentication flow work?")
	if c2.Class != ClassExploratory {
		t.Errorf("expected exploratory for question, got %s", c2.Class)
	}
}

func TestClassifyAmbiguous(t *testing.T) {
	c := Classify("maybe improve the API somehow")
	if c.Class != ClassAmbiguous {
		t.Errorf("expected ambiguous, got %s", c.Class)
	}
	if !c.NeedsClarification {
		t.Error("ambiguous tasks should need clarification")
	}
}

func TestClassifyOpenEnded(t *testing.T) {
	c := Classify("redesign the authentication system to support OAuth, SAML, and API keys, " +
		"with rate limiting, audit logging, token rotation, and backward compatibility for existing sessions")
	if c.Class != ClassOpenEnded {
		t.Errorf("expected open_ended, got %s", c.Class)
	}
}

func TestGatePromptContents(t *testing.T) {
	tests := []struct {
		class Class
		check string
	}{
		{ClassTrivial, "Quick Verification"},
		{ClassExplicit, "Intent Verbalization"},
		{ClassExploratory, "Research-First"},
		{ClassOpenEnded, "Decision Framework"},
		{ClassAmbiguous, "Clarification Required"},
	}

	for _, tc := range tests {
		prompt := GatePrompt("task", Classification{Class: tc.class, Confidence: 0.8})
		if !strings.Contains(prompt, tc.check) {
			t.Errorf("gate prompt for %s should contain %q", tc.class, tc.check)
		}
	}
}

func TestGatePromptAmbiguousWarning(t *testing.T) {
	prompt := GatePrompt("task", Classification{Class: ClassAmbiguous, NeedsClarification: true})
	if !strings.Contains(prompt, "WARNING") {
		t.Error("ambiguous gate should include warning")
	}
}

func TestValidateVerbalizationExplicit(t *testing.T) {
	good := "I understand the task. I will add JWT validation to the login handler in auth.go."
	issues := ValidateVerbalization(good, Classification{Class: ClassExplicit})
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}

	bad := "Here's the code change:"
	issues = ValidateVerbalization(bad, Classification{Class: ClassExplicit})
	if len(issues) == 0 {
		t.Error("expected issues for missing intent verbalization")
	}
}

func TestValidateVerbalizationOpenEnded(t *testing.T) {
	good := "I see two approaches. Option A uses middleware, Option B uses decorators. The tradeoff is..."
	issues := ValidateVerbalization(good, Classification{Class: ClassOpenEnded})
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}

	bad := "I will implement the feature."
	issues = ValidateVerbalization(bad, Classification{Class: ClassOpenEnded})
	if len(issues) == 0 {
		t.Error("expected issues for missing approach comparison")
	}
}

func TestValidateVerbalizationAmbiguous(t *testing.T) {
	good := "The task is ambiguous. I'm assuming X. This is NOT doing Y, which is out of scope."
	issues := ValidateVerbalization(good, Classification{Class: ClassAmbiguous})
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidateVerbalizationExploratory(t *testing.T) {
	good := "I investigated the code and found that the auth module uses a custom token format."
	issues := ValidateVerbalization(good, Classification{Class: ClassExploratory})
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidateVerbalizationTrivial(t *testing.T) {
	// Trivial tasks always pass
	issues := ValidateVerbalization("done", Classification{Class: ClassTrivial})
	if len(issues) != 0 {
		t.Error("trivial tasks should always pass validation")
	}
}

func TestRequiresGate(t *testing.T) {
	if RequiresGate(Classification{Class: ClassTrivial, Confidence: 0.9}) {
		t.Error("high-confidence trivial should not require gate")
	}
	if !RequiresGate(Classification{Class: ClassTrivial, Confidence: 0.5}) {
		t.Error("low-confidence trivial should require gate")
	}
	if !RequiresGate(Classification{Class: ClassExplicit, Confidence: 0.9}) {
		t.Error("explicit tasks should always require gate")
	}
}
