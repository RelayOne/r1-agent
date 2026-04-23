package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempYAML writes body to a tempfile and returns the path.
func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}
	return path
}

// mustLoad loads a YAML policy client from body or fails the test.
func mustLoad(t *testing.T, body string) *YAMLClient {
	t.Helper()
	c, err := NewYAMLClient(writeTempYAML(t, body))
	if err != nil {
		t.Fatalf("NewYAMLClient: %v", err)
	}
	return c
}

// TestYAMLEngineParseValid covers case 1: parse valid YAML.
func TestYAMLEngineParseValid(t *testing.T) {
	body := `rules:
  - id: r1
    effect: permit
    actions: [bash, file_write]
    principal_trust: ">= 3"
    when:
      - command matches "^npm install"
      - phase equals "execute"
      - budget_remaining_usd > 1.0
`
	c, err := NewYAMLClient(writeTempYAML(t, body))
	if err != nil {
		t.Fatalf("NewYAMLClient: %v", err)
	}
	if len(c.rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(c.rules))
	}
	r := c.rules[0]
	if r.Effect != "permit" || r.ID != "r1" {
		t.Fatalf("unexpected rule shape: %+v", r)
	}
	if !r.HasTrust || r.TrustOp != ">=" || r.TrustVal != 3 {
		t.Fatalf("trust parse wrong: op=%q val=%d has=%v", r.TrustOp, r.TrustVal, r.HasTrust)
	}
	if len(r.Predicates) != 3 {
		t.Fatalf("want 3 preds, got %d", len(r.Predicates))
	}
}

// TestYAMLEnginePermitMatch covers case 2: permit-match -> Allow.
func TestYAMLEnginePermitMatch(t *testing.T) {
	body := `rules:
  - id: allow-bash
    effect: permit
    actions: [bash]
`
	c := mustLoad(t, body)
	got, err := c.Check(context.Background(), Request{Action: "bash"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Decision != DecisionAllow {
		t.Fatalf("want Allow, got %v (reasons=%v)", got.Decision, got.Reasons)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "allow-bash" {
		t.Fatalf("reasons want=[allow-bash] got=%v", got.Reasons)
	}
}

// TestYAMLEngineForbidOverridesAllow covers case 3: a forbid
// before a permit wins because the engine is first-match.
func TestYAMLEngineForbidOverridesAllow(t *testing.T) {
	body := `rules:
  - id: deny-first
    effect: forbid
    actions: [bash]
  - id: allow-later
    effect: permit
    actions: [bash]
`
	c := mustLoad(t, body)
	got, err := c.Check(context.Background(), Request{Action: "bash"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Decision != DecisionDeny {
		t.Fatalf("want Deny, got %v", got.Decision)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "deny-first" {
		t.Fatalf("want reasons=[deny-first], got %v", got.Reasons)
	}
}

// TestYAMLEngineDefaultDeny covers case 4: no rule matches ->
// default deny with the canonical reason.
func TestYAMLEngineDefaultDeny(t *testing.T) {
	body := `rules:
  - id: allow-file-read
    effect: permit
    actions: [file_read]
`
	c := mustLoad(t, body)
	got, _ := c.Check(context.Background(), Request{Action: "bash"})
	if got.Decision != DecisionDeny {
		t.Fatalf("want Deny, got %v", got.Decision)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "default-deny" {
		t.Fatalf("want reasons=[default-deny], got %v", got.Reasons)
	}
}

// TestYAMLEngineRegexCompileError covers case 5: a regex that
// fails to compile must produce a descriptive load error.
func TestYAMLEngineRegexCompileError(t *testing.T) {
	body := `rules:
  - id: bad-regex
    effect: permit
    actions: [bash]
    when:
      - command matches "(unclosed"
`
	_, err := NewYAMLClient(writeTempYAML(t, body))
	if err == nil {
		t.Fatalf("want compile error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error does not mention invalid regex: %v", err)
	}
}

// TestYAMLEngineMatchesPredicate covers case 6: matches operator
// with both a positive and a negative case.
func TestYAMLEngineMatchesPredicate(t *testing.T) {
	body := `rules:
  - id: npm-only
    effect: permit
    actions: [bash]
    when:
      - command matches "^npm "
`
	c := mustLoad(t, body)
	pos, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"command": "npm install foo"},
	})
	if pos.Decision != DecisionAllow {
		t.Fatalf("positive: want Allow, got %v", pos.Decision)
	}
	neg, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"command": "rm -rf /"},
	})
	if neg.Decision != DecisionDeny {
		t.Fatalf("negative: want Deny, got %v", neg.Decision)
	}
}

// TestYAMLEngineStartswithPredicate covers case 7: startswith
// positive + negative.
func TestYAMLEngineStartswithPredicate(t *testing.T) {
	body := `rules:
  - id: workspace-only
    effect: permit
    actions: [file_write]
    when:
      - path startswith "/workspace/"
`
	c := mustLoad(t, body)
	pos, _ := c.Check(context.Background(), Request{
		Action:  "file_write",
		Context: map[string]any{"path": "/workspace/foo.go"},
	})
	if pos.Decision != DecisionAllow {
		t.Fatalf("positive: want Allow, got %v", pos.Decision)
	}
	neg, _ := c.Check(context.Background(), Request{
		Action:  "file_write",
		Context: map[string]any{"path": "/etc/passwd"},
	})
	if neg.Decision != DecisionDeny {
		t.Fatalf("negative: want Deny, got %v", neg.Decision)
	}
}

// TestYAMLEngineEqualsPredicate covers case 8: equals operator.
func TestYAMLEngineEqualsPredicate(t *testing.T) {
	body := `rules:
  - id: execute-only
    effect: permit
    actions: [bash]
    when:
      - phase equals "execute"
`
	c := mustLoad(t, body)
	ok, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"phase": "execute"},
	})
	if ok.Decision != DecisionAllow {
		t.Fatalf("want Allow, got %v", ok.Decision)
	}
	bad, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"phase": "plan"},
	})
	if bad.Decision != DecisionDeny {
		t.Fatalf("want Deny, got %v", bad.Decision)
	}
}

// TestYAMLEngineInPredicate covers case 9: in-list membership
// for both member and non-member context values.
func TestYAMLEngineInPredicate(t *testing.T) {
	body := `rules:
  - id: tool-allowlist
    effect: permit
    actions: ["*"]
    when:
      - tool_name in [bash, file_read, file_write]
`
	c := mustLoad(t, body)
	member, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"tool_name": "file_read"},
	})
	if member.Decision != DecisionAllow {
		t.Fatalf("member: want Allow, got %v", member.Decision)
	}
	nonMember, _ := c.Check(context.Background(), Request{
		Action:  "bash",
		Context: map[string]any{"tool_name": "net_fetch"},
	})
	if nonMember.Decision != DecisionDeny {
		t.Fatalf("non-member: want Deny, got %v", nonMember.Decision)
	}
}

// TestYAMLEngineNumericPredicates covers case 10: each of the
// four numeric operators in isolation.
func TestYAMLEngineNumericPredicates(t *testing.T) {
	cases := []struct {
		name    string
		pred    string
		value   float64
		wantOK  bool
		wantBad float64
	}{
		{"ge", "trust_level >= 3", 3, true, 2},
		{"le", "trust_level <= 3", 3, true, 4},
		{"gt", "trust_level > 3", 4, true, 3},
		{"lt", "trust_level < 3", 2, true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `rules:
  - id: n
    effect: permit
    actions: [bash]
    when:
      - ` + tc.pred + `
`
			c := mustLoad(t, body)
			got, _ := c.Check(context.Background(), Request{
				Action:  "bash",
				Context: map[string]any{"trust_level": tc.value},
			})
			if got.Decision != DecisionAllow {
				t.Fatalf("ok case: want Allow for %v, got %v", tc.value, got.Decision)
			}
			bad, _ := c.Check(context.Background(), Request{
				Action:  "bash",
				Context: map[string]any{"trust_level": tc.wantBad},
			})
			if bad.Decision != DecisionDeny {
				t.Fatalf("bad case: want Deny for %v, got %v", tc.wantBad, bad.Decision)
			}
		})
	}
}

// TestYAMLEngineMissingContextKey covers case 11: a predicate
// whose key is absent from Context evaluates to false, so a
// rule with an otherwise-permitting shape is skipped.
func TestYAMLEngineMissingContextKey(t *testing.T) {
	body := `rules:
  - id: needs-path
    effect: permit
    actions: [file_write]
    when:
      - path startswith "/workspace/"
`
	c := mustLoad(t, body)
	got, _ := c.Check(context.Background(), Request{
		Action:  "file_write",
		Context: map[string]any{}, // no "path" key
	})
	if got.Decision != DecisionDeny {
		t.Fatalf("want default-Deny, got %v", got.Decision)
	}
	if got.Reasons[0] != "default-deny" {
		t.Fatalf("want default-deny reason, got %v", got.Reasons)
	}
}

// TestYAMLEnginePrincipalTrustOperators covers case 12: the six
// principal_trust operators.
func TestYAMLEnginePrincipalTrustOperators(t *testing.T) {
	cases := []struct {
		op    string
		rhs   int
		trust int
		allow bool
	}{
		{"==", 3, 3, true},
		{"==", 3, 2, false},
		{"!=", 3, 2, true},
		{"!=", 3, 3, false},
		{">=", 3, 3, true},
		{">=", 3, 2, false},
		{"<=", 3, 3, true},
		{"<=", 3, 4, false},
		{">", 3, 4, true},
		{">", 3, 3, false},
		{"<", 3, 2, true},
		{"<", 3, 3, false},
	}
	seen := make(map[string]bool)
	for _, tc := range cases {
		seen[tc.op] = true
		t.Run(tc.op+"_"+boolWord(tc.allow), func(t *testing.T) {
			body := `rules:
  - id: trust-rule
    effect: permit
    actions: [bash]
    principal_trust: "` + tc.op + ` ` + itoa(tc.rhs) + `"
`
			c := mustLoad(t, body)
			got, _ := c.Check(context.Background(), Request{
				Action:  "bash",
				Context: map[string]any{"trust_level": tc.trust},
			})
			wantDecision := DecisionDeny
			if tc.allow {
				wantDecision = DecisionAllow
			}
			if got.Decision != wantDecision {
				t.Fatalf("op=%s trust=%d rhs=%d want=%v got=%v",
					tc.op, tc.trust, tc.rhs, wantDecision, got.Decision)
			}
		})
	}
	// Sanity: every one of the 6 operators exercised.
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if !seen[op] {
			t.Fatalf("operator %q not covered by test table", op)
		}
	}
}

// --- small helpers kept package-local to avoid pulling fmt into prod --

func boolWord(b bool) string {
	if b {
		return "allow"
	}
	return "deny"
}

func itoa(n int) string {
	// Local integer->string to keep the test file self-contained
	// without importing strconv — trivial positive-int formatter.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
