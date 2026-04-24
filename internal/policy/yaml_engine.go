package policy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLClient is an in-process policy backend that evaluates a
// list of rules loaded from a YAML file. Evaluation is
// deny-by-default with first-match-wins semantics: the first
// rule whose action / principal_trust / when all satisfy the
// Request decides the verdict. If no rule matches the result is
// DecisionDeny with Reasons=["default-deny"].
type YAMLClient struct {
	rules []compiledRule
	path  string
}

// Effect strings on policy rules. These are part of the on-disk YAML
// contract — users write these verbatim in their rules files — so
// they must stay string-equal to the legacy wire format.
const (
	effectPermit = "permit"
	effectForbid = "forbid"
)

// compiledRule is the parse-time representation of one YAML
// rule. Regex compilation, operator parsing, and numeric
// coercion all happen at load time so Check is allocation-light.
type compiledRule struct {
	Effect     string // "permit" | "forbid"
	ID         string
	Actions    []string // exact match against Request.Action; "*" matches all
	TrustOp    string   // "==" | "!=" | ">=" | "<=" | ">" | "<"
	TrustVal   int
	HasTrust   bool
	Predicates []compiledPred
}

// ruleDoc is the on-disk YAML shape we parse.
type ruleDoc struct {
	Rules []ruleYAML `yaml:"rules"`
}

type ruleYAML struct {
	ID             string   `yaml:"id"`
	Effect         string   `yaml:"effect"`
	Actions        []string `yaml:"actions"`
	PrincipalTrust string   `yaml:"principal_trust"`
	When           []string `yaml:"when"`
}

// NewYAMLClient reads the YAML policy file at path, compiles
// every rule (including regex patterns in `matches` predicates),
// and returns a ready-to-use client. Any parse, regex-compile,
// or schema error is returned verbatim so the startup path can
// fail-hard — there is no partial-load mode.
func NewYAMLClient(path string) (*YAMLClient, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var doc ruleDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	compiled := make([]compiledRule, 0, len(doc.Rules))
	for i, r := range doc.Rules {
		cr, err := compileRule(r)
		if err != nil {
			return nil, fmt.Errorf("policy: rule[%d] %q: %w", i, r.ID, err)
		}
		compiled = append(compiled, cr)
	}
	return &YAMLClient{rules: compiled, path: path}, nil
}

// compileRule validates and compiles a single rule.
func compileRule(r ruleYAML) (compiledRule, error) {
	if r.Effect != effectPermit && r.Effect != effectForbid {
		return compiledRule{}, fmt.Errorf("effect must be \"permit\" or \"forbid\", got %q", r.Effect)
	}
	cr := compiledRule{
		Effect:  r.Effect,
		ID:      r.ID,
		Actions: append([]string(nil), r.Actions...),
	}
	if len(cr.Actions) == 0 {
		// No actions clause -> treat as wildcard (matches all).
		cr.Actions = []string{"*"}
	}
	if pt := strings.TrimSpace(r.PrincipalTrust); pt != "" {
		op, n, err := parseTrustExpr(pt)
		if err != nil {
			return compiledRule{}, fmt.Errorf("principal_trust: %w", err)
		}
		cr.TrustOp = op
		cr.TrustVal = n
		cr.HasTrust = true
	}
	for j, raw := range r.When {
		p, err := parsePredicate(raw)
		if err != nil {
			return compiledRule{}, fmt.Errorf("when[%d]: %w", j, err)
		}
		cr.Predicates = append(cr.Predicates, p)
	}
	return cr, nil
}

// parseTrustExpr parses expressions like ">= 3", "==5", "!=0".
// Whitespace between the operator and the integer is optional.
func parseTrustExpr(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	ops := []string{">=", "<=", "==", "!=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(s, op) {
			rest := strings.TrimSpace(s[len(op):])
			n, err := strconv.Atoi(rest)
			if err != nil {
				return "", 0, fmt.Errorf("invalid integer %q: %w", rest, err)
			}
			return op, n, nil
		}
	}
	return "", 0, fmt.Errorf("unknown trust operator in %q (want one of >=,<=,==,!=,>,<)", s)
}

// Check evaluates req against every compiled rule in order. The
// first rule that matches (action + principal_trust + all
// predicates) wins: permit -> Allow, forbid -> Deny with the
// rule ID as the reason. No match yields the default-deny result.
func (c *YAMLClient) Check(_ context.Context, req Request) (Result, error) {
	for _, r := range c.rules {
		if !actionMatches(r.Actions, req.Action) {
			continue
		}
		if r.HasTrust && !trustMatches(r.TrustOp, r.TrustVal, req.Context) {
			continue
		}
		if !allPredicates(r.Predicates, req.Context) {
			continue
		}
		switch r.Effect {
		case effectPermit:
			return Result{
				Decision: DecisionAllow,
				Reasons:  []string{r.ID},
			}, nil
		case effectForbid:
			return Result{
				Decision: DecisionDeny,
				Reasons:  []string{r.ID},
			}, nil
		}
	}
	return Result{
		Decision: DecisionDeny,
		Reasons:  []string{"default-deny"},
	}, nil
}

// actionMatches returns true when action is in the rule's action
// list, or the rule uses the ["*"] wildcard.
func actionMatches(actions []string, action string) bool {
	if len(actions) == 1 && actions[0] == "*" {
		return true
	}
	for _, a := range actions {
		if a == action {
			return true
		}
		if a == "*" {
			return true
		}
	}
	return false
}

// trustMatches compares the caller-supplied trust_level against
// the rule's principal_trust expression. A missing trust_level
// is treated as 0 per the YAML grammar spec.
func trustMatches(op string, want int, ctx map[string]any) bool {
	got := 0
	if v, ok := ctx["trust_level"]; ok {
		if n, ok := coerceFloat(v); ok {
			got = int(n)
		} else {
			return false
		}
	}
	switch op {
	case "==":
		return got == want
	case "!=":
		return got != want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case ">":
		return got > want
	case "<":
		return got < want
	default:
		return false
	}
}

// allPredicates returns true when every predicate evaluates
// true against ctx (AND semantics). An empty predicate slice
// trivially returns true.
func allPredicates(preds []compiledPred, ctx map[string]any) bool {
	for _, p := range preds {
		if !evalPred(p, ctx) {
			return false
		}
	}
	return true
}

// Compile-time assertion that *YAMLClient satisfies Client.
var _ Client = (*YAMLClient)(nil)
