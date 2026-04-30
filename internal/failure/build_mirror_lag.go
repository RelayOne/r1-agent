package failure

import (
	"strings"
	"time"
)

const mirrorLagRetryDelay = 30 * time.Second

// MirrorLagClassifier identifies the transient Cloud Build mirror race where a commit is not yet readable.
type MirrorLagClassifier struct{}

// Detect returns true when the failure matches the "triggered too soon" mirror-sync race.
func (MirrorLagClassifier) Detect(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"couldn't read commit",
		"could not read commit",
		"cannot read commit",
		"failed to fetch commit",
		"source revision was not found",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// ShouldRetry allows one retry after a fixed 30-second mirror catch-up delay.
func (MirrorLagClassifier) ShouldRetry(attempt int) (time.Duration, bool) {
	if attempt <= 1 {
		return mirrorLagRetryDelay, true
	}
	return 0, false
}
