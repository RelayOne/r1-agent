package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoPollingInV2Packages enforces the architectural principle that v2
// governance packages must not use polling primitives (time.Tick, time.NewTicker).
// All coordination should flow through the event bus, not periodic polling.
func TestNoPollingInV2Packages(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	thisFile = filepath.Clean(thisFile)

	internalDir := filepath.Dir(filepath.Dir(thisFile))

	v2Packages := []string{
		"ledger",
		"bus",
		"supervisor",
		"concern",
		"harness",
		"snapshot",
		"wizard",
		"skillmfr",
		"bench",
		"bridge",
	}

	forbiddenPatterns := []string{
		"time.Tick(",
		"time.NewTicker(",
	}

	var violations []string

	for _, pkg := range v2Packages {
		pkgDir := filepath.Join(internalDir, pkg)
		err := filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Skip test files.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Skip this test file itself.
			if filepath.Clean(path) == thisFile {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}
			content := string(data)
			lines := strings.Split(content, "\n")

			for i, line := range lines {
				for _, pattern := range forbiddenPatterns {
					if strings.Contains(line, pattern) {
						rel, _ := filepath.Rel(internalDir, path)
						violations = append(violations, fmt.Sprintf(
							"%s:%d: forbidden polling pattern %q found: %s",
							rel, i+1, pattern, strings.TrimSpace(line),
						))
					}
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("walking %s: %v", pkg, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("v2 packages must not use polling primitives; found %d violation(s):", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
	}
}
