// Package policy carries the parsed `throttling:` block from
// r1.policy.yaml. It is a leaf package (depends only on the standard
// library + golang.org/x/time/rate + gopkg.in/yaml.v3) so it can be
// imported by both `internal/config` (the policy loader) and
// `internal/throttle` (the limiter) without creating an import cycle
// — the config package already pulls in internal/mcp for the
// ServerConfig type, and the mcp package needs to import throttle for
// the Limiter type used in WithThrottler.
//
// See specs/per-tool-throttling.md §Data Models for the YAML shape.
package policy

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// Config is the parsed `throttling:` top-level block. The block is
// optional. When absent the throttle package substitutes its bundled
// default. When the operator supplies a partial block we deep-merge
// at the scope level (per_session inherits from defaults when
// missing) but NOT at the field level inside a tool entry — that was
// deliberately rejected as too confusing for operators reading the
// YAML.
type Config struct {
	Defaults  Scoped              `yaml:"defaults" json:"defaults"`
	Tools     map[string]Scoped   `yaml:"tools" json:"tools,omitempty"`
	Overrides []Override          `yaml:"overrides" json:"overrides,omitempty"`
}

// IsZero reports whether the throttling block was effectively absent
// from the policy. Used by the throttle package to substitute bundled
// defaults without forcing every test to construct a populated config.
func (c Config) IsZero() bool {
	return c.Defaults.PerSession.IsZero() &&
		c.Defaults.PerTenant.IsZero() &&
		len(c.Tools) == 0 &&
		len(c.Overrides) == 0
}

// Scoped pairs a per-session and per-tenant Limit. The two tiers are
// independent — a tool may have a tight per-session cap with a loose
// per-tenant one (the common pattern) or vice versa.
type Scoped struct {
	PerSession Limit `yaml:"per_session" json:"per_session"`
	PerTenant  Limit `yaml:"per_tenant" json:"per_tenant"`
}

// ForScope returns the Limit for the requested scope name. Used at
// effective-limit resolution time by the throttle package. The scope
// string is the same one the throttle package uses internally
// ("session" or "tenant").
func (s Scoped) ForScope(scope string) Effective {
	if scope == "tenant" {
		return s.PerTenant.Effective()
	}
	return s.PerSession.Effective()
}

// Effective is the parsed (Rate, Burst) pair the limiter consumes.
type Effective struct {
	Rate  rate.Limit
	Burst int
}

// Limit is the YAML-facing pair of a rate string ("10/s") and a
// burst integer. The parsed rate.Limit is computed once at load
// time and cached on the struct to keep the hot path allocation-free.
type Limit struct {
	Rate  string `yaml:"rate" json:"rate"`
	Burst int    `yaml:"burst" json:"burst"`

	// parsedRate is set by Validate once the rate string has been
	// validated. Hot path reads this directly via Effective().
	parsedRate rate.Limit
}

// IsZero reports whether the limit was effectively unset.
func (l Limit) IsZero() bool {
	return strings.TrimSpace(l.Rate) == "" && l.Burst == 0
}

// Effective returns the parsed rate+burst pair. Validators must have
// run; if parsedRate is zero we re-parse defensively (a caller who
// constructs a Config in code without going through Validate still
// gets a useful answer).
func (l Limit) Effective() Effective {
	r := l.parsedRate
	if r == 0 && strings.TrimSpace(l.Rate) != "" {
		r, _ = ParseRate(l.Rate)
	}
	return Effective{Rate: r, Burst: l.Burst}
}

// Override applies a multiplier to BOTH the rate and the burst for
// any principal whose glob matches via filepath.Match. Overrides are
// evaluated in declaration order; the first hit wins.
type Override struct {
	Principal  string  `yaml:"principal" json:"principal"`
	Multiplier float64 `yaml:"multiplier" json:"multiplier"`
}

// schema is the top-level wrapper used by ParseBlock to extract
// `throttling:` from raw policy YAML without forcing the line-scanner
// to learn the nested-map grammar.
type schema struct {
	Throttling Config `yaml:"throttling"`
}

// ParseBlock extracts the optional top-level `throttling:` section
// from raw policy YAML. Returns a zero Config (and nil error) when
// the section is absent.
func ParseBlock(raw []byte) (Config, error) {
	var doc schema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Config{}, fmt.Errorf("throttling: yaml parse: %w", err)
	}
	return doc.Throttling, nil
}

// Validate enforces the structural rules from spec T1:
//   - every rate string parses
//   - burst >= 1 when a limit is present
//   - multipliers > 0
//   - override globs compile via filepath.Match
//
// Parsed rate.Limit values are stored back on the Limit so the hot
// path never re-parses on every Allow call. The returned Config is
// the normalized copy.
func Validate(cfg Config) (Config, error) {
	normalized := cfg

	if !cfg.Defaults.PerSession.IsZero() {
		lim, err := validateLimit("defaults.per_session", cfg.Defaults.PerSession)
		if err != nil {
			return cfg, err
		}
		normalized.Defaults.PerSession = lim
	}
	if !cfg.Defaults.PerTenant.IsZero() {
		lim, err := validateLimit("defaults.per_tenant", cfg.Defaults.PerTenant)
		if err != nil {
			return cfg, err
		}
		normalized.Defaults.PerTenant = lim
	}

	if cfg.Tools != nil {
		normalized.Tools = make(map[string]Scoped, len(cfg.Tools))
		for name, scoped := range cfg.Tools {
			out := scoped
			if !scoped.PerSession.IsZero() {
				lim, err := validateLimit(fmt.Sprintf("tools.%s.per_session", name), scoped.PerSession)
				if err != nil {
					return cfg, err
				}
				out.PerSession = lim
			}
			if !scoped.PerTenant.IsZero() {
				lim, err := validateLimit(fmt.Sprintf("tools.%s.per_tenant", name), scoped.PerTenant)
				if err != nil {
					return cfg, err
				}
				out.PerTenant = lim
			}
			normalized.Tools[name] = out
		}
	}

	for i, ov := range cfg.Overrides {
		if strings.TrimSpace(ov.Principal) == "" {
			return cfg, fmt.Errorf("throttling.overrides[%d]: empty principal", i)
		}
		if ov.Multiplier <= 0 {
			return cfg, fmt.Errorf("throttling.overrides[%d] (%s): multiplier must be > 0 (got %v)",
				i, ov.Principal, ov.Multiplier)
		}
		// filepath.Match returns ErrBadPattern for malformed globs even
		// when the supplied string would not be a match; probe against
		// the empty string to surface the error at config-load time.
		if _, err := filepath.Match(ov.Principal, ""); err != nil {
			return cfg, fmt.Errorf("throttling.overrides[%d] (%s): malformed glob: %w",
				i, ov.Principal, err)
		}
	}

	return normalized, nil
}

// validateLimit parses the rate string, enforces burst >= 1, and
// returns a Limit with parsedRate populated.
func validateLimit(field string, l Limit) (Limit, error) {
	r, err := ParseRate(l.Rate)
	if err != nil {
		return l, fmt.Errorf("throttling.%s.rate %q: %w", field, l.Rate, err)
	}
	if l.Burst < 1 {
		return l, fmt.Errorf("throttling.%s.burst must be >= 1 (got %d)", field, l.Burst)
	}
	l.parsedRate = r
	return l, nil
}

// ParseRate parses the T5 rate-string grammar: `<int>/<unit>` where
// unit is one of s, sec, m, min, h, hour. Whitespace around the
// slash is tolerated. Returns rate.Limit (tokens per second) so a
// 100/min rate yields rate.Limit(100.0/60.0).
//
// Rejection cases: empty input, missing numerator, missing unit,
// non-positive numerator, unknown unit, non-integer numerator.
func ParseRate(s string) (rate.Limit, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty rate string")
	}
	slash := strings.IndexByte(trimmed, '/')
	if slash < 0 {
		return 0, fmt.Errorf("rate %q missing '/' separator (want N/{s,m,h})", s)
	}
	numStr := strings.TrimSpace(trimmed[:slash])
	unitStr := strings.TrimSpace(trimmed[slash+1:])
	if numStr == "" {
		return 0, fmt.Errorf("rate %q missing numerator", s)
	}
	if unitStr == "" {
		return 0, fmt.Errorf("rate %q missing unit", s)
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("rate %q numerator not an integer: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("rate %q numerator must be > 0", s)
	}
	var windowSeconds float64
	switch strings.ToLower(unitStr) {
	case "s", "sec", "second", "seconds":
		windowSeconds = 1
	case "m", "min", "minute", "minutes":
		windowSeconds = 60
	case "h", "hr", "hour", "hours":
		windowSeconds = 3600
	default:
		return 0, fmt.Errorf("rate %q unknown unit %q (want s, m, or h)", s, unitStr)
	}
	return rate.Limit(float64(n) / windowSeconds), nil
}
