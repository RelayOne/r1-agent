// Package main — no_direct_env_test.go
//
// Spec 4 §10 T2: enforces the rule that os.Getenv("R1_SERVER_UI_V2")
// and os.Getenv("R1_SERVER_SHARE_ENABLED") MUST NOT appear outside
// ui_v2_flag.go. Centralising the reads keeps the v2 surface's
// flag semantics (strict-"1") in one place + makes future YAML
// config wiring a one-file change.
//
// The test scans every .go file under this package's directory at
// test time and fails if any non-ui_v2_flag file references the
// env-var directly. New flag-controlled features can either:
//   - extend V2Config with their flag, OR
//   - declare their own helper that follows the same pattern (see
//     traceStubEnabled() in trace.go for an example — separate flag
//     name, allowed)
package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoDirectV2EnvReadsOutsideFlagFile(t *testing.T) {
	const allowedFile = "ui_v2_flag.go"
	bannedSubstrings := []string{
		`os.Getenv("R1_SERVER_UI_V2")`,
		`os.Getenv("R1_SERVER_SHARE_ENABLED")`,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Allow the centraliser file itself + tests (some tests want
		// to assert the env semantics directly).
		if e.Name() == allowedFile || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(".", e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			// Skip comments referencing the pattern in prose.
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") {
				continue
			}
			for _, banned := range bannedSubstrings {
				if strings.Contains(line, banned) {
					t.Errorf("%s:%d direct env read forbidden — use LoadV2Config() instead\n  line: %s", path, lineNum, line)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Errorf("scan %s: %v", path, err)
		}
	}
}
