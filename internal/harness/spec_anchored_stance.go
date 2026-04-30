package harness

import (
	"fmt"
	"os"
	"strings"
)

// SpecAnchoredStance requires commit messages to cite a real line from the spec.
type SpecAnchoredStance struct {
	SpecPath string
}

// ValidateCommitMsg validates that the commit message includes a meaningful line from the spec.
func (s SpecAnchoredStance) ValidateCommitMsg(msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("spec anchored stance: commit message is empty")
	}
	if strings.TrimSpace(s.SpecPath) == "" {
		return fmt.Errorf("spec anchored stance: spec path is required")
	}

	data, err := os.ReadFile(s.SpecPath)
	if err != nil {
		return fmt.Errorf("spec anchored stance: read spec: %w", err)
	}

	specLines := indexedSpecLines(string(data))
	if len(specLines) == 0 {
		return fmt.Errorf("spec anchored stance: no eligible spec lines found in %s", s.SpecPath)
	}

	for _, line := range strings.Split(msg, "\n") {
		candidate := normalizeSpecLine(line)
		if stripped, ok := stripSpecPrefix(candidate); ok {
			candidate = stripped
		}
		if candidate == "" {
			continue
		}
		if _, ok := specLines[candidate]; ok {
			return nil
		}
	}

	return fmt.Errorf("spec anchored stance: commit message must include a verbatim spec line prefixed with %q or pasted directly", "Spec:")
}

func indexedSpecLines(spec string) map[string]struct{} {
	lines := make(map[string]struct{})
	for _, raw := range strings.Split(spec, "\n") {
		line := normalizeSpecLine(raw)
		if len(line) < 15 {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines[line] = struct{}{}
	}
	return lines
}

func stripSpecPrefix(line string) (string, bool) {
	lower := strings.ToLower(line)
	for _, prefix := range []string{"spec:", "spec line:"} {
		if strings.HasPrefix(lower, prefix) {
			return normalizeSpecLine(strings.TrimSpace(line[len(prefix):])), true
		}
	}
	return line, false
}

func normalizeSpecLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}
