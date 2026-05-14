package analytics

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

// Policy is the subset of r1.policy.yaml the analytics package reads.
// Keeping the type local (rather than reaching into internal/config/'s
// full policy parser) keeps the analytics package self-contained and
// lets the policy block evolve in B1 independently of the broader
// schema work in the policy package.
//
// YAML shape:
//
//	analytics:
//	  disabled: false
//	  tenant_optouts:
//	    - tenant-uuid-1
//	    - tenant-uuid-2
type Policy struct {
	Analytics AnalyticsPolicy `yaml:"analytics"`
}

// AnalyticsPolicy carries the two operator-controlled knobs spec §11
// requires: a global disable and a per-tenant opt-out allowlist.
type AnalyticsPolicy struct {
	Disabled      bool     `yaml:"disabled"`
	TenantOptOuts []string `yaml:"tenant_optouts"`
}

// LoadPolicy reads an r1.policy.yaml from path and returns the parsed
// analytics block. A missing file is NOT an error — it returns the
// zero policy (analytics enabled, no opt-outs).
func LoadPolicy(path string) (AnalyticsPolicy, error) {
	if path == "" {
		return AnalyticsPolicy{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AnalyticsPolicy{}, nil
		}
		return AnalyticsPolicy{}, err
	}
	var p Policy
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return AnalyticsPolicy{}, err
	}
	return p.Analytics, nil
}

// PolicyOverlay applies an AnalyticsPolicy on top of a Config. The
// overlay disables the client when the policy disables analytics, and
// merges TenantOptOuts (policy entries are appended; duplicates are
// fine — the underlying sync.Map dedupes on Store).
func PolicyOverlay(cfg Config, p AnalyticsPolicy) Config {
	if p.Disabled {
		cfg.Disabled = true
	}
	cfg.TenantOptOuts = append(cfg.TenantOptOuts, p.TenantOptOuts...)
	return cfg
}
