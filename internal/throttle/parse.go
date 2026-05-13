package throttle

import (
	"golang.org/x/time/rate"

	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// ParseRate parses the T5 rate-string grammar (`<int>/<unit>` where
// unit is one of s, sec, m, min, h, hour) into a rate.Limit. Thin
// re-export of policy.ParseRate so existing callers that imported
// the throttle package directly do not need to add a second import.
func ParseRate(s string) (rate.Limit, error) { return throttlepolicy.ParseRate(s) }
