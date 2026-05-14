package throttle

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"

	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// defaultsYAML is the bundled conservative baseline. Operators who
// omit the `throttling:` block in their r1.policy.yaml get this
// applied automatically (T12). The file is checked in at
// configs/policies/throttling-defaults.yaml so reviewers can diff
// changes to the baseline in code review; an identical copy lives
// under embedded/ in this package because go:embed only sees files
// adjacent to the source file.
//
//go:embed embedded/throttling-defaults.yaml
var defaultsYAML []byte

// DefaultPolicy returns the parsed bundled policy. The file is parsed
// once on first call and cached; structural errors are a build-time
// regression (caught by tests) so we panic loudly if parsing fails at
// process start.
func DefaultPolicy() Config {
	var doc struct {
		Throttling throttlepolicy.Config `yaml:"throttling"`
	}
	if err := yaml.Unmarshal(defaultsYAML, &doc); err != nil {
		panic(fmt.Sprintf("throttle: bundled defaults parse failed: %v", err))
	}
	normalized, err := throttlepolicy.Validate(doc.Throttling)
	if err != nil {
		panic(fmt.Sprintf("throttle: bundled defaults invalid: %v", err))
	}
	return normalized
}

// ValidateConfig is a thin wrapper around policy.Validate so callers
// outside the throttle package can validate a Config without a
// separate import.
func ValidateConfig(cfg Config) error {
	_, err := throttlepolicy.Validate(cfg)
	return err
}
