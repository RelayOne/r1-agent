package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Finding is a doc drift issue detected in one of the canonical docs.
type Finding struct {
	Path    string
	Line    int
	Kind    string
	Message string
}

// DocDriftChecker scans the canonical repo docs for stale references.
type DocDriftChecker struct {
	Repo string
}

var (
	canonicalDocPaths = []string{
		"README.md",
		"docs/ARCHITECTURE.md",
		"docs/HOW-IT-WORKS.md",
		"docs/FEATURE-MAP.md",
		"docs/DEPLOYMENT.md",
		"docs/BUSINESS-VALUE.md",
	}
	docDriftNow          = time.Now
	docDriftCommitExists = func(ctx context.Context, repo, ref string) (bool, error) {
		out, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").CombinedOutput()
		if err == nil {
			return true, nil
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return false, nil
		}
		if strings.Contains(strings.ToLower(string(out)), "unknown revision") ||
			strings.Contains(strings.ToLower(string(out)), "needed a single revision") {
			return false, nil
		}
		return false, fmt.Errorf("git rev-parse %q: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
)

var (
	todoPattern         = regexp.MustCompile(`(?i)\bTODO\b`)
	isoDatePattern      = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	monthDatePattern    = regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]* \d{1,2}, \d{4}\b`)
	commitPattern       = regexp.MustCompile(`(?i)\bcommit\s+([0-9a-f]{7,40})\b`)
	markdownLinkPattern = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	backtickPathPattern = regexp.MustCompile("`([^`]+)`")
	bareRepoPathPattern = regexp.MustCompile(`(?:^|[\s(])((?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+(?:\.[A-Za-z0-9_.-]+)?)`)
)

// Check scans the canonical docs and returns drift findings.
func (c DocDriftChecker) Check(ctx context.Context) ([]Finding, error) {
	repo := c.Repo
	if strings.TrimSpace(repo) == "" {
		repo = "."
	}

	var findings []Finding
	for _, rel := range canonicalDocPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(repo, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				findings = append(findings, Finding{
					Path:    rel,
					Line:    1,
					Kind:    "missing_doc",
					Message: "canonical document is missing",
				})
				continue
			}
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		findingsForDoc, err := scanDoc(ctx, repo, rel, string(data))
		if err != nil {
			return nil, err
		}
		findings = append(findings, findingsForDoc...)
	}
	return findings, nil
}

func scanDoc(ctx context.Context, repo, rel, content string) ([]Finding, error) {
	lines := strings.Split(content, "\n")
	var findings []Finding
	for idx, line := range lines {
		lineNum := idx + 1
		if todoPattern.MatchString(line) {
			findings = append(findings, Finding{
				Path:    rel,
				Line:    lineNum,
				Kind:    "todo_marker",
				Message: "TODO marker found in canonical documentation",
			})
		}
		if staleDate, ok := extractStaleDate(line); ok {
			findings = append(findings, Finding{
				Path:    rel,
				Line:    lineNum,
				Kind:    "stale_date",
				Message: fmt.Sprintf("freshness marker is stale (%s)", staleDate.Format("2006-01-02")),
			})
		}
		for _, ref := range extractPathRefs(line) {
			ok, err := repoPathExists(repo, ref)
			if err != nil {
				return nil, err
			}
			if !ok {
				findings = append(findings, Finding{
					Path:    rel,
					Line:    lineNum,
					Kind:    "missing_file_ref",
					Message: fmt.Sprintf("referenced file does not exist: %s", ref),
				})
			}
		}
		for _, commit := range extractCommitRefs(line) {
			exists, err := docDriftCommitExists(ctx, repo, commit)
			if err != nil {
				return nil, err
			}
			if !exists {
				findings = append(findings, Finding{
					Path:    rel,
					Line:    lineNum,
					Kind:    "missing_commit_ref",
					Message: fmt.Sprintf("referenced commit does not exist: %s", commit),
				})
			}
		}
	}
	return findings, nil
}

func extractStaleDate(line string) (time.Time, bool) {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, "updated") && !strings.Contains(lower, "reviewed") && !strings.Contains(lower, "as of") {
		return time.Time{}, false
	}

	for _, candidate := range []string{
		isoDatePattern.FindString(line),
		monthDatePattern.FindString(line),
	} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		for _, layout := range []string{"2006-01-02", "Jan 2, 2006", "January 2, 2006"} {
			parsed, err := time.Parse(layout, candidate)
			if err != nil {
				continue
			}
			if docDriftNow().Sub(parsed) > 365*24*time.Hour {
				return parsed, true
			}
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}

func extractPathRefs(line string) []string {
	refs := make(map[string]struct{})
	for _, pattern := range []*regexp.Regexp{markdownLinkPattern, backtickPathPattern, bareRepoPathPattern} {
		matches := pattern.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			ref := cleanDocRef(match[1])
			if ref == "" {
				continue
			}
			refs[ref] = struct{}{}
		}
	}

	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	return out
}

func cleanDocRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, ".,:;()")
	if ref == "" || strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") {
		return ""
	}
	if idx := strings.IndexAny(ref, "?#"); idx >= 0 {
		ref = ref[:idx]
	}
	if strings.HasPrefix(ref, "/") {
		ref = strings.TrimPrefix(ref, "/")
	}
	if !strings.Contains(ref, "/") && !strings.Contains(ref, ".") {
		return ""
	}
	return filepath.Clean(ref)
}

func repoPathExists(repo, ref string) (bool, error) {
	target := filepath.Join(repo, ref)
	_, err := os.Stat(target)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", ref, err)
}

func extractCommitRefs(line string) []string {
	matches := commitPattern.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			refs = append(refs, match[1])
		}
	}
	return refs
}
