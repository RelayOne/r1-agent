package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/agentloop"
)

// SpecExpectation describes what a specific file is supposed to contain
// according to the SOW. The native runner's midturn supervisor uses
// these to scan files after writes and push a correction note when the
// code diverges.
type SpecExpectation struct {
	// File is the path (relative to the work directory) that this
	// expectation targets.
	File string
	// MustContain is a list of verbatim substrings the file must
	// contain. Extracted from content_match.Pattern criteria and
	// from canonical identifiers for this file.
	MustContain []string
	// MustNotContain is a list of substrings that indicate the file
	// has drifted from spec (e.g. wrong struct names the model
	// hallucinated). Usually empty on the first pass; filled in by
	// the caller when a specific anti-pattern is known.
	MustNotContain []string
}

// SupervisorConfig controls the midturn spec-check hook.
type SupervisorConfig struct {
	// WorkDir is the absolute path where the agent operates — all
	// SpecExpectation file paths are resolved against this.
	WorkDir string
	// Expectations is the list of files the supervisor should watch.
	Expectations []SpecExpectation
	// WritesPerCheck is how many write/edit tool calls must happen
	// before the supervisor runs another scan. Default 3 ("every few
	// writes", not "every write"). The first write is never checked
	// immediately — we wait until the model has had a chance to
	// produce meaningful content.
	WritesPerCheck int
}

// BuildNativeSupervisor returns an agentloop.MidturnCheckFunc that
// tracks write_file / edit_file tool calls and, every WritesPerCheck
// writes, scans the declared files for spec violations. When a
// violation is found it returns a short correction note that the
// loop injects into the next user message as a "[SUPERVISOR NOTE]".
//
// The check is deterministic (substring matching) and cheap — no LLM
// calls. The model sees:
//
//	[SUPERVISOR NOTE] You wrote crates/persys-concern/src/lib.rs but
//	it's missing canonical identifier "pub struct Concern" from the
//	spec. Re-read the SPEC EXCERPT and fix this file before continuing.
//
// Returns nil if Expectations is empty or WorkDir is unset (the hook
// would have nothing to check).
func BuildNativeSupervisor(cfg SupervisorConfig) agentloop.MidturnCheckFunc {
	if len(cfg.Expectations) == 0 || cfg.WorkDir == "" {
		return nil
	}
	if cfg.WritesPerCheck <= 0 {
		cfg.WritesPerCheck = 3
	}

	// State captured in the closure, protected by a mutex because
	// the loop is sequential (turn-by-turn) but tool execution inside
	// a turn can be parallel.
	var (
		mu            sync.Mutex
		writeCount    int
		lastChecked   int
		alreadyWarned = make(map[string]bool)
	)

	return func(messages []agentloop.Message, turn int) string {
		mu.Lock()
		defer mu.Unlock()

		// Count new tool calls in the latest assistant message.
		// Only write_file / edit_file count toward the threshold;
		// reads and bash calls don't.
		if len(messages) < 2 {
			return ""
		}
		// Walk backwards until we find the most recent assistant
		// message; that's the turn that just completed.
		var last agentloop.Message
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "assistant" {
				last = messages[i]
				break
			}
		}
		for _, block := range last.Content {
			if block.Type != "tool_use" {
				continue
			}
			if block.Name == "write_file" || block.Name == "edit_file" {
				writeCount++
			}
		}

		// Not enough new writes yet — defer.
		if writeCount-lastChecked < cfg.WritesPerCheck {
			return ""
		}
		lastChecked = writeCount

		// Run the scan.
		violations := scanSpecExpectations(cfg.WorkDir, cfg.Expectations, alreadyWarned)
		if len(violations) == 0 {
			return ""
		}
		return formatSupervisorNote(violations)
	}
}

// specViolation is a single mismatch between an expectation and the
// file's current content.
type specViolation struct {
	File     string
	Missing  []string
	Forbidden []string
}

// scanSpecExpectations runs the deterministic substring scan. Files
// that don't exist yet are skipped (the agent might create them in a
// later turn). alreadyWarned is updated so we don't nag about the
// same missing identifier twice in a row — each entry is re-checked
// the next time after alreadyWarned is cleared by the caller.
func scanSpecExpectations(workDir string, expectations []SpecExpectation, alreadyWarned map[string]bool) []specViolation {
	var out []specViolation
	for _, exp := range expectations {
		abs := filepath.Join(workDir, exp.File)
		data, err := os.ReadFile(abs)
		if err != nil {
			// File doesn't exist yet — not a violation, the task
			// might create it on a later turn. Only existing files
			// with content can "drift".
			continue
		}
		content := string(data)

		var missing []string
		for _, must := range exp.MustContain {
			if must == "" {
				continue
			}
			if !strings.Contains(content, must) {
				warnKey := exp.File + "::missing::" + must
				if alreadyWarned[warnKey] {
					continue
				}
				alreadyWarned[warnKey] = true
				missing = append(missing, must)
			}
		}

		var forbidden []string
		for _, bad := range exp.MustNotContain {
			if bad == "" {
				continue
			}
			if strings.Contains(content, bad) {
				warnKey := exp.File + "::forbidden::" + bad
				if alreadyWarned[warnKey] {
					continue
				}
				alreadyWarned[warnKey] = true
				forbidden = append(forbidden, bad)
			}
		}

		if len(missing) > 0 || len(forbidden) > 0 {
			out = append(out, specViolation{
				File:      exp.File,
				Missing:   missing,
				Forbidden: forbidden,
			})
		}
	}
	return out
}

// formatSupervisorNote produces the single-line text the loop injects
// into the next user message. Terse — the model is in the middle of a
// turn and we don't want to burn tokens on a long lecture.
func formatSupervisorNote(violations []specViolation) string {
	var b strings.Builder
	b.WriteString("Spec-faithfulness scan found ")
	b.WriteString(pluralize(len(violations), "file"))
	b.WriteString(" diverging from the SOW. Re-read the SPEC EXCERPT and fix before continuing.\n")
	for _, v := range violations {
		fmt.Fprintf(&b, "  %s:\n", v.File)
		for _, m := range v.Missing {
			fmt.Fprintf(&b, "    - MISSING canonical identifier: %s\n", truncateForNote(m, 120))
		}
		for _, f := range v.Forbidden {
			fmt.Fprintf(&b, "    - FORBIDDEN (drift from spec): %s\n", truncateForNote(f, 120))
		}
	}
	b.WriteString("Do not proceed to new files until the flagged ones match the spec.")
	return b.String()
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func truncateForNote(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// spaceRe is unused but kept for future expansion when we want to
// normalize whitespace during matching. Silences the unused import if
// this file shrinks.
var spaceRe = regexp.MustCompile(`\s+`)
