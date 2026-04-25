package convergence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/r1dir"
)

// IgnoreEntry is a single ignore rule that suppresses a specific finding
// from contributing to convergence failure. Entries are matched against
// Findings by rule_id + file + (optionally) line range + pattern.
//
// Design: entries are deliberately specific so a blanket "ignore no-secrets"
// doesn't silently mask new real issues. A human (or the CTO stance) must
// sign off on each entry with a Reason.
type IgnoreEntry struct {
	RuleID       string    `json:"rule_id"`              // convergence rule ID to ignore
	File         string    `json:"file"`                 // exact file path or glob
	LineStart    int       `json:"line_start,omitempty"` // 0 = any line
	LineEnd      int       `json:"line_end,omitempty"`   // 0 = any line; inclusive
	Pattern      string    `json:"pattern,omitempty"`    // optional substring match on Finding.Evidence
	Reason       string    `json:"reason"`               // human-readable justification (REQUIRED)
	ProposedBy   string    `json:"proposed_by"`          // stance that proposed (e.g. "vp-eng")
	ApprovedBy   string    `json:"approved_by"`          // stance that signed off (e.g. "cto")
	CreatedAt    time.Time `json:"created_at"`
	BlockCount   int       `json:"block_count,omitempty"` // how many times this flag blocked convergence before override
}

// IgnoreList is the top-level ignore list persisted at
// .stoke/convergence-ignores.json. Thread-safe via an internal mutex.
type IgnoreList struct {
	mu      sync.RWMutex
	Version int           `json:"version"`
	Entries []IgnoreEntry `json:"entries"`
}

// IgnoreListPath returns the canonical on-disk path.
func IgnoreListPath(projectRoot string) string {
	return r1dir.JoinFor(projectRoot, "convergence-ignores.json")
}

// LoadIgnores reads the ignore list. Returns an empty list (not an error)
// if the file doesn't exist.
func LoadIgnores(projectRoot string) (*IgnoreList, error) {
	path := IgnoreListPath(projectRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &IgnoreList{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ignore list: %w", err)
	}
	var list IgnoreList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse ignore list: %w", err)
	}
	if list.Version == 0 {
		list.Version = 1
	}
	return &list, nil
}

// Save persists the ignore list atomically.
func (l *IgnoreList) Save(projectRoot string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	path := IgnoreListPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if l.Version == 0 {
		l.Version = 1
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add appends an entry, rejecting duplicates and entries without Reason.
// An entry is a duplicate if rule_id + file + line range + pattern all match.
func (l *IgnoreList) Add(entry IgnoreEntry) error {
	if strings.TrimSpace(entry.Reason) == "" {
		return fmt.Errorf("ignore entry requires a Reason (CTO sign-off)")
	}
	if strings.TrimSpace(entry.RuleID) == "" {
		return fmt.Errorf("ignore entry requires a RuleID")
	}
	if strings.TrimSpace(entry.File) == "" {
		return fmt.Errorf("ignore entry requires a File")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, existing := range l.Entries {
		if entriesDuplicate(existing, entry) {
			return nil // silently drop duplicates
		}
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	l.Entries = append(l.Entries, entry)
	return nil
}

func entriesDuplicate(a, b IgnoreEntry) bool {
	return a.RuleID == b.RuleID &&
		a.File == b.File &&
		a.LineStart == b.LineStart &&
		a.LineEnd == b.LineEnd &&
		a.Pattern == b.Pattern
}

// Matches reports whether the ignore list suppresses a given finding.
// A match requires: rule ID equal, file match (exact or glob), line in
// range (if range set), pattern substring in Evidence (if pattern set).
func (l *IgnoreList) Matches(f Finding) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, e := range l.Entries {
		if e.RuleID != "" && e.RuleID != f.RuleID {
			continue
		}
		if !matchFile(e.File, f.File) {
			continue
		}
		if (e.LineStart > 0 || e.LineEnd > 0) && !lineInRange(f.Line, e.LineStart, e.LineEnd) {
			continue
		}
		if e.Pattern != "" && !strings.Contains(f.Evidence, e.Pattern) {
			continue
		}
		return true
	}
	return false
}

func matchFile(pattern, file string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == file {
		return true
	}
	// Glob match. Errors from filepath.Match are treated as "no match" so
	// a bad pattern in the ignore list doesn't silently suppress
	// everything.
	if ok, err := filepath.Match(pattern, file); err == nil && ok {
		return true
	}
	// Also try matching the basename so "*.generated.ts" works against
	// "src/foo/bar.generated.ts".
	base := filepath.Base(file)
	if ok, err := filepath.Match(pattern, base); err == nil && ok {
		return true
	}
	return false
}

func lineInRange(line, start, end int) bool {
	if line == 0 {
		// Finding doesn't know its line; only match if the range is also
		// unconstrained or the entry has a 0 start.
		return start == 0
	}
	if end == 0 {
		end = start
	}
	return line >= start && line <= end
}

// ApplyIgnores returns a copy of the report with ignored findings removed.
// The original report is not mutated. Informational findings about how many
// were ignored are appended so the user has an audit trail.
func ApplyIgnores(report *Report, list *IgnoreList) *Report {
	if list == nil || len(list.Entries) == 0 {
		return report
	}
	filtered := make([]Finding, 0, len(report.Findings))
	ignoredCount := 0
	for _, f := range report.Findings {
		if list.Matches(f) {
			ignoredCount++
			continue
		}
		filtered = append(filtered, f)
	}
	if ignoredCount == 0 {
		return report
	}

	// Build a new report with the filtered findings and a summary note.
	out := *report
	out.Findings = filtered
	// Recompute score + converged state.
	out.Score = 1.0
	for _, f := range filtered {
		switch f.Severity {
		case SevBlocking:
			out.Score -= 0.15
		case SevMajor:
			out.Score -= 0.08
		case SevMinor:
			out.Score -= 0.03
		case SevInfo:
			out.Score -= 0.01
		}
	}
	if out.Score < 0 {
		out.Score = 0
	}
	out.IsConverged = countBlocking(filtered) == 0
	if out.Summary != "" {
		out.Summary = fmt.Sprintf("%s (%d ignored by CTO-approved overrides)", out.Summary, ignoredCount)
	} else {
		out.Summary = fmt.Sprintf("%d findings ignored by CTO-approved overrides", ignoredCount)
	}
	return &out
}

func countBlocking(findings []Finding) int {
	c := 0
	for _, f := range findings {
		if f.Severity == SevBlocking {
			c++
		}
	}
	return c
}

// --- Repeat tracker (supervisor-side) ---

// FlagSignature is the identity a finding uses for repeat counting. Two
// findings with the same signature are treated as the same flag for the
// purpose of triggering a VP Eng review.
type FlagSignature struct {
	RuleID string
	File   string
	Line   int
}

// RepeatTracker counts how many times the same flag signature has blocked
// convergence. When the count crosses a threshold, the supervisor should
// trigger the VP Eng → CTO override flow. Persisted to
// .stoke/convergence-repeats.json so the count survives across mission
// iterations.
type RepeatTracker struct {
	mu     sync.Mutex
	Counts map[string]int `json:"counts"` // key = signature string
}

// NewRepeatTracker creates an in-memory tracker. Call LoadRepeatTracker to
// restore persisted state.
func NewRepeatTracker() *RepeatTracker {
	return &RepeatTracker{Counts: make(map[string]int)}
}

func repeatTrackerPath(projectRoot string) string {
	return r1dir.JoinFor(projectRoot, "convergence-repeats.json")
}

// LoadRepeatTracker reads persisted counts. Returns a fresh tracker if the
// file is missing.
func LoadRepeatTracker(projectRoot string) (*RepeatTracker, error) {
	path := repeatTrackerPath(projectRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewRepeatTracker(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read repeat tracker: %w", err)
	}
	t := NewRepeatTracker()
	if err := json.Unmarshal(data, t); err != nil {
		return nil, fmt.Errorf("parse repeat tracker: %w", err)
	}
	if t.Counts == nil {
		t.Counts = make(map[string]int)
	}
	return t, nil
}

// Save persists the tracker atomically.
func (t *RepeatTracker) Save(projectRoot string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	path := repeatTrackerPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Record bumps the count for a finding's signature. Returns the new count.
func (t *RepeatTracker) Record(f Finding) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := signatureKey(FlagSignature{RuleID: f.RuleID, File: f.File, Line: f.Line})
	t.Counts[key]++
	return t.Counts[key]
}

// Count returns the current count for a signature without incrementing.
func (t *RepeatTracker) Count(sig FlagSignature) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Counts[signatureKey(sig)]
}

// Reset clears the count for a specific signature (e.g. after an override
// has been approved).
func (t *RepeatTracker) Reset(sig FlagSignature) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.Counts, signatureKey(sig))
}

// RecordReportBlocks counts every blocking finding in the report and returns
// the list of findings that have now crossed the repeat threshold — the
// supervisor's trigger for kicking off the VP Eng review.
func (t *RepeatTracker) RecordReportBlocks(report *Report, threshold int) []Finding {
	if threshold < 1 {
		threshold = 2
	}
	var triggered []Finding
	for _, f := range report.Findings {
		if f.Severity != SevBlocking {
			continue
		}
		count := t.Record(f)
		if count >= threshold {
			triggered = append(triggered, f)
		}
	}
	return triggered
}

func signatureKey(sig FlagSignature) string {
	return fmt.Sprintf("%s|%s|%d", sig.RuleID, sig.File, sig.Line)
}

// validPatternRegex compiles a pattern string as a regex (or returns nil if
// the pattern is empty or unparseable). Used by the judge when matching
// evidence against proposed ignore patterns.
func validPatternRegex(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return r
}
