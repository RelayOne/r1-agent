package consent

import (
	"testing"
)

func TestClassifyRisk(t *testing.T) {
	w := NewWorkflow(nil)

	if r := w.Classify("push --force", "git"); r != RiskHigh {
		t.Errorf("expected high risk, got %d", r)
	}
	if r := w.Classify("push origin main", "git"); r != RiskMedium {
		t.Errorf("expected medium risk, got %d", r)
	}
	if r := w.Classify("read file.txt", "file"); r != RiskNone {
		t.Errorf("expected no risk, got %d", r)
	}
}

func TestCheckNoRisk(t *testing.T) {
	w := NewWorkflow(nil)
	d := w.Check("read file.go", "file", "reading a file")
	if d != DecisionApproved {
		t.Errorf("no-risk should auto-approve, got %s", d)
	}
}

func TestCheckLowRiskAutoApprove(t *testing.T) {
	w := NewWorkflow(nil)
	d := w.Check("curl https://example.com", "network", "fetching data")
	if d != DecisionAuto {
		t.Errorf("low risk should auto-approve, got %s", d)
	}
}

func TestCheckMediumRiskApproved(t *testing.T) {
	w := NewWorkflow(func(req *Request) bool {
		return true // approve everything
	})

	d := w.Check("git push origin main", "git", "pushing changes")
	if d != DecisionApproved {
		t.Errorf("expected approved, got %s", d)
	}
}

func TestCheckMediumRiskDenied(t *testing.T) {
	w := NewWorkflow(func(req *Request) bool {
		return false // deny everything
	})

	d := w.Check("git push origin main", "git", "pushing changes")
	if d != DecisionDenied {
		t.Errorf("expected denied, got %s", d)
	}
}

func TestCheckHighRiskDenied(t *testing.T) {
	w := NewWorkflow(func(req *Request) bool {
		return false
	})

	d := w.Check("git push --force", "git", "force push")
	if d != DecisionDenied {
		t.Errorf("expected denied, got %s", d)
	}
}

func TestAutoApproveRule(t *testing.T) {
	w := NewWorkflow(nil) // no manual handler

	w.AddRule(Rule{
		Pattern:  "push origin",
		Category: "git",
		Decision: DecisionApproved,
	})

	d := w.Check("git push origin main", "git", "pushing")
	if d != DecisionApproved {
		t.Errorf("auto-approve rule should match, got %s", d)
	}
}

func TestAutoDenyRule(t *testing.T) {
	w := NewWorkflow(func(req *Request) bool { return true })

	w.AddRule(Rule{
		Pattern:  "drop table",
		Category: "exec",
		Decision: DecisionDenied,
	})

	d := w.Check("DROP TABLE users", "exec", "sql")
	if d != DecisionDenied {
		t.Errorf("auto-deny rule should match, got %s", d)
	}
}

func TestHistory(t *testing.T) {
	w := NewWorkflow(nil)
	w.Check("read file", "file", "reading")
	w.Check("curl url", "network", "fetching")

	history := w.History()
	if len(history) != 2 {
		t.Errorf("expected 2 entries, got %d", len(history))
	}
}

func TestStats(t *testing.T) {
	w := NewWorkflow(nil)
	w.Check("read file", "file", "reading")     // approved (no risk)
	w.Check("curl url", "network", "fetching")   // auto (low risk)

	stats := w.Stats()
	if stats[DecisionApproved] != 1 {
		t.Errorf("expected 1 approved, got %d", stats[DecisionApproved])
	}
	if stats[DecisionAuto] != 1 {
		t.Errorf("expected 1 auto, got %d", stats[DecisionAuto])
	}
}

func TestNoApproveHandler(t *testing.T) {
	w := NewWorkflow(nil) // no handler
	d := w.Check("git merge feature", "git", "merging")
	if d != DecisionDenied {
		t.Errorf("should deny when no handler, got %s", d)
	}
}

func TestDefaultClassifiers(t *testing.T) {
	classifiers := DefaultClassifiers()
	if len(classifiers) < 5 {
		t.Errorf("expected at least 5 classifiers, got %d", len(classifiers))
	}
}
