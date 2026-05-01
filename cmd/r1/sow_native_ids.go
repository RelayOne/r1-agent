package main

import (
	"crypto/rand"
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// newRunID returns a compact identifier for this `r1 sow` invocation.
// Used as the WorkerLogContext.RunID stamp on every JSONL entry.
// Format: "run-YYYYMMDDThhmmss-<6 hex>" — lexicographically sortable.
func newRunID() string {
	var buf [3]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("run-%s-%x", time.Now().UTC().Format("20060102T150405"), buf[:])
}

var (
	stokeBuildOnce sync.Once
	stokeBuildStr  string
)

// readStokeBuild returns a short identifier for the r1 binary that's
// running. In priority order:
//  1. Go build VCS revision (set by `go build` automatically for VCS-
//     tracked source trees) — `vcs.revision` in debug.ReadBuildInfo.
//  2. Fallback to `git -C /home/eric/repos/r1-agent rev-parse --short HEAD`
//     at first call, cached via sync.Once.
//  3. Empty string if both fail.
//
// Invoked once at the start of a `r1 sow` run so every worker JSONL
// entry can be traced back to a specific binary — critical when
// diagnosing whether a behavior change came from code or input.
func readStokeBuild() string {
	stokeBuildOnce.Do(func() {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && s.Value != "" {
					if len(s.Value) > 12 {
						stokeBuildStr = s.Value[:12]
					} else {
						stokeBuildStr = s.Value
					}
					return
				}
			}
		}
		// Fallback to running git. Use a short timeout so this never
		// blocks a live run.
		cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
		cmd.Dir = "/home/eric/repos/r1-agent"
		out, err := cmd.Output()
		if err == nil {
			stokeBuildStr = strings.TrimSpace(string(out))
		}
	})
	return stokeBuildStr
}
