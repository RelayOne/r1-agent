package failure

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var stableIDSeqRe = regexp.MustCompile(`-(\d{6})$`)

// StableTaskID returns a deterministic task ID for a namespace-local sequence.
func StableTaskID(namespace string, sequence int) string {
	if sequence < 1 {
		sequence = 1
	}
	base := normalizeStableNamespace(namespace)
	return fmt.Sprintf("%s-%06d", base, sequence)
}

// NextStableSequence infers the next deterministic sequence number from task IDs.
func NextStableSequence(namespace string, taskIDs []string) int {
	base := normalizeStableNamespace(namespace)
	maxSeq := 0
	prefix := base + "-"
	for _, id := range taskIDs {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		m := stableIDSeqRe.FindStringSubmatch(id)
		if len(m) != 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > maxSeq {
			maxSeq = n
		}
	}
	if maxSeq == 0 {
		return len(taskIDs) + 1
	}
	return maxSeq + 1
}

func normalizeStableNamespace(namespace string) string {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	if namespace == "" {
		return "task"
	}
	var b strings.Builder
	for _, r := range namespace {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			if b.Len() > 0 {
				b.WriteRune(r)
			}
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "task"
	}
	if len(base) <= 18 {
		return base
	}
	sum := sha1.Sum([]byte(namespace))
	return base[:10] + "-" + hex.EncodeToString(sum[:])[:7]
}
